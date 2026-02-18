package auth

import (
	"html/template"
	"log"
	"net/http"
)

// HandleLoginPage renders the login form.
func (s *Service) HandleLoginPage(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := tmpl.ExecuteTemplate(w, "layout.html", nil); err != nil {
			log.Printf("render login page: %v", err)
		}
	}
}

// HandleLoginSubmit processes the login form and shows the confirmation page.
func (s *Service) HandleLoginSubmit(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.FormValue("email")
		if email == "" {
			http.Error(w, "Email is required", http.StatusBadRequest)
			return
		}

		if err := s.CreateMagicLink(email); err != nil {
			log.Printf("create magic link: %v", err)
		}

		if err := tmpl.ExecuteTemplate(w, "layout.html", map[string]string{"Email": email}); err != nil {
			log.Printf("render login sent page: %v", err)
		}
	}
}

// HandleVerify validates a magic link token and creates a session.
func (s *Service) HandleVerify() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token", http.StatusBadRequest)
			return
		}

		_, sessionToken, err := s.VerifyToken(token)
		if err != nil {
			http.Error(w, "Invalid or expired link. Please request a new one.", http.StatusUnauthorized)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    sessionToken,
			Path:     "/",
			MaxAge:   int(SessionExpiry.Seconds()),
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// HandleLogout clears the session and redirects to login.
func (s *Service) HandleLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err == nil {
			s.DB.Exec("DELETE FROM sessions WHERE token = ?", cookie.Value)
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}
