package admin

import (
	"database/sql"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
)

// Service handles admin operations.
type Service struct {
	DB *sql.DB
}

// UserRow represents a user for display in the admin view.
type UserRow struct {
	ID        int
	Name      string
	Email     string
	IsAdmin   bool
	CreatedAt string
}

 // PortRow represents a port for display in the admin view.
 type PortRow struct {
     ID   int
     Name string
     Type string
 }

// HandleUsers renders the admin user list.
func (s *Service) HandleUsers(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentUser := auth.UserFromContext(r.Context())

		rows, err := s.DB.Query(`
			SELECT id, name, email, is_admin, created_at
			FROM users
			ORDER BY created_at DESC
		`)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var users []UserRow
		for rows.Next() {
			var u UserRow
			var createdAtStr string
			if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.IsAdmin, &createdAtStr); err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			if t, err := time.Parse("2006-01-02 15:04:05", createdAtStr); err == nil {
				u.CreatedAt = t.Format("Jan 2, 2006")
			} else {
				u.CreatedAt = createdAtStr
			}
			users = append(users, u)
		}

		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  currentUser,
			"Users": users,
		})
	}
}

// HandleAddUser adds a new user by name and email.
func (s *Service) HandleAddUser() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.FormValue("name")
		email := r.FormValue("email")
		if name == "" || email == "" {
			http.Error(w, "Name and email are required", http.StatusBadRequest)
			return
		}
		_, err := s.DB.Exec(
			"INSERT OR IGNORE INTO users (name, email) VALUES (?, ?)",
			name, email,
		)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// HandleDeleteUser removes a user by id. Guards against self-deletion.
func (s *Service) HandleDeleteUser() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		currentUser := auth.UserFromContext(r.Context())
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "Invalid user id", http.StatusBadRequest)
			return
		}
		if id == currentUser.ID {
			http.Error(w, "Cannot delete your own account", http.StatusBadRequest)
			return
		}
		if _, err = s.DB.Exec("DELETE FROM users WHERE id = ?", id); err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
}

// HandlePorts renders the admin port list.
func (s *Service) HandlePorts(tmpl *template.Template) http.HandlerFunc {
	 return func(w http.ResponseWriter, r *http.Request) {
			 currentUser := auth.UserFromContext(r.Context())
			 rows, err := s.DB.Query(`SELECT id, name, type FROM ports ORDER BY type, name`)
			 if err != nil {
					 http.Error(w, "Internal server error", http.StatusInternalServerError)
					 return
			 }
			 defer rows.Close()
			 var ports []PortRow
			 for rows.Next() {
					 var p PortRow
					 if err := rows.Scan(&p.ID, &p.Name, &p.Type); err != nil {
							 http.Error(w, "Internal server error", http.StatusInternalServerError)
							 return
					 }
					 ports = append(ports, p)
			 }
			 tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
					 "User":  currentUser,
					 "Ports": ports,
			 })
	 }
}

// HandleAddPort adds a new port by name and type.
func (s *Service) HandleAddPort() http.HandlerFunc {
	 return func(w http.ResponseWriter, r *http.Request) {
			 name := r.FormValue("name")
			 portType := r.FormValue("type")
			 if name == "" || portType == "" {
					 http.Error(w, "Name and type are required", http.StatusBadRequest)
					 return
			 }
			 if portType != "Seaport" && portType != "Rail Ramp" {
					 http.Error(w, "Invalid port type", http.StatusBadRequest)
					 return
			 }
			 _, err := s.DB.Exec("INSERT INTO ports (name, type) VALUES (?, ?)", name, portType)
			 if err != nil {
					 http.Error(w, "Internal server error", http.StatusInternalServerError)
					 return
			 }
			 http.Redirect(w, r, "/admin/ports", http.StatusSeeOther)
	 }
}

// HandleDeletePort removes a port by id.
func (s *Service) HandleDeletePort() http.HandlerFunc {
	 return func(w http.ResponseWriter, r *http.Request) {
			 id, err := strconv.Atoi(r.PathValue("id"))
			 if err != nil || id <= 0 {
					 http.Error(w, "Invalid port id", http.StatusBadRequest)
					 return
			 }
			 if _, err = s.DB.Exec("DELETE FROM ports WHERE id = ?", id);
			 err != nil {
					 http.Error(w, "Internal server error", http.StatusInternalServerError)
					 return
			 }
			 http.Redirect(w, r, "/admin/ports", http.StatusSeeOther)
	 }
}
