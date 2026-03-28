package carriers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
)

// Service handles carrier operations.
type Service struct {
	DB *sql.DB
}

// CarrierRow holds list-view data for a single carrier.
type CarrierRow struct {
	ID           int
	Name         string
	Ports        string // comma-joined port names from GROUP_CONCAT
	ContactCount int
	IsPreferred  bool
}

// CarrierDetail holds all data for the carrier detail view.
type CarrierDetail struct {
	ID             int
	Name           string
	AssignedPorts  []CarrierPort // ports this carrier services, each with their contacts and notes
	AvailablePorts []PortRef    // ports not yet assigned (for add dropdown)
	CreatedAt      string
	UpdatedAt      string
}

// CarrierPort is a carrier_ports row joined with port name/type, its contacts, and port-scoped notes.
type CarrierPort struct {
	ID        int // carrier_ports.id (used for contact, note, and remove-port routes)
	PortID    int // ports.id (used for preference toggle route)
	Name      string
	Type      string
	Preferred bool
	Contacts  []ContactRow
	Notes     []NoteRow
}

// PortRef is a minimal port record for dropdowns.
type PortRef struct {
	ID   int
	Name string
}

// ContactRow holds a single carrier contact.
type ContactRow struct {
	ID    int
	Name  string
	Email string
}

// NoteRow holds a carrier note with its author's display name.
type NoteRow struct {
	ID         int
	AuthorName string
	Content    string
	CreatedAt  string
}

// fetchCarrierDetail loads the carrier plus all sub-resources, with contacts grouped by port.
func (s *Service) fetchCarrierDetail(carrierID, userID int) (*CarrierDetail, error) {
	var v CarrierDetail
	var created, updated string

	err := s.DB.QueryRow(
		`SELECT id, name, created_at, updated_at FROM carriers WHERE id = ?`, carrierID,
	).Scan(&v.ID, &v.Name, &created, &updated)
	if err != nil {
		return nil, err
	}
	v.CreatedAt = fmtDate(created)
	v.UpdatedAt = fmtDate(updated)

	// Assigned ports with preference flag, ordered by port type and name.
	pRows, err := s.DB.Query(`
		SELECT vp.id, vp.port_id, p.name, p.type,
			(SELECT COUNT(*) FROM carrier_preferences pref
			 WHERE pref.carrier_id = ? AND pref.user_id = ? AND pref.port_id = vp.port_id) > 0
		FROM carrier_ports vp
		JOIN ports p ON p.id = vp.port_id
		WHERE vp.carrier_id = ?
		ORDER BY p.type, p.name
	`, carrierID, userID, carrierID)
	if err != nil {
		return nil, err
	}
	for pRows.Next() {
		var vp CarrierPort
		var preferred int
		if err := pRows.Scan(&vp.ID, &vp.PortID, &vp.Name, &vp.Type, &preferred); err != nil {
			pRows.Close()
			return nil, err
		}
		vp.Preferred = preferred != 0
		v.AssignedPorts = append(v.AssignedPorts, vp)
	}
	pRows.Close()

	// Contacts and notes for each assigned port (N queries each, but N is small).
	for i := range v.AssignedPorts {
		vpID := v.AssignedPorts[i].ID

		cRows, err := s.DB.Query(
			`SELECT id, COALESCE(name,''), email
			 FROM carrier_contacts WHERE carrier_ports_id = ? ORDER BY id`,
			vpID,
		)
		if err != nil {
			return nil, err
		}
		for cRows.Next() {
			var c ContactRow
			if err := cRows.Scan(&c.ID, &c.Name, &c.Email); err != nil {
				cRows.Close()
				return nil, err
			}
			v.AssignedPorts[i].Contacts = append(v.AssignedPorts[i].Contacts, c)
		}
		cRows.Close()

		nRows, err := s.DB.Query(`
			SELECT n.id, u.name, n.content, n.created_at
			FROM carrier_notes n
			JOIN users u ON n.author_id = u.id
			WHERE n.carrier_ports_id = ? ORDER BY n.created_at DESC
		`, vpID)
		if err != nil {
			return nil, err
		}
		for nRows.Next() {
			var n NoteRow
			var ts string
			if err := nRows.Scan(&n.ID, &n.AuthorName, &n.Content, &ts); err != nil {
				nRows.Close()
				return nil, err
			}
			n.CreatedAt = fmtDate(ts)
			v.AssignedPorts[i].Notes = append(v.AssignedPorts[i].Notes, n)
		}
		nRows.Close()
	}

	// Available ports: those not yet assigned to this carrier.
	avRows, err := s.DB.Query(`
		SELECT id, name FROM ports
		WHERE id NOT IN (SELECT port_id FROM carrier_ports WHERE carrier_id = ?)
		ORDER BY type, name
	`, carrierID)
	if err != nil {
		return nil, err
	}
	for avRows.Next() {
		var p PortRef
		if err := avRows.Scan(&p.ID, &p.Name); err != nil {
			avRows.Close()
			return nil, err
		}
		v.AvailablePorts = append(v.AvailablePorts, p)
	}
	avRows.Close()

	return &v, nil
}

// fetchPorts returns all ports ordered by type, name (for the new-carrier form dropdown).
func (s *Service) fetchPorts() ([]PortRef, error) {
	rows, err := s.DB.Query(`SELECT id, name FROM ports ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ports []PortRef
	for rows.Next() {
		var p PortRef
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, nil
}

// fmtDate parses a SQLite DATETIME string into a UTC RFC3339 string for client-side formatting.
// Falls back to the raw string on parse failure.
func fmtDate(s string) string {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

// nullableStr returns nil for empty strings so blank form fields become SQL NULL.
func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// carrierIDFromPath extracts and validates the {id} path segment.
func carrierIDFromPath(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	return id, err == nil && id > 0
}

// HandleSearch returns a carrier-name autocomplete fragment (min 2 chars).
func (s *Service) HandleSearch() http.HandlerFunc {
	suggTmpl := template.Must(template.New("vsearch").Parse(
		`{{range .}}<button type="button" class="suggestion-item" ` +
			`data-id="{{.ID}}" data-name="{{.Name}}" ` +
			`onclick="selectCarrier(this)">{{.Name}}</button>{{end}}`,
	))
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("name")
		if len(q) < 2 {
			w.Write([]byte(""))
			return
		}
		rows, err := s.DB.Query(
			`SELECT id, name FROM carriers WHERE name LIKE ? ORDER BY name LIMIT 8`,
			"%"+q+"%",
		)
		if err != nil {
			http.Error(w, "Query carriers failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type result struct {
			ID   int
			Name string
		}
		var matches []result
		for rows.Next() {
			var res result
			if err := rows.Scan(&res.ID, &res.Name); err != nil {
				continue
			}
			matches = append(matches, res)
		}
		suggTmpl.Execute(w, matches)
	}
}

// HandleList renders the carrier list with optional port and name filters.
func (s *Service) HandleList(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		q := r.URL.Query().Get("q")
		portID, _ := strconv.Atoi(r.URL.Query().Get("port"))

		// Preferred subquery: scoped to the filtered port when active, otherwise any port.
		preferredSubq := `EXISTS(SELECT 1 FROM carrier_preferences pref
			WHERE pref.carrier_id = v.id AND pref.user_id = ?)`
		args := []interface{}{user.ID}
		if portID > 0 {
			preferredSubq = `EXISTS(SELECT 1 FROM carrier_preferences pref
				WHERE pref.carrier_id = v.id AND pref.user_id = ? AND pref.port_id = ?)`
			args = []interface{}{user.ID, portID}
		}

		query := `
			SELECT v.id, v.name,
				(SELECT COUNT(*) FROM carrier_contacts vc
				 JOIN carrier_ports vp ON vc.carrier_ports_id = vp.id
				 WHERE vp.carrier_id = v.id),
				COALESCE(GROUP_CONCAT(p.name, ', '),''),
				` + preferredSubq + `
			FROM carriers v
			LEFT JOIN carrier_ports vp ON vp.carrier_id = v.id
			LEFT JOIN ports p ON p.id = vp.port_id
			WHERE 1=1`

		if q != "" {
			query += ` AND v.name LIKE ?`
			args = append(args, "%"+q+"%")
		}
		if portID > 0 {
			query += ` AND v.id IN (SELECT carrier_id FROM carrier_ports WHERE port_id = ?)`
			args = append(args, portID)
		}
		query += ` GROUP BY v.id ORDER BY v.name`

		rows, err := s.DB.Query(query, args...)
		if err != nil {
			http.Error(w, "Query carriers failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var carriers []CarrierRow
		for rows.Next() {
			var v CarrierRow
			var preferred int
			if err := rows.Scan(&v.ID, &v.Name, &v.ContactCount, &v.Ports, &preferred); err != nil {
				http.Error(w, "Scan carrier row failed", http.StatusInternalServerError)
				return
			}
			v.IsPreferred = preferred != 0
			carriers = append(carriers, v)
		}

		ports, err := s.fetchPorts()
		if err != nil {
			http.Error(w, "Query ports failed", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":    user,
			"Carriers": carriers,
			"Ports":   ports,
			"Query":   q,
			"PortID":  portID,
		})
	}
}

// HandleNewForm renders the new carrier form.
func (s *Service) HandleNewForm(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		ports, err := s.fetchPorts()
		if err != nil {
			http.Error(w, "Query ports failed", http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  user,
			"Ports": ports,
		})
	}
}

// HandleCreate creates a carrier (or finds an existing one by name), adds a port assignment,
// and inserts any provided contacts for that port.
func (s *Service) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(r.FormValue("name"))

		if name == "" {
			http.Error(w, "Carrier name is required", http.StatusBadRequest)
			return
		}

		// Resolve carrier: use autocomplete selection if available, else find/create by name.
		var carrierID int64
		if idStr := r.FormValue("carrier_id"); idStr != "" {
			carrierID, _ = strconv.ParseInt(idStr, 10, 64)
		}
		if carrierID == 0 {
			err := s.DB.QueryRow(`SELECT id FROM carriers WHERE LOWER(name) = LOWER(?)`, name).Scan(&carrierID)
			if err == sql.ErrNoRows {
				res, err := s.DB.Exec(`INSERT INTO carriers (name) VALUES (?)`, name)
				if err != nil {
					http.Error(w, "Insert carrier failed", http.StatusInternalServerError)
					return
				}
				carrierID, _ = res.LastInsertId()
			} else if err != nil {
				http.Error(w, "Lookup carrier failed", http.StatusInternalServerError)
				return
			}
		}

		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", carrierID), http.StatusSeeOther)
	}
}

// HandleDetail renders the carrier detail view.
func (s *Service) HandleDetail(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		carrier, err := s.fetchCarrierDetail(id, user.ID)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Load carrier failed", http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":   user,
			"Carrier": carrier,
		})
	}
}

// HandleEditForm renders the carrier edit form (name only).
func (s *Service) HandleEditForm(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		var name string
		if err := s.DB.QueryRow(`SELECT name FROM carriers WHERE id = ?`, id).Scan(&name); err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "Load carrier failed", http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":     user,
			"CarrierID": id,
			"Name":     name,
		})
	}
}

// HandleUpdate processes the carrier name edit form.
func (s *Service) HandleUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Error(w, "Name is required", http.StatusBadRequest)
			return
		}
		res, err := s.DB.Exec(
			`UPDATE carriers SET name = ?, updated_at = ? WHERE id = ?`,
			name, time.Now().UTC().Format("2006-01-02 15:04:05"), id,
		)
		if err != nil {
			http.Error(w, "Update carrier failed", http.StatusInternalServerError)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}

// HandleDelete deletes a carrier; cascade removes ports, contacts, notes, and preferences.
func (s *Service) HandleDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if _, err := s.DB.Exec(`DELETE FROM carriers WHERE id = ?`, id); err != nil {
			http.Error(w, "Delete carrier failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/carriers", http.StatusSeeOther)
	}
}

// HandleAddPort assigns a new port to a carrier (creates a carrier_ports row).
func (s *Service) HandleAddPort() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		portID, err := strconv.Atoi(r.FormValue("port_id"))
		if err != nil || portID <= 0 {
			http.Error(w, "Invalid port", http.StatusBadRequest)
			return
		}
		s.DB.Exec(`INSERT OR IGNORE INTO carrier_ports (carrier_id, port_id) VALUES (?, ?)`, id, portID)
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}

// HandleRemovePort removes a port assignment; cascade deletes its contacts and clears preferences.
func (s *Service) HandleRemovePort() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		vpid, err := strconv.Atoi(r.PathValue("vpid"))
		if err != nil || vpid <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// Verify this carrier_ports row belongs to this carrier.
		var portID int
		if err := s.DB.QueryRow(
			`SELECT port_id FROM carrier_ports WHERE id = ? AND carrier_id = ?`, vpid, id,
		).Scan(&portID); err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// Clear preferences at this port before removing (no FK link between preferences and carrier_ports).
		s.DB.Exec(`DELETE FROM carrier_preferences WHERE carrier_id = ? AND port_id = ?`, id, portID)
		// Contacts cascade-delete via carrier_contacts.carrier_ports_id FK.
		s.DB.Exec(`DELETE FROM carrier_ports WHERE id = ?`, vpid)
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}

// HandleAddContact adds a contact to a specific carrier port.
func (s *Service) HandleAddContact() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		vpid, err := strconv.Atoi(r.PathValue("vpid"))
		if err != nil || vpid <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// Verify the carrier_ports row belongs to this carrier.
		var exists int
		s.DB.QueryRow(`SELECT COUNT(*) FROM carrier_ports WHERE id = ? AND carrier_id = ?`, vpid, id).Scan(&exists)
		if exists == 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		if email == "" {
			http.Error(w, "Email is required", http.StatusBadRequest)
			return
		}
		s.DB.Exec(
			`INSERT INTO carrier_contacts (carrier_ports_id, name, email) VALUES (?, ?, ?)`,
			vpid,
			nullableStr(strings.TrimSpace(r.FormValue("name"))),
			email,
		)
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}

// HandleDeleteContact removes a contact, scoped to a carrier_ports row to prevent cross-carrier deletion.
func (s *Service) HandleDeleteContact() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		vpid, err := strconv.Atoi(r.PathValue("vpid"))
		if err != nil || vpid <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		cid, err := strconv.Atoi(r.PathValue("cid"))
		if err != nil || cid <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// Verify the carrier_ports row belongs to this carrier before deleting the contact.
		var ownerCarrierID int
		if err := s.DB.QueryRow(
			`SELECT carrier_id FROM carrier_ports WHERE id = ?`, vpid,
		).Scan(&ownerCarrierID); err != nil || ownerCarrierID != id {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		s.DB.Exec(`DELETE FROM carrier_contacts WHERE id = ? AND carrier_ports_id = ?`, cid, vpid)
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}

// HandleAddNote appends a shared note to a carrier_port (author = current user).
func (s *Service) HandleAddNote() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		vpid, err := strconv.Atoi(r.PathValue("vpid"))
		if err != nil || vpid <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// Verify the carrier_ports row belongs to this carrier.
		var exists int
		s.DB.QueryRow(
			`SELECT COUNT(*) FROM carrier_ports WHERE id = ? AND carrier_id = ?`, vpid, id,
		).Scan(&exists)
		if exists == 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		content := strings.TrimSpace(r.FormValue("content"))
		if content != "" {
			s.DB.Exec(
				`INSERT INTO carrier_notes (carrier_ports_id, author_id, content) VALUES (?, ?, ?)`,
				vpid, user.ID, content,
			)
		}
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}

// HandleTogglePreference toggles the current user's preferred-carrier flag for a carrier at a port.
// pid is ports.id (not carrier_ports.id). Only permitted when the carrier services that port.
func (s *Service) HandleTogglePreference() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		id, ok := carrierIDFromPath(r)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		pid, err := strconv.Atoi(r.PathValue("pid"))
		if err != nil || pid <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		// Guard: carrier must service this port.
		var serviced int
		s.DB.QueryRow(
			`SELECT COUNT(*) FROM carrier_ports WHERE carrier_id = ? AND port_id = ?`, id, pid,
		).Scan(&serviced)
		if serviced == 0 {
			http.Error(w, "Carrier does not service this port", http.StatusBadRequest)
			return
		}

		// Toggle: remove if already preferred, add if not.
		var exists int
		s.DB.QueryRow(
			`SELECT COUNT(*) FROM carrier_preferences WHERE user_id = ? AND carrier_id = ? AND port_id = ?`,
			user.ID, id, pid,
		).Scan(&exists)
		if exists > 0 {
			s.DB.Exec(
				`DELETE FROM carrier_preferences WHERE user_id = ? AND carrier_id = ? AND port_id = ?`,
				user.ID, id, pid,
			)
		} else {
			s.DB.Exec(
				`INSERT INTO carrier_preferences (user_id, carrier_id, port_id) VALUES (?, ?, ?)`,
				user.ID, id, pid,
			)
		}
		http.Redirect(w, r, fmt.Sprintf("/carriers/%d", id), http.StatusSeeOther)
	}
}
