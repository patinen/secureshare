ALTER TABLE shares ADD COLUMN file_state text;
ALTER TABLE shares ADD COLUMN file_purged_at timestamptz;
ALTER TABLE shares ADD COLUMN exhausted_at timestamptz;
UPDATE shares SET file_state='READY', exhausted_at=CASE WHEN max_redemptions IS NOT NULL AND redemption_count>=max_redemptions THEN updated_at END WHERE type='FILE';
ALTER TABLE shares ADD CONSTRAINT shares_file_lifecycle CHECK (
 (type='TEXT' AND file_state IS NULL AND file_purged_at IS NULL AND exhausted_at IS NULL) OR
 (type='FILE' AND file_state IS NOT NULL AND ((file_state IN ('PENDING','READY') AND file_purged_at IS NULL) OR (file_state='PURGED' AND file_purged_at IS NOT NULL)))
);
ALTER TABLE shares ADD CONSTRAINT shares_exhaustion CHECK (exhausted_at IS NULL OR (type='FILE' AND max_redemptions IS NOT NULL AND redemption_count=max_redemptions));
CREATE INDEX shares_pending_cleanup ON shares(created_at) WHERE type='FILE' AND file_state='PENDING';
CREATE INDEX shares_ready_cleanup ON shares(LEAST(expires_at,revoked_at,exhausted_at)) WHERE type='FILE' AND file_state='READY';
