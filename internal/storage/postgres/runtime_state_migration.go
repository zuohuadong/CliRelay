package postgres

const runtimeStateSQL = `
CREATE TABLE IF NOT EXISTS cluster_runtime_kv (
    state_key TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS cluster_runtime_kv_expiry ON cluster_runtime_kv (expires_at);

CREATE TABLE IF NOT EXISTS cluster_runtime_leases (
    scope TEXT NOT NULL,
    member TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, member)
);
CREATE INDEX IF NOT EXISTS cluster_runtime_leases_expiry ON cluster_runtime_leases (expires_at);

CREATE TABLE IF NOT EXISTS cluster_runtime_rates (
    subject TEXT NOT NULL,
    minute BIGINT NOT NULL,
    requests BIGINT NOT NULL,
    tokens BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (subject, minute)
);
CREATE INDEX IF NOT EXISTS cluster_runtime_rates_expiry ON cluster_runtime_rates (expires_at);

CREATE TABLE IF NOT EXISTS runtime_job_queue (
    sequence BIGSERIAL PRIMARY KEY,
    queue_name TEXT NOT NULL,
    payload JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_job_queue_order ON runtime_job_queue (queue_name, sequence);

CREATE TABLE IF NOT EXISTS runtime_named_locks (
    lock_name TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS runtime_named_locks_expiry ON runtime_named_locks (expires_at);

CREATE TABLE IF NOT EXISTS runtime_usage_snapshots (
    node_id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS home_runtime_kv (
    state_key TEXT PRIMARY KEY,
    payload BYTEA NOT NULL,
    expires_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS home_runtime_kv_expiry ON home_runtime_kv (expires_at);
`
