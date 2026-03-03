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
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/events"
)

// Service orchestrates rate ingestion: DB access and optional LLM correction.
type Service struct {
	DB     *sql.DB
	LLM    LLMCorrector   // nil → NoopCorrector used in Parse
	Broker *events.Broker // nil → no SSE publish
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
		statusRes, _ := tx.Exec(`
			UPDATE lanes SET status = 'rates_received', updated_at = ?
			WHERE id = (SELECT lane_id FROM rate_requests WHERE id = ?)
			  AND status = 'rates_requested'
		`, time.Now().UTC().Format("2006-01-02 15:04:05"), rrID)

		if err := tx.Commit(); err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if ra, _ := statusRes.RowsAffected(); ra > 0 && s.Broker != nil {
			var laneID int
			if s.DB.QueryRow(`SELECT lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(&laneID) == nil {
				s.Broker.Publish(fmt.Sprintf("lane:%d", laneID), "rates_received")
			}
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
	statusRes, _ := tx.Exec(`
		UPDATE lanes SET status = 'rates_received', updated_at = ?
		WHERE id = (SELECT lane_id FROM rate_requests WHERE id = ?)
		  AND status = 'rates_requested'
	`, time.Now().UTC().Format("2006-01-02 15:04:05"), rrID)

	if err := tx.Commit(); err != nil {
		return err
	}

	if ra, _ := statusRes.RowsAffected(); ra > 0 && s.Broker != nil {
		var laneID int
		if s.DB.QueryRow(`SELECT lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(&laneID) == nil {
			s.Broker.Publish(fmt.Sprintf("lane:%d", laneID), "rates_received")
		}
	}
	return nil
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

// FmtCellAmount formats an amount+unit pair for display in the comparison table.
func FmtCellAmount(amount float64, unit string) string {
	switch unit {
	case "$":
		return fmt.Sprintf("$%.2f", amount)
	case "%":
		return fmt.Sprintf("%.2f%%", amount)
	case "$/day":
		return fmt.Sprintf("$%.2f/day", amount)
	case "$/hour":
		return fmt.Sprintf("$%.2f/hr", amount)
	case "days":
		return fmt.Sprintf("%.0f days", amount)
	case "hours":
		return fmt.Sprintf("%.0f hrs", amount)
	default:
		return fmt.Sprintf("%.2f %s", amount, unit)
	}
}

// cellCSSClass returns the CSS class for a comparison table cell based on its parse origin.
func cellCSSClass(parsedBy string, manuallyEdited bool) string {
	if manuallyEdited {
		return "cell-edited"
	}
	if parsedBy == "llm" {
		return "cell-llm"
	}
	return ""
}

// rateItemView holds the data needed to render a comparison cell (display or edit mode).
type rateItemView struct {
	ID             int
	Amount         float64
	Unit           string
	ChargeType     string
	VendorRateID   int
	RateRequestID  int
	ManuallyEdited bool
	ParsedBy       string
	Display        string
	CSSClass       string
}

// fetchRateItemView loads a vendor_rate_item and computes display fields.
func (s *Service) fetchRateItemView(id int) (*rateItemView, error) {
	var v rateItemView
	var manuallyEdited int
	err := s.DB.QueryRow(
		`SELECT vri.id, vri.amount, vri.unit, vri.charge_type, vr.id, vr.rate_request_id, vri.manually_edited, vr.parsed_by
		 FROM vendor_rate_items vri
		 JOIN vendor_rates vr ON vr.id = vri.vendor_rate_id
		 WHERE vri.id = ?`, id,
	).Scan(&v.ID, &v.Amount, &v.Unit, &v.ChargeType, &v.VendorRateID, &v.RateRequestID, &manuallyEdited, &v.ParsedBy)
	if err != nil {
		return nil, err
	}
	v.ManuallyEdited = manuallyEdited != 0
	v.Display = FmtCellAmount(v.Amount, v.Unit)
	v.CSSClass = cellCSSClass(v.ParsedBy, v.ManuallyEdited)
	return &v, nil
}

// computeVendorTotal returns the formatted linehaul+fuel total for a single vendor rate record.
func (s *Service) computeVendorTotal(vrID int) string {
	var lh, fuel sql.NullFloat64
	s.DB.QueryRow(`
		SELECT
			MAX(CASE WHEN charge_type = 'linehaul' THEN amount END),
			MAX(CASE WHEN charge_type = 'fuel'     THEN amount END)
		FROM vendor_rate_items WHERE vendor_rate_id = ?
	`, vrID).Scan(&lh, &fuel)
	if !lh.Valid {
		return ""
	}
	fuelPct := 0.0
	if fuel.Valid {
		fuelPct = fuel.Float64
	}
	return FmtCellAmount(lh.Float64+lh.Float64*fuelPct/100, "$")
}

// computeChargeTypeAvg returns the formatted average amount for one charge type across all vendors on a rate request.
func (s *Service) computeChargeTypeAvg(rrID int, ct string) string {
	rows, err := s.DB.Query(`
		SELECT vri.amount, vri.unit
		FROM vendor_rate_items vri
		JOIN vendor_rates vr ON vr.id = vri.vendor_rate_id
		WHERE vr.rate_request_id = ? AND vri.charge_type = ?
	`, rrID, ct)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var total float64
	var unit string
	var count int
	for rows.Next() {
		var amount float64
		if rows.Scan(&amount, &unit) == nil {
			total += amount
			count++
		}
	}
	if count == 0 {
		return ""
	}
	return FmtCellAmount(total/float64(count), unit)
}

// computeTotalAvg returns the formatted average of (linehaul + linehaul*fuel%) across all vendors on a rate request.
func (s *Service) computeTotalAvg(rrID int) string {
	rows, err := s.DB.Query(`
		SELECT
			MAX(CASE WHEN vri.charge_type = 'linehaul' THEN vri.amount END),
			MAX(CASE WHEN vri.charge_type = 'fuel'     THEN vri.amount END)
		FROM vendor_rates vr
		JOIN vendor_rate_items vri ON vri.vendor_rate_id = vr.id
		WHERE vr.rate_request_id = ? AND vri.charge_type IN ('linehaul','fuel')
		GROUP BY vr.id
	`, rrID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var sum float64
	var count int
	for rows.Next() {
		var lh, fuel sql.NullFloat64
		if rows.Scan(&lh, &fuel) == nil && lh.Valid {
			fuelPct := 0.0
			if fuel.Valid {
				fuelPct = fuel.Float64
			}
			sum += lh.Float64 + lh.Float64*fuelPct/100
			count++
		}
	}
	if count == 0 {
		return ""
	}
	return FmtCellAmount(sum/float64(count), "$")
}

// HandleViewRateItem returns the display-mode TD for a vendor rate item (for HTMX swap or cancel).
// Also writes OOB swaps to update the average column for that charge type (and the total row for linehaul/fuel).
// GET /vendor-rate-items/{id}
func (s *Service) HandleViewRateItem() http.HandlerFunc {
	cellTmpl := template.Must(template.New("cell").Parse(
		`<td class="{{.CSSClass}}" hx-get="/vendor-rate-items/{{.ID}}/edit" hx-trigger="click" hx-target="this" hx-swap="outerHTML" style="text-align:center;cursor:pointer" title="Click to edit">{{.Display}}</td>`,
	))
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		v, err := s.fetchRateItemView(id)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		cellTmpl.Execute(w, v)

		// OOB: refresh the average cell for this charge type row.
		avg := s.computeChargeTypeAvg(v.RateRequestID, v.ChargeType)
		fmt.Fprintf(w,
			`<td id="avg-%s" hx-swap-oob="true" style="text-align:center;background:#f9fafb;font-style:italic;color:#555;">%s</td>`,
			v.ChargeType, avg)

		// OOB: refresh the vendor's total cell and the overall average total when linehaul or fuel changes.
		if v.ChargeType == "linehaul" || v.ChargeType == "fuel" {
			fmt.Fprintf(w,
				`<td id="total-%d" hx-swap-oob="true" style="text-align:center;font-weight:700;">%s</td>`,
				v.VendorRateID, s.computeVendorTotal(v.VendorRateID))
			fmt.Fprintf(w,
				`<td id="avg-total" hx-swap-oob="true" style="text-align:center;background:#f9fafb;font-style:italic;">%s</td>`,
				s.computeTotalAvg(v.RateRequestID))
		}
	}
}

// HandleEditRateItem returns an editable TD form for a vendor rate item.
// GET /vendor-rate-items/{id}/edit
func (s *Service) HandleEditRateItem() http.HandlerFunc {
	tmpl := template.Must(template.New("celledit").Parse(`<td class="cell-edit-form">
  <form hx-post="/vendor-rate-items/{{.ID}}" hx-target="closest td" hx-swap="outerHTML" style="display:flex;align-items:center;gap:4px">
    <input type="text" name="amount" value="{{.Amount}}" style="width:80px">
    <button type="submit">✓</button>
    <button type="button" hx-get="/vendor-rate-items/{{.ID}}" hx-target="closest td" hx-swap="outerHTML">✗</button>
  </form>
</td>`))
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		v, err := s.fetchRateItemView(id)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		tmpl.Execute(w, v)
	}
}

// HandleUpdateRateItem saves a manually edited rate item amount and returns the updated display TD.
// POST /vendor-rate-items/{id}
func (s *Service) HandleUpdateRateItem() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
		if err != nil {
			http.Error(w, "Invalid amount", http.StatusBadRequest)
			return
		}
		if _, err := s.DB.Exec(
			`UPDATE vendor_rate_items SET amount = ?, manually_edited = 1 WHERE id = ?`,
			amount, id,
		); err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		// Delegate to HandleViewRateItem to render the updated display cell.
		s.HandleViewRateItem()(w, r)
	}
}

// HandleViewOriginalEmail returns the raw email content for a vendor rate record.
// HTML emails are rendered in a sandboxed iframe; plain text is shown in a <pre>.
// GET /vendor-rates/{id}/email
func (s *Service) HandleViewOriginalEmail() http.HandlerFunc {
	plainTmpl := template.Must(template.New("email-plain").Parse(
		`<pre style="white-space:pre-wrap;word-break:break-word;font-size:.78rem;font-family:monospace;margin:0">{{.}}</pre>`,
	))
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		var rawEmail string
		err = s.DB.QueryRow(`SELECT original_email FROM vendor_rates WHERE id = ?`, id).Scan(&rawEmail)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		lower := strings.ToLower(rawEmail)
		if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") {
			// Render HTML emails in a sandboxed iframe (scripts blocked, CSS isolated).
			// srcdoc value must be HTML-attribute-escaped so the browser re-parses it correctly.
			fmt.Fprintf(w,
				`<iframe srcdoc="%s" sandbox style="width:100%%;border:none;height:520px;" title="Email content"></iframe>`,
				template.HTMLEscapeString(rawEmail))
		} else {
			plainTmpl.Execute(w, rawEmail)
		}
	}
}
