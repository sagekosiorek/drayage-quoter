package rates

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
)

// Service orchestrates rate ingestion: DB access and optional LLM correction.
type Service struct {
	DB  *sql.DB
	LLM LLMCorrector // nil → NoopCorrector used in Parse
}

// VendorOption is a minimal vendor row for dropdown rendering.
type VendorOption struct {
	ID   int
	Name string
}

// IngestFormData is the template data for the manual paste form.
type IngestFormData struct {
	RateRequestID int
	ReferenceID   string
	Vendors       []VendorOption
	PreVendorID   int // from ?vendor_id= query param
}

// IngestResultData is the template data for the parse-preview page.
type IngestResultData struct {
	RateRequestID int
	VendorID      int
	VendorName    string
	RawEmail      string
	Items         []RateItem
	Notes         []string
}

// Parse orchestrates the three-stage pipeline:
// strip HTML → ExtractRates (Stage 1) → LLM correction (Stage 2) → standardize (Stage 3).
func (s *Service) Parse(rawBody string) (ParseResult, error) {
	result := ExtractRates(rawBody)

	corrector := LLMCorrector(NoopCorrector{})
	if s.LLM != nil {
		corrector = s.LLM
	}

	corrected, err := corrector.CorrectRates(rawBody, result.Items)
	if err != nil {
		return result, err
	}
	result.Items = standardize(corrected)
	return result, nil
}

// reReferenceID matches a rate request reference ID embedded in an email subject.
var reReferenceID = regexp.MustCompile(`RR-\d{4}-\d{5}`)

// apiIngestRequest is the JSON body for POST /api/rates/parse.
type apiIngestRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Sender  string `json:"sender"`
}

// HandleAPIIngest parses an inbound email JSON payload and stores or orphans the result.
// POST /api/rates/parse
func (s *Service) HandleAPIIngest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apiIngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		result, err := s.Parse(req.Body)
		if err != nil {
			http.Error(w, "parse error", http.StatusInternalServerError)
			return
		}

		refMatch := reReferenceID.FindString(req.Subject)
		if refMatch == "" {
			s.insertOrphan(req.Body, req.Subject, req.Sender, 0)
			writeJSON(w, map[string]any{"status": "orphaned", "reason": "no reference ID in subject"})
			return
		}

		var rrID int
		err = s.DB.QueryRow(
			`SELECT id FROM rate_requests WHERE reference_id = ?`, refMatch,
		).Scan(&rrID)
		if err == sql.ErrNoRows {
			s.insertOrphan(req.Body, req.Subject, req.Sender, 0)
			writeJSON(w, map[string]any{"status": "orphaned", "reason": "reference ID not found"})
			return
		}
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		var vendorID int
		err = s.DB.QueryRow(`
			SELECT vp.vendor_id
			FROM vendor_contacts vc
			JOIN vendor_ports vp ON vc.vendor_ports_id = vp.id
			JOIN rate_request_vendors rrv ON rrv.vendor_id = vp.vendor_id
			WHERE vc.email = ? AND rrv.rate_request_id = ?
			LIMIT 1
		`, req.Sender, rrID).Scan(&vendorID)
		if err == sql.ErrNoRows {
			s.insertOrphan(req.Body, req.Subject, req.Sender, rrID)
			writeJSON(w, map[string]any{"status": "orphaned", "reason": "sender not matched to vendor on this rate request"})
			return
		}
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		if err := s.saveVendorRate(rrID, vendorID, req.Body, "regex", result.Items); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"status":          "matched",
			"rate_request_id": rrID,
			"vendor_id":       vendorID,
			"items_count":     len(result.Items),
		})
	}
}

// HandleIngestForm renders the manual paste form for a rate request.
// GET /rate-requests/{id}/ingest
func (s *Service) HandleIngestForm(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		var refID string
		err = s.DB.QueryRow(`SELECT reference_id FROM rate_requests WHERE id = ?`, rrID).Scan(&refID)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		vendors, err := s.fetchVendorsOnRR(rrID)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		preVendorID, _ := strconv.Atoi(r.URL.Query().Get("vendor_id"))

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User": user,
			"Data": IngestFormData{
				RateRequestID: rrID,
				ReferenceID:   refID,
				Vendors:       vendors,
				PreVendorID:   preVendorID,
			},
		})
	}
}

// HandleIngestSubmit parses the pasted email and renders a preview/edit table.
// POST /rate-requests/{id}/ingest
func (s *Service) HandleIngestSubmit(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		vendorID, _ := strconv.Atoi(r.FormValue("vendor_id"))
		rawEmail := strings.TrimSpace(r.FormValue("raw_email"))

		if rawEmail == "" {
			http.Error(w, "Email body required", http.StatusBadRequest)
			return
		}

		var vendorName string
		s.DB.QueryRow(`SELECT name FROM vendors WHERE id = ?`, vendorID).Scan(&vendorName)

		result, err := s.Parse(rawEmail)
		if err != nil {
			http.Error(w, "Parse error", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User": user,
			"Data": IngestResultData{
				RateRequestID: rrID,
				VendorID:      vendorID,
				VendorName:    vendorName,
				RawEmail:      rawEmail,
				Items:         result.Items,
				Notes:         result.Notes,
			},
		})
	}
}

// HandleIngestConfirm saves edited rate items to the DB and redirects to the rate request.
// POST /rate-requests/{id}/ingest/confirm
func (s *Service) HandleIngestConfirm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		vendorID, _ := strconv.Atoi(r.FormValue("vendor_id"))
		rawEmail := r.FormValue("raw_email")

		chargeTypes := r.Form["charge_type[]"]
		amountStrs := r.Form["amount[]"]
		units := r.Form["unit[]"]
		origAmountStrs := r.Form["original_amount[]"]

		// Build items; detect edits by comparing amount vs original_amount.
		anyEdited := false
		items := make([]RateItem, 0, len(chargeTypes))
		for i := range chargeTypes {
			amt, _ := strconv.ParseFloat(safeIdx(amountStrs, i), 64)
			origAmt, _ := strconv.ParseFloat(safeIdx(origAmountStrs, i), 64)
			edited := amt != origAmt
			if edited {
				anyEdited = true
			}
			manuallyEdited := 0
			if edited {
				manuallyEdited = 1
			}
			items = append(items, RateItem{
				ChargeType: safeIdx(chargeTypes, i),
				Amount:     amt,
				Unit:       safeIdx(units, i),
				Source:     "manual",
				Note:       strconv.Itoa(manuallyEdited), // reuse Note to carry flag into saveVendorRate
			})
		}

		parsedBy := "regex"
		if anyEdited {
			parsedBy = "manual"
		}

		tx, err := s.DB.Begin()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		res, err := tx.Exec(
			`INSERT INTO vendor_rates (rate_request_id, vendor_id, original_email, parsed_by) VALUES (?, ?, ?, ?)`,
			rrID, vendorID, rawEmail, parsedBy,
		)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		vrID, _ := res.LastInsertId()

		for _, item := range items {
			manuallyEdited, _ := strconv.Atoi(item.Note)
			_, err := tx.Exec(
				`INSERT INTO vendor_rate_items (vendor_rate_id, charge_type, amount, unit, manually_edited) VALUES (?, ?, ?, ?, ?)`,
				vrID, item.ChargeType, item.Amount, item.Unit, manuallyEdited,
			)
			if err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
		}

		tx.Exec(`UPDATE rate_request_vendors SET responded = 1 WHERE rate_request_id = ? AND vendor_id = ?`, rrID, vendorID)
		tx.Exec(`UPDATE rate_requests SET responses_received = responses_received + 1 WHERE id = ?`, rrID)
		tx.Exec(`
			UPDATE lanes SET status = 'rates_received', updated_at = ?
			WHERE id = (SELECT lane_id FROM rate_requests WHERE id = ?)
			  AND status = 'rates_requested'
		`, time.Now().UTC().Format("2006-01-02 15:04:05"), rrID)

		if err := tx.Commit(); err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/rate-requests/%d", rrID), http.StatusSeeOther)
	}
}

// fetchVendorsOnRR loads vendor options for a given rate request.
func (s *Service) fetchVendorsOnRR(rrID int) ([]VendorOption, error) {
	rows, err := s.DB.Query(`
		SELECT v.id, v.name
		FROM rate_request_vendors rrv
		JOIN vendors v ON v.id = rrv.vendor_id
		WHERE rrv.rate_request_id = ?
		ORDER BY v.name
	`, rrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var opts []VendorOption
	for rows.Next() {
		var o VendorOption
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			return nil, err
		}
		opts = append(opts, o)
	}
	return opts, nil
}

// saveVendorRate inserts a vendor_rates record and its items, then updates
// rate_request_vendors and rate_requests response counters.
func (s *Service) saveVendorRate(rrID, vendorID int, rawEmail, parsedBy string, items []RateItem) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO vendor_rates (rate_request_id, vendor_id, original_email, parsed_by) VALUES (?, ?, ?, ?)`,
		rrID, vendorID, rawEmail, parsedBy,
	)
	if err != nil {
		return err
	}
	vrID, _ := res.LastInsertId()

	for _, item := range items {
		if _, err := tx.Exec(
			`INSERT INTO vendor_rate_items (vendor_rate_id, charge_type, amount, unit) VALUES (?, ?, ?, ?)`,
			vrID, item.ChargeType, item.Amount, item.Unit,
		); err != nil {
			return err
		}
	}

	tx.Exec(`UPDATE rate_request_vendors SET responded = 1 WHERE rate_request_id = ? AND vendor_id = ?`, rrID, vendorID)
	tx.Exec(`UPDATE rate_requests SET responses_received = responses_received + 1 WHERE id = ?`, rrID)
	tx.Exec(`
		UPDATE lanes SET status = 'rates_received', updated_at = ?
		WHERE id = (SELECT lane_id FROM rate_requests WHERE id = ?)
		  AND status = 'rates_requested'
	`, time.Now().UTC().Format("2006-01-02 15:04:05"), rrID)

	return tx.Commit()
}

// insertOrphan stores an unmatched email for later manual assignment.
// rrID = 0 means no rate request could be identified.
func (s *Service) insertOrphan(rawEmail, subject, sender string, rrID int) {
	var assignedTo interface{}
	if rrID > 0 {
		assignedTo = rrID
	}
	s.DB.Exec(
		`INSERT INTO orphan_emails (raw_email, subject, sender, assigned_to_rate_request_id) VALUES (?, ?, ?, ?)`,
		rawEmail, subject, sender, assignedTo,
	)
}

// writeJSON writes a JSON response with Content-Type header.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// safeIdx returns s[i] or "" if i is out of bounds.
func safeIdx(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
