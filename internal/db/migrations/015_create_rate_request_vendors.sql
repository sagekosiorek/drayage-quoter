CREATE TABLE rate_request_vendors (
    rate_request_id INTEGER NOT NULL REFERENCES rate_requests(id) ON DELETE CASCADE,
    vendor_id       INTEGER NOT NULL REFERENCES vendors(id)       ON DELETE CASCADE,
    responded       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (rate_request_id, vendor_id)
);
