CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY, name text NOT NULL, grade integer NOT NULL CHECK (grade BETWEEN 7 AND 12),
  email text NOT NULL UNIQUE, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS email_challenges (
  id text PRIMARY KEY, email text NOT NULL, purpose text NOT NULL CHECK (purpose IN ('sign-in','sign-up')),
  name text, grade integer, code_hash text NOT NULL, expires_at timestamptz NOT NULL,
  resend_at timestamptz NOT NULL, attempts integer NOT NULL DEFAULT 0, used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS email_challenges_email_created ON email_challenges(email, created_at DESC);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS exams (
  id text PRIMARY KEY, title text NOT NULL, opens_at timestamptz NOT NULL,
  entry_closes_at timestamptz NOT NULL, explanations_released_at timestamptz,
  leaderboard_released_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS questions (
  id text PRIMARY KEY, exam_id text NOT NULL REFERENCES exams(id) ON DELETE CASCADE,
  section_id text NOT NULL CHECK (section_id IN ('reading-writing','math')),
  position integer NOT NULL, public_json jsonb NOT NULL, correct_answer jsonb NOT NULL,
  explanation jsonb NOT NULL DEFAULT '[]'::jsonb,
  UNIQUE(exam_id, section_id, position), UNIQUE(exam_id, id)
);
CREATE TABLE IF NOT EXISTS attempts (
  id text PRIMARY KEY, exam_id text NOT NULL REFERENCES exams(id),
  user_id text NOT NULL REFERENCES users(id), phase text NOT NULL DEFAULT 'instructions',
  section_id text NOT NULL DEFAULT 'reading-writing', started_at timestamptz,
  deadline_at timestamptz, first_started_at timestamptz, completed_at timestamptz,
  disqualified_at timestamptz, disqualification_reason text,
  reading_score integer, math_score integer, event_count integer NOT NULL DEFAULT 0,
  last_event_reason text, created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(exam_id, user_id)
);
CREATE INDEX IF NOT EXISTS attempts_deadline ON attempts(deadline_at) WHERE phase = 'in-progress';
CREATE TABLE IF NOT EXISTS answers (
  attempt_id text NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  question_id text NOT NULL REFERENCES questions(id),
  value jsonb, marked_for_review boolean NOT NULL DEFAULT false,
  tools jsonb, revision integer NOT NULL DEFAULT 1, saved_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(attempt_id, question_id)
);
CREATE TABLE IF NOT EXISTS mutations (
  attempt_id text NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  mutation_id text NOT NULL, operation text NOT NULL, payload_hash text NOT NULL,
  response jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(attempt_id, mutation_id)
);
CREATE TABLE IF NOT EXISTS browser_events (
  attempt_id text NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
  event_id text NOT NULL, kind text NOT NULL, observed_at timestamptz,
  related_event_id text, counted boolean NOT NULL, message text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(attempt_id, event_id)
);
CREATE INDEX IF NOT EXISTS browser_events_recent ON browser_events(attempt_id, received_at DESC);
