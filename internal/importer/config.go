// Package importer maps a CSV or Excel file to one or more SQLite tables using
// a declarative YAML config. Tables are processed in config order so parent
// rows are always inserted before their children.
package importer

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config describes how a single file maps to one or more DB tables.
type Config struct {
	Tables []TableConfig `yaml:"tables"`
}

// TableConfig maps a group of CSV columns to a single DB table.
// Tables must appear in dependency order: parents before children.
type TableConfig struct {
	Name string `yaml:"name"`

	// DedupKey is the DB column (or list of columns) used to detect duplicates.
	// Rows that already exist (by this key) are skipped; their IDs are cached so
	// child tables can still reference them. Omit to insert every row unconditionally.
	DedupKey any `yaml:"dedup_key"` // string | []string

	// CacheKey lists CSV headers whose values form the cache lookup key for this
	// table. Required when child tables reference this one as a parent. Defaults
	// to the CSV headers for DedupKey columns (via the Columns map) when possible.
	CacheKey any `yaml:"cache_key"` // string | []string (CSV headers)

	// Parent is the name of a previously-processed table whose inserted ID should
	// be injected as ParentFK in this table's rows.
	Parent   string `yaml:"parent"`
	ParentFK string `yaml:"parent_fk"`

	// Columns maps DB column names to CSV header names for direct value copies.
	Columns map[string]string `yaml:"columns"` // db_col → csv_header

	// FKs resolves FK columns by SELECT-ing from an existing DB table.
	// Use this for reference tables (e.g. ports) that are not part of the import.
	FKs map[string]FKLookup `yaml:"foreign_keys"`
}

// FKLookup resolves a single FK column by matching a CSV value against an
// existing table row.
type FKLookup struct {
	Table string `yaml:"table"` // DB table to look up
	Match string `yaml:"match"` // DB column to match against
	CSV   string `yaml:"csv"`   // CSV header whose value to match
}

// LoadConfig reads and parses a YAML config file.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
