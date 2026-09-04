-- Migration 0002: Add default sync schedule cron and staleness threshold settings
INSERT OR IGNORE INTO settings (key, value, is_encrypted, updated_by)
VALUES
  ('sync_schedule_cron', '0 4 * * *', 0, 'system'),
  ('staleness_threshold_days', '8', 0, 'system');
