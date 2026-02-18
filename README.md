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

Then run Litestream as a wrapper around your
server:
```
litestream replicate -config litestream.yml
```

Or to run it as a sidecar that also starts
your app:
```
litestream replicate -config litestream.yml
-exec "go run ./cmd/server"
```

The -exec flag is the recommended pattern -
Litestream starts replication, then launches
your app as a child process. If your app
dies, Litestream stops too. Test a restore
with:
```
litestream restore -config litestream.yml -o
./restored.db /data/drayage.db
```



