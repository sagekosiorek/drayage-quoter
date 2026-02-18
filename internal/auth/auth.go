package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"time"
)

const (
	TokenExpiry   = 15 * time.Minute
	SessionExpiry = 30 * 24 * time.Hour
	TokenBytes    = 32
)

// User represents an authenticated user.
type User struct {
	ID    int
	Name  string
	Email string
}

// EmailSender abstracts email delivery.
type EmailSender interface {
	SendMagicLink(to string, link string) error
}

// LogEmailSender prints magic links to stdout for development.
type LogEmailSender struct{}

func (s LogEmailSender) SendMagicLink(to string, link string) error {
	log.Printf("MAGIC LINK for %s: %s", to, link)
	return nil
}

// Service handles auth token and session operations.
type Service struct {
	DB      *sql.DB
	Email   EmailSender
	BaseURL string
}

// GenerateToken creates a cryptographically random hex token.
func GenerateToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateMagicLink generates a magic link token for the given email.
// Returns nil even for unknown emails to prevent enumeration.
func (s *Service) CreateMagicLink(email string) error {
	var userID int
	err := s.DB.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	token, err := GenerateToken()
	if err != nil {
		return err
	}

	expiresAt := time.Now().UTC().Add(TokenExpiry)
	_, err = s.DB.Exec(
		"INSERT INTO auth_tokens (user_id, token, expires_at) VALUES (?, ?, ?)",
		userID, token, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("store token: %w", err)
	}

	link := fmt.Sprintf("%s/auth/verify?token=%s", s.BaseURL, token)
	return s.Email.SendMagicLink(email, link)
}

// VerifyToken validates a magic link token, marks it used, and creates a session.
// Returns the authenticated user and a session token.
func (s *Service) VerifyToken(token string) (*User, string, error) {
	var tokenID, userID int
	err := s.DB.QueryRow(`
		SELECT id, user_id FROM auth_tokens
		WHERE token = ? AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
	`, token).Scan(&tokenID, &userID)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("invalid or expired token")
	}
	if err != nil {
		return nil, "", fmt.Errorf("lookup token: %w", err)
	}

	if _, err := s.DB.Exec("UPDATE auth_tokens SET used_at = CURRENT_TIMESTAMP WHERE id = ?", tokenID); err != nil {
		return nil, "", fmt.Errorf("mark token used: %w", err)
	}

	var user User
	err = s.DB.QueryRow("SELECT id, name, email FROM users WHERE id = ?", userID).Scan(&user.ID, &user.Name, &user.Email)
	if err != nil {
		return nil, "", fmt.Errorf("lookup user: %w", err)
	}

	sessionToken, err := GenerateToken()
	if err != nil {
		return nil, "", err
	}

	expiresAt := time.Now().UTC().Add(SessionExpiry)
	_, err = s.DB.Exec(
		"INSERT INTO sessions (user_id, token, expires_at) VALUES (?, ?, ?)",
		user.ID, sessionToken, expiresAt,
	)
	if err != nil {
		return nil, "", fmt.Errorf("create session: %w", err)
	}

	return &user, sessionToken, nil
}
