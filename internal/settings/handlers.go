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

// UserPreferences holds per-user application preferences.
type UserPreferences struct {
	DefaultMarkupMethod string // "flat" | "flat_rate" | "percent"
	UpdatedAt           string
}

// FetchPreferences loads preferences for userID; returns defaults if none saved.
func FetchPreferences(db *sql.DB, userID int) UserPreferences {
	var m string
	var rawUpdatedAt sql.NullString
	err := db.QueryRow(`SELECT default_markup_method, updated_at FROM user_preferences WHERE user_id = ?`, userID).Scan(&m, &rawUpdatedAt)
	if err != nil {
		return UserPreferences{DefaultMarkupMethod: "flat"}
	}
	prefs := UserPreferences{DefaultMarkupMethod: m}
	if rawUpdatedAt.Valid {
		if parsed, err := time.Parse(time.RFC3339, rawUpdatedAt.String); err == nil {
			prefs.UpdatedAt = parsed.UTC().Format(time.RFC3339)
		} else {
			prefs.UpdatedAt = rawUpdatedAt.String
		}
	}
	return prefs
}

// HandlePreferences renders the preferences settings tab.
// GET /settings/preferences
func (s *Service) HandlePreferences(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		prefs := FetchPreferences(s.DB, user.ID)
		tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
			"User":  user,
			"Prefs": prefs,
		})
	}
}

// HandleSavePreferences persists the user's application preferences.
// POST /settings/preferences
func (s *Service) HandleSavePreferences() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request: could not parse form", http.StatusBadRequest)
			return
		}
		method := r.FormValue("default_markup_method")
		if method != "flat_rate" && method != "percent" {
			method = "flat"
		}
		if _, err := s.DB.Exec(
			`INSERT OR REPLACE INTO user_preferences (user_id, default_markup_method, updated_at) VALUES (?, ?, ?)`,
			user.ID, method, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			http.Error(w, fmt.Sprintf("Failed to save preferences: %v", err), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings/preferences", http.StatusSeeOther)
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

