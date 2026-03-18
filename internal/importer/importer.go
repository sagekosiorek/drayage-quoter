package importer

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// Import inserts rows into the DB tables described by cfg.
// Tables are processed in order, so parent rows must appear before children.
// All inserts run inside a single transaction. If dryRun is true, the SQL is
// logged but the transaction is rolled back — nothing is persisted.
func Import(db *sql.DB, cfg Config, rows []Row, dryRun bool) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// cache[tableName][cacheKey] = row ID (inserted or pre-existing).
	cache := make(map[string]map[string]int64, len(cfg.Tables))
	for _, t := range cfg.Tables {
		cache[t.Name] = make(map[string]int64)
	}

	// Index configs by name for parent lookups.
	byName := make(map[string]*TableConfig, len(cfg.Tables))
	for i := range cfg.Tables {
		byName[cfg.Tables[i].Name] = &cfg.Tables[i]
	}

	// Monotonically decreasing counter for fake IDs during dry-run so that the
	// in-memory cache still dedups correctly without touching the DB.
	var fakeID int64 = -1

	for _, tcfg := range cfg.Tables {
		log.Printf("→ importing into %s", tcfg.Name)
		inserted, skipped := 0, 0

		for _, row := range rows {
			values := make(map[string]any, len(tcfg.Columns)+len(tcfg.FKs)+1)

			// 1. Direct column mappings: db_col ← row[csv_header].
			for dbCol, csvHeader := range tcfg.Columns {
				values[dbCol] = row[csvHeader]
			}

			// 2. FK lookups against existing DB tables (e.g. port_id from ports.name).
			for dbCol, fk := range tcfg.FKs {
				val := row[fk.CSV]
				var id int64
				q := fmt.Sprintf("SELECT id FROM %s WHERE %s = ?", fk.Table, fk.Match)
				if err := tx.QueryRow(q, val).Scan(&id); err != nil {
					if err == sql.ErrNoRows {
						return fmt.Errorf("table %s: no row in %s where %s = %q (from CSV column %q)",
							tcfg.Name, fk.Table, fk.Match, val, fk.CSV)
					}
					return fmt.Errorf("table %s: lookup %s.%s for %q: %w", tcfg.Name, fk.Table, fk.Match, val, err)
				}
				values[dbCol] = id
			}

			// 3. Parent FK injection from the in-memory cache.
			if tcfg.Parent != "" {
				parentCfg, ok := byName[tcfg.Parent]
				if !ok {
					return fmt.Errorf("table %s: parent %q not in config", tcfg.Name, tcfg.Parent)
				}
				key, err := buildCacheKey(parentCfg, row)
				if err != nil {
					return fmt.Errorf("table %s row %v: parent cache key: %w", tcfg.Name, row, err)
				}
				parentID, ok := cache[tcfg.Parent][key]
				if !ok {
					return fmt.Errorf("table %s: no cached ID for parent %s with key %q — "+
						"ensure the parent row was imported first and its cache_key/dedup_key is set correctly",
						tcfg.Name, tcfg.Parent, key)
				}
				values[tcfg.ParentFK] = parentID
			}

			// 4. Dedup: check in-memory cache first, then DB.
			if tcfg.DedupKey != nil {
				key, err := buildCacheKey(&tcfg, row)
				if err != nil {
					return fmt.Errorf("table %s row %v: cache key: %w", tcfg.Name, row, err)
				}

				if existingID, hit := cache[tcfg.Name][key]; hit {
					_ = existingID
					skipped++
					continue
				}

				dedupCols := toStringSlice(tcfg.DedupKey)
				conds := make([]string, len(dedupCols))
				dedupVals := make([]any, len(dedupCols))
				for i, col := range dedupCols {
					v, ok := values[col]
					if !ok {
						return fmt.Errorf("table %s: dedup_key column %q not present in resolved values", tcfg.Name, col)
					}
					conds[i] = col + " = ?"
					dedupVals[i] = v
				}
				q := fmt.Sprintf("SELECT id FROM %s WHERE %s", tcfg.Name, strings.Join(conds, " AND "))
				var existingID int64
				dbErr := tx.QueryRow(q, dedupVals...).Scan(&existingID)
				if dbErr == nil {
					cache[tcfg.Name][key] = existingID
					skipped++
					continue
				} else if dbErr != sql.ErrNoRows {
					return fmt.Errorf("table %s: dedup check: %w", tcfg.Name, dbErr)
				}
			}

			// 5. Build and execute INSERT.
			cols := make([]string, 0, len(values))
			placeholders := make([]string, 0, len(values))
			vals := make([]any, 0, len(values))
			for col, val := range values {
				cols = append(cols, col)
				placeholders = append(placeholders, "?")
				vals = append(vals, val)
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				tcfg.Name, strings.Join(cols, ", "), strings.Join(placeholders, ", "))

			if dryRun {
				log.Printf("  [dry-run] %s %v", q, vals)
				if tcfg.DedupKey != nil {
					key, _ := buildCacheKey(&tcfg, row)
					cache[tcfg.Name][key] = fakeID
					fakeID--
				}
				inserted++
				continue
			}

			result, err := tx.Exec(q, vals...)
			if err != nil {
				return fmt.Errorf("table %s: insert %v: %w", tcfg.Name, row, err)
			}
			newID, err := result.LastInsertId()
			if err != nil {
				return fmt.Errorf("table %s: last insert ID: %w", tcfg.Name, err)
			}
			if tcfg.DedupKey != nil {
				key, _ := buildCacheKey(&tcfg, row)
				cache[tcfg.Name][key] = newID
			}
			inserted++
		}

		log.Printf("  %s: %d inserted, %d skipped (dedup)", tcfg.Name, inserted, skipped)
	}

	if dryRun {
		log.Println("→ dry-run complete: rolling back (no changes persisted)")
		return nil
	}
	return tx.Commit()
}

// buildCacheKey constructs a "|"-joined string from the CSV values that
// uniquely identify a row in t, for use as an in-memory cache lookup key.
//
// Resolution order:
//  1. Use t.CacheKey CSV headers if specified.
//  2. Derive from t.DedupKey DB columns via the t.Columns map.
//  3. Error if neither can produce a key.
func buildCacheKey(t *TableConfig, row Row) (string, error) {
	var headers []string

	switch {
	case t.CacheKey != nil:
		headers = toStringSlice(t.CacheKey)

	case t.DedupKey != nil:
		for _, dbCol := range toStringSlice(t.DedupKey) {
			csvHeader, ok := t.Columns[dbCol]
			if !ok {
				return "", fmt.Errorf("dedup_key column %q has no entry in columns map; "+
					"add cache_key to the config for table %s", dbCol, t.Name)
			}
			headers = append(headers, csvHeader)
		}

	default:
		return "", fmt.Errorf("table %s has no cache_key or dedup_key", t.Name)
	}

	parts := make([]string, len(headers))
	for i, h := range headers {
		parts[i] = row[h]
	}
	return strings.Join(parts, "|"), nil
}

// toStringSlice normalises a YAML value that may be a bare string or a
// sequence of strings into a []string.
func toStringSlice(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		ss := make([]string, len(x))
		for i, e := range x {
			ss[i] = fmt.Sprint(e)
		}
		return ss
	default:
		return []string{fmt.Sprint(x)}
	}
}
