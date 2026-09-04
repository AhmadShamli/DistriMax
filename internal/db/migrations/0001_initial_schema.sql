-- Migration tracking table
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Settings table (AES-256-GCM for encrypted secrets)
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    is_encrypted INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_by TEXT NOT NULL
);

-- Admin users (Argon2id password hashes)
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_disabled INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Admin sessions
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

-- Products catalog
CREATE TABLE IF NOT EXISTS products (
    id TEXT PRIMARY KEY,             -- e.g. "geolite-city"
    edition_id TEXT NOT NULL UNIQUE, -- e.g. "GeoLite2-City"
    display_name TEXT NOT NULL,
    artifact_filename TEXT NOT NULL, -- e.g. "GeoLite2-City.mmdb"
    is_enabled INTEGER NOT NULL DEFAULT 1,
    sync_schedule_cron TEXT NOT NULL DEFAULT "0 4 * * *",
    retention_days INTEGER NOT NULL DEFAULT 30,
    staleness_days INTEGER NOT NULL DEFAULT 8,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Product versions
CREATE TABLE IF NOT EXISTS product_versions (
    id TEXT PRIMARY KEY,
    product_id TEXT NOT NULL REFERENCES products(id),
    version TEXT NOT NULL,
    released_at TIMESTAMP NOT NULL,
    sha256 TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    storage_backend TEXT NOT NULL,   -- "filesystem" or "s3"
    storage_path TEXT NOT NULL,
    is_current INTEGER NOT NULL DEFAULT 0,
    superseded_at TIMESTAMP,
    cleanup_deadline TIMESTAMP,
    is_deleted INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_product_versions_unique ON product_versions(product_id, version);
CREATE INDEX IF NOT EXISTS idx_product_versions_cleanup ON product_versions(is_current, is_deleted, cleanup_deadline);

-- Client API keys (Downloads and metadata only)
CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    key_prefix TEXT NOT NULL,        -- First 12 characters (e.g. "dm_live_abcd")
    key_hash TEXT NOT NULL UNIQUE,   -- Argon2id hash of entire secret
    display_name TEXT NOT NULL,
    scopes TEXT NOT NULL,            -- "manifest:read,download"
    allowed_products TEXT NOT NULL,  -- "*" or comma-separated products
    rate_limit_per_min INTEGER NOT NULL DEFAULT 60,
    is_revoked INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMP,
    last_used_at TIMESTAMP,
    last_used_ip TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(key_prefix);

-- Request audit logs (90-day retention)
CREATE TABLE IF NOT EXISTS request_audit_logs (
    id TEXT PRIMARY KEY,
    timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    source_ip TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,              -- Sanitized (tokens and passwords redacted)
    status INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    bytes_sent INTEGER NOT NULL,
    api_key_id TEXT REFERENCES api_keys(id) ON DELETE SET NULL,
    product_id TEXT,
    version TEXT,
    user_agent TEXT,
    failure_reason TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp ON request_audit_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_logs_product ON request_audit_logs(product_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_logs_api_key ON request_audit_logs(api_key_id, timestamp);

-- Sync run history
CREATE TABLE IF NOT EXISTS sync_runs (
    id TEXT PRIMARY KEY,
    product_id TEXT NOT NULL REFERENCES products(id),
    status TEXT NOT NULL,            -- "SUCCESS", "FAILED", "SKIPPED"
    trigger_type TEXT NOT NULL,      -- "SCHEDULED", "MANUAL"
    version_discovered TEXT,
    duration_ms INTEGER NOT NULL,
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sync_runs_product ON sync_runs(product_id, created_at);

-- Webhook delivery audit history
CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    product_id TEXT,
    version TEXT,
    target_url TEXT NOT NULL,
    status_code INTEGER,
    attempt_count INTEGER NOT NULL DEFAULT 1,
    duration_ms INTEGER,
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_created ON webhook_deliveries(created_at);

-- Baseline Seeding
INSERT OR IGNORE INTO products (id, edition_id, display_name, artifact_filename, is_enabled, retention_days, staleness_days)
VALUES
  ('geolite-city', 'GeoLite2-City', 'GeoLite2 City', 'GeoLite2-City.mmdb', 1, 30, 8),
  ('geolite-country', 'GeoLite2-Country', 'GeoLite2 Country', 'GeoLite2-Country.mmdb', 1, 30, 8),
  ('geolite-asn', 'GeoLite2-ASN', 'GeoLite2 ASN', 'GeoLite2-ASN.mmdb', 1, 30, 8);

INSERT OR IGNORE INTO settings (key, value, is_encrypted, updated_by)
VALUES
  ('setup_completed', 'false', 0, 'system'),
  ('download_concurrency_limit', '50', 0, 'system'),
  ('audit_retention_days', '90', 0, 'system'),
  ('artifact_retention_days', '30', 0, 'system'),
  ('sync_schedule_cron', '0 4 * * *', 0, 'system'),
  ('staleness_threshold_days', '8', 0, 'system'),
  ('storage_backend', 'filesystem', 0, 'system');

