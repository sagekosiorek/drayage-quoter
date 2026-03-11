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

// HandleLoginSubmit processes the login form, sends a 6-digit code, and
// redirects to the code-entry page.
func (s *Service) HandleLoginSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.FormValue("email")
		if email == "" {
			http.Error(w, "Email is required", http.StatusBadRequest)
			return
		}

		if err := s.CreateLoginCode(email); err != nil {
			log.Printf("create login code: %v", err)
		}

		http.Redirect(w, r, "/login/verify?email="+email, http.StatusSeeOther)
	}
}

// HandleCodePage renders the 6-digit code entry form.
func (s *Service) HandleCodePage(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.URL.Query().Get("email")
		if err := tmpl.ExecuteTemplate(w, "layout.html", map[string]string{"Email": email}); err != nil {
			log.Printf("render code entry page: %v", err)
		}
	}
}

// HandleVerifyCode validates the submitted 6-digit code and creates a session.
func (s *Service) HandleVerifyCode() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.FormValue("code")
		if code == "" {
			http.Error(w, "Login code is required", http.StatusBadRequest)
			return
		}

		_, sessionToken, err := s.VerifyCode(code)
		if err != nil {
			http.Error(w, "Invalid or expired code. Please request a new one.", http.StatusUnauthorized)
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
