package email

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Sender delivers email via the Mailgun HTTP API.
type Sender struct {
	APIKey string // Mailgun private API key
	Domain string // Mailgun sending domain, e.g. "mail.example.com"
	From   string // From address, e.g. "Drayage Quoter <noreply@mail.example.com>"
}

// SendMagicLink implements auth.EmailSender: sends a passwordless login link.
func (s *Sender) SendMagicLink(to, link string) error {
	subject := "Your Drayage Quoter login link"
	body := fmt.Sprintf("Click the link below to log in. It expires in 15 minutes.\n\n%s\n\nIf you did not request this, you can ignore this email.", link)
	return s.Send(to, subject, body)
}

// Send delivers a plain-text email to a single recipient via Mailgun.
func (s *Sender) Send(to, subject, body string) error {
	params := url.Values{}
	params.Set("from", s.From)
	params.Set("to", to)
	params.Set("subject", subject)
	params.Set("text", body)

	endpoint := fmt.Sprintf("https://api.mailgun.net/v3/%s/messages", s.Domain)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return fmt.Errorf("build mailgun request: %w", err)
	}
	req.SetBasicAuth("api", s.APIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailgun send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mailgun send: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
