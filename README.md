# Drayage Quoter

## Usage

### Init database

To initialize db locally, just run the server:
```
go run ./cmd/server
```

When the server starts (cmd/server/main.go):

1. os.MkdirAll ensures /data/ exists
2. db.Open(dbPath) opens (or creates) the SQLite file at /data/drayage.db - SQLite creates the file automatically if it doesn't exist
3. WAL mode and foreign keys are enabled via PRAGMAs
4. db.Migrate() runs any pending .sql files from internal/db/migrations/

### Local Litestream replication to MinIO

_Save for integration testing; not needed for daily development_

To test replication locally, you need a litestream.yml config pointing at your MinIO instance. Something like:

```
dbs:
- path: /data/drayage.db
  replicas:
    - type: s3
      bucket: drayage-backups
      endpoint: http://localhost:9000
      access-key-id: minioadmin
      secret-access-key: minioadmin
      force-path-style: true  # required
for MinIO
```

Then run Litestream as a wrapper around your server:
```
litestream replicate -config litestream.yml
```

Or to run it as a sidecar that also starts your app:
```
litestream replicate -config litestream.yml
-exec "go run ./cmd/server"
```

The -exec flag is the recommended pattern - Litestream starts replication, then launches your app as a child process. If your app dies, Litestream stops too. Test a restore with:
```
litestream restore -config litestream.yml -o
./restored.db /data/drayage.db
```

### Seed Drayage Quoter with dummy data
There's a separate `/cmd/seed` binary that can be used to inject dummy data into an instance for testing purposes. These are the commands it accepts:
```
go run ./cmd/seed               # seeds if the DB is empty, skips otherwise
go run ./cmd/seed --reset       # wipes all non-port/user data and re-seeds
go run ./cmd/seed --wipe        # deletes all data leaving the instance empty
go run ./cmd/seed --db <path>   # targets a specific DB file
```

To seed the production instance, ensure the `/cmd/seed` binary is added to the Docker image and built upon deployment. Then run via SSH:
```
fly ssh console -C "/seed --db /data/drayage.db"
```
Or to reset:
```
fly ssh console -C "/seed --db /data/drayage.db" --reset
```

### Mailgun email setup
1. Add your sending domain in the Mailgun dashboard → verify MX + SPF/DKIM DNS records
2. Create an Inbound Route: match recipient = rates@<domain> → forward to https://<app>.fly.dev/webhooks/email/inbound
3. Set Fly.io secrets: fly secrets set MAILGUN_API_KEY=... MAILGUN_DOMAIN=... MAILGUN_WEBHOOK_SIGNING_KEY=...

\*If email is not set up, the app will default to logging the login code to terminal output. However, outbound notifications nor rate ingests will work without email configured.

### LLM Setup
Modular model setup. Comes with scaffolding built for Claude, but any other model can be plugged in with small additions to `internal/rates/llm.go`. See `ClaudeCorrector` for example.

To configure Cladue, wire it up by adding these to your .env file:
`ANTHROPIC_API_KEY=` -> ClaudeCorrector
`ANTRHOPIC_MODEL=`   -> optional model override (defaults to Haiku)
(unset)              -> NoopCorrector, logs "LLM correction disabled"
