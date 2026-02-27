package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// portMap maps port name → ports.id.
type portMap map[string]int

// vendorInfo tracks a vendor's DB id and its vendor_ports row IDs keyed by
// the same port-name substring used when the row was inserted.
type vendorInfo struct {
	id    int
	vpIDs map[string]int // portSubstr → vendor_ports.id
}

// laneRecord carries just enough lane data for rate request generation.
type laneRecord struct {
	id        int
	portSubstr string // matches the portSubstr used in mustSeedVendors
	portName  string
	portType  string
	dest      string
	direction string
	container string
}

// ── reset ────────────────────────────────────────────────────────────────────

// resetAll deletes all seeded data in FK-safe order, leaving ports and users intact.
func resetAll(db *sql.DB) error {
	for _, t := range []string{
		"rate_request_vendors",
		"rate_requests",
		"vendor_preferences",
		"vendor_notes",
		"vendor_contacts",
		"vendor_ports",
		"vendors",
		"lanes",
		"customers",
	} {
		if _, err := db.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
		log.Printf("  cleared %s", t)
	}
	return nil
}

// ── ports ────────────────────────────────────────────────────────────────────

func mustLoadPorts(db *sql.DB) portMap {
	rows, err := db.Query("SELECT id, name FROM ports")
	if err != nil {
		log.Fatalf("load ports: %v", err)
	}
	defer rows.Close()
	m := portMap{}
	for rows.Next() {
		var id int
		var name string
		rows.Scan(&id, &name)
		m[name] = id
	}
	if len(m) == 0 {
		log.Fatal("no ports found — run migrations first")
	}
	return m
}

// portBySubstr finds a port ID by case-insensitive substring match.
func portBySubstr(ports portMap, substr string) int {
	lower := strings.ToLower(substr)
	for name, id := range ports {
		if strings.Contains(strings.ToLower(name), lower) {
			return id
		}
	}
	log.Fatalf("port not found matching %q", substr)
	return 0
}

// portFullName returns the full port name matching a substring.
func portFullName(ports portMap, substr string) string {
	lower := strings.ToLower(substr)
	for name := range ports {
		if strings.Contains(strings.ToLower(name), lower) {
			return name
		}
	}
	log.Fatalf("port not found matching %q", substr)
	return ""
}

// portTypeFor returns the type of the first port matching substr.
func portTypeFor(ports portMap, db *sql.DB, substr string) string {
	id := portBySubstr(ports, substr)
	var t string
	db.QueryRow("SELECT type FROM ports WHERE id = ?", id).Scan(&t)
	return t
}

// ── customers ────────────────────────────────────────────────────────────────

func mustSeedCustomers(db *sql.DB) map[string]int {
	rows := []struct{ name string }{
		{"Pacific Rim Imports LLC"},
		{"Target Global Sourcing"},
		{"Home Depot Supply Chain"},
		{"Costco Wholesale Corp"},
		{"Best Buy Logistics"},
		{"Harbor Freight Tools"},
	}
	ids := map[string]int{}
	for _, c := range rows {
		res, err := db.Exec("INSERT INTO customers (name) VALUES (?)", c.name)
		if err != nil {
			log.Fatalf("insert customer %q: %v", c.name, err)
		}
		id, _ := res.LastInsertId()
		ids[c.name] = int(id)
	}
	log.Printf("  seeded %d customers", len(rows))
	return ids
}

// ── vendors ──────────────────────────────────────────────────────────────────

func mustSeedVendors(db *sql.DB, adminID int, ports portMap) map[string]vendorInfo {
	type contact struct{ name, email string }
	type portDef struct {
		sub       string // substring used to match port name
		preferred bool
		contacts  []contact
	}
	type vendorDef struct {
		name  string
		ports []portDef
	}

	defs := []vendorDef{
		{
			name: "Pacific Coast Drayage",
			ports: []portDef{
				{sub: "los angeles", preferred: true, contacts: []contact{
					{"Tom Chen", "tom.chen@pacificcoastdray.com"},
					{"Maria Rodriguez", "m.rodriguez@pacificcoastdray.com"},
				}},
				{sub: "seattle", preferred: false, contacts: []contact{
					{"James Park", "j.park@pacificcoastdray.com"},
				}},
				{sub: "oakland", preferred: false, contacts: []contact{
					{"Sandra Okafor", "s.okafor@pacificcoastdray.com"},
				}},
			},
		},
		{
			name: "Harbor Logistics Group",
			ports: []portDef{
				{sub: "los angeles", preferred: false, contacts: []contact{
					{"Sandra Lee", "s.lee@harborlogisticsgrp.com"},
				}},
				{sub: "houston", preferred: true, contacts: []contact{
					{"Derek Williams", "d.williams@harborlogisticsgrp.com"},
					{"Priya Patel", "p.patel@harborlogisticsgrp.com"},
				}},
			},
		},
		{
			name: "Intermodal Express LLC",
			ports: []portDef{
				{sub: "chicago", preferred: true, contacts: []contact{
					{"Kevin O'Brien", "k.obrien@intermodalexpress.com"},
				}},
				{sub: "dallas", preferred: false, contacts: []contact{
					{"Ashley Grant", "a.grant@intermodalexpress.com"},
				}},
				{sub: "memphis", preferred: false, contacts: []contact{
					{"Marcus Johnson", "m.johnson@intermodalexpress.com"},
				}},
			},
		},
		{
			name: "Gulf Transport Solutions",
			ports: []portDef{
				{sub: "houston", preferred: false, contacts: []contact{
					{"Carlos Mendez", "c.mendez@gulftransportsol.com"},
				}},
				{sub: "savannah", preferred: true, contacts: []contact{
					{"Rachel Kim", "r.kim@gulftransportsol.com"},
					{"Ben Foster", "b.foster@gulftransportsol.com"},
				}},
				{sub: "new orleans", preferred: false, contacts: []contact{
					{"Louis Tran", "l.tran@gulftransportsol.com"},
				}},
			},
		},
		{
			name: "Atlantic Drayage Co",
			ports: []portDef{
				{sub: "new york", preferred: true, contacts: []contact{
					{"Linda Nguyen", "l.nguyen@atlanticdrayage.com"},
				}},
				{sub: "savannah", preferred: false, contacts: []contact{
					{"Robert Hayes", "r.hayes@atlanticdrayage.com"},
				}},
				{sub: "norfolk", preferred: false, contacts: []contact{
					{"Emily Russo", "e.russo@atlanticdrayage.com"},
				}},
			},
		},
		{
			name: "Midwest Container Services",
			ports: []portDef{
				{sub: "chicago", preferred: false, contacts: []contact{
					{"Tina Brooks", "t.brooks@midwestcontainer.com"},
					{"Alan Foster", "a.foster@midwestcontainer.com"},
				}},
				{sub: "dallas", preferred: true, contacts: []contact{
					{"George Tan", "g.tan@midwestcontainer.com"},
				}},
				{sub: "kansas", preferred: false, contacts: []contact{
					{"Nicole West", "n.west@midwestcontainer.com"},
				}},
			},
		},
	}

	vendors := map[string]vendorInfo{}
	for _, def := range defs {
		res, err := db.Exec("INSERT INTO vendors (name) VALUES (?)", def.name)
		if err != nil {
			log.Fatalf("insert vendor %q: %v", def.name, err)
		}
		vid, _ := res.LastInsertId()
		vi := vendorInfo{id: int(vid), vpIDs: map[string]int{}}

		for _, pd := range def.ports {
			pid := portBySubstr(ports, pd.sub)
			vpRes, err := db.Exec(
				"INSERT INTO vendor_ports (vendor_id, port_id) VALUES (?, ?)", vid, pid,
			)
			if err != nil {
				log.Fatalf("insert vendor_port %q @ %s: %v", def.name, pd.sub, err)
			}
			vpID, _ := vpRes.LastInsertId()
			vi.vpIDs[pd.sub] = int(vpID)

			for _, c := range pd.contacts {
				db.Exec(
					"INSERT INTO vendor_contacts (vendor_ports_id, name, email) VALUES (?, ?, ?, ?)",
					vpID, ns(c.name), c.email,
				)
			}
			if pd.preferred {
				db.Exec(
					"INSERT INTO vendor_preferences (user_id, vendor_id, port_id) VALUES (?, ?, ?)",
					adminID, vid, pid,
				)
			}
		}
		vendors[def.name] = vi
	}
	log.Printf("  seeded %d vendors", len(defs))
	return vendors
}

// ── lanes ────────────────────────────────────────────────────────────────────

func mustSeedLanes(db *sql.DB, adminID int, customers map[string]int, ports portMap) []laneRecord {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	type laneDef struct {
		customer  string
		portSub   string
		dest      string
		container string
		direction string
		loadType  string
		status    string
		hazmat    bool
		overweight bool
		commodity string
		notes     string
	}

	defs := []laneDef{
		// ── Draft ───────────────────────────────────────────────────────────
		{
			customer: "Pacific Rim Imports LLC", portSub: "los angeles",
			dest: "Phoenix, AZ", container: "40HC", direction: "import",
			loadType: "live", status: "draft",
		},
		{
			customer: "Home Depot Supply Chain", portSub: "houston",
			dest: "Dallas, TX", container: "40", direction: "import",
			loadType: "drop_pick", status: "draft",
			notes: "Customer needs flatbed option if available",
		},
		{
			customer: "Best Buy Logistics", portSub: "new york",
			dest: "Boston, MA", container: "20_40", direction: "import",
			loadType: "live", status: "draft",
		},
		{
			customer: "Costco Wholesale Corp", portSub: "seattle",
			dest: "Portland, OR", container: "40", direction: "import",
			loadType: "live", status: "draft",
		},
		{
			customer: "Harbor Freight Tools", portSub: "chicago",
			dest: "Nashville, TN", container: "20", direction: "import",
			loadType: "drop_pick", status: "draft",
			hazmat: true,
		},
		// ── Rates Requested (rate requests will be created for these) ────────
		{
			customer: "Pacific Rim Imports LLC", portSub: "los angeles",
			dest: "Chicago, IL", container: "40HC", direction: "import",
			loadType: "live", status: "rates_requested",
			commodity: "Consumer Electronics",
		},
		{
			customer: "Target Global Sourcing", portSub: "savannah",
			dest: "Charlotte, NC", container: "40", direction: "import",
			loadType: "live", status: "rates_requested",
		},
		{
			customer: "Home Depot Supply Chain", portSub: "new york",
			dest: "Atlanta, GA", container: "20_40", direction: "import",
			loadType: "drop_pick", status: "rates_requested",
			overweight: true,
			notes: "Overweight permit already arranged — confirm carrier can accept",
		},
		// ── Rates Received ───────────────────────────────────────────────────
		{
			customer: "Costco Wholesale Corp", portSub: "los angeles",
			dest: "Las Vegas, NV", container: "40", direction: "import",
			loadType: "live", status: "rates_received",
		},
		{
			customer: "Best Buy Logistics", portSub: "dallas",
			dest: "Oklahoma City, OK", container: "20_40", direction: "import",
			loadType: "drop_pick", status: "rates_received",
		},
		// ── Quoting ─────────────────────────────────────────────────────────
		{
			customer: "Pacific Rim Imports LLC", portSub: "houston",
			dest: "San Antonio, TX", container: "40HC", direction: "import",
			loadType: "live", status: "quoting",
			commodity: "Industrial Equipment",
			overweight: true,
		},
		// ── Quoted ──────────────────────────────────────────────────────────
		{
			customer: "Target Global Sourcing", portSub: "chicago",
			dest: "Memphis, TN", container: "40", direction: "import",
			loadType: "live", status: "quoted",
		},
	}

	var records []laneRecord
	for _, d := range defs {
		cid, ok := customers[d.customer]
		if !ok {
			log.Fatalf("customer not found: %q", d.customer)
		}
		pid := portBySubstr(ports, d.portSub)

		res, err := db.Exec(`
			INSERT INTO lanes (
				owner_id, customer_id, origin_port_id, destination,
				container_size, direction, load_type,
				commodity, hazmat, overweight, out_of_gauge,
				notes, status, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			adminID, cid, pid, d.dest,
			d.container, d.direction, d.loadType,
			ns(d.commodity), btoi(d.hazmat), btoi(d.overweight),
			ns(d.notes), d.status, now,
		)
		if err != nil {
			log.Fatalf("insert lane %q→%s: %v", d.portSub, d.dest, err)
		}
		id, _ := res.LastInsertId()

		records = append(records, laneRecord{
			id:         int(id),
			portSubstr: d.portSub,
			portName:   portFullName(ports, d.portSub),
			portType:   portTypeFor(ports, db, d.portSub),
			dest:       d.dest,
			direction:  d.direction,
			container:  d.container,
		})
	}
	log.Printf("  seeded %d lanes", len(defs))
	return records
}

// ── rate requests ─────────────────────────────────────────────────────────────

func mustSeedRateRequests(db *sql.DB, lanes []laneRecord, vendors map[string]vendorInfo, ports portMap) {
	// Only lanes at rates_requested status (indices 5, 6, 7 in the defs above).
	requested := []laneRecord{}
	for _, l := range lanes {
		var status string
		db.QueryRow("SELECT status FROM lanes WHERE id = ?", l.id).Scan(&status)
		if status == "rates_requested" {
			requested = append(requested, l)
		}
	}

	type rrDef struct {
		lane        laneRecord
		vendorNames []string
		threshold   interface{}
		deadlineHrs int // 0 = no deadline
	}

	if len(requested) < 3 {
		log.Printf("  warning: expected 3 rates_requested lanes, found %d", len(requested))
		return
	}

	defs := []rrDef{
		{
			lane:        requested[0], // LA/LB → Chicago
			vendorNames: []string{"Pacific Coast Drayage", "Harbor Logistics Group"},
			threshold:   3,
			deadlineHrs: 48,
		},
		{
			lane:        requested[1], // Savannah → Charlotte
			vendorNames: []string{"Gulf Transport Solutions", "Atlantic Drayage Co"},
		},
		{
			lane:        requested[2], // NY/NJ → Atlanta
			vendorNames: []string{"Atlantic Drayage Co"},
			threshold:   2,
			deadlineHrs: 72,
		},
	}

	now := time.Now().UTC()
	for i, def := range defs {
		refID := fmt.Sprintf("RR-%d-%05d", now.Year(), i+1)
		subject := buildSeedSubject(def.lane, refID)
		body := buildSeedBody(def.lane, refID)

		var deadline interface{}
		if def.deadlineHrs > 0 {
			deadline = now.Add(time.Duration(def.deadlineHrs) * time.Hour).Format("2006-01-02 15:04:05")
		}

		res, err := db.Exec(`
			INSERT INTO rate_requests
				(lane_id, reference_id, subject, body, response_threshold, deadline)
			VALUES (?, ?, ?, ?, ?, ?)`,
			def.lane.id, refID, subject, body, def.threshold, deadline,
		)
		if err != nil {
			log.Fatalf("insert rate_request %s: %v", refID, err)
		}
		rrID, _ := res.LastInsertId()

		for _, vname := range def.vendorNames {
			vi, ok := vendors[vname]
			if !ok {
				log.Fatalf("vendor not found: %q", vname)
			}
			db.Exec(
				"INSERT OR IGNORE INTO rate_request_vendors (rate_request_id, vendor_id) VALUES (?, ?)",
				rrID, vi.id,
			)
		}
	}
	log.Printf("  seeded %d rate requests", len(defs))
}

// ── email helpers ─────────────────────────────────────────────────────────────

func buildSeedSubject(l laneRecord, refID string) string {
	dir := "Import"
	if l.direction == "export" {
		dir = "Export"
	}
	return fmt.Sprintf("Rate Request: %s %s - %s - %s - %s",
		l.portName, l.dest, dir, refID)
}

func buildSeedBody(l laneRecord, refID string) string {
	dir := "Import"
	if l.direction == "export" {
		dir = "Export"
	}
	size := map[string]string{
		"20_40": "20' / 40'", "20": "20'", "40": "40'", "40HC": "40' High Cube",
	}[l.container]
	if size == "" {
		size = l.container
	}
	return fmt.Sprintf(
		"Hi [First Name],\n\n"+
			"We are reaching out to request drayage rates for the following lane:\n\n"+
			"Reference:       %s\n"+
			"Origin Port:     %s (%s)\n"+
			"Destination:     %s\n"+
			"Direction:       %s\n"+
			"Container Size:  %s\n"+
			"Load Type:       Live\n\n"+
			"Please reply with your best all-in rate at your earliest convenience.\n\n"+
			"Thank you,\nSchneider Logistics",
		refID, l.portName, l.portType, l.dest, dir, size,
	)
}

// ── local helpers ─────────────────────────────────────────────────────────────

// ns returns nil for empty strings so blank fields become SQL NULL.
func ns(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
