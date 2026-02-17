package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/data/drayage.db"
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if err := database.Ping(); err != nil {
			http.Error(w, "database unreachable", http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "drayage-quoter is running")
	})

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}
