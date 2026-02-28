package rate_requests

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rates"
)

// Service handles rate request operations.
type Service struct {
	DB *sql.DB
}

// LaneSnippet holds lean lane info for display in rate request views.
type LaneSnippet struct {
	ID             int
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

// buildBody generates the standard rate request email body with a [First Name] placeholder.
// The refID is included as the Reference line in the body (separate from the subject).
func buildBody(lane *LaneSnippet, refID string) string {
	var sb strings.Builder

	dir := "Import"
	if lane.Direction == "export" {
		dir = "Export"
	}
	load := "Live"
	if lane.LoadType == "drop_pick" {
		load = "Drop / Pick"
	}

	fmt.Fprintf(&sb, "Hi [First Name],\n\n")
	fmt.Fprintf(&sb, "We are reaching out to request drayage rates for the following lane:\n\n")
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

// fmtDate parses a SQLite DATETIME string into a short readable date.
func fmtDate(s string) string {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.Format("Jan 2, 2006")
	}
	return s
}

// formatDeadline parses a stored deadline and returns a human-readable string.
func formatDeadline(s string) string {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("Jan 2, 2006 3:04 PM")
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
		       l.notes, l.hazmat, l.overweight, l.out_of_gauge, l.status
		FROM lanes l
		JOIN ports p ON p.id = l.origin_port_id
		WHERE l.id = ?
	`, laneID).Scan(
		&lane.ID, &lane.OriginPortID, &lane.OriginPort, &lane.OriginPortType,
		&lane.Destination, &lane.Direction,
		&lane.ContainerSize, &lane.LoadType, &lane.Commodity, &lane.Weight,
		&lane.Notes, &hazmat, &overweight, &outOfGauge, &lane.Status,
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		refID, err := generateReferenceID(s.DB)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		vendors, err := s.fetchVendorsForPort(lane.OriginPortID, user.ID)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":    user,
			"Lane":    lane,
			"RefID":   refID,
			"Subject": buildSubjectBase(lane),
			"Body":    buildBody(lane, refID),
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Append the reference ID to the subject so it only appears on saved records.
		finalSubject := subject + " - " + refID

		var threshold interface{}
		if t, err := strconv.Atoi(thresholdStr); err == nil && t > 0 {
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
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
	RateRequest  RateRequestSnippet
	Lane         LaneSnippet
	ChargeRows   []ChargeTypeRow    // ordered; only types present in ≥1 vendor
	Vendors      []VendorColumn     // sorted ascending by Total
	Averages     map[string]string  // pre-formatted average per charge_type
	AvgTotal     string             // pre-formatted average of vendor totals
	Lineups      map[int]int        // vendor_rate_id → current rank (0 = unranked)
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":        user,
			"RateRequest": rr,
		})
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		lane, err := s.fetchLane(rr.LaneID)
		if err != nil {
			log.Printf("HandleComparison: fetch lane %d: %v", rr.LaneID, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
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
				http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		for _, ct := range rates.FieldOrder {
			if presentTypes[ct] {
				chargeRows = append(chargeRows, ChargeTypeRow{
					Key:   ct,
					Label: ctLabel(ct),
				})
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
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
				RateRequest: rr,
				Lane:        *lane,
				ChargeRows:  chargeRows,
				Vendors:     vendors,
				Averages:    averages,
				AvgTotal:    avgTotal,
				Lineups:     lineups,
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`DELETE FROM vendor_lineups WHERE lane_id = ?`, laneID); err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		for _, l := range selected {
			if _, err := tx.Exec(
				`INSERT INTO vendor_lineups (lane_id, vendor_rate_id, rank) VALUES (?, ?, ?)`,
				laneID, l.vrID, l.rank,
			); err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
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
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/rate-requests/%d/comparison", rrID), http.StatusSeeOther)
	}
}
