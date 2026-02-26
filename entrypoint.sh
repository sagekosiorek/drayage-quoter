#!/bin/sh
set -e

# Restore database from S3 replica on cold start (no-op if no replica exists).
litestream restore -if-replica-exists -if-db-not-exists -config /etc/litestream.yml /data/drayage.db

# Run the app under Litestream supervision for continuous replication.
exec litestream replicate -config /etc/litestream.yml -exec "/drayage-quoter"
