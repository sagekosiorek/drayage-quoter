package rate_requests

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
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
