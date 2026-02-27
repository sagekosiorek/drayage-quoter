CREATE TABLE vendor_contacts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    vendor_ports_id INTEGER NOT NULL REFERENCES vendor_ports(id) ON DELETE CASCADE,
    name            TEXT,
    email           TEXT NOT NULL
);
