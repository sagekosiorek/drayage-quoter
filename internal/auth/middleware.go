package auth

import (
	"context"
	"database/sql"
	"net/http"
)

type contextKey string

const userContextKey contextKey = "user"

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(userContextKey).(*User)
	return u
}

// RequireAdmin wraps RequireAuth and additionally requires the user to be an admin.
// Returns 403 Forbidden (not a redirect) for authenticated non-admins.
func (s *Service) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if !user.IsAdmin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// RequireAuth wraps a handler, redirecting unauthenticated requests to /login.
func (s *Service) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		var user User
		err = s.DB.QueryRow(`
			SELECT u.id, u.name, u.email, u.is_admin
			FROM sessions s
			JOIN users u ON u.id = s.user_id
			WHERE s.token = ? AND s.expires_at > CURRENT_TIMESTAMP
		`, cookie.Value).Scan(&user.ID, &user.Name, &user.Email, &user.IsAdmin)
		if err == sql.ErrNoRows {
			http.SetCookie(w, &http.Cookie{
				Name:     "session",
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
			})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, &user)
		next(w, r.WithContext(ctx))
	}
}
