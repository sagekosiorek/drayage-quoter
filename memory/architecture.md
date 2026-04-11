# Architecture Reference

## Directory Tree
```
drayage-quoter/
├── cmd/
│   ├── server/main.go          # Route registration, service wiring, server start
│   └── seed/                   # Optional DB seeding (main.go + seed.go)
├── internal/
│   ├── admin/handlers.go       # Admin: users + ports CRUD
│   ├── auth/
│   │   ├── auth.go             # Magic link logic, session creation
│   │   ├── handlers.go         # Login/logout/verify endpoints
│   │   └── middleware.go       # RequireAuth, RequireAdmin middleware
│   ├── db/
│   │   ├── db.go               # SQLite init (WAL, FK pragma), migration runner
│   │   └── migrations/         # 001–018 .sql files (schema + seed separate)
│   ├── lanes/handlers.go       # Lane CRUD, dashboard, status advance
│   ├── rate_requests/handlers.go  # Rate request creation, detail, blast status
│   ├── rates/
│   │   ├── handlers.go         # Ingest endpoints (API + manual form)
│   │   ├── parser.go           # Stage 1: regex extraction (389 lines)
│   │   ├── parser_test.go
│   │   ├── llm.go              # Stage 2: LLMCorrector interface + NoopCorrector
│   │   └── standardize.go      # Stage 3: unit/type normalization
│   ├── static/
│   │   ├── static.go           # embed.FS for static assets
│   │   └── style.css           # Full app styling (~212 lines)
│   ├── templates/
│   │   ├── templates.go        # embed.FS loader + MustParse()
│   │   ├── layout.html         # Dual-mode shell (auth vs. app nav)
│   │   └── [17 page templates] # See Template Inventory below
│   └── vendors/handlers.go     # Vendor CRUD + ports/contacts/notes/preferences
├── data/drayage.db             # SQLite DB (local dev only; gitignored)
├── Dockerfile                  # Multi-stage: Go build → Litestream → Debian slim
├── docker-compose.yml          # Local dev: MinIO (S3 simulation) + app
├── entrypoint.sh               # Litestream restore-then-replicate wrapper
├── fly.toml                    # Fly.io config (app: drayage-quoter, region: ewr)
├── litestream.yml              # Dev backup → MinIO
├── litestream.prod.yml         # Prod backup → S3 (env var secrets)
└── go.mod                      # Key deps: modernc.org/sqlite (pure Go, no CGO)
```

---

## Route Map

### Auth (unauthenticated)
| Method | Path | Handler |
|--------|------|---------|
| GET | `/login` | auth.HandleLoginPage |
| POST | `/login` | auth.HandleLoginSubmit |
| GET | `/auth/verify` | auth.HandleVerify |
| POST | `/logout` | auth.HandleLogout |

### Admin (RequireAdmin)
| Method | Path | Handler |
|--------|------|---------|
| GET | `/admin/users` | admin.HandleUsers |
| POST | `/admin/users` | admin.HandleAddUser |
| POST | `/admin/users/{id}/delete` | admin.HandleDeleteUser |
| GET | `/admin/ports` | admin.HandlePorts |
| POST | `/admin/ports` | admin.HandleAddPort |
| POST | `/admin/ports/{id}/delete` | admin.HandleDeletePort |

### Dashboard & Lanes (RequireAuth)
| Method | Path | Handler |
|--------|------|---------|
| GET | `/{$}` | lanes.HandleDashboard |
| GET | `/customers/search` | lanes.HandleCustomerSearch (HTMX fragment) |
| GET | `/lanes/new` | lanes.HandleNewForm |
| POST | `/lanes` | lanes.HandleCreate |
| GET | `/lanes/{id}` | lanes.HandleDetail |
| GET | `/lanes/{id}/edit` | lanes.HandleEditForm (owner only) |
| POST | `/lanes/{id}` | lanes.HandleUpdate (owner only) |
| POST | `/lanes/{id}/status` | lanes.HandleAdvanceStatus (owner only) |

### Rate Requests (RequireAuth)
| Method | Path | Handler |
|--------|------|---------|
| GET | `/lanes/{id}/rate-request/new` | rate_requests.HandleNewForm |
| POST | `/lanes/{id}/rate-request` | rate_requests.HandleCreate |
| GET | `/rate-requests/{id}` | rate_requests.HandleDetail |
| GET | `/rate-requests/{id}/comparison` | rate_requests.HandleComparison (comparison table + lineup builder) |
| POST | `/rate-requests/{id}/lineup` | rate_requests.HandleSaveLineup (save ranked lineup) |

### Rate Ingestion + Inline Editing (RequireAuth)
| Method | Path | Handler |
|--------|------|---------|
| GET | `/rate-requests/{id}/ingest` | rates.HandleIngestForm |
| POST | `/rate-requests/{id}/ingest` | rates.HandleIngestSubmit (parse preview) |
| POST | `/rate-requests/{id}/ingest/confirm` | rates.HandleIngestConfirm (save) |
| POST | `/api/rates/parse` | rates.HandleAPIIngest (automated JSON API) |
| GET | `/vendor-rate-items/{id}` | rates.HandleViewRateItem (HTMX cell fragment) |
| GET | `/vendor-rate-items/{id}/edit` | rates.HandleEditRateItem (HTMX edit form fragment) |
| POST | `/vendor-rate-items/{id}` | rates.HandleUpdateRateItem (save + return cell fragment) |
| GET | `/vendor-rates/{id}/email` | rates.HandleViewOriginalEmail (HTMX email panel fragment) |

### Vendors (RequireAuth)
| Method | Path | Handler |
|--------|------|---------|
| GET | `/vendors` | vendors.HandleList |
| GET | `/vendors/search` | vendors.HandleSearch (HTMX fragment) |
| GET | `/vendors/new` | vendors.HandleNewForm |
| POST | `/vendors` | vendors.HandleCreate |
| GET | `/vendors/{id}` | vendors.HandleDetail |
| GET | `/vendors/{id}/edit` | vendors.HandleEditForm |
| POST | `/vendors/{id}` | vendors.HandleUpdate |
| POST | `/vendors/{id}/delete` | vendors.HandleDelete |
| POST | `/vendors/{id}/ports` | vendors.HandleAddPort |
| POST | `/vendors/{id}/ports/{vpid}/delete` | vendors.HandleRemovePort |
| POST | `/vendors/{id}/ports/{vpid}/notes` | vendors.HandleAddNote |
| POST | `/vendors/{id}/ports/{vpid}/contacts` | vendors.HandleAddContact |
| POST | `/vendors/{id}/ports/{vpid}/contacts/{cid}/delete` | vendors.HandleDeleteContact |
| POST | `/vendors/{id}/preferences/{pid}` | vendors.HandleTogglePreference |

---

## Database Schema (18 Migrations)

### users (001)
```sql
id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL UNIQUE,
is_admin INTEGER NOT NULL DEFAULT 0,  -- added in 004
created_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### auth_tokens (002)
```sql
id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id),
token TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL,
used_at DATETIME,  -- NULL until consumed (15-min expiry)
created_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### sessions (003)
```sql
id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id),
token TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL,  -- 30-day expiry
created_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### customers (005)
```sql
id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE,
created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
```
Auto-created on lane save (case-insensitive LOWER() match, INSERT OR IGNORE).

### ports (006) + seed (007)
```sql
id INTEGER PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL  -- "Seaport" or "Rail Ramp"
```
Seeded: LA, Long Beach, NY/NJ, Savannah, Seattle/Tacoma, Houston + Chicago, Dallas, Memphis, KC.

### lanes (008)
```sql
id INTEGER PRIMARY KEY,
owner_id INTEGER NOT NULL REFERENCES users(id),
customer_id INTEGER NOT NULL REFERENCES customers(id),
origin_port_id INTEGER NOT NULL REFERENCES ports(id),
destination TEXT NOT NULL,
container_size TEXT NOT NULL,  -- "20_40"|"20"|"40"|"40HC"|"Flat Rack"
weight INTEGER,  -- nullable
direction TEXT NOT NULL DEFAULT 'import',  -- "import"|"export"
load_type TEXT NOT NULL DEFAULT 'live',    -- "live"|"drop_pick"
commodity TEXT, hazmat INTEGER NOT NULL DEFAULT 0,
overweight INTEGER NOT NULL DEFAULT 0,  -- auto-computed (20'>38k, others>45k lbs)
out_of_gauge INTEGER NOT NULL DEFAULT 0, notes TEXT,
status TEXT NOT NULL DEFAULT 'draft',   -- state machine; see MEMORY.md
created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### vendors (009)
```sql
id INTEGER PRIMARY KEY, name TEXT NOT NULL,
created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### vendor_ports (010)
```sql
id INTEGER PRIMARY KEY,
vendor_id INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
port_id INTEGER NOT NULL REFERENCES ports(id) ON DELETE CASCADE,
contact_id INTEGER,  -- soft ref to vendor_contacts.id (no FK — avoids circular dep)
UNIQUE(vendor_id, port_id)
```

### vendor_contacts (011)
```sql
id INTEGER PRIMARY KEY,
vendor_ports_id INTEGER NOT NULL REFERENCES vendor_ports(id) ON DELETE CASCADE,
name TEXT, email TEXT NOT NULL  -- name optional
```

### vendor_notes (012)
```sql
id INTEGER PRIMARY KEY,
vendor_ports_id INTEGER NOT NULL REFERENCES vendor_ports(id) ON DELETE CASCADE,
author_id INTEGER NOT NULL REFERENCES users(id),
content TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### vendor_preferences (013)
```sql
user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
vendor_id INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
port_id INTEGER NOT NULL REFERENCES ports(id) ON DELETE CASCADE,
PRIMARY KEY (user_id, vendor_id, port_id)
```

### rate_requests (014)
```sql
id INTEGER PRIMARY KEY,
lane_id INTEGER NOT NULL REFERENCES lanes(id) ON DELETE CASCADE,
reference_id TEXT NOT NULL UNIQUE,  -- "RR-2026-00001", year-scoped sequential
subject TEXT NOT NULL, body TEXT NOT NULL,  -- [First Name] placeholder in body
response_threshold INTEGER,  -- nullable; notify after N responses
deadline DATETIME,           -- nullable; notify after deadline regardless
responses_received INTEGER NOT NULL DEFAULT 0,
notified INTEGER NOT NULL DEFAULT 0,
created_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### rate_request_vendors (015)
```sql
rate_request_id INTEGER NOT NULL REFERENCES rate_requests(id) ON DELETE CASCADE,
vendor_id INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
responded INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY (rate_request_id, vendor_id)
```

### vendor_rates (016)
```sql
id INTEGER PRIMARY KEY,
rate_request_id INTEGER NOT NULL REFERENCES rate_requests(id) ON DELETE CASCADE,
vendor_id INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
original_email TEXT NOT NULL,  -- always persisted for reference
parsed_by TEXT NOT NULL DEFAULT 'regex',  -- "regex"|"llm"|"manual"
created_at DATETIME DEFAULT CURRENT_TIMESTAMP
```

### vendor_rate_items (017)
```sql
id INTEGER PRIMARY KEY,
vendor_rate_id INTEGER NOT NULL REFERENCES vendor_rates(id) ON DELETE CASCADE,
charge_type TEXT NOT NULL,  -- 24 types (see parser.go chargeTypeMeta)
amount REAL NOT NULL, unit TEXT NOT NULL,
manually_edited INTEGER NOT NULL DEFAULT 0  -- 1 if user edited during ingest confirm
```

### orphan_emails (018)
```sql
id INTEGER PRIMARY KEY, raw_email TEXT NOT NULL,
subject TEXT NOT NULL DEFAULT '', sender TEXT NOT NULL DEFAULT '',
received_at DATETIME DEFAULT CURRENT_TIMESTAMP,
assigned_to_rate_request_id INTEGER REFERENCES rate_requests(id) ON DELETE SET NULL
```

### vendor_lineups (019)
```sql
id INTEGER PRIMARY KEY,
lane_id INTEGER NOT NULL REFERENCES lanes(id) ON DELETE CASCADE,
vendor_rate_id INTEGER NOT NULL REFERENCES vendor_rates(id) ON DELETE CASCADE,
rank INTEGER NOT NULL,
UNIQUE(lane_id, rank)
```

---

## Package Summaries

| Package | File | Purpose |
|---------|------|---------|
| `auth` | auth.go, handlers.go, middleware.go | Magic link flow, session management, RequireAuth/RequireAdmin middleware |
| `admin` | handlers.go | User + port CRUD (admin-only) |
| `lanes` | handlers.go | Lane CRUD, dashboard with filters, status state machine |
| `rate_requests` | handlers.go | Rate request create/detail, reference ID gen, email template, blast status |
| `rates` | handlers.go, parser.go, llm.go, standardize.go | 3-stage parse pipeline (regex→LLM→normalize), ingest API, manual ingest form |
| `vendors` | handlers.go | Vendor CRUD with port/contact/note/preference hierarchy |
| `admin` | handlers.go | Admin-only user + port management |
| `db` | db.go + migrations/ | SQLite init, WAL mode, FK enforcement, migration runner |
| `templates` | templates.go + *.html | Embedded template FS, MustParse loader |
| `static` | static.go + style.css | Embedded CSS |

---

## Template Inventory

| Template | Data Binding Key Fields |
|----------|------------------------|
| `layout.html` | `.User` (nil → auth layout) |
| `login.html` | (none) |
| `login_sent.html` | `.Email` |
| `dashboard.html` | `.User`, `.Lanes` ([]LaneRow), `.Query`, `.Status`, `.PortID`, `.OwnerID`, `.Ports`, `.AllUsers` |
| `lane_new.html` | `.User`, `.Ports` |
| `lane_detail.html` | `.User`, `.Lane` (LaneDetail), `.RateRequest` (RateRequestSnippet, nullable) |
| `lane_edit.html` | `.User`, `.Lane`, `.Ports` |
| `rate_request_new.html` | `.User`, `.Data` (has `.Lane`, `.Vendors` []VendorSelectRow) |
| `rate_request_detail.html` | `.User`, `.RateRequest` (RateRequestDetail with `.Lane`, `.Vendors` []VendorBlastRow) |
| `rate_ingest.html` | `.User`, `.Data` (IngestFormData: RateRequest, Vendors, PreselectedVendorID) |
| `rate_ingest_result.html` | `.User`, `.Data` (IngestResultData: Items []RateItem, RateRequestID, VendorID) |
| `rate_comparison.html` | `.User`, `.Data` (ComparisonData: RateRequest, Lane, ChargeRows, Vendors, Averages, AvgTotal, Lineups) |
| `vendor_list.html` | `.User`, `.Vendors` ([]VendorRow), `.Query`, `.PortID`, `.Ports` |
| `vendor_new.html` | `.User`, `.Ports` |
| `vendor_detail.html` | `.User`, `.Vendor` (VendorDetail with AssignedPorts, AvailablePorts) |
| `vendor_edit.html` | `.User`, `.Vendor` |
| `admin_users.html` | `.User`, `.Users` ([]UserRow) |
| `admin_ports.html` | `.User`, `.Ports` ([]PortRow) |

---

## Infrastructure

### Deployment Stack
- **Docker**: Multi-stage build (Go build → Litestream binary → Debian slim). `CGO_ENABLED=0`.
- **Fly.io**: `drayage-quoter` app, `ewr` region, 1GB shared CPU. `force_https = true`. Persistent `/data` volume.
- **Litestream**: Continuous SQLite replication to S3. Restore on cold start via `entrypoint.sh`.

### Environment Variables
| Var | Default | Notes |
|-----|---------|-------|
| `PORT` | `8080` | HTTP listen port |
| `DB_PATH` | `./data/drayage.db` | `/data/drayage.db` on Fly.io |
| `BASE_URL` | `http://localhost:{PORT}` | Used in magic link emails |
| `ADMIN_EMAIL` | (required) | Grants is_admin=1 on startup |
| `BUCKET_NAME`, `AWS_REGION`, `AWS_ENDPOINT_URL_S3`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | — | Litestream S3 (Fly.io secrets) |

### Charge Types (24 implemented in parser.go)
`linehaul`, `fuel`, `chassis`, `chassis_min`, `detention`, `detention_free`, `storage`, `yard_pull`, `chassis_split`, `mount`, `lift`, `redelivery`, `dry_run`, `toll`, `triaxle`, `extreme_overweight`, `regular_overweight`, `reefer`, `genset`, `hazmat`, `stop_off`, `layover`, `drop`, `scale`, `congestion`, `congestion_free`, `gate`

(SPEC lists 27 total; parser.go `fieldOrder` determines extraction precedence — `extreme_overweight` before `regular_overweight` to prevent misclassification.)
