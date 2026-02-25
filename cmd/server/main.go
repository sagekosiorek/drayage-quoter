package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/admin"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/lanes"
	rate_requests "gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rate_requests"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/static"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/templates"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/vendors"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/drayage.db"
	}

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%s", port)
	}

	// Ensure the parent directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatalf("create db directory: %v", err)
	}

	// Set up database; run migrations and config admin
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail == "" {
		log.Fatal("ADMIN_EMAIL environment variable must be set")
	}
	database.Exec("INSERT OR IGNORE INTO users (name, email) VALUES (?, ?)", adminEmail, adminEmail)
	database.Exec("UPDATE users SET is_admin=1 WHERE email=?", adminEmail)

	log.Println("database ready")

	// Services
	authService := &auth.Service{
		DB:      database,
		Email:   auth.LogEmailSender{},
		BaseURL: baseURL,
	}

	adminSvc := &admin.Service{DB: database}
	lanesSvc := &lanes.Service{DB: database}
	vendorsSvc := &vendors.Service{DB: database}
	rrSvc := &rate_requests.Service{DB: database}

	// Parse templates
	loginTmpl := templates.MustParse("layout.html", "login.html")
	loginSentTmpl := templates.MustParse("layout.html", "login_sent.html")
	dashboardTmpl := templates.MustParse("layout.html", "dashboard.html")
	laneNewTmpl := templates.MustParse("layout.html", "lane_new.html")
	laneDetailTmpl := templates.MustParse("layout.html", "lane_detail.html")
	laneEditTmpl := templates.MustParse("layout.html", "lane_edit.html")
	adminUsersTmpl := templates.MustParse("layout.html", "admin_users.html")
	adminPortsTmpl := templates.MustParse("layout.html", "admin_ports.html")
	vendorListTmpl := templates.MustParse("layout.html", "vendor_list.html")
	vendorNewTmpl := templates.MustParse("layout.html", "vendor_new.html")
	vendorDetailTmpl := templates.MustParse("layout.html", "vendor_detail.html")
	vendorEditTmpl := templates.MustParse("layout.html", "vendor_edit.html")
	rrNewTmpl := templates.MustParse("layout.html", "rate_request_new.html")
	rrDetailTmpl := templates.MustParse("layout.html", "rate_request_detail.html")

	customerSuggTmpl := template.Must(template.New("suggestions").Parse(
		`{{range .}}<button type="button" class="suggestion-item" ` +
			`data-id="{{.ID}}" data-name="{{.Name}}" data-lp="{{.LPNumber}}" ` +
			`onclick="selectCustomer(this)">{{.Name}}<span class="suggestion-lp">{{.LPNumber}}</span></button>{{end}}`,
	))

	mux := http.NewServeMux()

	// Static assets
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))))

	// Auth routes (unauthenticated)
	mux.HandleFunc("GET /login", authService.HandleLoginPage(loginTmpl))
	mux.HandleFunc("POST /login", authService.HandleLoginSubmit(loginSentTmpl))
	mux.HandleFunc("GET /auth/verify", authService.HandleVerify())
	mux.HandleFunc("POST /logout", authService.HandleLogout())

	// Admin routes
	mux.HandleFunc("GET /admin/users", authService.RequireAdmin(adminSvc.HandleUsers(adminUsersTmpl)))
	mux.HandleFunc("POST /admin/users", authService.RequireAdmin(adminSvc.HandleAddUser()))
	mux.HandleFunc("POST /admin/users/{id}/delete", authService.RequireAdmin(adminSvc.HandleDeleteUser()))
	mux.HandleFunc("GET /admin/ports", authService.RequireAdmin(adminSvc.HandlePorts(adminPortsTmpl)))
	mux.HandleFunc("POST /admin/ports", authService.RequireAdmin(adminSvc.HandleAddPort()))
	mux.HandleFunc("POST /admin/ports/{id}/delete", authService.RequireAdmin(adminSvc.HandleDeletePort()))

	// Customer autocomplete (protected, returns HTML fragment)
	mux.HandleFunc("GET /customers/search", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("customer_name")
		if len(q) < 2 {
			w.Write([]byte(""))
			return
		}
		rows, err := database.Query(
			`SELECT id, name, lp_number FROM customers WHERE name LIKE ? ORDER BY name LIMIT 8`,
			"%"+q+"%",
		)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type suggestion struct {
			ID       int
			Name     string
			LPNumber string
		}
		var matches []suggestion
		for rows.Next() {
			var s suggestion
			if err := rows.Scan(&s.ID, &s.Name, &s.LPNumber); err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			matches = append(matches, s)
		}
		customerSuggTmpl.Execute(w, matches)
	}))

	// Dashboard
	mux.HandleFunc("GET /{$}", authService.RequireAuth(lanesSvc.HandleDashboard(dashboardTmpl)))

	// Lane routes (specific paths before wildcard)
	mux.HandleFunc("GET /lanes/new", authService.RequireAuth(lanesSvc.HandleNewForm(laneNewTmpl)))
	mux.HandleFunc("POST /lanes", authService.RequireAuth(lanesSvc.HandleCreate()))
	mux.HandleFunc("GET /lanes/{id}", authService.RequireAuth(lanesSvc.HandleDetail(laneDetailTmpl)))
	mux.HandleFunc("GET /lanes/{id}/edit", authService.RequireAuth(lanesSvc.HandleEditForm(laneEditTmpl)))
	mux.HandleFunc("POST /lanes/{id}", authService.RequireAuth(lanesSvc.HandleUpdate()))
	mux.HandleFunc("POST /lanes/{id}/status", authService.RequireAuth(lanesSvc.HandleAdvanceStatus()))

	// Rate request routes
	mux.HandleFunc("GET /lanes/{id}/rate-request/new", authService.RequireAuth(rrSvc.HandleNewForm(rrNewTmpl)))
	mux.HandleFunc("POST /lanes/{id}/rate-request", authService.RequireAuth(rrSvc.HandleCreate()))
	mux.HandleFunc("GET /rate-requests/{id}", authService.RequireAuth(rrSvc.HandleDetail(rrDetailTmpl)))

	// Vendor routes
	mux.HandleFunc("GET /vendors", authService.RequireAuth(vendorsSvc.HandleList(vendorListTmpl)))
	mux.HandleFunc("GET /vendors/search", authService.RequireAuth(vendorsSvc.HandleSearch()))
	mux.HandleFunc("GET /vendors/new", authService.RequireAuth(vendorsSvc.HandleNewForm(vendorNewTmpl)))
	mux.HandleFunc("POST /vendors", authService.RequireAuth(vendorsSvc.HandleCreate()))
	mux.HandleFunc("GET /vendors/{id}", authService.RequireAuth(vendorsSvc.HandleDetail(vendorDetailTmpl)))
	mux.HandleFunc("GET /vendors/{id}/edit", authService.RequireAuth(vendorsSvc.HandleEditForm(vendorEditTmpl)))
	mux.HandleFunc("POST /vendors/{id}", authService.RequireAuth(vendorsSvc.HandleUpdate()))
	mux.HandleFunc("POST /vendors/{id}/delete", authService.RequireAuth(vendorsSvc.HandleDelete()))
	mux.HandleFunc("POST /vendors/{id}/ports", authService.RequireAuth(vendorsSvc.HandleAddPort()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/delete", authService.RequireAuth(vendorsSvc.HandleRemovePort()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/notes", authService.RequireAuth(vendorsSvc.HandleAddNote()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/contacts", authService.RequireAuth(vendorsSvc.HandleAddContact()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/contacts/{cid}/delete", authService.RequireAuth(vendorsSvc.HandleDeleteContact()))
	mux.HandleFunc("POST /vendors/{id}/preferences/{pid}", authService.RequireAuth(vendorsSvc.HandleTogglePreference()))

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
