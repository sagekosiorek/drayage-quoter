package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"time"
)

const (
	TokenExpiry   = 15 * time.Minute
	SessionExpiry = 30 * 24 * time.Hour
)

// User represents an authenticated user.
type User struct {
	ID    int
	Name  string
	Email string
	IsAdmin bool
}

// EmailSender abstracts email delivery.
type EmailSender interface {
	SendLoginCode(to string, code string) error
}

// LogEmailSender prints login codes to stdout for development.
type LogEmailSender struct{}

func (s LogEmailSender) SendLoginCode(to string, code string) error {
	log.Printf("LOGIN CODE for %s: %s", to, code)
	return nil
}

// Service handles auth token and session operations.
type Service struct {
	DB      *sql.DB
	Email   EmailSender
	BaseURL string
}

// generateSessionToken creates a cryptographically random 32-byte hex session token.
func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GenerateCode creates a cryptographically random 6-digit numeric code.
func GenerateCode() (string, error) {
	var n uint32
	if err := binary.Read(rand.Reader, binary.BigEndian, &n); err != nil {
		return "", fmt.Errorf("generate code: %w", err)
	}
	return fmt.Sprintf("%06d", n%1_000_000), nil
}

// CreateLoginCode generates a 6-digit code for the given email and sends it.
// Returns nil even for unknown emails to prevent enumeration.
func (s *Service) CreateLoginCode(email string) error {
	var userID int
	err := s.DB.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	code, err := GenerateCode()
	if err != nil {
		return err
	}

	expiresAt := time.Now().UTC().Add(TokenExpiry)
	_, err = s.DB.Exec(
		"INSERT INTO auth_tokens (user_id, token, expires_at) VALUES (?, ?, ?)",
		userID, code, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("store login code: %w", err)
	}

	return s.Email.SendLoginCode(email, code)
}

// VerifyCode validates a 6-digit login code, marks it used, and creates a session.
// Returns the authenticated user and a session token.
func (s *Service) VerifyCode(code string) (*User, string, error) {
	var tokenID, userID int
	err := s.DB.QueryRow(`
		SELECT id, user_id FROM auth_tokens
		WHERE token = ? AND used_at IS NULL AND expires_at > CURRENT_TIMESTAMP
	`, code).Scan(&tokenID, &userID)
	if err == sql.ErrNoRows {
		return nil, "", fmt.Errorf("invalid or expired code")
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

	sessionToken, err := generateSessionToken()
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
