package lanes

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
)

// Service handles lane operations.
type Service struct {
	DB *sql.DB
}

// LaneDetail holds all data needed to render the lane detail/edit views.
type LaneDetail struct {
	ID             int
	OwnerID        int
	IsOwner        bool
	OwnerName      string
	CustomerID     int
	CustomerName   string
	CustomerLP     string
	OriginPortID   int
	OriginPort     string
	OriginPortType string
	Destination    string
	ContainerSize  string
	Weight         *int
	Direction      string
	LoadType       string
	Commodity      *string
	Hazmat         bool
	Overweight     bool
	OutOfGauge     bool
	Notes          *string
	Status         string
	CreatedAt      string
	UpdatedAt      string
}

// LaneRow holds the data needed for a single row in the dashboard list.
type LaneRow struct {
	ID            int
	CustomerName  string
	OriginPort    string
	Destination   string
	ContainerSize string
	Direction     string
	Status        string
	OwnerName     string
	CreatedAt     string
}

type portOption struct {
	ID   int
	Name string
}

type userOption struct {
	ID   int
	Name string
}

// fetchLane loads a lane with its joined owner/customer/port data.
func (s *Service) fetchLane(id int) (*LaneDetail, error) {
	var l LaneDetail
	var hazmat, overweight, outOfGauge int
	var createdStr, updatedStr string

	err := s.DB.QueryRow(`
		SELECT l.id, l.owner_id, l.destination, l.container_size, l.weight,
		       l.direction, l.load_type, l.commodity, l.hazmat, l.overweight,
		       l.out_of_gauge, l.notes, l.status, l.created_at, l.updated_at,
		       u.name, c.id, c.name, c.lp_number, p.id, p.name, p.type
		FROM lanes l
		JOIN users u ON l.owner_id = u.id
		JOIN customers c ON l.customer_id = c.id
		JOIN ports p ON l.origin_port_id = p.id
		WHERE l.id = ?
	`, id).Scan(
		&l.ID, &l.OwnerID, &l.Destination, &l.ContainerSize, &l.Weight,
		&l.Direction, &l.LoadType, &l.Commodity, &hazmat, &overweight,
		&outOfGauge, &l.Notes, &l.Status, &createdStr, &updatedStr,
		&l.OwnerName, &l.CustomerID, &l.CustomerName, &l.CustomerLP,
		&l.OriginPortID, &l.OriginPort, &l.OriginPortType,
	)
	if err != nil {
		return nil, err
	}

	l.Hazmat = hazmat == 1
	l.Overweight = overweight == 1
	l.OutOfGauge = outOfGauge == 1

	if t, err := time.Parse("2006-01-02 15:04:05", createdStr); err == nil {
		l.CreatedAt = t.Format("Jan 2, 2006")
	} else {
		l.CreatedAt = createdStr
	}
	if t, err := time.Parse("2006-01-02 15:04:05", updatedStr); err == nil {
		l.UpdatedAt = t.Format("Jan 2, 2006")
	} else {
		l.UpdatedAt = updatedStr
	}

	return &l, nil
}

// containerSizeLabel converts a storage value to a display label.
func containerSizeLabel(v string) string {
	switch v {
	case "20_40":
		return "20' / 40'"
	case "20":
		return "20'"
	case "40":
		return "40'"
	case "40HC":
		return "40' HC"
	default:
		return v
	}
}

// fetchPorts returns all ports ordered by type, name.
func (s *Service) fetchPorts() ([]portOption, error) {
	rows, err := s.DB.Query(`SELECT id, name FROM ports ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ports []portOption
	for rows.Next() {
		var p portOption
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, err
		}
		ports = append(ports, p)
	}
	return ports, nil
}

// fetchUsers returns all users ordered by name.
func (s *Service) fetchUsers() ([]userOption, error) {
	rows, err := s.DB.Query(`SELECT id, name FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []userOption
	for rows.Next() {
		var u userOption
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// HandleDashboard renders the opportunity list with optional filtering.
func (s *Service) HandleDashboard(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		q := r.URL.Query().Get("q")
		status := r.URL.Query().Get("status")
		portID, _ := strconv.Atoi(r.URL.Query().Get("port"))

		// owner param: absent → default to current user; "0" → all reps; positive int → specific rep.
		ownerStr := r.URL.Query().Get("owner")
		var ownerID int
		if ownerStr == "" {
			ownerID = user.ID
		} else {
			ownerID, _ = strconv.Atoi(ownerStr)
		}

		query := `
			SELECT l.id, c.name, p.name, l.destination, l.container_size,
			       l.direction, l.status, u.name, l.created_at
			FROM lanes l
			JOIN customers c ON l.customer_id = c.id
			JOIN ports p ON l.origin_port_id = p.id
			JOIN users u ON l.owner_id = u.id
			WHERE 1=1`
		var args []interface{}

		if q != "" {
			query += ` AND (c.name LIKE ? OR l.destination LIKE ?)`
			args = append(args, "%"+q+"%", "%"+q+"%")
		}
		if status != "" {
			query += ` AND l.status = ?`
			args = append(args, status)
		}
		if portID > 0 {
			query += ` AND l.origin_port_id = ?`
			args = append(args, portID)
		}
		if ownerID > 0 {
			query += ` AND l.owner_id = ?`
			args = append(args, ownerID)
		}
		query += ` ORDER BY l.created_at DESC`

		rows, err := s.DB.Query(query, args...)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var laneRows []LaneRow
		for rows.Next() {
			var l LaneRow
			var createdStr string
			if err := rows.Scan(&l.ID, &l.CustomerName, &l.OriginPort, &l.Destination,
				&l.ContainerSize, &l.Direction, &l.Status, &l.OwnerName, &createdStr); err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			if t, err := time.Parse("2006-01-02 15:04:05", createdStr); err == nil {
				l.CreatedAt = t.Format("Jan 2, 2006")
			} else {
				l.CreatedAt = createdStr
			}
			l.ContainerSize = containerSizeLabel(l.ContainerSize)
			if l.Direction == "import" {
				l.Direction = "Import"
			} else {
				l.Direction = "Export"
			}
			laneRows = append(laneRows, l)
		}

		ports, err := s.fetchPorts()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		users, err := s.fetchUsers()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":     user,
			"Lanes":    laneRows,
			"Query":    q,
			"Status":   status,
			"PortID":   portID,
			"OwnerID":  ownerID,
			"Ports":    ports,
			"AllUsers": users,
		})
	}
}

// HandleNewForm renders the new lane form.
func (s *Service) HandleNewForm(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		ports, err := s.fetchPorts()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  user,
			"Ports": ports,
		})
	}
}

// HandleCreate processes the new lane form submission.
func (s *Service) HandleCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		customerName := r.FormValue("customer_name")
		customerIDStr := r.FormValue("customer_id")
		lpNumber := r.FormValue("lp_number")
		originPortIDStr := r.FormValue("origin_port_id")
		destination := r.FormValue("destination")
		containerSize := r.FormValue("container_size")
		weightStr := r.FormValue("weight")
		direction := r.FormValue("direction")
		loadType := r.FormValue("load_type")
		commodity := r.FormValue("commodity")
		notes := r.FormValue("notes")
		action := r.FormValue("action")

		hazmat := r.FormValue("hazmat") == "1"
		overweightChecked := r.FormValue("overweight") == "1"
		outOfGauge := r.FormValue("out_of_gauge") == "1"

		if customerName == "" || lpNumber == "" || originPortIDStr == "" || destination == "" || containerSize == "" {
			http.Error(w, "Required fields missing", http.StatusBadRequest)
			return
		}

		originPortID, err := strconv.Atoi(originPortIDStr)
		if err != nil || originPortID <= 0 {
			http.Error(w, "Invalid port", http.StatusBadRequest)
			return
		}
		var portExists int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM ports WHERE id = ?", originPortID).Scan(&portExists); err != nil || portExists == 0 {
			http.Error(w, "Port not found", http.StatusBadRequest)
			return
		}

		var customerID int
		if customerIDStr != "" {
			customerID, _ = strconv.Atoi(customerIDStr)
		}
		if customerID == 0 {
			err = s.DB.QueryRow(
				"SELECT id FROM customers WHERE LOWER(name) = LOWER(?)", customerName,
			).Scan(&customerID)
			if err == sql.ErrNoRows {
				res, err := s.DB.Exec(
					"INSERT INTO customers (name, lp_number) VALUES (?, ?)",
					customerName, lpNumber,
				)
				if err != nil {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
					return
				}
				id, _ := res.LastInsertId()
				customerID = int(id)
			} else if err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
		}

		var weight interface{}
		if weightStr != "" {
			if wt, err := strconv.Atoi(weightStr); err == nil && wt > 0 {
				weight = wt
				if !overweightChecked {
					switch containerSize {
					case "20":
						overweightChecked = wt > 38000
					case "20_40", "40", "40HC", "Flat Rack":
						overweightChecked = wt > 44000
					}
				}
			}
		}

		btoi := func(b bool) int {
			if b {
				return 1
			}
			return 0
		}

		status := "draft"
		if action == "request_rates" {
			status = "pending"
		}

		_, err = s.DB.Exec(`
			INSERT INTO lanes (
				owner_id, customer_id, origin_port_id, destination,
				container_size, weight, direction, load_type,
				commodity, hazmat, overweight, out_of_gauge,
				notes, status
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			user.ID, customerID, originPortID, destination,
			containerSize, weight, direction, loadType,
			nullableStr(commodity), btoi(hazmat), btoi(overweightChecked), btoi(outOfGauge),
			nullableStr(notes), status,
		)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// HandleDetail renders the read-only lane detail view.
func (s *Service) HandleDetail(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		lane, err := s.fetchLane(id)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		lane.IsOwner = lane.OwnerID == user.ID

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User": user,
			"Lane": lane,
		})
	}
}

// HandleEditForm renders the pre-filled lane edit form (owner only).
func (s *Service) HandleEditForm(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		lane, err := s.fetchLane(id)
		if err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if lane.OwnerID != user.ID {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		lane.IsOwner = true

		ports, err := s.fetchPorts()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  user,
			"Lane":  lane,
			"Ports": ports,
		})
	}
}

// HandleUpdate processes the lane edit form submission (owner only).
func (s *Service) HandleUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		var ownerID int
		if err := s.DB.QueryRow("SELECT owner_id FROM lanes WHERE id = ?", id).Scan(&ownerID); err == sql.ErrNoRows {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if ownerID != user.ID {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		customerName := r.FormValue("customer_name")
		customerIDStr := r.FormValue("customer_id")
		lpNumber := r.FormValue("lp_number")
		originPortIDStr := r.FormValue("origin_port_id")
		destination := r.FormValue("destination")
		containerSize := r.FormValue("container_size")
		weightStr := r.FormValue("weight")
		direction := r.FormValue("direction")
		loadType := r.FormValue("load_type")
		commodity := r.FormValue("commodity")
		notes := r.FormValue("notes")

		hazmat := r.FormValue("hazmat") == "1"
		overweightChecked := r.FormValue("overweight") == "1"
		outOfGauge := r.FormValue("out_of_gauge") == "1"

		if customerName == "" || lpNumber == "" || originPortIDStr == "" || destination == "" || containerSize == "" {
			http.Error(w, "Required fields missing", http.StatusBadRequest)
			return
		}

		originPortID, err := strconv.Atoi(originPortIDStr)
		if err != nil || originPortID <= 0 {
			http.Error(w, "Invalid port", http.StatusBadRequest)
			return
		}
		var portExists int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM ports WHERE id = ?", originPortID).Scan(&portExists); err != nil || portExists == 0 {
			http.Error(w, "Port not found", http.StatusBadRequest)
			return
		}

		var customerID int
		if customerIDStr != "" {
			customerID, _ = strconv.Atoi(customerIDStr)
		}
		if customerID == 0 {
			err = s.DB.QueryRow(
				"SELECT id FROM customers WHERE LOWER(name) = LOWER(?)", customerName,
			).Scan(&customerID)
			if err == sql.ErrNoRows {
				res, err := s.DB.Exec(
					"INSERT INTO customers (name, lp_number) VALUES (?, ?)",
					customerName, lpNumber,
				)
				if err != nil {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
					return
				}
				id64, _ := res.LastInsertId()
				customerID = int(id64)
			} else if err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
		}

		var weight interface{}
		if weightStr != "" {
			if wt, err := strconv.Atoi(weightStr); err == nil && wt > 0 {
				weight = wt
				if !overweightChecked {
					switch containerSize {
					case "20":
						overweightChecked = wt > 38000
					case "20_40", "40", "40HC", "Flat Rack":
						overweightChecked = wt > 45000
					}
				}
			}
		}

		btoi := func(b bool) int {
			if b {
				return 1
			}
			return 0
		}

		_, err = s.DB.Exec(`
			UPDATE lanes SET
				customer_id = ?, origin_port_id = ?, destination = ?,
				container_size = ?, weight = ?, direction = ?, load_type = ?,
				commodity = ?, hazmat = ?, overweight = ?, out_of_gauge = ?,
				notes = ?, updated_at = ?
			WHERE id = ?`,
			customerID, originPortID, destination,
			containerSize, weight, direction, loadType,
			nullableStr(commodity), btoi(hazmat), btoi(overweightChecked), btoi(outOfGauge),
			nullableStr(notes), time.Now().UTC().Format("2006-01-02 15:04:05"),
			id,
		)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, fmt.Sprintf("/lanes/%d", id), http.StatusSeeOther)
	}
}

// nullableStr returns nil if s is empty, otherwise s — so empty form fields become NULL in SQLite.
func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
