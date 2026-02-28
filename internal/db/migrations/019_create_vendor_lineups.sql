CREATE TABLE vendor_lineups (
    id             INTEGER PRIMARY KEY,
    lane_id        INTEGER NOT NULL REFERENCES lanes(id) ON DELETE CASCADE,
    vendor_rate_id INTEGER NOT NULL REFERENCES vendor_rates(id) ON DELETE CASCADE,
    rank           INTEGER NOT NULL,
    UNIQUE(lane_id, rank)
);
