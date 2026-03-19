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

// Service orchestrates rate ingestion: DB access, optional LLM correction, and notifications.
type Service struct {
	DB     *sql.DB
	LLM    LLMCorrector                         // nil → NoopCorrector used in Parse
	Notify func(to, subject, body string) error // nil → skip email notifications
	// BaseURL is used to build comparison page links in notification emails.
	BaseURL string
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

// reMailto extracts email addresses from mailto: links (HTML and plain-text occurrences).
var reMailto = regexp.MustCompile(`mailto:([^\s"&?<>\r\n]+)`)

// reFromTo extracts email addresses from plain-text From: and To: header lines.
var reFromTo = regexp.MustCompile(`(?i)(?:^|\n)(?:from|to):\s*[^\n]*?([a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,})`)

// apiIngestRequest is the JSON body for POST /api/rates/parse.
type apiIngestRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Sender  string `json:"sender"`
}

// IngestEmail is the shared core for processing an inbound rate response email.
// Flow: parse body → match reference ID → verify sender is a known user →
// match vendor via mailto/From/To addresses in body → save or orphan.
// Returns a status string ("matched" or "orphaned") and any hard error.
func (s *Service) IngestEmail(subject, body, sender string) (string, error) {
	result, err := s.Parse(body)
	if err != nil {
		return "", fmt.Errorf("parse email: %w", err)
	}

	refMatch := reReferenceID.FindString(subject)
	if refMatch == "" {
		s.insertOrphan(body, subject, sender, 0)
		return "orphaned", nil
	}

	var rrID int
	err = s.DB.QueryRow(`SELECT id FROM rate_requests WHERE reference_id = ?`, refMatch).Scan(&rrID)
	if err == sql.ErrNoRows {
		s.insertOrphan(body, subject, sender, 0)
		return "orphaned", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup rate request %s: %w", refMatch, err)
	}

	var userExists int
	err = s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, sender).Scan(&userExists)
	if err != nil || userExists == 0 {
		s.insertOrphan(body, subject, sender, rrID)
		return "orphaned", nil
	}

	vendorID, err := s.matchVendorByBody(body, rrID)
	if err == sql.ErrNoRows {
		s.insertOrphan(body, subject, sender, rrID)
		return "orphaned", nil
	}
	if err != nil {
		return "", fmt.Errorf("match vendor by mailto for rr %d: %w", rrID, err)
	}

	if err := s.saveVendorRate(rrID, vendorID, body, "regex", result.Items); err != nil {
		return "", fmt.Errorf("save vendor rate rr=%d vendor=%d: %w", rrID, vendorID, err)
	}
	return "matched", nil
}

// matchVendorByBody scans body for mailto:, from:, and to: addresses and returns the first
// vendor_id whose contacts appear in that set, scoped to the given rate request.
// HTML bodies are scanned for mailto: links; plain-text bodies for From:/To: header lines.
// Returns (vendorID, nil) on first match, (0, sql.ErrNoRows) if none found.
func (s *Service) matchVendorByBody(body string, rrID int) (int, error) {
	lower := strings.ToLower(body)
	isHTML := strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "mailto:")

	var addrs []string
	if isHTML {
		for _, m := range reMailto.FindAllStringSubmatch(body, -1) {
			addrs = append(addrs, strings.ToLower(m[1]))
		}
	} else {
		for _, m := range reFromTo.FindAllStringSubmatch(body, -1) {
			addrs = append(addrs, strings.ToLower(m[1]))
		}
	}

	for _, addr := range addrs {
		var vendorID int
		err := s.DB.QueryRow(`
			SELECT vp.vendor_id
			FROM vendor_contacts vc
			JOIN vendor_ports vp ON vc.vendor_ports_id = vp.id
			JOIN rate_request_vendors rrv ON rrv.vendor_id = vp.vendor_id
			WHERE vc.email = ? AND rrv.rate_request_id = ?
			LIMIT 1
		`, addr, rrID).Scan(&vendorID)
		if err == nil {
			return vendorID, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}
	return 0, sql.ErrNoRows
}

// HandleAPIIngest parses an inbound email JSON payload and stores or orphans the result.
// POST /api/rates/parse
func (s *Service) HandleAPIIngest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apiIngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		status, err := s.IngestEmail(req.Subject, req.Body, req.Sender)
		if err != nil {
			http.Error(w, fmt.Sprintf("ingest error: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"status": status})
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
			http.Error(w, "Load rate request failed", http.StatusInternalServerError)
			return
		}

		vendors, err := s.fetchVendorsOnRR(rrID)
		if err != nil {
			http.Error(w, "Load vendors failed", http.StatusInternalServerError)
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
			http.Error(w, "Form parse failed", http.StatusBadRequest)
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
			http.Error(w, "Email parse failed", http.StatusInternalServerError)
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
			http.Error(w, "Begin transaction failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		res, err := tx.Exec(
			`INSERT INTO vendor_rates (rate_request_id, vendor_id, original_email, parsed_by) VALUES (?, ?, ?, ?)`,
			rrID, vendorID, rawEmail, parsedBy,
		)
		if err != nil {
			http.Error(w, "Insert vendor rate failed", http.StatusInternalServerError)
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
				http.Error(w, "Insert rate items failed", http.StatusInternalServerError)
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
			http.Error(w, "Commit rate failed", http.StatusInternalServerError)
			return
		}

		s.checkAndNotify(rrID)

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

	if err := tx.Commit(); err != nil {
		return err
	}

	s.checkAndNotify(rrID)
	return nil
}

// checkAndNotify fires a threshold notification email if the rate request has hit
// its response threshold and has not yet been notified. No-op if Notify is nil.
func (s *Service) checkAndNotify(rrID int) {
	if s.Notify == nil {
		return
	}

	var threshold sql.NullInt64
	var responsesReceived, notified int
	var refID, originPort, destination, direction, ownerEmail string
	var laneID int

	err := s.DB.QueryRow(`
		SELECT rr.response_threshold, rr.responses_received, rr.notified,
		       rr.reference_id, rr.lane_id,
		       p.name, l.destination, l.direction,
		       u.email
		FROM rate_requests rr
		JOIN lanes l ON l.id = rr.lane_id
		JOIN ports p ON p.id = l.origin_port_id
		JOIN users u ON u.id = l.owner_id
		WHERE rr.id = ?
	`, rrID).Scan(
		&threshold, &responsesReceived, &notified,
		&refID, &laneID,
		&originPort, &destination, &direction,
		&ownerEmail,
	)
	if err != nil || notified != 0 {
		return
	}
	if !threshold.Valid || responsesReceived < int(threshold.Int64) {
		return
	}

	// Mark notified before sending to prevent duplicate sends on concurrent ingests.
	if _, err := s.DB.Exec(`UPDATE rate_requests SET notified = 1 WHERE id = ? AND notified = 0`, rrID); err != nil {
		return
	}

	compURL := fmt.Sprintf("%s/rate-requests/%d/comparison", s.BaseURL, rrID)
	dir := "Import"
	if direction == "export" {
		dir = "Export"
	}
	subject := fmt.Sprintf("[Drayage Quoter] Rates ready — %s → %s — %s", originPort, destination, refID)
	body := fmt.Sprintf(
		"You've received %d rate response(s) for %s → %s (%s).\n\nView the comparison:\n%s",
		responsesReceived, originPort, destination, dir, compURL,
	)
	_ = s.Notify(ownerEmail, subject, body)
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
		return fmt.Sprintf("$%.0f", amount)
	case "%":
		return fmt.Sprintf("%.2f%%", amount)
	case "$/day":
		return fmt.Sprintf("$%.0f/day", amount)
	case "$/hour":
		return fmt.Sprintf("$%.0f/hr", amount)
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
		`<td class="cell-center-click {{.CSSClass}}" hx-get="/vendor-rate-items/{{.ID}}/edit" hx-trigger="click" hx-target="this" hx-swap="outerHTML" title="Click to edit">{{.Display}}</td>`,
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
			http.Error(w, "Load rate item failed", http.StatusInternalServerError)
			return
		}
		cellTmpl.Execute(w, v)

		// OOB: refresh the average cell for this charge type row.
		avg := s.computeChargeTypeAvg(v.RateRequestID, v.ChargeType)
		fmt.Fprintf(w,
			`<td id="avg-%s" hx-swap-oob="true" class="cell-avg">%s</td>`,
			v.ChargeType, avg)

		// OOB: refresh the vendor's total cell and the overall average total when linehaul or fuel changes.
		if v.ChargeType == "linehaul" || v.ChargeType == "fuel" {
			fmt.Fprintf(w,
				`<td id="total-%d" hx-swap-oob="true" class="cell-total">%s</td>`,
				v.VendorRateID, s.computeVendorTotal(v.VendorRateID))
			fmt.Fprintf(w,
				`<td id="avg-total" hx-swap-oob="true" class="cell-avg-total">%s</td>`,
				s.computeTotalAvg(v.RateRequestID))
		}
	}
}

// HandleEditRateItem returns an editable TD form for a vendor rate item.
// GET /vendor-rate-items/{id}/edit
func (s *Service) HandleEditRateItem() http.HandlerFunc {
	tmpl := template.Must(template.New("celledit").Parse(`<td class="cell-edit-form">
<form hx-post="/vendor-rate-items/{{.ID}}" hx-target="closest td" hx-swap="outerHTML" >
    <input type="text" name="amount" value="{{.Amount}}" class="input-amount">
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
			http.Error(w, "Load rate item failed", http.StatusInternalServerError)
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
			http.Error(w, "Update rate item failed", http.StatusInternalServerError)
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
		`<pre class="email-plain">{{.}}</pre>`,
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
			http.Error(w, "Load vendor email failed", http.StatusInternalServerError)
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

// HandleNewRateItem returns an inline create form for an empty cell (no existing item).
// GET /vendor-rates/{id}/items/new?charge_type=
func (s *Service) HandleNewRateItem() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || vrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		ct := strings.TrimSpace(r.URL.Query().Get("charge_type"))
		if ct == "" {
			http.Error(w, "charge_type required", http.StatusBadRequest)
			return
		}
		// Confirm vendor_rate exists.
		var exists int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM vendor_rates WHERE id = ?`, vrID).Scan(&exists); err != nil || exists == 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		unit := DefaultUnit(ct)
		fmt.Fprintf(w, `<td class="cell-edit-form">`+
		`<form hx-post="/vendor-rates/%d/items" hx-target="closest td" hx-swap="outerHTML" >`+
			`<input type="hidden" name="charge_type" value="%s">`+
			`<input type="hidden" name="unit" value="%s">`+
			`<input type="text" name="amount" placeholder="0" class="input-amount">`+
			`<button type="submit">✓</button>`+
			`<button type="button" hx-get="/vendor-rates/%d/items/empty?charge_type=%s" hx-target="closest td" hx-swap="outerHTML">✗</button>`+
			`</form></td>`,
			vrID, ct, unit, vrID, ct)
	}
}

// HandleCreateRateItem inserts a new vendor_rate_item and returns the display TD.
// POST /vendor-rates/{id}/items
func (s *Service) HandleCreateRateItem() http.HandlerFunc {
	cellTmpl := template.Must(template.New("newcell").Parse(
		`<td class="cell-center-click {{.CSSClass}}" hx-get="/vendor-rate-items/{{.ID}}/edit" hx-trigger="click" hx-target="this" hx-swap="outerHTML" title="Click to edit">{{.Display}}</td>`,
	))
	return func(w http.ResponseWriter, r *http.Request) {
		vrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || vrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		ct := strings.TrimSpace(r.FormValue("charge_type"))
		unit := strings.TrimSpace(r.FormValue("unit"))
		if ct == "" {
			http.Error(w, "charge_type required", http.StatusBadRequest)
			return
		}
		if unit == "" {
			unit = DefaultUnit(ct)
		}
		amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
		if err != nil {
			http.Error(w, "Invalid amount", http.StatusBadRequest)
			return
		}

		var rrID int
		if err := s.DB.QueryRow(`SELECT rate_request_id FROM vendor_rates WHERE id = ?`, vrID).Scan(&rrID); err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		res, err := s.DB.Exec(
			`INSERT INTO vendor_rate_items (vendor_rate_id, charge_type, amount, unit, manually_edited) VALUES (?, ?, ?, ?, 1)`,
			vrID, ct, amount, unit,
		)
		if err != nil {
			http.Error(w, "Failed to save rate item", http.StatusInternalServerError)
			return
		}
		newID, _ := res.LastInsertId()

		v, err := s.fetchRateItemView(int(newID))
		if err != nil {
			http.Error(w, "Failed to load rate item", http.StatusInternalServerError)
			return
		}
		cellTmpl.Execute(w, v)

		// OOB: refresh average for this charge type.
		avg := s.computeChargeTypeAvg(rrID, ct)
		fmt.Fprintf(w,
			`<td id="avg-%s" hx-swap-oob="true" class="cell-avg">%s</td>`,
			ct, avg)

		// OOB: refresh vendor total and overall average total if linehaul or fuel was added.
		if ct == "linehaul" || ct == "fuel" {
			fmt.Fprintf(w,
				`<td id="total-%d" hx-swap-oob="true" class="cell-total">%s</td>`,
				vrID, s.computeVendorTotal(vrID))
			fmt.Fprintf(w,
				`<td id="avg-total" hx-swap-oob="true" class="cell-avg-total">%s</td>`,
				s.computeTotalAvg(rrID))
		}
	}
}

// HandleEmptyRateItem returns a clickable empty TD for cancel on a new-item form.
// GET /vendor-rates/{id}/items/empty?charge_type=
func (s *Service) HandleEmptyRateItem() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || vrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		ct := strings.TrimSpace(r.URL.Query().Get("charge_type"))
		if ct == "" {
			http.Error(w, "charge_type required", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w,
		`<td class="cell-center-empty" hx-get="/vendor-rates/%d/items/new?charge_type=%s" hx-trigger="click" hx-target="this" hx-swap="outerHTML" title="Click to add">—</td>`,
			vrID, ct)
	}
}
