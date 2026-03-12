package settings

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
)

const (
	defaultGreeting  = "Hi"
	defaultBody      = "I'm looking for a partner carrier for this lane. Details are below. If interested, please let me know what your rate would be. Thank you."
	defaultClosing   = "Kind regards,"
	defaultSignature = "Schneider Transportation Management"
)

// Service handles user-scoped settings operations.
type Service struct {
	DB *sql.DB
}

// EmailTemplate holds a user's email template fields with fallback defaults.
type EmailTemplate struct {
	Greeting, Body, Closing, Signature, UpdatedAt string
}

// FetchTemplate loads the email template for the given user, returning defaults if none saved.
func FetchTemplate(db *sql.DB, userID int) EmailTemplate {
	var t EmailTemplate
	var rawUpdatedAt sql.NullString
	err := db.QueryRow(
		`SELECT greeting, body, closing, signature, updated_at FROM email_templates WHERE user_id = ?`,
		userID,
	).Scan(&t.Greeting, &t.Body, &t.Closing, &t.Signature, &rawUpdatedAt)
	if err != nil {
		return EmailTemplate{
			Greeting:  defaultGreeting,
			Body:      defaultBody,
			Closing:   defaultClosing,
			Signature: defaultSignature,
		}
	}
	if rawUpdatedAt.Valid {
		if parsed, err := time.Parse(time.RFC3339, rawUpdatedAt.String); err == nil {
			t.UpdatedAt = parsed.UTC().Format(time.RFC3339)
		} else {
			t.UpdatedAt = rawUpdatedAt.String
		}
	}
	return t
}

// HandleSettings renders the email template settings page.
// GET /settings
func (s *Service) HandleSettings(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		et := FetchTemplate(s.DB, user.ID)
		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":     user,
			"Template": et,
		})
	}
}

// HandleSaveTemplate saves or replaces the user's email template and publishes an SSE event.
// POST /settings
func (s *Service) HandleSaveTemplate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request: could not parse form", http.StatusBadRequest)
			return
		}
		greeting := r.FormValue("greeting")
		body := r.FormValue("body")
		closing := r.FormValue("closing")
		signature := r.FormValue("signature")
		now := time.Now().UTC()

		_, err := s.DB.Exec(
			`INSERT OR REPLACE INTO email_templates (user_id, greeting, body, closing, signature, updated_at)
             VALUES (?, ?, ?, ?, ?, ?)`,
			user.ID, greeting, body, closing, signature, now.Format(time.RFC3339),
		)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to save email template: %v", err), http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

