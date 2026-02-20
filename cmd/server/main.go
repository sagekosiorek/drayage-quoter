package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/admin"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/static"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/templates"
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

	// Parse templates
	loginTmpl := templates.MustParse("layout.html", "login.html")
	loginSentTmpl := templates.MustParse("layout.html", "login_sent.html")
	dashboardTmpl := templates.MustParse("layout.html", "dashboard.html")
	laneNewTmpl := templates.MustParse("layout.html", "lane_new.html")
	adminUsersTmpl := templates.MustParse("layout.html", "admin_users.html")
	adminPortsTmpl := templates.MustParse("layout.html", "admin_ports.html")

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


	// TODO: consolidate later, as it grows, need dedicated handler functions in separate file, similar to /auth/handlers.go
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

	// Protected routes
	mux.HandleFunc("GET /{$}", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		dashboardTmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":   user,
			"Lanes":  nil,
			"Query":  r.URL.Query().Get("q"),
			"Status": r.URL.Query().Get("status"),
		})
	}))

	mux.HandleFunc("GET /lanes/new", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := database.Query(`SELECT id, name FROM ports ORDER BY type, name`)
		if err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			  return
		}
		defer rows.Close()
		var ports []struct {
				ID   int
				Name string
		}
		for rows.Next() {
				var p struct{ ID int; Name string }
				if err := rows.Scan(&p.ID, &p.Name); err != nil {
						http.Error(w, "Internal server error", http.StatusInternalServerError)
						return
				}
				ports = append(ports, p)
		}
		laneNewTmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  user,
			"Ports": ports,
		})
	}))

	mux.HandleFunc("POST /lanes", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		customerName := r.FormValue("customer_name")
		customerIDStr := r.FormValue("customer_id")
		lpNumber := r.FormValue("lp_number")
		originPortIDStr := r.FormValue("origin_port_id")
		destination := r.FormValue("destination")
		containerSize := r.FormValue("container_size")
		weightStr := r.FormValue("weight")
		direction := r.FormValue("direction")
		loadType := r.FormValue("load_type")
		commodity := r.FormValue("commodity")
		notes := r.FormValue("notes")
		action := r.FormValue("action")

		hazmat := r.FormValue("hazmat") == "1"
		overweightChecked := r.FormValue("overweight") == "1"
		outOfGauge := r.FormValue("out_of_gauge") == "1"

		if customerName == "" || lpNumber == "" || originPortIDStr == "" || destination == "" || containerSize == "" {
			http.Error(w, "Required fields missing", http.StatusBadRequest)
			return
		}

		// Parse and validate port.
		originPortID, err := strconv.Atoi(originPortIDStr)
		if err != nil || originPortID <= 0 {
			http.Error(w, "Invalid port", http.StatusBadRequest)
			return
		}
		var portExists int
		if err := database.QueryRow("SELECT COUNT(*) FROM ports WHERE id = ?", originPortID).Scan(&portExists); err != nil || portExists == 0 {
			http.Error(w, "Port not found", http.StatusBadRequest)
			return
		}

		// Resolve or create customer.
		// If the autocomplete set a customer_id, trust it; otherwise search by name.
		var customerID int
		if customerIDStr != "" {
			customerID, _ = strconv.Atoi(customerIDStr)
		}
		if customerID == 0 {
			err = database.QueryRow(
				"SELECT id FROM customers WHERE LOWER(name) = LOWER(?)", customerName,
			).Scan(&customerID)
			if err == sql.ErrNoRows {
				res, err := database.Exec(
					"INSERT INTO customers (name, lp_number) VALUES (?, ?)",
					customerName, lpNumber,
				)
				if err != nil {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
					return
				}
				id, _ := res.LastInsertId()
				customerID = int(id)
			} else if err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
		}

		// Parse weight; store NULL if blank or zero.
		var weight interface{}
		if weightStr != "" {
			if wt, err := strconv.Atoi(weightStr); err == nil && wt > 0 {
				weight = wt

				// Auto-compute overweight from weight rules if not already checked.
				if !overweightChecked {
					switch containerSize {
					case "20":
						overweightChecked = wt > 38000
					case "20_40", "40", "40HC", "Flat Rack":
						overweightChecked = wt > 45000
					}
				}
			}
		}

		btoi := func(b bool) int {
			if b {
				return 1
			}
			return 0
		}

		status := "draft"
		if action == "request_rates" {
			status = "pending"
		}

		_, err = database.Exec(`
			INSERT INTO lanes (
				owner_id, customer_id, origin_port_id, destination,
				container_size, weight, direction, load_type,
				commodity, hazmat, overweight, out_of_gauge,
				notes, status
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			user.ID, customerID, originPortID, destination,
			containerSize, weight, direction, loadType,
			commodity, btoi(hazmat), btoi(overweightChecked), btoi(outOfGauge),
			notes, status,
		)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}))

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
