CREATE TABLE vendor_ports (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    vendor_id  INTEGER NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
    port_id    INTEGER NOT NULL REFERENCES ports(id)   ON DELETE CASCADE,
    -- contact_id is a soft reference to vendor_contacts.id (primary contact for this port).
    -- No REFERENCES clause to avoid a circular FK with vendor_contacts.vendor_ports_id.
    contact_id INTEGER,
    UNIQUE(vendor_id, port_id)
);
