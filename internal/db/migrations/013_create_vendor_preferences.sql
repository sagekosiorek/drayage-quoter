CREATE TABLE vendor_preferences (
    user_id   INTEGER NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    vendor_id INTEGER NOT NULL REFERENCES vendors(id)  ON DELETE CASCADE,
    port_id   INTEGER NOT NULL REFERENCES ports(id)    ON DELETE CASCADE,
    PRIMARY KEY (user_id, vendor_id, port_id)
);
