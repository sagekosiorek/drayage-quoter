package rate_requests

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rates"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/settings"
)

// Service handles rate request operations.
type Service struct {
	DB *sql.DB
}

// LaneSnippet holds lean lane info for display in rate request views.
type LaneSnippet struct {
	ID             int
	CustomerName   string
	OriginPortID   int
	OriginPort     string
	OriginPortType string
	Destination    string
	Direction      string
	ContainerSize  string
	LoadType       string
	Commodity      *string
	Weight         *int
	Notes          *string
	Hazmat         bool
	Overweight     bool
	OutOfGauge     bool
	Status         string
	StatusLabel    string
}

// ContactInfo holds minimal vendor contact data.
type ContactInfo struct {
	Name  string
	Email string
}

// VendorSelectRow holds per-vendor checklist data for the new rate request form.
type VendorSelectRow struct {
	VendorID     int
	VendorPortID int
	VendorName   string
	Contacts     []ContactInfo
	Preferred    bool
}

// VendorBlastRow holds per-vendor blast status for the rate request detail view.
type VendorBlastRow struct {
	VendorID     int
	VendorName   string
	Contacts     []ContactInfo
	Responded    bool
	MailtoHref   string // pre-built mailto: link (emails, subject, body all pre-filled)
	GreetingName string // replaces [First Name] in the body (e.g. "Joe", "Joe and Mary")
}

// RateRequestDetail holds the full rate request record for the detail view.
type RateRequestDetail struct {
	ID                int
	LaneID            int
	ReferenceID       string
	Subject           string
	Body              string
	Threshold         *int
	Deadline          string
	ResponsesReceived int
	Notified          bool
	CreatedAt         string
	Lane              LaneSnippet
	Vendors           []VendorBlastRow
}

// ctLabel converts a snake_case charge type key to a human-readable label with unit hint.
func ctLabel(ct string) string {
	labels := map[string]string{
		"linehaul":           "Linehaul",
		"fuel":               "Fuel (%)",
		"chassis":            "Chassis ($/day)",
		"chassis_min":        "Chassis Min (days)",
		"detention":          "Detention ($/hr)",
		"detention_free":     "Detention Free (hrs)",
		"storage":            "Storage ($/day)",
		"yard_pull":          "Yard Pull",
		"chassis_split":      "Chassis Split",
		"mount":              "Mount",
		"lift":               "Lift",
		"redelivery":         "Redelivery",
		"dry_run":            "Dry Run",
		"toll":               "Toll",
		"triaxle":            "Triaxle ($/day)",
		"extreme_overweight": "Extreme Overweight",
		"regular_overweight": "Regular Overweight",
		"reefer":             "Reefer",
		"genset":             "Genset ($/day)",
		"hazmat":             "Hazmat",
		"stop_off":           "Stop Off",
		"layover":            "Layover",
		"drop":               "Drop",
		"scale":              "Scale",
		"congestion":         "Congestion ($/hr)",
		"congestion_free":    "Congestion Free (hrs)",
		"gate":               "Gate",
	}
	if l, ok := labels[ct]; ok {
		return l
	}
	return strings.ReplaceAll(ct, "_", " ")
}

// applicableChargeTypes lists the standard expected charges shown in the top XLSX section.
// All other entered charges fall under "Accessorials (only if needed)".
var applicableChargeTypes = map[string]bool{
	"linehaul_fuel": true,
	"chassis":       true,
	"chassis_min":   true,
}

// csvLabel returns a clean charge-type label for the XLSX quote (no unit hints in parens).
func csvLabel(ct string) string {
	labels := map[string]string{
		"linehaul_fuel":      "Linehaul",
		"chassis":            "Chassis",
		"chassis_min":        "Chassis Minimum",
		"detention":          "Detention",
		"detention_free":     "Detention Free Time",
		"storage":            "Storage",
		"yard_pull":          "Prepull",
		"chassis_split":      "Chassis Split",
		"mount":              "Mount",
		"lift":               "Lift",
		"redelivery":         "Redelivery",
		"dry_run":            "Dry Run",
		"toll":               "Toll",
		"triaxle":            "Triaxle",
		"extreme_overweight": "Extreme Overweight",
		"regular_overweight": "Regular Overweight",
		"reefer":             "Reefer",
		"genset":             "Genset",
		"hazmat":             "Hazmat",
		"stop_off":           "Stop Off",
		"layover":            "Layover",
		"drop":               "Drop",
		"scale":              "Scale",
		"congestion":         "Congestion",
		"congestion_free":    "Congestion Free Time",
		"gate":               "Gate",
	}
	if l, ok := labels[ct]; ok {
		return l
	}
	return strings.ReplaceAll(ct, "_", " ")
}

// csvValue formats a charge amount for the XLSX value column.
// Time/count-based charges render as a plain integer; all others as "$X.XX".
func csvValue(ct string, val float64) string {
	switch ct {
	case "detention_free", "congestion_free", "chassis_min":
		return fmt.Sprintf("%.0f", val)
	}
	return fmt.Sprintf("$%.2f", val)
}

// csvNotes returns the per-unit context string for the XLSX Notes column.
func csvNotes(ct string) string {
	notes := map[string]string{
		"linehaul_fuel":   "fuel included",
		"chassis":         "per day",
		"chassis_min":     "days minimum",
		"detention":       "per hour",
		"detention_free":  "hours",
		"storage":         "per day",
		"triaxle":         "per day",
		"genset":          "per day",
		"congestion":      "per hour",
		"congestion_free": "hours",
	}
	return notes[ct]
}

// xlCell converts 1-based (col, row) coordinates to an Excel cell address (e.g. "B3").
func xlCell(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

// generateReferenceID produces the next sequential reference ID for the current year.
func generateReferenceID(db *sql.DB) (string, error) {
	year := time.Now().Year()
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM rate_requests WHERE reference_id LIKE ?`,
		fmt.Sprintf("RR-%d-%%", year),
	).Scan(&count)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("RR-%d-%05d", year, count+1), nil
}

// buildSubjectBase generates the lane-specific portion of the subject line (no reference ID).
// The reference ID is appended by HandleCreate so it only appears on saved records.
func buildSubjectBase(lane *LaneSnippet) string {
	dir := "Import"
	typ := "Port"
	if lane.Direction == "export" {
		dir = "Export"
	}
	if lane.OriginPortType == "Rail Ramp" {
		typ = "Rail Ramp"
	}
	return fmt.Sprintf("Rate Request: %s %s - %s - %s",
		lane.OriginPort, typ, lane.Destination, dir)
}

// buildBody generates the rate request email body using the user's saved template.
// The [First Name] placeholder is replaced per-vendor at mailto generation time.
func buildBody(lane *LaneSnippet, refID string, tmpl settings.EmailTemplate) string {
	var sb strings.Builder

	dir := "Import"
	if lane.Direction == "export" {
		dir = "Export"
	}
	load := "Live"
	if lane.LoadType == "drop_pick" {
		load = "Drop / Pick"
	}

	fmt.Fprintf(&sb, "%s [First Name],\n\n", tmpl.Greeting)
	fmt.Fprintf(&sb, "%s\n\n", tmpl.Body)
	fmt.Fprintf(&sb, "Reference:       %s\n", refID)
	fmt.Fprintf(&sb, "Origin Port:     %s (%s)\n", lane.OriginPort, lane.OriginPortType)
	fmt.Fprintf(&sb, "Destination:     %s\n", lane.Destination)
	fmt.Fprintf(&sb, "Direction:       %s\n", dir)
	fmt.Fprintf(&sb, "Container Size:  %s\n", containerSizeDisplay(lane.ContainerSize))
	fmt.Fprintf(&sb, "Load Type:       %s\n", load)

	if lane.Commodity != nil && *lane.Commodity != "" {
		fmt.Fprintf(&sb, "Commodity:       %s\n", *lane.Commodity)
	}
	if lane.Weight != nil {
		fmt.Fprintf(&sb, "Weight:          %d lbs\n", *lane.Weight)
	}

	var specials []string
	if lane.Hazmat {
		specials = append(specials, "Hazmat")
	}
	if lane.Overweight {
		specials = append(specials, "Overweight")
	}
	if lane.OutOfGauge {
		specials = append(specials, "Out of Gauge")
	}
	if len(specials) > 0 {
		fmt.Fprintf(&sb, "Special Reqs:    %s\n", strings.Join(specials, ", "))
	}

	if lane.Notes != nil && *lane.Notes != "" {
		fmt.Fprintf(&sb, "\nNotes:\n%s\n", *lane.Notes)
	}

	if tmpl.Closing != "" || tmpl.Signature != "" {
		fmt.Fprintf(&sb, "\n%s\n%s\n", tmpl.Closing, tmpl.Signature)
	}

	return sb.String()
}

// containerSizeDisplay converts a DB value to a display string.
func containerSizeDisplay(v string) string {
	switch v {
	case "20_40":
		return "20' / 40'"
	case "20":
		return "20'"
	case "40":
		return "40'"
	case "40HC":
		return "40' High Cube"
	default:
		return v
	}
}

// laneStatusLabel returns the human-readable label for a lane status value.
func laneStatusLabel(s string) string {
	switch s {
	case "draft":
		return "Draft"
	case "rates_requested":
		return "Rates Requested"
	case "rates_received":
		return "Rates Received"
	case "quoting":
		return "Quoting"
	case "quoted":
		return "Quoted"
	default:
		return s
	}
}

// fmtDate parses a SQLite DATETIME string into a UTC RFC3339 string for client-side formatting.
// Falls back to the raw string on parse failure.
func fmtDate(s string) string {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

// formatDeadline parses a stored deadline into a UTC RFC3339 string for client-side formatting.
// Accepts multiple input layouts; falls back to raw string.
func formatDeadline(s string) string {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}

// encodeMailtoParam percent-encodes a string for use in a mailto: URI query value.
// Uses %20 for spaces (not +, which is form-encoding).
func encodeMailtoParam(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// encodeMailtoBody percent-encodes a body string for use in a mailto: URI.
// Normalises line endings to CRLF (%0D%0A) so email clients (including Outlook)
// preserve paragraph breaks instead of collapsing everything to one line.
func encodeMailtoBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n") // strip any existing CRs first
	s = strings.ReplaceAll(s, "\n", "\r\n") // then normalise to CRLF
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// buildGreetingName returns a natural-language greeting from all contact first names.
// 0 contacts → "there"; 1 → "Joe"; 2 → "Joe and Mary"; 3+ → "Joe, Mary, and Maggie"
func buildGreetingName(contacts []ContactInfo) string {
	var names []string
	for _, c := range contacts {
		if c.Name != "" {
			if parts := strings.Fields(c.Name); len(parts) > 0 {
				names = append(names, parts[0])
			}
		}
	}
	switch len(names) {
	case 0:
		return "there"
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// fetchLane loads a lean LaneSnippet for a given lane ID.
func (s *Service) fetchLane(laneID int) (*LaneSnippet, error) {
	var lane LaneSnippet
	var hazmat, overweight, outOfGauge int

	err := s.DB.QueryRow(`
		SELECT l.id, p.id, p.name, p.type, l.destination, l.direction,
		       l.container_size, l.load_type, l.commodity, l.weight,
		       l.notes, l.hazmat, l.overweight, l.out_of_gauge, l.status, c.name
		FROM lanes l
		JOIN ports p ON p.id = l.origin_port_id
		JOIN customers c ON c.id = l.customer_id
		WHERE l.id = ?
	`, laneID).Scan(
		&lane.ID, &lane.OriginPortID, &lane.OriginPort, &lane.OriginPortType,
		&lane.Destination, &lane.Direction,
		&lane.ContainerSize, &lane.LoadType, &lane.Commodity, &lane.Weight,
		&lane.Notes, &hazmat, &overweight, &outOfGauge, &lane.Status, &lane.CustomerName,
	)
	if err != nil {
		return nil, err
	}
	lane.Hazmat = hazmat != 0
	lane.Overweight = overweight != 0
	lane.OutOfGauge = outOfGauge != 0
	lane.StatusLabel = laneStatusLabel(lane.Status)
	return &lane, nil
}

// fetchVendorsForPort loads all vendors servicing a port with contacts and preference flag.
// Preferred vendors appear first; within groups, sorted by name.
func (s *Service) fetchVendorsForPort(portID, userID int) ([]VendorSelectRow, error) {
	rows, err := s.DB.Query(`
		SELECT vp.id, v.id, v.name,
			EXISTS(
				SELECT 1 FROM vendor_preferences pref
				WHERE pref.vendor_id = v.id AND pref.user_id = ? AND pref.port_id = ?
			) as preferred
		FROM vendor_ports vp
		JOIN vendors v ON v.id = vp.vendor_id
		WHERE vp.port_id = ?
		ORDER BY preferred DESC, v.name
	`, userID, portID, portID)
	if err != nil {
		return nil, err
	}

	var vendors []VendorSelectRow
	for rows.Next() {
		var vsr VendorSelectRow
		var preferred int
		if err := rows.Scan(&vsr.VendorPortID, &vsr.VendorID, &vsr.VendorName, &preferred); err != nil {
			rows.Close()
			return nil, err
		}
		vsr.Preferred = preferred != 0
		vendors = append(vendors, vsr)
	}
	rows.Close()

	for i := range vendors {
		cRows, err := s.DB.Query(
			`SELECT COALESCE(name,''), email FROM vendor_contacts WHERE vendor_ports_id = ? ORDER BY id`,
			vendors[i].VendorPortID,
		)
		if err != nil {
			return nil, err
		}
		for cRows.Next() {
			var c ContactInfo
			if err := cRows.Scan(&c.Name, &c.Email); err != nil {
				cRows.Close()
				return nil, err
			}
			vendors[i].Contacts = append(vendors[i].Contacts, c)
		}
		cRows.Close()
	}
	return vendors, nil
}

// fetchRateRequestDetail loads the full rate request record with lane snippet and vendor blast rows.
func (s *Service) fetchRateRequestDetail(id int) (*RateRequestDetail, error) {
	var rr RateRequestDetail
	var threshold sql.NullInt64
	var deadline sql.NullString
	var notified int
	var createdStr string

	err := s.DB.QueryRow(`
		SELECT id, lane_id, reference_id, subject, body,
		       response_threshold, deadline, responses_received, notified, created_at
		FROM rate_requests WHERE id = ?
	`, id).Scan(
		&rr.ID, &rr.LaneID, &rr.ReferenceID, &rr.Subject, &rr.Body,
		&threshold, &deadline, &rr.ResponsesReceived, &notified, &createdStr,
	)
	if err != nil {
		return nil, err
	}

	if threshold.Valid {
		v := int(threshold.Int64)
		rr.Threshold = &v
	}
	if deadline.Valid {
		rr.Deadline = formatDeadline(deadline.String)
	}
	rr.Notified = notified != 0
	rr.CreatedAt = fmtDate(createdStr)

	lane, err := s.fetchLane(rr.LaneID)
	if err != nil {
		return nil, err
	}
	rr.Lane = *lane

	vRows, err := s.DB.Query(`
		SELECT v.id, v.name, rrv.responded
		FROM rate_request_vendors rrv
		JOIN vendors v ON v.id = rrv.vendor_id
		WHERE rrv.rate_request_id = ?
		ORDER BY v.name
	`, id)
	if err != nil {
		return nil, err
	}

	var vendorList []VendorBlastRow
	for vRows.Next() {
		var vbr VendorBlastRow
		var responded int
		if err := vRows.Scan(&vbr.VendorID, &vbr.VendorName, &responded); err != nil {
			vRows.Close()
			return nil, err
		}
		vbr.Responded = responded != 0
		vendorList = append(vendorList, vbr)
	}
	vRows.Close()

	for i := range vendorList {
		cRows, err := s.DB.Query(`
			SELECT COALESCE(vc.name,''), vc.email
			FROM vendor_contacts vc
			JOIN vendor_ports vp ON vc.vendor_ports_id = vp.id
			WHERE vp.vendor_id = ? AND vp.port_id = ?
			ORDER BY vc.id
		`, vendorList[i].VendorID, rr.Lane.OriginPortID)
		if err != nil {
			return nil, err
		}
		for cRows.Next() {
			var c ContactInfo
			if err := cRows.Scan(&c.Name, &c.Email); err != nil {
				cRows.Close()
				return nil, err
			}
			vendorList[i].Contacts = append(vendorList[i].Contacts, c)
		}
		cRows.Close()

		var emails []string
		for _, c := range vendorList[i].Contacts {
			if c.Email != "" {
				emails = append(emails, c.Email)
			}
		}
		vendorList[i].GreetingName = buildGreetingName(vendorList[i].Contacts)
		if len(emails) > 0 {
			bodyFilled := strings.ReplaceAll(rr.Body, "[First Name]", vendorList[i].GreetingName)
			vendorList[i].MailtoHref = "mailto:" + strings.Join(emails, "; ") +
				"?subject=" + encodeMailtoParam(rr.Subject) +
				"&body=" + encodeMailtoBody(bodyFilled)
		}
	}

	rr.Vendors = vendorList
	return &rr, nil
}

// HandleNewForm renders the rate request creation form for a given lane.
// GET /lanes/{id}/rate-request/new
func (s *Service) HandleNewForm(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		laneID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || laneID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		lane, err := s.fetchLane(laneID)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Load lane failed", http.StatusInternalServerError)
			return
		}

		refID, err := generateReferenceID(s.DB)
		if err != nil {
			http.Error(w, "Generate reference ID failed", http.StatusInternalServerError)
			return
		}

		vendors, err := s.fetchVendorsForPort(lane.OriginPortID, user.ID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to load vendors: %v", err), http.StatusInternalServerError)
			return
		}

		emailTmpl := settings.FetchTemplate(s.DB, user.ID)

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":    user,
			"Lane":    lane,
			"RefID":   refID,
			"Subject": buildSubjectBase(lane),
			"Body":    buildBody(lane, refID, emailTmpl),
			"Vendors": vendors,
		})
	}
}

// HandleCreate saves a new rate request, links vendors, advances lane status, and redirects to the detail view.
// POST /lanes/{id}/rate-request
func (s *Service) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		laneID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || laneID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		subject := strings.TrimSpace(r.FormValue("subject"))
		body := strings.TrimSpace(r.FormValue("body"))
		thresholdStr := r.FormValue("response_threshold")
		deadlineHoursStr := r.FormValue("deadline_hours")
		vendorIDStrs := r.Form["vendor_ids"]

		if subject == "" || body == "" {
			http.Error(w, "Subject and body are required", http.StatusBadRequest)
			return
		}

		// Re-generate at save time to handle concurrent requests.
		refID, err := generateReferenceID(s.DB)
		if err != nil {
			http.Error(w, "Generate reference ID failed", http.StatusInternalServerError)
			return
		}

		// Append the reference ID to the subject so it only appears on saved records.
		finalSubject := subject + " - " + refID

		var threshold interface{}
		if t, err := strconv.Atoi(thresholdStr); err == nil && t > 0 {
			if n := len(vendorIDStrs); t > n {
				t = n // clamp: threshold cannot exceed vendors blasted
			}
			threshold = t
		}

		// Deadline is expressed as hours from now; compute the absolute timestamp on save.
		var deadline interface{}
		if h, err := strconv.Atoi(deadlineHoursStr); err == nil && h > 0 {
			deadline = time.Now().UTC().Add(time.Duration(h) * time.Hour).Format("2006-01-02 15:04:05")
		}

		res, err := s.DB.Exec(`
			INSERT INTO rate_requests (lane_id, reference_id, subject, body, response_threshold, deadline)
			VALUES (?, ?, ?, ?, ?, ?)
		`, laneID, refID, finalSubject, body, threshold, deadline)
		if err != nil {
			http.Error(w, "Insert rate request failed", http.StatusInternalServerError)
			return
		}

		rrID, _ := res.LastInsertId()

		for _, vidStr := range vendorIDStrs {
			vid, err := strconv.Atoi(vidStr)
			if err != nil || vid <= 0 {
				continue
			}
			s.DB.Exec(
				`INSERT OR IGNORE INTO rate_request_vendors (rate_request_id, vendor_id) VALUES (?, ?)`,
				rrID, vid,
			)
		}

		s.DB.Exec(
			`UPDATE lanes SET status = 'rates_requested', updated_at = ? WHERE id = ? AND status = 'draft'`,
			time.Now().UTC().Format("2006-01-02 15:04:05"), laneID,
		)

		http.Redirect(w, r, fmt.Sprintf("/rate-requests/%d", rrID), http.StatusSeeOther)
	}
}

// --- Comparison + Lineup types ---

// RateRequestSnippet is minimal rate request info for the comparison page header.
type RateRequestSnippet struct {
	ID          int
	ReferenceID string
	LaneID      int
}

// ChargeTypeRow is one row label in the comparison table.
type ChargeTypeRow struct {
	Key   string // canonical key, e.g. "chassis_min"
	Label string // display label, e.g. "Chassis Min (days)"
}

// RateItemCell is one editable cell in the comparison table.
type RateItemCell struct {
	ID             int
	Amount         float64
	Unit           string
	ManuallyEdited bool
	ParsedBy       string // "regex" | "llm" | "manual"
	Display        string // pre-formatted for display, e.g. "$1,200.00" or "35%"
}

// VendorColumn holds one vendor's column in the comparison table.
type VendorColumn struct {
	VendorRateID int
	VendorName   string
	Total        float64                 // linehaul + (linehaul * fuel / 100)
	TotalDisplay string                  // pre-formatted total for display
	Items        map[string]RateItemCell // keyed by charge_type
}

// ComparisonData is template data for rate_comparison.html.
type ComparisonData struct {
	RateRequest          RateRequestSnippet
	Lane                 LaneSnippet
	ChargeRows           []ChargeTypeRow   // ordered; only types present in ≥1 vendor
	Vendors              []VendorColumn    // sorted ascending by Total
	Averages             map[string]string // pre-formatted average per charge_type
	AvgTotal             string            // pre-formatted average of vendor totals
	Lineups              map[int]int       // vendor_rate_id → current rank (0 = unranked)
	AvailableChargeTypes []ChargeTypeRow   // FieldOrder types not yet in ChargeRows
}

// HandleDetail renders the rate request detail view with blast status and mailto links.
// GET /rate-requests/{id}
func (s *Service) HandleDetail(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		rr, err := s.fetchRateRequestDetail(id)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Load rate request failed", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":        user,
			"RateRequest": rr,
		})
	}
}

// HandleBlastStatus returns an HTML fragment for the blast status panel and an OOB swap for the
// responses count span. Intended for HTMX polling: GET /rate-requests/{id}/blast-status
// Polling continues while lane status is rates_requested or rates_received.
func (s *Service) HandleBlastStatus() http.HandlerFunc {
	tmpl := template.Must(template.New("blast").Parse(`
{{- if .Polling -}}
<span id="responses-count" hx-swap-oob="true"><strong>Responses:</strong> {{.Received}} / {{.Total}}</span>
{{- end -}}
<p class="detail-section">Blast Status</p>
{{if .Vendors}}
<table>
<thead><tr><th>Vendor</th><th>Status</th><th>Actions</th></tr></thead>
<tbody>
{{range .Vendors}}<tr class="{{if .Responded}}rr-responded{{end}}">
<td>
<div style="font-weight:500;">{{.VendorName}}</div>
{{range .Contacts}}<div class="rr-vendor-item-meta">{{.Name}}{{if and .Name .Email}} · {{end}}<a href="mailto:{{.Email}}">{{.Email}}</a></div>{{end}}
{{if not .Contacts}}<div class="rr-vendor-item-warn">(no contact on file)</div>{{end}}
</td>
<td>{{if .Responded}}<span class="text-success">Responded</span>{{else}}<span class="text-muted">Pending</span>{{end}}</td>
<td><div class="flex-wrap-sm">
{{if .MailtoHref}}<a href="{{.MailtoHref}}"><button class="primary">Open Email Draft</button></a>{{end}}
<a href="/rate-requests/{{$.RRID}}/ingest?vendor_id={{.VendorID}}"><button type="button">Paste Response</button></a>
</div></td>
</tr>{{end}}
</tbody>
</table>
{{else}}<p class="empty-state">No vendors on this rate request.</p>{{end}}`))

	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		rr, err := s.fetchRateRequestDetail(id)
		if err == sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("rate request %d not found", id), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("load blast status for rate request %d: %v", id, err), http.StatusInternalServerError)
			return
		}
		polling := rr.Lane.Status == "rates_requested" || rr.Lane.Status == "rates_received"
		w.Header().Set("Content-Type", "text/html")
		tmpl.Execute(w, map[string]any{
			"RRID":     rr.ID,
			"Received": rr.ResponsesReceived,
			"Total":    len(rr.Vendors),
			"Vendors":  rr.Vendors,
			"Polling":  polling,
		})
	}
}

// HandleResponsesCount returns an HTML fragment with the current responses count for a rate request.
// Intended for HTMX polling on the lane detail page: GET /rate-requests/{id}/responses-count
// Polling continues while lane status is rates_requested or rates_received.
func (s *Service) HandleResponsesCount() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		var received int
		var laneStatus string
		err = s.DB.QueryRow(`
			SELECT rr.responses_received, l.status
			FROM rate_requests rr
			JOIN lanes l ON l.id = rr.lane_id
			WHERE rr.id = ?
		`, id).Scan(&received, &laneStatus)
		if err == sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("rate request %d not found", id), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("load responses count for rate request %d: %v", id, err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if laneStatus == "rates_requested" || laneStatus == "rates_received" {
			fmt.Fprintf(w, `<span id="responses-received" class="detail-value" hx-get="/rate-requests/%d/responses-count" hx-trigger="every 30s" hx-swap="outerHTML">%d</span>`,
				id, received)
		} else {
			fmt.Fprintf(w, `<span id="responses-received" class="detail-value">%d</span>`, received)
		}
	}
}

// HandleComparison renders the side-by-side vendor rate comparison and lineup builder.
// GET /rate-requests/{id}/comparison
func (s *Service) HandleComparison(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		var rr RateRequestSnippet
		err = s.DB.QueryRow(`SELECT id, reference_id, lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(
			&rr.ID, &rr.ReferenceID, &rr.LaneID,
		)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			log.Printf("HandleComparison: fetch rate request %d: %v", rrID, err)
			http.Error(w, "Load rate request failed", http.StatusInternalServerError)
			return
		}

		lane, err := s.fetchLane(rr.LaneID)
		if err != nil {
			log.Printf("HandleComparison: fetch lane %d: %v", rr.LaneID, err)
			http.Error(w, "Load lane failed", http.StatusInternalServerError)
			return
		}

		// Load all rate items for this rate request, grouped by vendor_rate.
		itemRows, err := s.DB.Query(`
			SELECT vr.id, v.name,
			       vri.id, vri.charge_type, vri.amount, vri.unit, vri.manually_edited, vr.parsed_by
			FROM vendor_rates vr
			JOIN vendors v ON vr.vendor_id = v.id
			JOIN vendor_rate_items vri ON vri.vendor_rate_id = vr.id
			WHERE vr.rate_request_id = ?
			ORDER BY vr.id, vri.charge_type
		`, rrID)
		if err != nil {
			log.Printf("HandleComparison: query items rr=%d: %v", rrID, err)
			http.Error(w, "Load rate items failed", http.StatusInternalServerError)
			return
		}

		vendorMap := make(map[int]*VendorColumn)
		var vendorOrder []int

		for itemRows.Next() {
			var vrID int
			var vName string
			var itemID int
			var ct, unit, parsedBy string
			var amount float64
			var manuallyEdited int
			if err := itemRows.Scan(&vrID, &vName, &itemID, &ct, &amount, &unit, &manuallyEdited, &parsedBy); err != nil {
				itemRows.Close()
				log.Printf("HandleComparison: scan item row: %v", err)
				http.Error(w, "Scan rate item failed", http.StatusInternalServerError)
				return
			}
			if _, ok := vendorMap[vrID]; !ok {
				vendorMap[vrID] = &VendorColumn{
					VendorRateID: vrID,
					VendorName:   vName,
					Items:        make(map[string]RateItemCell),
				}
				vendorOrder = append(vendorOrder, vrID)
			}
			vendorMap[vrID].Items[ct] = RateItemCell{
				ID:             itemID,
				Amount:         amount,
				Unit:           unit,
				ManuallyEdited: manuallyEdited != 0,
				ParsedBy:       parsedBy,
				Display:        rates.FmtCellAmount(amount, unit),
			}
		}
		itemRows.Close()

		// Build vendor slice with computed totals, then sort by total ascending.
		vendors := make([]VendorColumn, 0, len(vendorOrder))
		for _, vrID := range vendorOrder {
			vc := *vendorMap[vrID]
			lh := vc.Items["linehaul"].Amount
			fuel := vc.Items["fuel"].Amount
			vc.Total = lh + lh*fuel/100
			vc.TotalDisplay = rates.FmtCellAmount(vc.Total, "$")
			vendors = append(vendors, vc)
		}
		sort.Slice(vendors, func(i, j int) bool {
			return vendors[i].Total < vendors[j].Total
		})

		// Collect charge types present in any vendor, in canonical FieldOrder.
		presentTypes := make(map[string]bool)
		for _, vc := range vendors {
			for ct := range vc.Items {
				presentTypes[ct] = true
			}
		}
		var chargeRows []ChargeTypeRow
		var availableTypes []ChargeTypeRow
		for _, ct := range rates.FieldOrder {
			if presentTypes[ct] {
				chargeRows = append(chargeRows, ChargeTypeRow{Key: ct, Label: ctLabel(ct)})
			} else {
				availableTypes = append(availableTypes, ChargeTypeRow{Key: ct, Label: ctLabel(ct)})
			}
		}

		// Compute per-charge-type averages (formatted) across vendors that have that type.
		rawAvgAmounts := make(map[string]float64)
		rawAvgUnits := make(map[string]string)
		counts := make(map[string]int)
		for _, vc := range vendors {
			for ct, cell := range vc.Items {
				rawAvgAmounts[ct] += cell.Amount
				rawAvgUnits[ct] = cell.Unit
				counts[ct]++
			}
		}
		averages := make(map[string]string, len(rawAvgAmounts))
		for ct, total := range rawAvgAmounts {
			if counts[ct] > 0 {
				avg := total / float64(counts[ct])
				averages[ct] = rates.FmtCellAmount(avg, rawAvgUnits[ct])
			}
		}

		// Compute average of vendor totals.
		var totalSum float64
		for _, vc := range vendors {
			totalSum += vc.Total
		}
		var avgTotal string
		if len(vendors) > 0 {
			avgTotal = rates.FmtCellAmount(totalSum/float64(len(vendors)), "$")
		}

		// Load existing lineups for this lane (vendor_rate_id → rank).
		lineups := make(map[int]int)
		lRows, err := s.DB.Query(`SELECT vendor_rate_id, rank FROM vendor_lineups WHERE lane_id = ?`, rr.LaneID)
		if err != nil {
			log.Printf("HandleComparison: query lineups lane=%d: %v", rr.LaneID, err)
			http.Error(w, "Load lineups failed", http.StatusInternalServerError)
			return
		}
		for lRows.Next() {
			var vrID, rank int
			lRows.Scan(&vrID, &rank)
			lineups[vrID] = rank
		}
		lRows.Close()

		if err := tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User": user,
			"Data": ComparisonData{
				RateRequest:          rr,
				Lane:                 *lane,
				ChargeRows:           chargeRows,
				Vendors:              vendors,
				Averages:             averages,
				AvgTotal:             avgTotal,
				Lineups:              lineups,
				AvailableChargeTypes: availableTypes,
			},
		}); err != nil {
			log.Printf("HandleComparison: template execution rr=%d: %v", rrID, err)
		}
	}
}

// HandleSaveLineup saves the vendor ranking for a lane and advances its status to "quoting".
// POST /rate-requests/{id}/lineup
func (s *Service) HandleSaveLineup() http.HandlerFunc {
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

		var laneID int
		if err := s.DB.QueryRow(`SELECT lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(&laneID); err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Collect (vendor_rate_id → rank) from form fields named "rank_{vrID}".
		type lineupEntry struct {
			vrID int
			rank int
		}
		var selected []lineupEntry
		for key, vals := range r.Form {
			if !strings.HasPrefix(key, "rank_") {
				continue
			}
			vrID, err := strconv.Atoi(strings.TrimPrefix(key, "rank_"))
			if err != nil || vrID <= 0 {
				continue
			}
			rank, err := strconv.Atoi(vals[0])
			if err != nil || rank <= 0 {
				continue
			}
			selected = append(selected, lineupEntry{vrID: vrID, rank: rank})
		}

		tx, err := s.DB.Begin()
		if err != nil {
			http.Error(w, "Begin transaction failed", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`DELETE FROM vendor_lineups WHERE lane_id = ?`, laneID); err != nil {
			http.Error(w, "Clear lineup failed", http.StatusInternalServerError)
			return
		}
		for _, l := range selected {
			if _, err := tx.Exec(
				`INSERT INTO vendor_lineups (lane_id, vendor_rate_id, rank) VALUES (?, ?, ?)`,
				laneID, l.vrID, l.rank,
			); err != nil {
				http.Error(w, "Insert lineup failed", http.StatusInternalServerError)
				return
			}
		}
		if len(selected) > 0 {
			tx.Exec(
				`UPDATE lanes SET status = 'quoting', updated_at = ? WHERE id = ? AND status IN ('rates_received', 'quoting')`,
				time.Now().UTC().Format("2006-01-02 15:04:05"), laneID,
			)
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "Commit lineup failed", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/rate-requests/%d/comparison/saved", rrID), http.StatusSeeOther)
	}
}

// --- Saved comparison + CSV generation types ---

// SavedVendorColumn holds one lineup vendor for the saved comparison view (rank is plain text).
type SavedVendorColumn struct {
	VendorRateID int
	VendorName   string
	Rank         int
	Items        map[string]RateItemCell
	Total        float64
	TotalDisplay string
}

// MarkupItemRow holds an existing markup value for a charge type, used to pre-fill form inputs.
type MarkupItemRow struct {
	ChargeType string
	Value      float64
	MarkupType string // "flat" | "percent"
}

// SavedComparisonData is the template data for rate_comparison_saved.html.
type SavedComparisonData struct {
	RateRequest          RateRequestSnippet
	Lane                 LaneSnippet
	ChargeRows           []ChargeTypeRow
	Vendors              []SavedVendorColumn // sorted by rank asc
	Averages             map[string]string
	AvgTotal             string
	Markups              map[string]MarkupItemRow // keyed by charge_type; pre-fills inputs if exists
	LineupBases          template.JS              // JSON for JS carousel: [{linehaul:X, fuel:Y, ...}, ...]
}

// HandleSavedComparison renders the markup entry + CSV generation page for a lineup.
// GET /rate-requests/{id}/comparison/saved
func (s *Service) HandleSavedComparison(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		var rr RateRequestSnippet
		if err := s.DB.QueryRow(`SELECT id, reference_id, lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(
			&rr.ID, &rr.ReferenceID, &rr.LaneID,
		); err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		} else if err != nil {
			log.Printf("HandleSavedComparison: fetch rr %d: %v", rrID, err)
			http.Error(w, "Failed to load rate request", http.StatusInternalServerError)
			return
		}

		lane, err := s.fetchLane(rr.LaneID)
		if err != nil {
			log.Printf("HandleSavedComparison: fetch lane %d: %v", rr.LaneID, err)
			http.Error(w, "Failed to load lane", http.StatusInternalServerError)
			return
		}

		// Load lineup: rank-ordered vendor_rate_ids with vendor names.
		type lineupRow struct {
			rank   int
			vrID   int
			vName  string
		}
		lRows, err := s.DB.Query(`
			SELECT vl.rank, vr.id, v.name
			FROM vendor_lineups vl
			JOIN vendor_rates vr ON vr.id = vl.vendor_rate_id
			JOIN vendors v ON v.id = vr.vendor_id
			WHERE vl.lane_id = ?
			ORDER BY vl.rank
		`, rr.LaneID)
		if err != nil {
			log.Printf("HandleSavedComparison: query lineup lane=%d: %v", rr.LaneID, err)
			http.Error(w, "Failed to load lineup", http.StatusInternalServerError)
			return
		}
		var lineupRows []lineupRow
		for lRows.Next() {
			var lr lineupRow
			if err := lRows.Scan(&lr.rank, &lr.vrID, &lr.vName); err != nil {
				lRows.Close()
				log.Printf("HandleSavedComparison: scan lineup row: %v", err)
				http.Error(w, "Failed to load lineup", http.StatusInternalServerError)
				return
			}
			lineupRows = append(lineupRows, lr)
		}
		lRows.Close()

		if len(lineupRows) == 0 {
			http.Redirect(w, r, fmt.Sprintf("/rate-requests/%d/comparison", rrID), http.StatusSeeOther)
			return
		}

		// Collect lineup vendor_rate_ids for the item query.
		vrIDs := make([]int, len(lineupRows))
		for i, lr := range lineupRows {
			vrIDs[i] = lr.vrID
		}

		// Build placeholder list for IN clause.
		placeholders := strings.Repeat("?,", len(vrIDs))
		placeholders = placeholders[:len(placeholders)-1]

		args := make([]any, len(vrIDs)+1)
		args[0] = rrID
		for i, id := range vrIDs {
			args[i+1] = id
		}

		itemRows, err := s.DB.Query(fmt.Sprintf(`
			SELECT vr.id, vri.id, vri.charge_type, vri.amount, vri.unit, vri.manually_edited, vr.parsed_by
			FROM vendor_rates vr
			JOIN vendor_rate_items vri ON vri.vendor_rate_id = vr.id
			WHERE vr.rate_request_id = ? AND vr.id IN (%s)
			ORDER BY vr.id, vri.charge_type
		`, placeholders), args...)
		if err != nil {
			log.Printf("HandleSavedComparison: query items rr=%d: %v", rrID, err)
			http.Error(w, "Failed to load rate items", http.StatusInternalServerError)
			return
		}

		vendorItemMap := make(map[int]map[string]RateItemCell)
		for itemRows.Next() {
			var vrID, itemID, manuallyEdited int
			var ct, unit, parsedBy string
			var amount float64
			if err := itemRows.Scan(&vrID, &itemID, &ct, &amount, &unit, &manuallyEdited, &parsedBy); err != nil {
				itemRows.Close()
				log.Printf("HandleSavedComparison: scan item: %v", err)
				http.Error(w, "Failed to load rate items", http.StatusInternalServerError)
				return
			}
			if vendorItemMap[vrID] == nil {
				vendorItemMap[vrID] = make(map[string]RateItemCell)
			}
			vendorItemMap[vrID][ct] = RateItemCell{
				ID:             itemID,
				Amount:         amount,
				Unit:           unit,
				ManuallyEdited: manuallyEdited != 0,
				ParsedBy:       parsedBy,
				Display:        rates.FmtCellAmount(amount, unit),
			}
		}
		itemRows.Close()

		// Build SavedVendorColumn slice in rank order.
		vendors := make([]SavedVendorColumn, len(lineupRows))
		for i, lr := range lineupRows {
			items := vendorItemMap[lr.vrID]
			if items == nil {
				items = make(map[string]RateItemCell)
			}
			lh := items["linehaul"].Amount
			fuel := items["fuel"].Amount
			total := lh + lh*fuel/100
			vendors[i] = SavedVendorColumn{
				VendorRateID: lr.vrID,
				VendorName:   lr.vName,
				Rank:         lr.rank,
				Items:        items,
				Total:        total,
				TotalDisplay: rates.FmtCellAmount(total, "$"),
			}
		}

		// Collect charge types present in lineup vendors, in canonical order.
		presentTypes := make(map[string]bool)
		for _, vc := range vendors {
			for ct := range vc.Items {
				presentTypes[ct] = true
			}
		}
		var chargeRows []ChargeTypeRow
		var availableTypes []ChargeTypeRow
		for _, ct := range rates.FieldOrder {
			if presentTypes[ct] {
				chargeRows = append(chargeRows, ChargeTypeRow{Key: ct, Label: ctLabel(ct)})
			} else {
				availableTypes = append(availableTypes, ChargeTypeRow{Key: ct, Label: ctLabel(ct)})
			}
		}

		// Compute per-charge-type averages and average total.
		rawSums := make(map[string]float64)
		rawUnits := make(map[string]string)
		counts := make(map[string]int)
		for _, vc := range vendors {
			for ct, cell := range vc.Items {
				rawSums[ct] += cell.Amount
				rawUnits[ct] = cell.Unit
				counts[ct]++
			}
		}
		averages := make(map[string]string, len(rawSums))
		rawAvgFloats := make(map[string]float64, len(rawSums))
		for ct, total := range rawSums {
			if counts[ct] > 0 {
				avg := total / float64(counts[ct])
				rawAvgFloats[ct] = avg
				averages[ct] = rates.FmtCellAmount(avg, rawUnits[ct])
			}
		}
		var totalSum float64
		for _, vc := range vendors {
			totalSum += vc.Total
		}
		var avgTotal string
		if len(vendors) > 0 {
			avgTotal = rates.FmtCellAmount(totalSum/float64(len(vendors)), "$")
		}

		// Build lineupBases JSON: index 0 = averages (raw floats), 1..N = vendor amounts.
		// linehaul_fuel is pre-computed as the actual total (not re-derived from avgLH × avgFuel%).
		basesList := make([]map[string]float64, 1+len(vendors))
		basesList[0] = rawAvgFloats
		if len(vendors) > 0 {
			basesList[0]["linehaul_fuel"] = totalSum / float64(len(vendors))
		}
		for i, vc := range vendors {
			m := make(map[string]float64, len(vc.Items))
			for ct, cell := range vc.Items {
				m[ct] = cell.Amount
			}
			m["linehaul_fuel"] = vc.Total
			basesList[i+1] = m
		}
		basesJSON, err := json.Marshal(basesList)
		if err != nil {
			log.Printf("HandleSavedComparison: marshal lineupBases: %v", err)
			http.Error(w, "Failed to build carousel data", http.StatusInternalServerError)
			return
		}

		// Load existing markups for pre-fill.
		markupsMap := make(map[string]MarkupItemRow)
		miRows, err := s.DB.Query(`
			SELECT mi.charge_type, mi.value, mi.markup_type
			FROM markups m
			JOIN markup_items mi ON mi.markup_id = m.id
			WHERE m.lane_id = ?
		`, rr.LaneID)
		if err != nil {
			log.Printf("HandleSavedComparison: query markups lane=%d: %v", rr.LaneID, err)
			http.Error(w, "Failed to load markups", http.StatusInternalServerError)
			return
		}
		for miRows.Next() {
			var mi MarkupItemRow
			if err := miRows.Scan(&mi.ChargeType, &mi.Value, &mi.MarkupType); err != nil {
				miRows.Close()
				log.Printf("HandleSavedComparison: scan markup item: %v", err)
				http.Error(w, "Failed to load markups", http.StatusInternalServerError)
				return
			}
			markupsMap[mi.ChargeType] = mi
		}
		miRows.Close()

		if err := tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User": user,
			"Data": SavedComparisonData{
				RateRequest:          rr,
				Lane:                 *lane,
				ChargeRows:           chargeRows,
				Vendors:              vendors,
				Averages:             averages,
				AvgTotal:             avgTotal,
				Markups:              markupsMap,
				LineupBases:          template.JS(basesJSON),
			},
		}); err != nil {
			log.Printf("HandleSavedComparison: template execution rr=%d: %v", rrID, err)
		}
	}
}

// markupEntry holds a single parsed markup row from form input.
type markupEntry struct {
	chargeType string
	value      float64
	markupType string
}

// persistMarkups parses markup form values, resolves or creates the lane's quote,
// and upserts markup + markup_items rows. Returns the resolved quoteID and parsed entries.
func (s *Service) persistMarkups(tx *sql.Tx, laneID int, r *http.Request) (int64, []markupEntry, error) {
	// Parse markup form values: each charge_type field, plus markup_type_linehaul_fuel.
	var entries []markupEntry
	for _, ct := range rates.FieldOrder {
		if ct == "linehaul" || ct == "fuel" {
			continue // rolled into linehaul_fuel
		}
		valStr := r.FormValue(ct)
		if valStr == "" {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil || val == 0 {
			continue
		}
		entries = append(entries, markupEntry{chargeType: ct, value: val, markupType: "flat"})
	}
	// linehaul_fuel is a special combined entry with flat/percent toggle.
	if lfStr := r.FormValue("linehaul_fuel"); lfStr != "" {
		if lfVal, err := strconv.ParseFloat(lfStr, 64); err == nil && lfVal != 0 {
			mtype := r.FormValue("markup_type_linehaul_fuel")
			if mtype != "percent" {
				mtype = "flat"
			}
			entries = append(entries, markupEntry{chargeType: "linehaul_fuel", value: lfVal, markupType: mtype})
		}
	}

	// Resolve or create the quote for this lane (reuse if one already exists).
	// Customer association: lane owner is the customer proxy until a customer picker is added.
	var quoteID int64
	err := tx.QueryRow(`SELECT q.id FROM quotes q JOIN quote_lanes ql ON ql.quote_id = q.id WHERE ql.lane_id = ?`, laneID).Scan(&quoteID)
	if err == sql.ErrNoRows {
		var ownerID int
		if err2 := tx.QueryRow(`SELECT owner_id FROM lanes WHERE id = ?`, laneID).Scan(&ownerID); err2 != nil {
			return 0, nil, fmt.Errorf("fetch owner lane=%d: %w", laneID, err2)
		}
		var customerID int64
		err2 := tx.QueryRow(`SELECT id FROM customers WHERE name = (SELECT email FROM users WHERE id = ?)`, ownerID).Scan(&customerID)
		if err2 == sql.ErrNoRows {
			ownerEmail := ""
			tx.QueryRow(`SELECT email FROM users WHERE id = ?`, ownerID).Scan(&ownerEmail)
			res2, err3 := tx.Exec(`INSERT INTO customers (name) VALUES (?)`, ownerEmail)
			if err3 != nil {
				return 0, nil, fmt.Errorf("insert customer: %w", err3)
			}
			customerID, _ = res2.LastInsertId()
		} else if err2 != nil {
			return 0, nil, fmt.Errorf("lookup customer: %w", err2)
		}
		res, err3 := tx.Exec(`INSERT INTO quotes (owner_id, customer_id) VALUES (?, ?)`, ownerID, customerID)
		if err3 != nil {
			return 0, nil, fmt.Errorf("insert quote: %w", err3)
		}
		quoteID, _ = res.LastInsertId()
		if _, err3 := tx.Exec(`INSERT INTO quote_lanes (quote_id, lane_id) VALUES (?, ?)`, quoteID, laneID); err3 != nil {
			return 0, nil, fmt.Errorf("insert quote_lane: %w", err3)
		}
	} else if err != nil {
		return 0, nil, fmt.Errorf("lookup quote lane=%d: %w", laneID, err)
	}

	// Delete existing markup for this lane and recreate.
	if _, err := tx.Exec(`DELETE FROM markups WHERE lane_id = ?`, laneID); err != nil {
		return 0, nil, fmt.Errorf("delete markups lane=%d: %w", laneID, err)
	}
	if len(entries) > 0 {
		res, err := tx.Exec(`INSERT INTO markups (quote_id, lane_id) VALUES (?, ?)`, quoteID, laneID)
		if err != nil {
			return 0, nil, fmt.Errorf("insert markup: %w", err)
		}
		markupID, _ := res.LastInsertId()
		for _, e := range entries {
			if _, err := tx.Exec(
				`INSERT INTO markup_items (markup_id, charge_type, value, markup_type) VALUES (?, ?, ?, ?)`,
				markupID, e.chargeType, e.value, e.markupType,
			); err != nil {
				return 0, nil, fmt.Errorf("insert markup_item ct=%s: %w", e.chargeType, err)
			}
		}
	}
	return quoteID, entries, nil
}

// HandleSaveMarkups persists markup values without generating a CSV.
// Returns a small HTML fragment for the auto-save status indicator.
// POST /rate-requests/{id}/markups
func (s *Service) HandleSaveMarkups() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Rate request not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request: could not parse form", http.StatusBadRequest)
			return
		}
		var laneID int
		if err := s.DB.QueryRow(`SELECT lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(&laneID); err == sql.ErrNoRows {
			http.Error(w, "Rate request not found", http.StatusNotFound)
			return
		} else if err != nil {
			log.Printf("HandleSaveMarkups: fetch rr=%d: %v", rrID, err)
			http.Error(w, "Failed to load rate request", http.StatusInternalServerError)
			return
		}
		tx, err := s.DB.Begin()
		if err != nil {
			http.Error(w, "Failed to begin transaction", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if _, _, err := s.persistMarkups(tx, laneID, r); err != nil {
			log.Printf("HandleSaveMarkups: rr=%d: %v", rrID, err)
			http.Error(w, "Failed to save markups", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("HandleSaveMarkups: commit rr=%d: %v", rrID, err)
			http.Error(w, "Failed to commit markup save", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `<span class="text-muted">Saved ✓</span>`)
	}
}

// HandleGenerateCSV persists markup values, writes the customer quote XLSX, and advances lane to "quoted".
// POST /rate-requests/{id}/csv
func (s *Service) HandleGenerateCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request: could not parse form", http.StatusBadRequest)
			return
		}

		var laneID int
		var refID string
		if err := s.DB.QueryRow(`SELECT lane_id, reference_id FROM rate_requests WHERE id = ?`, rrID).Scan(&laneID, &refID); err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		} else if err != nil {
			log.Printf("HandleGenerateCSV: fetch rr %d: %v", rrID, err)
			http.Error(w, "Failed to load rate request", http.StatusInternalServerError)
			return
		}

		lane, err := s.fetchLane(laneID)
		if err != nil {
			log.Printf("HandleGenerateCSV: fetch lane %d: %v", laneID, err)
			http.Error(w, "Failed to load lane", http.StatusInternalServerError)
			return
		}

		// Resolve or create the quote for this lane atomically.
		// If a quote already exists for this lane, reuse it; otherwise create one.
		// Customer association: use lane owner as customer proxy for now (no customer picker yet).
		tx, err := s.DB.Begin()
		if err != nil {
			http.Error(w, "Failed to begin transaction", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		_, entries, err := s.persistMarkups(tx, laneID, r)
		if err != nil {
			log.Printf("HandleGenerateCSV: persistMarkups rr=%d: %v", rrID, err)
			http.Error(w, "Failed to save markup: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Advance lane to "quoted".
		tx.Exec(
			`UPDATE lanes SET status = 'quoted', updated_at = ? WHERE id = ? AND status IN ('quoting', 'quoted')`,
			time.Now().UTC().Format("2006-01-02 15:04:05"), laneID,
		)

		if err := tx.Commit(); err != nil {
			log.Printf("HandleGenerateCSV: commit: %v", err)
			http.Error(w, "Failed to save quote", http.StatusInternalServerError)
			return
		}

		// Build markup lookup for CSV generation.
		markupLookup := make(map[string]markupEntry, len(entries))
		for _, e := range entries {
			markupLookup[e.chargeType] = e
		}

		// Load rank-1 vendor rate items for the CSV (primary carrier's rates as the basis).
		// We use the full lineup to compute values; CSV shows customer charges derived from markup.
		type vendorAmounts struct {
			linehaul float64
			fuel     float64
			items    map[string]float64
		}
		var primaryVendor vendorAmounts
		primaryVendor.items = make(map[string]float64)

		priRows, err := s.DB.Query(`
			SELECT vri.charge_type, vri.amount
			FROM vendor_lineups vl
			JOIN vendor_rates vr ON vr.id = vl.vendor_rate_id
			JOIN vendor_rate_items vri ON vri.vendor_rate_id = vr.id
			WHERE vl.lane_id = ? AND vl.rank = 1
		`, laneID)
		if err != nil {
			log.Printf("HandleGenerateCSV: query primary vendor items lane=%d: %v", laneID, err)
			http.Error(w, "Failed to load primary vendor rates", http.StatusInternalServerError)
			return
		}
		for priRows.Next() {
			var ct string
			var amount float64
			priRows.Scan(&ct, &amount)
			primaryVendor.items[ct] = amount
		}
		priRows.Close()
		primaryVendor.linehaul = primaryVendor.items["linehaul"]
		primaryVendor.fuel = primaryVendor.items["fuel"]

		// Compute linehaul+fuel customer rate (percent mode uses rank-1 vendor base).
		lhFuelBase := primaryVendor.linehaul + primaryVendor.linehaul*primaryVendor.fuel/100
		var lhFuelCustomer float64
		if lfEntry, ok := markupLookup["linehaul_fuel"]; ok {
			if lfEntry.markupType == "percent" {
				lhFuelCustomer = lhFuelBase * (1 + lfEntry.value/100)
			} else {
				lhFuelCustomer = lfEntry.value // flat: user entered customer total directly
			}
		} else {
			lhFuelCustomer = lhFuelBase
		}
		// Replace linehaul_fuel entry so the output loop uses the computed dollar amount.
		markupLookup["linehaul_fuel"] = markupEntry{chargeType: "linehaul_fuel", value: lhFuelCustomer, markupType: "flat"}

		// Build XLSX workbook.
		f := excelize.NewFile()
		const sheet = "Quote"
		f.SetSheetName("Sheet1", sheet)

		orangeStyle, _ := f.NewStyle(&excelize.Style{
			Fill: excelize.Fill{Type: "pattern", Color: []string{"F85F14"}, Pattern: 1},
			Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		})

		row := 1

		// Port / Destination / Direction metadata rows (column A label = orange).
		f.SetCellValue(sheet, xlCell(1, row), "Port:")
		f.SetCellValue(sheet, xlCell(2, row), lane.OriginPort)
		f.SetCellStyle(sheet, xlCell(1, row), xlCell(1, row), orangeStyle)
		row++

		f.SetCellValue(sheet, xlCell(1, row), "Destination:")
		f.SetCellValue(sheet, xlCell(2, row), lane.Destination)
		f.SetCellStyle(sheet, xlCell(1, row), xlCell(1, row), orangeStyle)
		row++

		f.SetCellValue(sheet, xlCell(1, row), "Direction:")
		f.SetCellValue(sheet, xlCell(2, row), lane.Direction)
		f.SetCellStyle(sheet, xlCell(1, row), xlCell(1, row), orangeStyle)
		row++


		// Section header: "Applicable charges" | (empty) | "Notes" — both orange.
		f.SetCellValue(sheet, xlCell(1, row), "Applicable charges")
		f.SetCellValue(sheet, xlCell(3, row), "Notes")
		f.SetCellStyle(sheet, xlCell(1, row), xlCell(1, row), orangeStyle)
		f.SetCellStyle(sheet, xlCell(2, row), xlCell(2, row), orangeStyle)
		f.SetCellStyle(sheet, xlCell(3, row), xlCell(3, row), orangeStyle)
		row++

		// Partition entered charges into applicable vs. accessorials (preserving FieldOrder).
		// linehaul_fuel is a synthetic key not in FieldOrder, so it is handled first.
		type quoteRow struct{ label, value, notes string }
		var applicable, accessorials []quoteRow
		if e := markupLookup["linehaul_fuel"]; e.value != 0 {
			applicable = append(applicable, quoteRow{csvLabel("linehaul_fuel"), csvValue("linehaul_fuel", e.value), csvNotes("linehaul_fuel")})
		}
		for _, ct := range rates.FieldOrder {
			if ct == "linehaul" || ct == "fuel" {
				continue
			}
			e, ok := markupLookup[ct]
			if !ok || e.value == 0 {
				continue
			}
			qr := quoteRow{csvLabel(ct), csvValue(ct, e.value), csvNotes(ct)}
			if applicableChargeTypes[ct] {
				applicable = append(applicable, qr)
			} else {
				accessorials = append(accessorials, qr)
			}
		}

		for _, qr := range applicable {
			f.SetCellValue(sheet, xlCell(1, row), qr.label)
			f.SetCellValue(sheet, xlCell(2, row), qr.value)
			f.SetCellValue(sheet, xlCell(3, row), qr.notes)
			row++
		}

		if len(accessorials) > 0 {
			f.SetCellValue(sheet, xlCell(1, row), "Accessorials (only if needed)")
			f.SetCellStyle(sheet, xlCell(1, row), xlCell(1, row), orangeStyle)
			f.SetCellStyle(sheet, xlCell(2, row), xlCell(2, row), orangeStyle)
			f.SetCellStyle(sheet, xlCell(3, row), xlCell(3, row), orangeStyle)
			row++
			for _, qr := range accessorials {
				f.SetCellValue(sheet, xlCell(1, row), qr.label)
				f.SetCellValue(sheet, xlCell(2, row), qr.value)
				f.SetCellValue(sheet, xlCell(3, row), qr.notes)
				row++
			}
		}

		// Stream XLSX response.
		cust := strings.ReplaceAll(lane.CustomerName, " ", "_")
		origin := strings.ReplaceAll(lane.OriginPort, " ", "_")
		dest := strings.ReplaceAll(lane.Destination, " ", "_")
		filename := fmt.Sprintf("quote-%s-%s-%s-%s.xlsx", cust, origin, dest, refID)

		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		f.Write(w)
	}
}

// availableChargeTypesAfterAdd returns the FieldOrder types not yet in the DB for a rate request,
// treating newCT as already present (it was just added to the table via HTMX).
func (s *Service) availableChargeTypesAfterAdd(rrID int, newCT string) []ChargeTypeRow {
	rows, err := s.DB.Query(`
		SELECT DISTINCT vri.charge_type
		FROM vendor_rate_items vri
		JOIN vendor_rates vr ON vr.id = vri.vendor_rate_id
		WHERE vr.rate_request_id = ?
	`, rrID)
	present := map[string]bool{newCT: true}
	if err == nil {
		for rows.Next() {
			var ct string
			rows.Scan(&ct)
			present[ct] = true
		}
		rows.Close()
	}
	var available []ChargeTypeRow
	for _, ct := range rates.FieldOrder {
		if !present[ct] {
			available = append(available, ChargeTypeRow{Key: ct, Label: ctLabel(ct)})
		}
	}
	return available
}

// HandleAddComparisonRow returns a <tr> fragment for a new charge type on the lineup builder page,
// plus an OOB swap to remove the added type from the add-charge-type dropdown.
// GET /rate-requests/{id}/comparison/add-row?charge_type=
func (s *Service) HandleAddComparisonRow() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		ct := strings.TrimSpace(r.URL.Query().Get("charge_type"))
		valid := false
		for _, f := range rates.FieldOrder {
			if f == ct {
				valid = true
				break
			}
		}
		if !valid {
			http.Error(w, "Unknown or missing charge_type", http.StatusBadRequest)
			return
		}

		// Load all vendor_rates for this rate request.
		vRows, err := s.DB.Query(`
			SELECT vr.id, v.name
			FROM vendor_rates vr
			JOIN vendors v ON v.id = vr.vendor_id
			WHERE vr.rate_request_id = ?
			ORDER BY v.name
		`, rrID)
		if err != nil {
			log.Printf("HandleAddComparisonRow: query vendors rr=%d: %v", rrID, err)
			http.Error(w, "Failed to load vendors", http.StatusInternalServerError)
			return
		}
		type vendorRow struct {
			vrID int
			name string
		}
		var vendors []vendorRow
		for vRows.Next() {
			var vr vendorRow
			vRows.Scan(&vr.vrID, &vr.name)
			vendors = append(vendors, vr)
		}
		vRows.Close()

		// Check which vendors already have this charge type.
		existingCells := make(map[int]RateItemCell)
		for _, vr := range vendors {
			var itemID, manuallyEdited int
			var amount float64
			var unit, parsedBy string
			err := s.DB.QueryRow(`
				SELECT vri.id, vri.amount, vri.unit, vri.manually_edited, vr.parsed_by
				FROM vendor_rate_items vri
				JOIN vendor_rates vr ON vr.id = vri.vendor_rate_id
				WHERE vri.vendor_rate_id = ? AND vri.charge_type = ?
			`, vr.vrID, ct).Scan(&itemID, &amount, &unit, &manuallyEdited, &parsedBy)
			if err == nil {
				existingCells[vr.vrID] = RateItemCell{
					ID: itemID, Amount: amount, Unit: unit,
					ManuallyEdited: manuallyEdited != 0, ParsedBy: parsedBy,
					Display: rates.FmtCellAmount(amount, unit),
				}
			}
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "<tr>\n<td>%s</td>\n", ctLabel(ct))
		for _, vr := range vendors {
			if cell, ok := existingCells[vr.vrID]; ok {
				cssClass := ""
				if cell.ManuallyEdited {
					cssClass = "cell-edited"
				} else if cell.ParsedBy == "llm" {
					cssClass = "cell-llm"
				}
				fmt.Fprintf(&sb,
					`<td class="cell-center-click %s" hx-get="/vendor-rate-items/%d/edit" hx-trigger="click" hx-target="this" hx-swap="outerHTML" title="Click to edit">%s</td>`,
					cssClass, cell.ID, cell.Display)
			} else {
				fmt.Fprintf(&sb,
					`<td class="cell-center-empty" hx-get="/vendor-rates/%d/items/new?charge_type=%s" hx-trigger="click" hx-target="this" hx-swap="outerHTML" title="Click to add">—</td>`,
					vr.vrID, ct)
			}
		}
		sb.WriteString("</tr>\n")

		// OOB: update dropdown to remove the just-added charge type.
		available := s.availableChargeTypesAfterAdd(rrID, ct)
		sb.WriteString(`<select id="add-charge-type-select" hx-swap-oob="true" name="charge_type" style="width:auto;"><option value="">Add charge type...</option>`)
		for _, row := range available {
			fmt.Fprintf(&sb, `<option value="%s">%s</option>`, row.Key, row.Label)
		}
		sb.WriteString(`</select>`)

		w.Write([]byte(sb.String()))
	}
}

// HandleLanePanel returns the lane-detail-panel partial as an HTMX fragment.
// GET /rate-requests/{id}/lane-panel
func (s *Service) HandleLanePanel(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rrID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || rrID <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		var laneID int
		if err := s.DB.QueryRow(`SELECT lane_id FROM rate_requests WHERE id = ?`, rrID).Scan(&laneID); err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		lane, err := s.fetchLane(laneID)
		if err != nil {
			log.Printf("HandleLanePanel: fetch lane %d: %v", laneID, err)
			http.Error(w, "Failed to load lane", http.StatusInternalServerError)
			return
		}
		if err := tmpl.ExecuteTemplate(w, "lane-detail-panel", lane); err != nil {
			log.Printf("HandleLanePanel: template execution: %v", err)
		}
	}
}
