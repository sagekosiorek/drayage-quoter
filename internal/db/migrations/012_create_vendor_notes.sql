CREATE TABLE vendor_notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    vendor_ports_id INTEGER NOT NULL REFERENCES vendor_ports(id) ON DELETE CASCADE,
    author_id  INTEGER NOT NULL REFERENCES users(id),
    content    TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
