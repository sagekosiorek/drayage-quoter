// cmd/import loads a CSV or Excel file into the database using a YAML mapping
// config. One file maps to one or more tables; parent tables must appear before
// children in the config.
//
//	go run ./cmd/import --file carriers.xlsx --config import-configs/carriers.yaml
//	go run ./cmd/import --file carriers.csv  --config import-configs/carriers.yaml --dry-run
//	go run ./cmd/import --file carriers.csv  --config import-configs/carriers.yaml --db /data/drayage.db
package main

import (
	"flag"
	"log"
	"os"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/importer"
)

func main() {
	file   := flag.String("file",   "", "path to .csv or .xlsx file (required)")
	config := flag.String("config", "", "path to YAML mapping config (required)")
	dbPath := flag.String("db",     "", "SQLite database path (default: $DB_PATH or ./data/drayage.db)")
	dryRun := flag.Bool("dry-run",  false, "log inserts without committing")
	flag.Parse()

	if *file == "" || *config == "" {
		flag.Usage()
		os.Exit(1)
	}

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

	cfg, err := importer.LoadConfig(*config)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	rows, err := importer.ReadFile(*file)
	if err != nil {
		log.Fatalf("read file: %v", err)
	}
	log.Printf("read %d rows from %s", len(rows), *file)

	if err := importer.Import(database, cfg, rows, *dryRun); err != nil {
		log.Fatalf("import: %v", err)
	}

	if !*dryRun {
		log.Println("✓ import complete")
	}
}
