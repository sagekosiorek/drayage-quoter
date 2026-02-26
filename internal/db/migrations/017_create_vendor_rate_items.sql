CREATE TABLE vendor_rate_items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    vendor_rate_id  INTEGER NOT NULL REFERENCES vendor_rates(id) ON DELETE CASCADE,
    charge_type     TEXT    NOT NULL,
    amount          REAL    NOT NULL,
    unit            TEXT    NOT NULL,
    manually_edited INTEGER NOT NULL DEFAULT 0
);
