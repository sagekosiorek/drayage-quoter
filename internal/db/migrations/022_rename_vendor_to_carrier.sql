-- Rename all vendor-prefixed tables and columns to carrier.
-- SQLite 3.26+ automatically updates FK references when renaming tables.

ALTER TABLE vendors               RENAME TO carriers;
ALTER TABLE vendor_ports          RENAME TO carrier_ports;
ALTER TABLE vendor_contacts       RENAME TO carrier_contacts;
ALTER TABLE vendor_notes          RENAME TO carrier_notes;
ALTER TABLE vendor_preferences    RENAME TO carrier_preferences;
ALTER TABLE rate_request_vendors  RENAME TO rate_request_carriers;
ALTER TABLE vendor_rates          RENAME TO carrier_rates;
ALTER TABLE vendor_rate_items     RENAME TO carrier_rate_items;
ALTER TABLE vendor_lineups        RENAME TO carrier_lineups;

ALTER TABLE carrier_ports     RENAME COLUMN vendor_id       TO carrier_id;
ALTER TABLE carrier_contacts  RENAME COLUMN vendor_ports_id TO carrier_ports_id;
ALTER TABLE carrier_notes     RENAME COLUMN vendor_ports_id TO carrier_ports_id;
ALTER TABLE carrier_preferences RENAME COLUMN vendor_id     TO carrier_id;
ALTER TABLE rate_request_carriers RENAME COLUMN vendor_id   TO carrier_id;
ALTER TABLE carrier_rates     RENAME COLUMN vendor_id       TO carrier_id;
ALTER TABLE carrier_rate_items RENAME COLUMN vendor_rate_id TO carrier_rate_id;
ALTER TABLE carrier_lineups   RENAME COLUMN vendor_rate_id  TO carrier_rate_id;
