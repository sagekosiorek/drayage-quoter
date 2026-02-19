package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

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

	// TODO: consolidate later, as it grows, need dedicated handler functions in separate file, similar to /auth/handlers.go
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
		laneNewTmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  user,
			"Ports": nil, // TODO: populate from DB once ports migration is in place
		})
	}))

	mux.HandleFunc("POST /lanes", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		// TODO: implement once lanes/customers/ports migrations are in place
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}))

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
