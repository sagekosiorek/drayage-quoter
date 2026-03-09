# Drayage Quoter - Technical Specification

## Overview

A web application that streamlines the end-to-end drayage quoting process for Schneider's sales reps. The system replaces manual Excel-based workflows with a structured tool for managing vendor networks, requesting rates, parsing vendor responses, comparing rates, building carrier lineups, applying markups, and generating customer-facing quotes.

**Target users**: 6-15 drayage sales reps
**Primary goal**: 30-50% reduction in quoting time per lane by eliminating rote tasks

---

## Architecture

### Stack
- **Backend**: Go (net/http + html/template)
- **Frontend**: HTMX with server-side rendered HTML (desktop-only, no mobile optimization)
- **Database**: SQLite
- **Backup**: Litestream continuous replication to S3
- **Deployment**: Docker on Fly.io
- **Auth**: Magic link / email-based (passwordless)
- **Notifications**: In-app + email from app's own address
- **Email provider (org)**: Microsoft 365 (Graph API integration deferred)

### UI Approach
Hybrid layout:
- **Dashboard/list views**: Data-dense, filterable flat lists (opportunity list, vendor list)
- **Quoting flow**: Wizard-style step-by-step progression (rate request -> comparison -> lineup -> markup -> export)

### Key Architectural Decisions
- **No automated email send/receive in v1**. Reps manually send rate requests via mailto: links and forward vendor responses to the app. This sidesteps the M365 admin consent blocker and lets us demonstrate value through the parsing/analysis/markup flow first.
- **LLM-assisted parsing is model-agnostic**. The parsing layer accepts raw email content and outputs standardized rate buckets. The LLM provider/model is a configuration choice, not baked into the architecture.
- **Independent quotes, no rate history trending**. Each rate request is standalone. Data model does not need to support historical rate analytics.
- **No MasterMind CRM integration**. The LP# field on customers is the only bridge. Full CRM integration is a separate future project.
- **Reporting/analytics deferred**. Data model supports future queries, but no dashboards or aggregate metrics in this build.

---

## Data Model

### Users
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| name | TEXT | Display name |
| email | TEXT | Unique; used for magic link auth |
| created_at | DATETIME | |

### Customers
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| name | TEXT | Customer/prospect name |
| created_at | DATETIME | |
| updated_at | DATETIME | |

### Vendors
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| name | TEXT | Company name |
| created_at | DATETIME | |
| updated_at | DATETIME | |

### Vendor Ports
| Field | Type | Notes |
|-------|------|-------|
| id | INTEGER | Primary key (autoincrement) |
| vendor_id | INTEGER (FK) | References vendors.id |
| port_id | INTEGER (FK) | References ports.id |
| contact_id | INTEGER | Soft ref to vendor_contacts.id (primary contact for this port); no DB-enforced FK to avoid circular reference |
| | | UNIQUE(vendor_id, port_id) |

### Vendor Contacts
| Field | Type | Notes |
|-------|------|-------|
| id | INTEGER | Primary key (autoincrement) |
| vendor_ports_id | INTEGER (FK) | References vendor_ports.id; contacts are port-specific |
| name | TEXT | Contact name, optional |
| email | TEXT | Contact email (required) |

### Ports
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| name | TEXT | CHECK(name IN (... list ports here) |
| type | TEXT | "port" or "railhead" |

### Vendor Notes
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| vendor_id | TEXT (FK) | References vendors.id |
| author_id | TEXT (FK) | References users.id |
| content | TEXT | Note content |
| created_at | DATETIME | |

All notes are shared/visible to the entire team.

### Vendor Preferences
| Field | Type | Notes |
|-------|------|-------|
| user_id | TEXT (FK) | References users.id |
| vendor_id | TEXT (FK) | References vendors.id |
| port_id | TEXT (FK) | References ports.id; preference is port-specific |

A rep's preferred vendor list is scoped per port/service area.

### Lanes
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| owner_id | TEXT (FK) | References users.id; the rep who owns this lane |
| customer_id | TEXT (FK) | References customers.id |
| origin_port_id | TEXT (FK) | References ports.id |
| destination | TEXT | Destination city/zip |
| container_size | TEXT | "20", "40", "40HC", "Flat Rack" |
| commodity | TEXT | Commodity description |
| weight | INTEGER | Weight in lbs |
| direction | TEXT NOT NULL DEFAULT 'import' | "import" or "export" |
| load_type | TEXT NOT NULL DEFAULT 'live' | "live" or "drop" |
| hazmat | BOOLEAN | Default false |
| overweight | BOOLEAN | Default false |
| out_of_gauge | BOOLEAN | Default false |
| notes | TEXT | Free-form lane notes |
| status | TEXT | "draft", "rates_requested", "rates_received", "quoting", "quoted" |
| created_at | DATETIME | |
| updated_at | DATETIME | |

Lane fields beyond these (e.g., hazmat class, specific chassis requirements) will be finalized during implementation as the full field set is confirmed with the client.

Ownership model: one rep owns each lane, all reps can view all lanes.

### Rate Requests
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| lane_id | TEXT (FK) | References lanes.id |
| reference_id | TEXT | Unique ID embedded in email subject for matching (e.g., "RR-2024-00142") |
| subject | TEXT | Email subject line |
| body | TEXT | Email body template (rep may have edited before sending) |
| response_threshold | INTEGER | Number of vendor responses before notification |
| deadline | DATETIME | Time-based fallback notification deadline |
| responses_received | INTEGER | Running count |
| notified | BOOLEAN | Whether threshold/deadline notification was sent |
| created_at | DATETIME | |

### Rate Request Vendors
| Field | Type | Notes |
|-------|------|-------|
| rate_request_id | TEXT (FK) | References rate_requests.id |
| vendor_id | TEXT (FK) | References vendors.id |
| responded | BOOLEAN | Whether a response has been received |

### Vendor Rates
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| rate_request_id | TEXT (FK) | References rate_requests.id |
| vendor_id | TEXT (FK) | References vendors.id |
| original_email | TEXT | Full original email content for reference |
| parsed_by | TEXT | "regex", "llm", "manual" |
| created_at | DATETIME | |

### Vendor Rate Items
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| vendor_rate_id | TEXT (FK) | References vendor_rates.id |
| charge_type | TEXT | One of the 27 standardized buckets (see below) |
| amount | DECIMAL(10,2) | Rate value |
| unit | TEXT | "$", "%", "$/day", "$/hour", "days", "hours" |
| manually_edited | BOOLEAN | Whether this specific field was human-corrected |

#### Standardized Rate Buckets

| Charge Type | Unit | Notes |
|------------|------|-------|
| linehaul | $ | Base line haul rate |
| fuel | % | Fuel surcharge as percentage of linehaul |
| chassis | $/day | Chassis usage fee |
| chassis_min | days | Minimum chassis rental days |
| detention | $/hour | Detention fee |
| detention_free | hours | Free detention hours |
| storage | $/day | Storage fee |
| yard_pull | $ | Yard pull / pre-pull charge |
| chassis_split | $ | Chassis split fee |
| toll | $ | Toll charges |
| regular_overweight | $ | Standard overweight surcharge |
| extreme_overweight | $ | Extreme overweight surcharge |
| triaxle | $/day | Tri-axle chassis fee |
| reefer | $ | Reefer surcharge |
| genset | $/day | Generator set rental |
| hazmat | $ | Hazardous materials surcharge |
| mount | $ | Mount/dismount fee |
| lift | $ | Lift charge |
| gate | $ | Gate fee |
| redelivery | $ | Redelivery charge |
| dry_run | $ | Dry run / wasted trip fee |
| stop_off | $ | Stop-off charge |
| scale | $ | Scale/weigh fee |
| congestion | $/hour | Congestion surcharge |
| congestion_free | hours | Free congestion hours |
| other | $ | Catch-all for unlisted charges |

**Computed**: `total = linehaul + (linehaul * fuel / 100)`

### Orphan Emails
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| raw_email | TEXT | Full email content |
| subject | TEXT | Extracted subject line |
| sender | TEXT | Sender email address |
| received_at | DATETIME | |
| assigned_to_rate_request_id | TEXT (FK) | Nullable; set when manually matched |

Emails forwarded to the app that can't be matched to a rate request via reference ID land here for manual triage.

### Vendor Lineups
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| lane_id | TEXT (FK) | References lanes.id |
| vendor_rate_id | TEXT (FK) | References vendor_rates.id |
| rank | INTEGER | 1 = primary, 2 = secondary, etc. |

Lineup is internal reference only; not exposed on customer-facing quotes.

### Quotes
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| owner_id | TEXT (FK) | References users.id |
| customer_id | TEXT (FK) | References customers.id |
| created_at | DATETIME | |

A quote can bundle multiple lanes for a single customer.

### Quote Lanes
| Field | Type | Notes |
|-------|------|-------|
| quote_id | TEXT (FK) | References quotes.id |
| lane_id | TEXT (FK) | References lanes.id |

### Markups
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| quote_id | TEXT (FK) | References quotes.id |
| lane_id | TEXT (FK) | References lanes.id |

### Markup Items
| Field | Type | Notes |
|-------|------|-------|
| id | TEXT (UUID) | Primary key |
| markup_id | TEXT (FK) | References markups.id |
| charge_type | TEXT | specific bucket for per-item |
| value | DECIMAL(10,2) | The markup value |

---

## Rate Parsing Pipeline

### Architecture
```
Email Content (raw text/HTML)
  |
  v
[Stage 1: Regex/Template Parser]  -- Port of existing tool
  |                                   Rule-based extraction
  |                                   ~60% accuracy baseline
  v
[Stage 2: LLM Correction]         -- Model-agnostic interface
  |                                   Receives: raw email + Stage 1 output
  |                                   Returns: corrected standardized buckets
  |                                   Handles: wrong charge types, missed items, format confusion
  v
[Stage 3: Standardization]        -- Map to 27 fixed buckets
  |                                   Validate units and types
  |                                   Flag low-confidence extractions
  v
[Stage 4: Human Review]           -- In-app UI
                                      Side-by-side: parsed rates vs. original email
                                      Inline editing of any value
                                      Manual entry for anything missed
```

### Parser Requirements
- Accept HTML email bodies (70% of inputs), plain text (20%), and document attachments (10%)
- The LLM interface is abstracted: accepts `(rawEmail string, regexOutput []RateItem) -> []RateItem`
- Model choice (Claude Haiku, Sonnet, or other) is a runtime configuration
- All parsed rates are tagged with their source: "regex", "llm", or "manual"
- Original email is always persisted for reference

---

## Email Integration

### Phase 1 (This Build): Manual Flow

**Outbound (rate requests):**
1. Rep enters lane details, which pre-populate a rate request template
2. Template defaults: port/rail name, container sizes (20'/40'), legal weight, non-hazmat
3. Rep edits the subject line and body as needed, working around placeholders like vendor contact name and lane fields (origin port, container size, weight, etc.)
4. One-click copy for subject and body (separate clipboard actions)
5. Vendor list displays filtered by port + personal preferences
6. Each vendor contact email renders as a `mailto:` link and populates name in email body
7. Rep clicks each link -> relevant data populates email body in app + Outlook draft opens -> copy subject + body -> pastes -> sends
8. All vendors receive templated email with populated placeholders - not unlike a mail merge - for a given rate request

**Inbound (rate responses):**
1. Email subject line contains a reference ID (e.g. RR-2024-00142)
2. Rep forwards vendor response to app's dedicated inbox
3. App matches incoming email to rate request via reference ID
4. Unmatched emails go to orphan pool for manual assignment
5. Parsing pipeline extracts rates into standardized buckets
6. Rep is notified when: response count hits threshold OR deadline passes (whichever first)

**Inbound infrastructure**: Design the parsing API endpoint (`POST /api/rates/parse`). Defer the email ingestion mechanism (Mailgun, SMTP receiver, etc.) to a later decision point.

### Phase 2 (Future): M365 Graph API Integration
- Automated sending from rep's own email address
- Automated inbox monitoring for rate responses
- Requires Azure AD admin consent (blocked for now; demonstrate value with Phase 1 first)

---

## User Flows

### Authentication
1. Rep enters email address on login page
2. App sends a magic link to that email
3. Rep clicks link -> authenticated session created
4. Session persists via secure cookie

### Flow 1: Create a Lane
1. Rep clicks "New Lane" from dashboard
2. Fills in: customer (select or create) + LP#, origin port/rail head, destination, container size (defaulted to 20/40), commodity (optional), weight (optional), direction (import default), load type, hazmat/overweight/OOG flags (all optional)
3. Option to continue to Flow 3 or stop here, merely logging it in their opportunity list with status "draft"

### Flow 2: Manage Vendor Network
1. Any rep can add a new vendor: name, contacts (name, email), ports serviced
2. Any rep can add shared notes to any vendor
3. Each rep can mark vendors as "preferred" per port
4. Vendor list is searchable/filterable by port, name, preference status

### Flow 3: Create and Send Rate Request
1. If not coming from Flow 1, rep opens a lane -> clicks "Request Rates"
2. App shows vendors filtered by the lane's origin port, with rep's preferred vendors selected. There are button options to: toggle "all" selection and toggle "preferred" selection
3. Rep has option to deselect vendors, ignoring them for this blast (checkboxes). Notably, this does not modify their underlying preferred vendor list.
4. App generates rate request template:
   - Subject: `Rate Request: [Port Name] - [Destination] - [Direction] - [REF_ID]`
   - Body: Pre-populated with all lane details (port, container size, weight, direction, hazmat, etc.).
   - *No customer information is included in any of this.
5. Rep can edit subject and body
6. Rep sets response threshold (e.g., "notify me after 3 responses")
7. Rep optionally sets a deadline (e.g., "notify me after X hours regardless")
8. Vendor contact emails shown as `mailto:` links
9. Rep clicks each (automatically populating vendor-related placeholders into the email - e.g. first name of vendor_contacts.name), copy/pastes content, sends from Outlook.
10. Lane status updates to "rates_requested" after the first vendor contact email is clicked.

### Flow 4: Rate Comparison
1. Forwarded vendor rate emails are parsed and matched to rate requests
2. Lane status updates to "rates_received" after one or more vendor responses is matched to the request. This causes the 'Build Lineup' button on the lane_detail.html page to show which takes them to the rate comparison page.
3. When threshold or deadline is met, rep receives notification via email
4. Rep opens the rate comparison page -> sees comparison table:
   - Rows: standardized rate buckets
   - Columns: vendors, sorted by cheapest total (linehaul + fuel)
   - Cells: parsed amounts with visual indicator if LLM-corrected
5. "View Original" button per vendor opens the raw email side-by-side
6. Any cell is inline-editable; edits are flagged as "manually_edited"
7. Rep can also manually enter rates for a vendor (e.g., if they got rates verbally)

### Flow 5: Build Vendor Lineup
1. From the comparison view, rep selects vendors in ranked order
2. Primary (rank 1) is the carrier they intend to quote to the customer
3. Secondary, tertiary, etc. are internal backups
4. Lineup is saved and can be changed at any time
5. Lane status updates to "quoting"

### Flow 6: Markup and Quote Generation
1. Rep opens the markup interface for a lane
2. Each charge type can be optionally marked up. All types are simply flat number entires except for `linehaul + fuel` which can be toggled to either mark up via a flat number entry or percentage off the base rate (which is also toggleable: starts with average, but can be switched to first, second, third etc. in lineup).
3. "Generate CSV" produces a file with full accessorial breakdown for the lane
4. Rep downloads CSV, sends to customer manually
5. Lane status updates to "quoted"
6. Lane and all associated data persist in history for future reference

---

## Notifications

### Email (from app's own address)
- Sent when a rate request hits its response threshold
- Sent when a rate request's deadline expires (if threshold not yet met)
- Sent from the app's configured outbound address (not the rep's address)

---

## CSV Quote Format

The customer-facing CSV includes a full accessorial breakdown, but only includes items that have a rate populated (i.e. no empty fields should show up on the CSV). Format is defined by the app (no existing template to match).

**Structure per lane:**
| Column | Content |
|--------|---------|
| Customer | Customer name |
| Origin | Port/rail head name |
| Destination | City/Zip |
| Direction | Import/Export |
| Linehaul | Marked-up amount |
| Fuel Surcharge | Marked-up amount |
| Total (LH + Fuel) | Computed |
| Chassis ($/day) | Marked-up amount |
| Chassis Min (days) | Days |
| Detention ($/hr) | Marked-up amount |
| Detention Free (hrs) | Hours |
| ... | All 27 charge types |

Multi-lane quotes produce multiple rows, distinctly organized by a coloful table, in the same CSV, one per lane.

---

## Security

- **Auth**: Magic link / email-based, session cookie
- **Access**: All reps see all lanes and vendors; lane editing restricted to owner
- **Data**: SQLite on Fly.io persistent volume, Litestream replication to S3
- **Secrets**: Email service credentials, LLM API keys stored as Fly.io secrets (env vars)
- **No PII concerns**: Vendor contacts and customer names are business data, not consumer PII

---

## Milestones

### Milestone 1: Project Setup + Lane Management (Sprint 1)
Foundation: auth, data model, and core lane CRUD with opportunity list.

- [x] Initialize Go project with module structure, Dockerfile, and Fly.io config
- [x] Set up SQLite database with migrations framework
- [x] Configure Litestream for S3 replication
- [x] Implement magic link authentication (send link, verify token, session cookie)
- [x] Build base HTML layout and HTMX infrastructure (templates, partials, static assets)
- [x] Before going any further, create a .css file and clean up existing templates.
- [x] Create users table and seed initial rep accounts
- [x] Create customers table with CRUD (name + optional LP#)
- [x] Create ports table and seed with known ports/rail heads (make this an admin function)
- [x] Create lanes table with full field set
- [x] Build "New Lane" form (wizard-style, fields pre-validated)
- [x] Build lane detail view (read-only for non-owners, editable for owner)
- [x] Fill in opportunity list dashboard (flat list, filterable by customer, port, status, owner); click lane to open detailed view
- [x] Implement lane status state machine (draft -> rates_requested -> rates_received -> quoting -> quoted)
- [x] Deploy on Docker and Fly.io and gather feedback

### Milestone 2: Vendor Network (Sprint 1)
Shared vendor database with personal preference lists.

- [x] Create vendors table with CRUD
- [x] Create vendor_contacts table (port-specific contacts via vendor_ports_id)
- [x] Create vendor_ports table (id PK, vendor_id, port_id, contact_id soft ref, UNIQUE constraint)
- [x] Create vendor_notes table (author-attributed, all shared)
- [x] Create vendor_preferences table (per-user, per-port)
- [x] Build vendor list view (filterable by port, name, searchable)
- [x] Build vendor detail view (contacts grouped by port, notes, add/edit forms)
- [x] Build "My Preferred Vendors" management UI (toggle preferences per port)
- [x] Vendor creation form (autocomplete existing vendors, port select, n contacts)
- [x] Vendor notes should be vendor_port specific; not vendor specific.

### Milestone 3: Rate Request Generation (Sprint 1)
Template-based rate request creation with manual email workflow.

- [x] Create rate_requests table with reference ID generation (e.g., RR-YYYY-NNNNN)
- [x] Create rate_request_vendors junction table
- [x] Build vendor selection UI. Kicked off from either the exiting 'Request Rates ->' button on the lane_detail.html page, or a button on the lane_new.html page called 'Save & Request Rates'.
- [x] Build rate request template engine: subject line and body generation from lane data
- [x] Pre-populate template defaults (port name, container size, legal weight, non-haz, etc.)
- [x] Editable subject and body fields with live preview
- [x] "Copy Subject" and "Copy Body" one-click clipboard buttons
- [x] Render selected vendor contacts as `mailto:` links which also update the email body with vendor contact name (i.e. "Hi Muphy,")
- [x] Response threshold input (number of vendors)
- [x] Deadline input (datetime picker)
- [x] Save rate request and update lane status to "rates_requested"
- [x] Rate request detail view showing blast status (which vendors, responded/pending)
- [x] Deploy and gather feedback

### Milestone 4: Rate Parsing + Ingestion (Sprint 2)
Parse forwarded vendor rate emails into standardized buckets.

- [x] Design and implement `POST /api/rates/parse` endpoint (accepts raw email content: subject, body, sender)
- [x] Port existing regex/template parser to Go as Stage 1
- [x] Implement LLM correction layer as Stage 2 (model-agnostic interface: input = raw email + regex output, output = corrected rate items)
- [x] Build standardization layer: map extracted rates to 27 fixed charge types with unit validation
- [x] Implement reference ID matching: extract reference ID from subject line, match to rate_request
- [x] Create orphan_emails table and pool UI for unmatched emails (list + manual assignment to rate request)
- [x] Create vendor_rates and vendor_rate_items tables
- [x] Track parse source per rate item ("regex", "llm", "manual")
- [x] Persist original email content on vendor_rate record
- [x] Update response count on rate_request; check threshold/deadline
- [x] Build manual rate entry form (for verbal quotes or unparseable emails)
- [x] Temporary UI for manual email paste/upload until email ingestion infra is built
- [x] Add a 'deselect all' button for the Blast Status table.
- [x] Remove Phone from Vendor contact - just name and email is fine
- [x] Remove LP # (not required and gets in the way)
- [x] Deploy and gather feedback

### Milestone 5: Rate Comparison + Vendor Lineup (Sprint 2)
Side-by-side comparison table and carrier ranking.

- [x] Build comparison table view: rows = charge types, columns = vendors sorted by total (LH + fuel)
- [x] Computed total column per vendor (linehaul + linehaul * fuel%) and computed average column per charge type.
- [x] Visual indicator on LLM-parsed vs. manually-edited cells
- [x] "View Original Email" button per vendor: renders raw email in a side panel
- [x] Inline editing on any rate cell (click-to-edit, saves as "manually_edited"). Remove increment arrows from cells - input text only.
- [x] Create vendor_lineups table
- [x] Build lineup selection UI within the same comparison table view: click vendors in rank order (primary, secondary, n-ary). Simple select ordering with real-time visual representation.
- [x] Lineup reorderable and editable after creation. Rates are also editable after creation.
- [x] Test end-to-end UX to smooth out.
- [x] Implement notification triggers: threshold met OR deadline passed
- [x] Send in-app notification (status update on lane, via HTMX SSE)
- [x] Send email notification from app's outbound address
- [x] Test email forwarding inbound flow

### Milestone 6: Markup + Quote Generation (Sprint 2)
Apply markups, preview, and export customer-facing CSV.

- [x] Create quotes table (can reference multiple lanes via quote_lanes)
- [x] Create quote_lanes junction table
- [x] Create markups and markup_items tables
- [x] rate_comparison.html modification
- [x] CSV download endpoint
- [x] Update lane status to "quoted" on export
- [x] Update / modify UX/UI
- [x] Smooth out the page navigation flow for quoting: currently very clunky and unintuitive.
- [x] Save a default email draft
- [x] On rate_request_detail.html, remove Copy Body function. Change Open Draft to Open Email Draft.
- [x] On rate_request_detail.html, add Build Lineup button at the top, that appears once 1+ response s are received.
- [x] Must be able to add rates on comparison view (if blank or not showing).
    - This means clicking on an empty cell and being able to add one, and adding a charge type that is not currently showing in the rate table.
- [x] Remove email view on rates_comparison_saved.html
- [x] Add lane details as default in email panel
- [x] Rates should auto-save on comparison saved sheet
- [x] 'Open Email Draft' should be primary class
- [x] User should be able to lock the Customer Rate in percentage mode.
- [x] CSV generation: lanes sectioned by colorful tables (same color) for visual distinction, with only populated rates added
- [ ] Fix other user bugs
- [ ] Ready for testing: deploy and clear the db of test data and upload all of your contacts all at once

### Milestone 7: Loose ends (Sprint 3)
Styling, last-min changes, stress-testing.

- [x] Modify IngestEmail()
- [ ] Change auth method from login link to login code
- [ ] Granularize the opaque "Internal server error" messages across the codebase.
- [ ] Refactor main.go - modularizing the route library into a separate file.
- [ ] User can modify the default rate request email body template
- [ ] Modify all dates to be human readable
- [ ] Add UI error message handling
- [ ] Polish some of the reactivity; e.g. utilize HTMX more for small components that otherwise require full page refresh
- [ ] Map FreightPower Shipper styling
- [ ] File-parsing support for rate ingestion
- [ ] Wire up LLM for rate parsing
- [ ] mv rate_comparison_saved.html rate_comparison_markup.hmtl && rate_comparison_edit.html rate_comparison_lineup.html (and modify all internal references - route, function names, etc. to reflect)
- [ ] Wire up email forwarding for auto-ingestion
- [ ] User should not be able to edit the company name on lane_edit.html (it's unchangeable at this point).
- [ ] Stress-test the performance, pagination, and UI using a lot of dummy data (github.com/brianvoe/gofakeit/v7).
- [ ] Need some UI feedback when clicking buttons that don't redirect: 'Copy Body' @internal/templates/rate_request_detail.html
- [ ] Live filtering on the dashboard soon as a new filter selection is clicked - remove the 'Filter' button.
- [ ] Add a carrier to a rate request after-the-fact
- [ ] Wire broker channel to rate_request_detail.html page; lane status and vendor status should refresh automatically. Also, wire it up to the vendor responses field in lane_detail.html.
- [ ] Remove all inline styling; all of it should be consolidated in style.css
- [ ] Save Lineup button should only show once at least one vendor rank is entered.
- [ ] User should be able to manually enter a carrier rate for any cell.
- [ ] A button to display lane details (in the email renderer) should be included on the rate_comparision_saved.html page so user knows which accessorial will incur.
- [ ] Set default accessorial rates by user.
- [ ] Delete a lane function
- [ ] Add tooltips where necessary
