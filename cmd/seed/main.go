// cmd/seed populates the database with realistic demo data covering the full
// M3 workflow. Safe to run multiple times — skips if data already exists.
// Use --reset to wipe and re-seed from scratch.
// Use --wipe to delete all data without reseeding (produces a blank-slate DB).
//
//	go run ./cmd/seed
//	go run ./cmd/seed --reset
//	go run ./cmd/seed --wipe
//	go run ./cmd/seed --db /data/drayage.db
package main

import (
	"flag"
	"log"
	"os"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
)

func main() {
	reset := flag.Bool("reset", false, "wipe all non-port/user data and re-seed")
	wipe  := flag.Bool("wipe", false, "delete all data (including users/sessions) without reseeding")
	dbPath := flag.String("db", "", "path to SQLite database (defaults to $DB_PATH or ./data/drayage.db)")
	flag.Parse()

	path := *dbPath
	if path == "" {
		path = os.Getenv("DB_PATH")
	}
	if path == "" {
		path = "./data/drayage.db"
	}

	database, err := db.Open(path)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if *wipe {
		log.Println("→ wiping all data...")
		if err := wipeAll(database); err != nil {
			log.Fatalf("wipe: %v", err)
		}
		log.Println("✓ wipe complete")
		return
	}

	if *reset {
		log.Println("→ resetting seed data...")
		if err := resetAll(database); err != nil {
			log.Fatalf("reset: %v", err)
		}
	}

	// Idempotency guard — skip if customers already exist.
	var n int
	database.QueryRow("SELECT COUNT(*) FROM customers").Scan(&n)
	if n > 0 {
		log.Printf("database already seeded (%d customers); run with --reset to re-seed", n)
		return
	}

	// All lane/RR records are owned by the admin user.
	var adminID int
	if err := database.QueryRow("SELECT id FROM users WHERE is_admin = 1 LIMIT 1").Scan(&adminID); err != nil {
		log.Fatal("no admin user found — start the server at least once to create one")
	}
	log.Printf("→ seeding as user %d", adminID)

	ports := mustLoadPorts(database)
	customers := mustSeedCustomers(database)
	vendors := mustSeedVendors(database, adminID, ports)
	lanes := mustSeedLanes(database, adminID, customers, ports)
	mustSeedRateRequests(database, lanes, vendors, ports)

	log.Println("✓ seed complete")
}
