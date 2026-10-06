-- Deliberately no cascading foreign keys: immutable history survives removal
-- of application rows. Only internal UUID references, never content/identity IPs.
CREATE TABLE audit_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid,
 share_id uuid,
 event_type text NOT NULL CHECK (event_type IN ('AUTH_LOGIN','AUTH_LOGOUT','SHARE_TEXT_CREATED','SHARE_FILE_CREATED','SHARE_REDEEMED','SHARE_REVOKED','FILE_PURGED')),
 request_id text CHECK (request_id IS NULL OR request_id ~ '^[0-9a-f]{32}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX audit_owner_recent ON audit_events(user_id,created_at DESC,id DESC);
CREATE FUNCTION audit_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'audit events are append-only'; END $$;
CREATE TRIGGER audit_no_mutation BEFORE UPDATE OR DELETE ON audit_events FOR EACH STATEMENT EXECUTE FUNCTION audit_append_only();
CREATE TRIGGER audit_no_truncate BEFORE TRUNCATE ON audit_events FOR EACH STATEMENT EXECUTE FUNCTION audit_append_only();
-- Future retention must explicitly change these guards in a schema migration.
