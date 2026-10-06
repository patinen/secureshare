CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 github_id bigint NOT NULL UNIQUE,
 login text NOT NULL,
 name text,
 avatar_url text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE shares (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id),
 type text NOT NULL CHECK (type = 'TEXT'),
 title text CHECK (char_length(title) <= 150),
 text_content text NOT NULL CHECK (octet_length(text_content) BETWEEN 1 AND 102400 AND length(btrim(text_content)) > 0),
 token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 max_redemptions integer CHECK (max_redemptions BETWEEN 1 AND 1000),
 redemption_count bigint NOT NULL DEFAULT 0 CHECK (redemption_count >= 0 AND (max_redemptions IS NULL OR redemption_count <= max_redemptions)),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (expires_at > created_at AND expires_at <= created_at + interval '30 days')
);
CREATE INDEX shares_owner_created ON shares(user_id, created_at DESC);
CREATE INDEX shares_expiry ON shares(expires_at);
