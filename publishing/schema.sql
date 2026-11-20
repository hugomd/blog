-- Apply only to the dedicated publishing database, never blog-reactions.
CREATE TABLE IF NOT EXISTS auth_requests (
  id TEXT PRIMARY KEY,
  state TEXT NOT NULL,
  client_id TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  challenge TEXT NOT NULL,
  challenge_method TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
-- Independent, short-lived admission buckets; additive for existing databases.
CREATE TABLE IF NOT EXISTS auth_admissions (
  client_hash TEXT PRIMARY KEY,
  attempts INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_codes (
  hash TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  challenge TEXT NOT NULL,
  challenge_method TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
  hash TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS drafts (
  id TEXT PRIMARY KEY,
  source_hash TEXT NOT NULL UNIQUE,
  content TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'publishing', 'published')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  github_url TEXT
);
