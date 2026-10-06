-- Preserve Phase 1 data and constraints unrelated to share content.
ALTER TABLE shares DROP CONSTRAINT shares_type_check;
ALTER TABLE shares DROP CONSTRAINT shares_text_content_check;
ALTER TABLE shares ALTER COLUMN text_content DROP NOT NULL;
ALTER TABLE shares ADD COLUMN object_key text;
ALTER TABLE shares ADD COLUMN file_name text;
ALTER TABLE shares ADD COLUMN content_type text;
ALTER TABLE shares ADD COLUMN file_size bigint;
ALTER TABLE shares ADD CONSTRAINT shares_type_check CHECK (type IN ('TEXT', 'FILE'));
ALTER TABLE shares ADD CONSTRAINT shares_content_check CHECK (
 (type = 'TEXT' AND text_content IS NOT NULL
  AND octet_length(text_content) BETWEEN 1 AND 102400 AND length(btrim(text_content)) > 0
  AND object_key IS NULL AND file_name IS NULL AND content_type IS NULL AND file_size IS NULL)
 OR
 (type = 'FILE' AND text_content IS NULL
  AND object_key IS NOT NULL AND object_key ~ '^files/[A-Za-z0-9_-]{43}$'
  AND file_name IS NOT NULL AND char_length(file_name) BETWEEN 1 AND 255
  AND content_type IS NOT NULL AND char_length(content_type) BETWEEN 1 AND 255
  AND file_size IS NOT NULL AND file_size BETWEEN 1 AND 26214400)
);
CREATE UNIQUE INDEX shares_object_key ON shares(object_key) WHERE object_key IS NOT NULL;
