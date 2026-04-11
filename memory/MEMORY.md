# Project Memory

## Current State
- **cmd/import**: General-purpose CSV/Excel importer. YAML config maps file headers to DB tables with dedup, FK lookup, and parent-child injection. Config: `import-configs/vendors.yaml`. Usage: `go run ./cmd/import --file f.csv --config c.yaml [--dry-run] [--db path]`. New dep: `gopkg.in/yaml.v3`. Note: tables without `dedup_key` always insert (contacts) — re-running duplicates them; add `dedup_key: email` if idempotency needed.
- **Email wired**: Mailgun integration complete (outbound + inbound + notifications). Env vars: `MAILGUN_API_KEY`, `MAILGUN_DOMAIN`, `MAILGUN_FROM`, `MAILGUN_WEBHOOK_SIGNING_KEY`. Falls back to `LogEmailSender` in dev if unset.
- **M6 complete** (partial — through CSV export): Markup + Quote Generation done. M7 next: LLM corrector.
- 21 migrations applied (001–021). Next migration: 022.
- Dev: `go run ./cmd/server`. DB at `./data/drayage.db`. No Litestream needed locally.
- Prod: Fly.io app `drayage-quoter` (region `ewr`), `/data` persistent volume.

## CSS Architecture
- `style.css` has CSS custom properties (`:root`) for all colors, radius, fonts. Use `var(--color-brand)` etc.
- Utility classes: `.flex-row`, `.flex-row-lg`, `.flex-between`, `.flex-wrap-sm`, `.content-md/.lg`, `.form-inline`
- Typography: `.text-muted/.secondary/.faint/.danger/.success`, `.label-caps`, `.meta-text`, `.ref-id`, `.text-pre-wrap`, `.text-sm/.xs/.2xs`
- Table cells: `.cell-center`, `.cell-center-click`, `.cell-center-empty`, `.cell-total`, `.cell-avg`, `.cell-avg-total`, `.col-header-rank`, `.row-highlight-green`
- Page headers: `.page-header` (space-between), `.page-header-start` (inline title+badge)
- Alerts: `.alert-warn` with scoped `strong` / `ul` children
- Display panels: `.display-box`, `.display-pre`, `.collapse-section`, `.collapse-summary`, `.detail-body`
- Inputs: `.input-amount` (80px), `.input-amount-lg` (100px)
- Legend: `.legend-swatch`, `.legend-swatch-llm/.edited`
- Subnav: `.subnav` (consolidated from `.admin-subnav` + `.settings-subnav`)
- Misc: `.link-secondary`, `.col-action-sm`, `.btn-remove`, `.email-plain`
- Carousel: `.carousel-header`, `.carousel-nav`, `.carousel-btn`, `.carousel-label-wrap`, `.carousel-label`, `.carousel-sub`
- Vendor col sub-headers: `.col-vendor-name`, `.col-vendor-rank`

## Architecture & Patterns Reference
- Full route map, schema, package inventory, templates: `memory/architecture.md`
- Code conventions and recurring patterns: `memory/patterns.md`

## Key Gotchas
- SQLite `CURRENT_TIMESTAMP` = UTC. Use `time.Now().UTC()` in Go for timestamp comparisons.
- Migration runner uses `tx.Exec(string(content))` — **separate files** for seed data vs schema (multi-statement ambiguity).
- Foreign keys require explicit `PRAGMA foreign_keys = ON` at DB init (done in `internal/db/db.go`).
- `modernc.org/sqlite` is pure Go (no CGO) — `CGO_ENABLED=0` in Dockerfile.
- Template `ExecuteTemplate` target is always `"layout.html"`, never the page template name.
- HTMX 2.0.4 from CDN — breaking changes from 1.x (event names, `hx-include` behavior differ).
- `nullableStr(s string) interface{}` helper: returns `nil` if empty → SQL NULL. Used in lanes + vendors.
- **`cmd/import --db` flag restores old schema**: Running the importer with `--db path` against an existing DB file that predates a migration change will keep the stale schema, since the migration runner skips already-applied migrations. If a migration modified an existing file (vs. adding a new one), the prod DB must be fully wiped for the change to take effect. Always use a new migration file for schema changes after first deploy.

## SSE Pattern (M5b, `internal/events/`)
- `events.Broker` — in-process pub/sub hub (`Subscribe`, `Unsubscribe`, `Publish`). Injected into `lanes.Service` and `rates.Service`.
- `rates` publishes `"lane:{id}" → "rates_received"` after the status flip (only when `RowsAffected > 0`). Both `saveVendorRate` and `HandleIngestConfirm` paths publish.
- `lanes.HandleEvents()` → `GET /lanes/{id}/events` — streams `event: status` SSE. Event data is a ready-to-swap `<span class="status-badge ...">` HTML fragment.
- Client: `hx-ext="sse"` + `sse-connect` + `sse-swap="status" hx-swap="outerHTML"` on the badge span. Dashboard adds SSE only for `rates_requested` lanes.
- HTMX SSE extension: `https://unpkg.com/htmx-ext-sse@2.2.2/sse.js` (loaded in `layout.html`).
- `LaneRow.StatusLabel` added; dashboard badge now shows human label (was raw status string).

## Service/Handler Shape (all packages follow this)
```go
type Service struct { DB *sql.DB }  // rates also has LLM, Broker; auth also has Email, BaseURL; lanes also has Broker

func (s *Service) HandleX(tmpl *template.Template) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) { ... }
}
```
Templates pre-parsed in `main.go`, injected into handler factories. HTMX fragments use inline `template.Must(template.New("").Parse(...))`.

## Template Conventions
- All in `internal/templates/`, embedded via `embed.FS`
- Parse pairs: `templates.MustParse("layout.html", "<page>.html")`
- Layout dual-mode: `.User` set → full app shell nav; absent → centered auth box (480px)
- Page templates: `{{ define "content" }}...{{ end }}`
- Execute: `tmpl.ExecuteTemplate(w, "layout.html", map[string]any{"User": user, ...})`

## Status State Machine
`draft → rates_requested → rates_received → quoting → quoted`
- `rates_requested`: first vendor mailto: link clicked (`POST /lanes/{id}/status`)
- `rates_received`: first parsed vendor rate saved (set in rates handler)
- `quoting`: vendor lineup saved (M5)
- `quoted`: CSV export (M6)

## Overweight Rules (lanes)
- 20' container: > 38,000 lbs → overweight = 1
- All others (40', 40HC, Flat Rack, 20_40): > 45,000 lbs → overweight = 1
- Auto-computed on create/update in `internal/lanes/handlers.go`

## Rate Parsing Pipeline (M4, `internal/rates/`)
- **Stage 1** (`parser.go`): Regex extraction, 24 charge types, order-dependent (`extreme_overweight` before `regular_overweight`)
- **Stage 2** (`llm.go`): `LLMCorrector` interface; `NoopCorrector` is default (wired in M7)
- **Stage 3** (`standardize.go`): Normalizes units and charge type naming
- `parsed_by` values: `"regex"` | `"llm"` | `"manual"`
- `manually_edited` flag set when user edits parsed amount during ingest confirm step

## Email Integration (`internal/email/`)
- `mailgun.go`: `Sender{APIKey, Domain, From}` — implements `auth.EmailSender` (SendMagicLink) + `Send(to, subject, body)` for notifications
- `inbound.go`: `HandleInbound(ratesSvc, signingKey)` — Mailgun webhook; verifies HMAC-SHA256 sig; delegates to `rates.Service.IngestEmail`
- Webhook route: `POST /webhooks/email/inbound` (unauthenticated, HMAC-verified)
- `rates.Service` gained: `Notify func(to, subject, body string) error`, `BaseURL string`
- `rates.Service.IngestEmail(subject, body, sender)` extracted from `HandleAPIIngest` — shared by API and webhook
- `rates.Service.checkAndNotify(rrID)` — fires threshold notification; called after every `saveVendorRate` and `HandleIngestConfirm`
- Deadline poller: background goroutine in `main.go`, ticks every 5 min, calls `checkDeadlines(db, notify, baseURL)`

## M5 Implementation Notes
- `vendor_lineups` table: `lane_id FK, vendor_rate_id FK, rank INTEGER, UNIQUE(lane_id, rank)` (migration 019)
- Comparison edit page: `GET /rate-requests/{id}/comparison` → `rate_comparison_edit.html` (no Average column; rank inputs)
- Lineup save: `POST /rate-requests/{id}/lineup` → DELETE all + INSERT ranked; advances lane to `quoting`; redirects to `/comparison/saved`
- HTMX inline editing: `GET /vendor-rate-items/{id}/edit`, `POST /vendor-rate-items/{id}`, `GET /vendor-rate-items/{id}`, `GET /vendor-rates/{id}/email`
- `rates.FieldOrder` exported (was `fieldOrder`) — used by rate_requests handler for column ordering
- `rates.FmtCellAmount(amount, unit)` exported — formats amounts with unit ($, %, $/day, hrs, days)
- Cell indicators: `cell-llm` (amber) = parsed_by="llm"; `cell-edited` (blue) = manually_edited=1

## M6 Implementation Notes
- Migrations 020 (quotes, quote_lanes) + 021 (markups, markup_items)
- `GET /rate-requests/{id}/comparison/saved` → `rate_comparison_saved.html`; `SavedComparisonData`
- `POST /rate-requests/{id}/csv` → `HandleGenerateCSV`: persists markup, creates/reuses quote, streams CSV, flips lane to `quoted`
- `linehaul_fuel` special markup key: combines linehaul+fuel; flat or percent mode
- Carousel JS: `window.lineupBases` JSON array; index 0 = averages, 1..N = rank-N vendor amounts
- Quote creation: atomic upsert — if `quote_lanes` row exists for lane, reuse quote; otherwise create
- Customer row: upserted by owner email if no customer exists (placeholder until customer picker added)

## Reference ID Format
`RR-YYYY-NNNNN` (e.g., `RR-2026-00001`). Year-scoped sequential counter. Extracted from email subject via regex in API ingest for rate matching.
