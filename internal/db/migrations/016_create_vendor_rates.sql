CREATE TABLE vendor_rates (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    rate_request_id INTEGER NOT NULL REFERENCES rate_requests(id) ON DELETE CASCADE,
    vendor_id       INTEGER NOT NULL REFERENCES vendors(id)       ON DELETE CASCADE,
    original_email  TEXT    NOT NULL,
    parsed_by       TEXT    NOT NULL DEFAULT 'regex',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);
