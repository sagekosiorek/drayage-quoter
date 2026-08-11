package email

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rates"
)

// mgJSONPayload is the structure of Mailgun's newer JSON webhook format.
type mgJSONPayload struct {
	Signature struct {
		Timestamp string `json:"timestamp"`
		Token     string `json:"token"`
		Signature string `json:"signature"`
	} `json:"signature"`
	EventData struct {
		Sender  string `json:"sender"`
		Message struct {
			Headers struct {
				Subject string `json:"subject"`
			} `json:"headers"`
			BodyPlain string `json:"body-plain"`
			BodyHTML  string `json:"body-html"`
		} `json:"message"`
	} `json:"event-data"`
}

// HandleInbound returns an http.HandlerFunc for POST /webhooks/email/inbound.
// It verifies the Mailgun HMAC signature, extracts the email fields from either
// a multipart/form-data payload (legacy Routes) or an application/json payload
// (new Webhooks API), and delegates to rates.Service.IngestEmail.
// The route must be registered WITHOUT auth middleware (Mailgun posts from outside).
func HandleInbound(ratesSvc *rates.Service, signingKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var subject, sender, body string
		var timestamp, token, sig string

		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			var p mgJSONPayload
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, "cannot parse JSON body", http.StatusBadRequest)
				return
			}
			timestamp = p.Signature.Timestamp
			token = p.Signature.Token
			sig = p.Signature.Signature
			subject = p.EventData.Message.Headers.Subject
			sender = p.EventData.Sender
			body = p.EventData.Message.BodyHTML
			if body == "" {
				body = p.EventData.Message.BodyPlain
			}
		} else {
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				http.Error(w, "cannot parse multipart form", http.StatusBadRequest)
				return
			}
			timestamp = r.FormValue("timestamp")
			token = r.FormValue("token")
			sig = r.FormValue("signature")
			subject = r.FormValue("subject")
			sender = r.FormValue("sender")
			body = r.FormValue("body-html")
			if body == "" {
				body = r.FormValue("body-plain")
			}
		}

		if signingKey != "" {
			if !verifyMailgunSignature(signingKey, timestamp, token, sig) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
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
