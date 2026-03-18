package importer

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Row is a single data record keyed by CSV header name.
type Row map[string]string

// ReadFile reads a .csv or .xlsx file and returns its data rows.
// The first row must be headers; all subsequent rows are returned as Row maps.
func ReadFile(path string) ([]Row, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return readCSV(path)
	case ".xlsx":
		return readXLSX(path)
	default:
		return nil, fmt.Errorf("unsupported file format %q: expected .csv or .xlsx", filepath.Ext(path))
	}
}

func readCSV(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return recordsToRows(records, path)
}

func readXLSX(path string) ([]Row, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("%s contains no sheets", path)
	}
	records, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read sheet %q from %s: %w", sheets[0], path, err)
	}
	return recordsToRows(records, path)
}

// recordsToRows converts a 2-D string slice (row 0 = headers) into Row maps.
func recordsToRows(records [][]string, path string) ([]Row, error) {
	if len(records) < 2 {
		return nil, fmt.Errorf("%s has no data rows (only %d line(s))", path, len(records))
	}
	headers := records[0]
	rows := make([]Row, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(Row, len(headers))
		for i, h := range headers {
			if i < len(record) {
				row[strings.TrimSpace(h)] = strings.TrimSpace(record[i])
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
