CREATE TABLE lanes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id       INTEGER NOT NULL REFERENCES users(id),
    customer_id    INTEGER NOT NULL REFERENCES customers(id),
    origin_port_id INTEGER NOT NULL REFERENCES ports(id),
    destination    TEXT NOT NULL,
    container_size TEXT NOT NULL,
    weight         INTEGER,
    direction      TEXT NOT NULL DEFAULT 'import',
    load_type      TEXT NOT NULL DEFAULT 'live',
    commodity      TEXT,
    hazmat         INTEGER NOT NULL DEFAULT 0,
    overweight     INTEGER NOT NULL DEFAULT 0,
    out_of_gauge   INTEGER NOT NULL DEFAULT 0,
    notes          TEXT,
    status         TEXT NOT NULL DEFAULT 'draft',
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
);
