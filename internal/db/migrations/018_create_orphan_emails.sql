CREATE TABLE orphan_emails (
    id                          INTEGER  PRIMARY KEY AUTOINCREMENT,
    raw_email                   TEXT     NOT NULL,
    subject                     TEXT     NOT NULL DEFAULT '',
    sender                      TEXT     NOT NULL DEFAULT '',
    received_at                 DATETIME DEFAULT CURRENT_TIMESTAMP,
    assigned_to_rate_request_id INTEGER  REFERENCES rate_requests(id) ON DELETE SET NULL
);
