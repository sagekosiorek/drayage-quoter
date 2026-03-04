package email

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rates"
)

// HandleInbound returns an http.HandlerFunc for POST /webhooks/email/inbound.
// It verifies the Mailgun HMAC signature, extracts the email fields from the
// multipart form payload, and delegates to rates.Service.IngestEmail.
// The route must be registered WITHOUT auth middleware (Mailgun posts from outside).
func HandleInbound(ratesSvc *rates.Service, signingKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, "cannot parse multipart form", http.StatusBadRequest)
			return
		}

		if signingKey != "" {
			if !verifyMailgunSignature(signingKey, r.FormValue("timestamp"), r.FormValue("token"), r.FormValue("signature")) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		subject := r.FormValue("subject")
		sender := r.FormValue("sender")

		// Prefer HTML body; fall back to plain text.
		body := r.FormValue("body-html")
		if body == "" {
			body = r.FormValue("body-plain")
		}

		status, err := ratesSvc.IngestEmail(subject, body, sender)
		if err != nil {
			log.Printf("inbound webhook ingest error: %v", err)
			http.Error(w, fmt.Sprintf("ingest error: %v", err), http.StatusInternalServerError)
			return
		}

		log.Printf("inbound email from %s: %s (subject: %q)", sender, status, subject)
		w.WriteHeader(http.StatusOK)
	}
}

// verifyMailgunSignature validates the HMAC-SHA256 signature on an inbound webhook.
// Mailgun signs with: HMAC-SHA256(timestamp + token, signingKey).
func verifyMailgunSignature(signingKey, timestamp, token, signature string) bool {
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(timestamp + token))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
