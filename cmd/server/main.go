package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/admin"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/auth"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/db"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/email"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/lanes"
	rate_requests "gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rate_requests"
	rates "gitlab.com/perenne/clients/schneider/drayage-quoter/internal/rates"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/settings"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/static"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/templates"
	"gitlab.com/perenne/clients/schneider/drayage-quoter/internal/vendors"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/drayage.db"
	}

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%s", port)
	}

	// Ensure the parent directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatalf("create db directory: %v", err)
	}

	// Set up database; run migrations and config admin
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	adminEmail := os.Getenv("ADMIN_EMAIL")
	if adminEmail == "" {
		log.Fatal("ADMIN_EMAIL environment variable must be set")
	}
	database.Exec("INSERT OR IGNORE INTO users (name, email) VALUES (?, ?)", adminEmail, adminEmail)
	database.Exec("UPDATE users SET is_admin=1 WHERE email=?", adminEmail)

	log.Println("database ready")

	// Wire email: use Mailgun in production when env vars are set; log-only in dev.
	mailgunKey := os.Getenv("MAILGUN_API_KEY")
	mailgunDomain := os.Getenv("MAILGUN_DOMAIN")
	mailgunFrom := os.Getenv("MAILGUN_FROM") // e.g. "Drayage Quoter <noreply@mail.example.com>"
	mailgunWebhookKey := os.Getenv("MAILGUN_WEBHOOK_SIGNING_KEY")

	var emailSender auth.EmailSender = auth.LogEmailSender{}
	var notifyFn func(to, subject, body string) error
	if mailgunKey != "" && mailgunDomain != "" {
		if mailgunFrom == "" {
			mailgunFrom = fmt.Sprintf("Drayage Quoter <noreply@%s>", mailgunDomain)
		}
		mg := &email.Sender{APIKey: mailgunKey, Domain: mailgunDomain, From: mailgunFrom}
		emailSender = mg
		notifyFn = mg.Send
		log.Printf("email: Mailgun enabled (domain: %s)", mailgunDomain)
	} else {
		log.Println("email: MAILGUN_API_KEY/MAILGUN_DOMAIN not set — logging only (dev mode)")
	}

	// Services
	authService := &auth.Service{
		DB:      database,
		Email:   emailSender,
		BaseURL: baseURL,
	}

	adminSvc := &admin.Service{DB: database}
	settingsSvc := &settings.Service{DB: database}
	lanesSvc := &lanes.Service{DB: database}
	vendorsSvc := &vendors.Service{DB: database}
	rrSvc := &rate_requests.Service{DB: database}
	ratesSvc := &rates.Service{
		DB:      database,
		LLM:     nil,
		Notify:  notifyFn,
		BaseURL: baseURL,
	}

	// Deadline poller: check for expired rate request deadlines every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			checkDeadlines(database, notifyFn, baseURL)
		}
	}()

	// Parse templates
	loginTmpl := templates.MustParse("layout.html", "login.html")
	loginVerifyTmpl := templates.MustParse("layout.html", "login_verify.html")
	dashboardTmpl := templates.MustParse("layout.html", "dashboard.html")
	laneNewTmpl := templates.MustParse("layout.html", "lane_new.html")
	laneDetailTmpl := templates.MustParse("layout.html", "lane_detail.html")
	laneEditTmpl := templates.MustParse("layout.html", "lane_edit.html")
	adminUsersTmpl := templates.MustParse("layout.html", "admin_users.html")
	adminPortsTmpl := templates.MustParse("layout.html", "admin_ports.html")
	vendorListTmpl := templates.MustParse("layout.html", "vendor_list.html")
	vendorNewTmpl := templates.MustParse("layout.html", "vendor_new.html")
	vendorDetailTmpl := templates.MustParse("layout.html", "vendor_detail.html")
	vendorEditTmpl := templates.MustParse("layout.html", "vendor_edit.html")
	rrNewTmpl := templates.MustParse("layout.html", "rate_request_new.html")
	rrDetailTmpl := templates.MustParse("layout.html", "rate_request_detail.html")
	rrComparisonLineupTmpl := templates.MustParse("layout.html", "rate_comparison_lineup.html", "lane_detail_panel.html")
	rrComparisonQuoteTmpl  := templates.MustParse("layout.html", "rate_comparison_quote.html", "lane_detail_panel.html")
	lanePanelTmpl         := templates.MustParse("lane_detail_panel.html")
	rateIngestTmpl := templates.MustParse("layout.html", "rate_ingest.html")
	rateResultTmpl := templates.MustParse("layout.html", "rate_ingest_result.html")
	settingsTmpl      := templates.MustParse("layout.html", "settings.html")
	settingsPrefsTmpl := templates.MustParse("layout.html", "settings_preferences.html")

	customerSuggTmpl := template.Must(template.New("suggestions").Parse(
		`{{range .}}<button type="button" class="suggestion-item" ` +
			`data-id="{{.ID}}" data-name="{{.Name}}" ` +
			`onclick="selectCustomer(this)">{{.Name}}</button>{{end}}`,
	))

	mux := http.NewServeMux()

	// Static assets
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))))

	// Auth routes (unauthenticated)
	mux.HandleFunc("GET /login", authService.HandleLoginPage(loginTmpl))
	mux.HandleFunc("POST /login", authService.HandleLoginSubmit())
	mux.HandleFunc("GET /login/verify", authService.HandleCodePage(loginVerifyTmpl))
	mux.HandleFunc("POST /login/verify", authService.HandleVerifyCode())
	mux.HandleFunc("POST /logout", authService.HandleLogout())

	// Mailgun inbound webhook (unauthenticated; HMAC-verified inside handler)
	mux.HandleFunc("POST /webhooks/email/inbound", email.HandleInbound(ratesSvc, mailgunWebhookKey))

	// Admin routes
	mux.HandleFunc("GET /admin/users", authService.RequireAdmin(adminSvc.HandleUsers(adminUsersTmpl)))
	mux.HandleFunc("POST /admin/users", authService.RequireAdmin(adminSvc.HandleAddUser()))
	mux.HandleFunc("POST /admin/users/{id}/delete", authService.RequireAdmin(adminSvc.HandleDeleteUser()))
	mux.HandleFunc("GET /admin/ports", authService.RequireAdmin(adminSvc.HandlePorts(adminPortsTmpl)))
	mux.HandleFunc("POST /admin/ports", authService.RequireAdmin(adminSvc.HandleAddPort()))
	mux.HandleFunc("POST /admin/ports/{id}/delete", authService.RequireAdmin(adminSvc.HandleDeletePort()))

	// Customer autocomplete (protected, returns HTML fragment)
	mux.HandleFunc("GET /customers/search", authService.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("customer_name")
		if len(q) < 2 {
			w.Write([]byte(""))
			return
		}
		rows, err := database.Query(
			`SELECT id, name FROM customers WHERE name LIKE ? ORDER BY name LIMIT 8`,
			"%"+q+"%",
		)
		if err != nil {
			http.Error(w, "Query customers failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type suggestion struct {
			ID   int
			Name string
		}
		var matches []suggestion
		for rows.Next() {
			var s suggestion
			if err := rows.Scan(&s.ID, &s.Name); err != nil {
				http.Error(w, "Scan customer row failed", http.StatusInternalServerError)
				return
			}
			matches = append(matches, s)
		}
		customerSuggTmpl.Execute(w, matches)
	}))

	// Settings routes (all authenticated users)
	mux.HandleFunc("GET /settings",               authService.RequireAuth(settingsSvc.HandleSettings(settingsTmpl)))
	mux.HandleFunc("POST /settings",              authService.RequireAuth(settingsSvc.HandleSaveTemplate()))
	mux.HandleFunc("GET /settings/preferences",   authService.RequireAuth(settingsSvc.HandlePreferences(settingsPrefsTmpl)))
	mux.HandleFunc("POST /settings/preferences",  authService.RequireAuth(settingsSvc.HandleSavePreferences()))

	// Dashboard
	mux.HandleFunc("GET /{$}", authService.RequireAuth(lanesSvc.HandleDashboard(dashboardTmpl)))

	// Lane routes (specific paths before wildcard)
	mux.HandleFunc("GET /lanes/new", authService.RequireAuth(lanesSvc.HandleNewForm(laneNewTmpl)))
	mux.HandleFunc("POST /lanes", authService.RequireAuth(lanesSvc.HandleCreate()))
	mux.HandleFunc("GET /lanes/{id}", authService.RequireAuth(lanesSvc.HandleDetail(laneDetailTmpl)))
	mux.HandleFunc("GET /lanes/{id}/status", authService.RequireAuth(lanesSvc.HandleStatusBadge()))
	mux.HandleFunc("GET /lanes/{id}/edit", authService.RequireAuth(lanesSvc.HandleEditForm(laneEditTmpl)))
	mux.HandleFunc("POST /lanes/{id}", authService.RequireAuth(lanesSvc.HandleUpdate()))
	mux.HandleFunc("POST /lanes/{id}/status", authService.RequireAuth(lanesSvc.HandleAdvanceStatus()))
	mux.HandleFunc("POST /lanes/{id}/delete", authService.RequireAuth(lanesSvc.HandleDelete()))

	// Rate request routes
	mux.HandleFunc("GET /lanes/{id}/rate-request/new", authService.RequireAuth(rrSvc.HandleNewForm(rrNewTmpl)))
	mux.HandleFunc("POST /lanes/{id}/rate-request", authService.RequireAuth(rrSvc.HandleCreate()))
	mux.HandleFunc("GET /rate-requests/{id}", authService.RequireAuth(rrSvc.HandleDetail(rrDetailTmpl)))
	mux.HandleFunc("GET /rate-requests/{id}/blast-status", authService.RequireAuth(rrSvc.HandleBlastStatus()))
	mux.HandleFunc("GET /rate-requests/{id}/responses-count", authService.RequireAuth(rrSvc.HandleResponsesCount()))
	mux.HandleFunc("GET /rate-requests/{id}/comparison", authService.RequireAuth(rrSvc.HandleComparison(rrComparisonLineupTmpl)))
	mux.HandleFunc("GET /rate-requests/{id}/comparison/add-row", authService.RequireAuth(rrSvc.HandleAddComparisonRow()))
	mux.HandleFunc("GET /rate-requests/{id}/lane-panel", authService.RequireAuth(rrSvc.HandleLanePanel(lanePanelTmpl)))
	mux.HandleFunc("GET /rate-requests/{id}/comparison/saved", authService.RequireAuth(rrSvc.HandleSavedComparison(rrComparisonQuoteTmpl)))
	mux.HandleFunc("POST /rate-requests/{id}/lineup", authService.RequireAuth(rrSvc.HandleSaveLineup()))
	mux.HandleFunc("POST /rate-requests/{id}/markups", authService.RequireAuth(rrSvc.HandleSaveMarkups()))
	mux.HandleFunc("POST /rate-requests/{id}/csv",     authService.RequireAuth(rrSvc.HandleGenerateCSV()))
	mux.HandleFunc("POST /rate-requests/{id}/lock",    authService.RequireAuth(rrSvc.HandleToggleLock()))

	// Rate ingestion + inline editing routes
	mux.HandleFunc("POST /api/rates/parse", authService.RequireAuth(ratesSvc.HandleAPIIngest()))
	mux.HandleFunc("GET /rate-requests/{id}/ingest", authService.RequireAuth(ratesSvc.HandleIngestForm(rateIngestTmpl)))
	mux.HandleFunc("POST /rate-requests/{id}/ingest", authService.RequireAuth(ratesSvc.HandleIngestSubmit(rateResultTmpl)))
	mux.HandleFunc("POST /rate-requests/{id}/ingest/confirm", authService.RequireAuth(ratesSvc.HandleIngestConfirm()))
	mux.HandleFunc("GET /vendor-rate-items/{id}", authService.RequireAuth(ratesSvc.HandleViewRateItem()))
	mux.HandleFunc("GET /vendor-rate-items/{id}/edit", authService.RequireAuth(ratesSvc.HandleEditRateItem()))
	mux.HandleFunc("POST /vendor-rate-items/{id}", authService.RequireAuth(ratesSvc.HandleUpdateRateItem()))
	mux.HandleFunc("GET /vendor-rates/{id}/email", authService.RequireAuth(ratesSvc.HandleViewOriginalEmail()))
	mux.HandleFunc("GET /vendor-rates/{id}/items/new", authService.RequireAuth(ratesSvc.HandleNewRateItem()))
	mux.HandleFunc("POST /vendor-rates/{id}/items", authService.RequireAuth(ratesSvc.HandleCreateRateItem()))
	mux.HandleFunc("GET /vendor-rates/{id}/items/empty", authService.RequireAuth(ratesSvc.HandleEmptyRateItem()))

	// Vendor routes
	mux.HandleFunc("GET /vendors", authService.RequireAuth(vendorsSvc.HandleList(vendorListTmpl)))
	mux.HandleFunc("GET /vendors/search", authService.RequireAuth(vendorsSvc.HandleSearch()))
	mux.HandleFunc("GET /vendors/new", authService.RequireAuth(vendorsSvc.HandleNewForm(vendorNewTmpl)))
	mux.HandleFunc("POST /vendors", authService.RequireAuth(vendorsSvc.HandleCreate()))
	mux.HandleFunc("GET /vendors/{id}", authService.RequireAuth(vendorsSvc.HandleDetail(vendorDetailTmpl)))
	mux.HandleFunc("GET /vendors/{id}/edit", authService.RequireAuth(vendorsSvc.HandleEditForm(vendorEditTmpl)))
	mux.HandleFunc("POST /vendors/{id}", authService.RequireAuth(vendorsSvc.HandleUpdate()))
	mux.HandleFunc("POST /vendors/{id}/delete", authService.RequireAuth(vendorsSvc.HandleDelete()))
	mux.HandleFunc("POST /vendors/{id}/ports", authService.RequireAuth(vendorsSvc.HandleAddPort()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/delete", authService.RequireAuth(vendorsSvc.HandleRemovePort()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/notes", authService.RequireAuth(vendorsSvc.HandleAddNote()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/contacts", authService.RequireAuth(vendorsSvc.HandleAddContact()))
	mux.HandleFunc("POST /vendors/{id}/ports/{vpid}/contacts/{cid}/delete", authService.RequireAuth(vendorsSvc.HandleDeleteContact()))
	mux.HandleFunc("POST /vendors/{id}/preferences/{pid}", authService.RequireAuth(vendorsSvc.HandleTogglePreference()))

	log.Printf("listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// checkDeadlines finds rate requests whose deadline has passed without notification
// and sends a notification email to the lane owner. Called by the background poller.
func checkDeadlines(database *sql.DB, notify func(to, subject, body string) error, baseURL string) {
	if notify == nil {
		return
	}

	rows, err := database.Query(`
		SELECT rr.id, rr.reference_id, rr.responses_received,
		       p.name, l.destination, l.direction,
		       u.email
		FROM rate_requests rr
		JOIN lanes l ON l.id = rr.lane_id
		JOIN ports p ON p.id = l.origin_port_id
		JOIN users u ON u.id = l.owner_id
		WHERE rr.deadline < CURRENT_TIMESTAMP
		  AND rr.notified = 0
	`)
	if err != nil {
		log.Printf("deadline poller: query error: %v", err)
		return
	}
	defer rows.Close()

	type deadlineRow struct {
		rrID              int
		refID             string
		responsesReceived int
		originPort        string
		destination       string
		direction         string
		ownerEmail        string
	}

	var due []deadlineRow
	for rows.Next() {
		var d deadlineRow
		if err := rows.Scan(&d.rrID, &d.refID, &d.responsesReceived, &d.originPort, &d.destination, &d.direction, &d.ownerEmail); err != nil {
			log.Printf("deadline poller: scan error: %v", err)
			continue
		}
		due = append(due, d)
	}

	for _, d := range due {
		// Mark notified first (CAS) to prevent duplicate sends across poller ticks.
		res, err := database.Exec(`UPDATE rate_requests SET notified = 1 WHERE id = ? AND notified = 0`, d.rrID)
		if err != nil {
			log.Printf("deadline poller: mark notified rr=%d: %v", d.rrID, err)
			continue
		}
		if ra, _ := res.RowsAffected(); ra == 0 {
			continue // another goroutine beat us to it
		}

		dir := "Import"
		if d.direction == "export" {
			dir = "Export"
		}
		compURL := fmt.Sprintf("%s/rate-requests/%d/comparison", baseURL, d.rrID)
		subject := fmt.Sprintf("[Drayage Quoter] Rate request deadline passed — %s → %s — %s", d.originPort, d.destination, d.refID)
		body := fmt.Sprintf(
			"The deadline for your rate request has passed.\n\n%s → %s (%s)\n%d response(s) received.\n\nView comparison:\n%s",
			d.originPort, d.destination, dir, d.responsesReceived, compURL,
		)

		if err := notify(d.ownerEmail, subject, body); err != nil {
			log.Printf("deadline poller: send notification to %s rr=%d: %v", d.ownerEmail, d.rrID, err)
		} else {
			log.Printf("deadline poller: notified %s for rr=%d (%s)", d.ownerEmail, d.rrID, d.refID)
		}
	}
}
