ALTER TABLE questions ADD COLUMN IF NOT EXISTS source_json jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE TABLE IF NOT EXISTS question_assets (
  question_id text NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
  id text NOT NULL,
  mime_type text NOT NULL CHECK (mime_type IN ('image/png','image/jpeg','image/svg+xml')),
  data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 10485760),
  PRIMARY KEY (question_id, id)
);
