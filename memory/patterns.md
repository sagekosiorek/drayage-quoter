# Code Patterns & Conventions

## Service Struct Pattern
Every domain package uses the same shape. Handler methods are factory functions that close over the template and service.

```go
type Service struct {
    DB *sql.DB
    // rates package adds: LLM LLMCorrector
    // auth package adds:  Email EmailSender, BaseURL string
}

// Handler factory: pre-parse template in main.go, pass into factory
func (s *Service) HandleDetail(tmpl *template.Template) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        user := auth.UserFromContext(r.Context())
        // ... query DB, build data struct ...
        tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
            "User": user,
            "Lane": lane,
        })
    }
}
```

## Route Registration Pattern (main.go)
Templates are parsed once at startup, then injected:

```go
laneDetailTmpl := templates.MustParse("layout.html", "lane_detail.html")
mux.HandleFunc("GET /lanes/{id}",
    authSvc.RequireAuth(lanesSvc.HandleDetail(laneDetailTmpl)))
```

Go 1.22+ method+path routing syntax: `"GET /path"`, `"POST /path"`.

## Auth Middleware Pattern
```go
// Wrap with RequireAuth at route registration
authSvc.RequireAuth(nextHandler)

// RequireAuth injects user into context:
ctx := context.WithValue(r.Context(), userContextKey, &user)
next(w, r.WithContext(ctx))

// Retrieve in any handler:
user := auth.UserFromContext(r.Context())  // returns *auth.User
```
Redirect to `/login` if session missing/expired. Admin-only routes additionally wrapped with `RequireAdmin` (returns 403 if `is_admin != 1`).

## Ownership Check Pattern
Returns 403 (not redirect) for non-owners:
```go
var ownerID int
s.DB.QueryRow("SELECT owner_id FROM lanes WHERE id = ?", id).Scan(&ownerID)
if ownerID != user.ID {
    http.Error(w, "Forbidden", http.StatusForbidden)
    return
}
```

## Template Execution Pattern
Always execute to `"layout.html"` — never the page template name:
```go
tmpl.ExecuteTemplate(w, "layout.html", map[string]any{
    "User":  user,       // always include; nil → auth-only layout
    "Lane":  lane,       // domain data
})
```
Each page template defines: `{{ define "content" }}...{{ end }}`

## HTMX Fragment Pattern
Endpoints returning partial HTML (autocomplete, search results):
```go
func (s *Service) HandleSearch() http.HandlerFunc {
    // Inline template — not pre-parsed at startup, fragment only
    tmpl := template.Must(template.New("suggestions").Parse(`
        {{ range . }}<button ...>{{ .Name }}</button>{{ end }}
    `))
    return func(w http.ResponseWriter, r *http.Request) {
        q := r.URL.Query().Get("q")
        if len(q) < 2 {
            w.WriteHeader(http.StatusOK) // empty response
            return
        }
        // query, render fragment
        tmpl.Execute(w, results)
    }
}
```

HTML side:
```html
<input hx-get="/customers/search"
       hx-trigger="input changed delay:300ms"
       hx-target="#suggestions"
       hx-include="this">
<div id="suggestions"></div>
```

JS helper to populate multiple hidden fields on suggestion click:
```js
function selectCustomer(el) {
    document.getElementById('customer-name').value = el.dataset.name;
    document.getElementById('customer-id').value = el.dataset.id;
    document.getElementById('suggestions').innerHTML = '';
}
```

## Nullable Field Handling
Use pointer types for DB-nullable fields. SQL NULL → Go nil automatically:
```go
type LaneDetail struct {
    Weight    *int    // nil if weight not set
    Commodity *string // nil if not set
}
// Scan:
row.Scan(&lane.Weight, &lane.Commodity)
// Template:
// {{ if .Lane.Weight }}{{ deref .Lane.Weight }} lbs{{ end }}
```

On INSERT/UPDATE, convert empty string → nil:
```go
func nullableStr(s string) interface{} {
    if s == "" { return nil }
    return s
}
// Usage:
s.DB.Exec("INSERT INTO lanes (..., commodity) VALUES (?, ...)", nullableStr(commodity), ...)
```

## Date Formatting Pattern
SQLite stores `DATETIME` as `"2006-01-02 15:04:05"` (UTC). Parse and reformat for display:
```go
if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
    formatted = t.Format("Jan 2, 2006")
} else {
    formatted = raw // fallback: show as-is
}
```
Always use `time.Now().UTC()` when writing timestamps (avoids drift vs. `CURRENT_TIMESTAMP`).

## Controlled Vocab Validation
Validate at server in handler (not just client-side):
```go
if portType != "Seaport" && portType != "Rail Ramp" {
    http.Error(w, "Invalid port type", http.StatusBadRequest)
    return
}
```

## Idempotent Insert Pattern
For entries that should not duplicate (users, ports):
```go
s.DB.Exec("INSERT OR IGNORE INTO users (name, email) VALUES (?, ?)", name, email)
```

## Upsert / Toggle Pattern (preferences)
For boolean-style junction table rows:
```go
// Check if exists
var exists int
s.DB.QueryRow("SELECT COUNT(*) FROM vendor_preferences WHERE user_id=? AND vendor_id=? AND port_id=?", ...).Scan(&exists)
if exists > 0 {
    s.DB.Exec("DELETE FROM vendor_preferences WHERE ...")
} else {
    s.DB.Exec("INSERT INTO vendor_preferences (user_id, vendor_id, port_id) VALUES (?, ?, ?)", ...)
}
```

## Transactional Insert Pattern (multi-table writes)
Use a transaction when inserting parent + children:
```go
tx, err := s.DB.Begin()
if err != nil { http.Error(w, "Internal server error", 500); return }
defer tx.Rollback()

result, err := tx.Exec("INSERT INTO vendor_rates (...) VALUES (...)", ...)
if err != nil { http.Error(w, "...", 500); return }
vrID, _ := result.LastInsertId()

for _, item := range items {
    tx.Exec("INSERT INTO vendor_rate_items (...) VALUES (...)", vrID, ...)
}
tx.Commit()
```

## Status State Machine Pattern
Centralized map for valid transitions:
```go
var nextStatus = map[string]string{
    "draft":           "rates_requested",
    "rates_requested": "rates_received",
    "rates_received":  "quoting",
    "quoting":         "quoted",
}

// In advance handler:
next, ok := nextStatus[currentStatus]
if !ok {
    http.Error(w, "No valid next status", http.StatusBadRequest)
    return
}
s.DB.Exec("UPDATE lanes SET status = ?, updated_at = ? WHERE id = ?", next, time.Now().UTC(), id)
```

## Cascading Deletes
Defined in migrations with `ON DELETE CASCADE`:
- `vendor_ports` → cascades to `vendor_contacts`, `vendor_notes` (by vendor_ports_id FK)
- `vendor_rates` → cascades to `vendor_rate_items`
- `rate_requests` → cascades to `rate_request_vendors`, `vendor_rates`
- `lanes` → cascades to `rate_requests`

Soft reference: `vendor_ports.contact_id` has NO FK (avoids circular dependency with `vendor_contacts`). Lookup is done manually in code.

## Email Enumeration Prevention
Auth `CreateMagicLink` returns nil (success) for unknown emails — never reveals whether email is registered:
```go
// Silently succeed even if user not found
var user User
err := s.DB.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&user.ID)
if err != nil { return nil } // no error exposed to caller
```

## Reference ID Generation
Year-scoped sequential counter (rate requests):
```go
func generateReferenceID(db *sql.DB) (string, error) {
    year := time.Now().Year()
    var count int
    db.QueryRow("SELECT COUNT(*) FROM rate_requests WHERE reference_id LIKE ?",
        fmt.Sprintf("RR-%d-%%", year)).Scan(&count)
    return fmt.Sprintf("RR-%d-%05d", year, count+1), nil
}
```

## Mailto Link Construction
For rate request blast, build `mailto:` href with pre-filled subject + body:
```go
func encodeMailtoParam(s string) string {
    return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
func encodeMailtoBody(s string) string {
    // CRLF for email line breaks; space as %20 (not +)
    s = strings.ReplaceAll(s, "\n", "%0D%0A")
    return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
// Result: mailto:email@vendor.com?subject=...&body=...
```
The `[First Name]` placeholder in the body is replaced per-vendor before building the mailto href.

## Admin Sub-Nav Pattern
In admin templates, active state is set statically per template:
```html
<nav class="admin-subnav">
    <a href="/admin/users" class="active">Users</a>
    <a href="/admin/ports">Ports</a>
</nav>
```
CSS class `.active` defined in `style.css` for the `admin-subnav` nav element.

## Form POST + Redirect Pattern
All mutating handlers follow POST → redirect (PRG pattern):
```go
// After successful DB write:
http.Redirect(w, r, "/vendors/"+vendorID, http.StatusSeeOther)
```
Prevents double-submit on browser back/refresh.

## Rate Parsing Pipeline
```go
// Stage 1: regex
result := rates.ExtractRates(rawBody)

// Stage 2: LLM correction (pluggable)
corrected, err := s.LLM.CorrectRates(rawBody, result.Items)

// Stage 3: standardize
standardized := rates.Standardize(corrected)

// Save
saveVendorRate(rrID, vendorID, rawEmail, parsedBy, standardized)
```
`LLMCorrector` interface allows swapping implementations without changing pipeline.

## API Ingest JSON Pattern
`POST /api/rates/parse` accepts JSON, returns JSON:
```go
// Input:
type IngestRequest struct {
    Subject string `json:"subject"`
    Body    string `json:"body"`
    Sender  string `json:"sender"`
}
// Output:
{"status": "matched", "rate_request_id": 42}
// or:
{"status": "orphaned", "orphan_id": 7, "reason": "no_reference_id"}
```
Orphan reasons: `no_reference_id`, `reference_not_found`, `sender_not_matched`.
