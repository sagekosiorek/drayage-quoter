CREATE TABLE rate_requests (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    lane_id            INTEGER NOT NULL REFERENCES lanes(id) ON DELETE CASCADE,
    reference_id       TEXT    NOT NULL UNIQUE,
    subject            TEXT    NOT NULL,
    body               TEXT    NOT NULL,
    response_threshold INTEGER,
    deadline           DATETIME,
    responses_received INTEGER NOT NULL DEFAULT 0,
    notified           INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME DEFAULT CURRENT_TIMESTAMP
);
