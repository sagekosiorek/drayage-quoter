package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
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

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	log.Println("database ready")

	authService := &auth.Service{
		DB:      database,
		Email:   auth.LogEmailSender{},
		BaseURL: baseURL,
	}

	loginTmpl := templates.MustParse("layout.html", "login.html")
	loginSentTmpl := templates.MustParse("layout.html", "login_sent.html")
	dashboardTmpl := templates.MustParse("layout.html", "dashboard.html")

	mux := http.NewServeMux()

	// Auth routes (unauthenticated)
	mux.HandleFunc("GET /login", authService.HandleLoginPage(loginTmpl))
	mux.HandleFunc("POST /login", authService.HandleLoginSubmit(loginSentTmpl))
	mux.HandleFunc("GET /auth/verify", authService.HandleVerify())
	mux.HandleFunc("POST /logout", authService.HandleLogout())

	// Protected routes
	mux.HandleFunc("GET /{$}", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		dashboardTmpl.ExecuteTemplate(w, "layout.html", map[string]any{"User": user})
	}))

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
