# PostgreSQL / Redis Runtime

## Runtime boundary

CliRelay runs exclusively on PostgreSQL 15+, Redis 7+, and Ent ORM.

- PostgreSQL is the only persistent runtime data source.
- Redis stores cache, locks, rate limits, queues, and rebuildable state only.
- The application entrypoint does not scan for legacy database files and does not run a data importer at startup, health checks, blue-green deploys, or OTA updates.
- Stack upgrades remove a stale `clirelay-migrate` service and leftover `CLIRELAY_SQLITE_*` application environment entries from old compose files. They do not delete files on disk.

For Docker Compose deployments:

```bash
docker compose up -d
```

The stack starts `clirelay-init`, PostgreSQL, Redis, the application, and `clirelay-updater`. OTA state is stored by the updater in `.clirelay-updater-status.json` and streamed to the management API through SSE.

## Old Compose and updater transition

For a deployment that still uses an old compose file:

1. Back up `docker-compose.yml`, `.env`, and PostgreSQL data.
2. Replace `docker-compose.yml` with the current repository version.
3. Start the runtime dependencies and updater:

   ```bash
   docker compose up -d postgres redis clirelay-updater
   ```

4. Start or recreate the application service.

An updater sidecar from a release that predates the SSE protocol must be recreated once:

```bash
docker compose up -d --force-recreate clirelay-updater
```

After that transition, the management panel receives real updater snapshots and can recover after API-container restarts or temporary SSE disconnects.

## Validation

Run the repository tests:

```bash
rtk go test ./cmd/updater -count=1
rtk go test ./internal/management/updateflow ./internal/api/handlers/management -count=1
rtk go test ./internal/storage/postgres/... ./internal/usage -count=1
rtk go test ./...
```

With integration services available:

```bash
CLIRELAY_POSTGRES_TEST_DSN='postgres://cliproxy:cliproxy@127.0.0.1:55432/cliproxy?sslmode=disable' \
CLIRELAY_REDIS_TEST_ADDR='127.0.0.1:56379' \
rtk go test ./internal/usage -run TestPostgresRuntimeDataStackIntegration -count=1 -v
```

## Single-node SQLite fallback

An empty `postgres.dsn` no longer stops the process. The runtime opens the
embedded database in `sqlite.path` (default `<config dir>/data/usage.db`, or
`CLIRELAY_SQLITE_PATH`) and applies the *same* migration list in
`internal/storage/postgres` through `internal/storage/sqlitecompat`:

- column types are translated (`UUID`/`JSONB` → `TEXT`, `TIMESTAMPTZ` →
  `DATETIME`, `BIGSERIAL` → `INTEGER PRIMARY KEY AUTOINCREMENT`, …);
- `ALTER TABLE … ADD PRIMARY KEY (a, b)` becomes a unique index, which keeps the
  runtime's `ON CONFLICT (a, b)` upserts working;
- query text is rewritten per statement: `$N` → `?N`, `now()`, interval
  arithmetic, `EXTRACT(EPOCH …)`, `LEAST`/`GREATEST`, `::type` casts, `FOR UPDATE`;
- timestamps are stored as fixed-width UTC text (`2006-01-02T15:04:05.000000000Z`)
  because SQLite compares `DATETIME` as text. Both `time.Time` arguments and
  RFC3339 strings (the usage spool replays records that way) are normalised to
  that layout.

`go test ./internal/storage/sqlitecompat` applies the whole migration list to a
temporary SQLite file, so a future PostgreSQL-only migration fails there instead
of at startup.

Limitations, logged as warnings at startup when the affected migration runs:

- `SELECT DISTINCT ON`, `UPDATE … AS alias` and `DELETE … USING` statements have
  no SQLite equivalent. They only appear in one-shot data backfills, which a
  fresh install has nothing to migrate; the statement is skipped, not retried.
- Foreign keys and `CHECK` constraints added by later `ALTER TABLE` statements
  are not enforced (SQLite cannot add them). Tables created by `CREATE TABLE`
  keep the constraints they were declared with.
- Cluster mode is disabled: membership, leader election and the notification bus
  require PostgreSQL. Setting `cluster.enable: true` without a DSN logs a warning
  and continues as a single node.

A database file that already contains tables but no `schema_migrations` row was
written by the pre-PostgreSQL builds. Startup refuses it instead of half-applying
the migration set on live data: back the file up, then either point `sqlite.path`
at a new file or migrate the data into PostgreSQL deliberately.

Use SQLite for a single process on one host. Anything that needs shared state
(blue-green slots, several nodes, Redis-backed queues) still requires the
PostgreSQL/Redis stack.
