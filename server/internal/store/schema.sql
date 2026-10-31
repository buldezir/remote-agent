CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
  id   TEXT PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  data       TEXT NOT NULL,
  item_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS turns (
  id         TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  n          INTEGER NOT NULL,
  data       TEXT NOT NULL,
  UNIQUE (session_id, n)
);

-- Per-stream seq counters.
CREATE TABLE IF NOT EXISTS streams (
  stream   TEXT PRIMARY KEY,
  last_seq INTEGER NOT NULL
);

-- Change feed: latest event per (stream, entity).
CREATE TABLE IF NOT EXISTS changes (
  stream TEXT NOT NULL,
  entity TEXT NOT NULL,
  seq    INTEGER NOT NULL,
  type   TEXT NOT NULL,
  data   TEXT NOT NULL,
  PRIMARY KEY (stream, entity)
);
CREATE UNIQUE INDEX IF NOT EXISTS changes_seq ON changes (stream, seq);
CREATE INDEX IF NOT EXISTS changes_type ON changes (type);

CREATE TABLE IF NOT EXISTS receipts (
  command_id TEXT PRIMARY KEY,
  result     TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER
);

CREATE TABLE IF NOT EXISTS pairing_codes (
  code_hash  TEXT PRIMARY KEY,
  expires_at INTEGER NOT NULL
);

-- Search (session.search): a row per item with searchable text (model.SearchText),
-- and one per session for its title and branch, indexed by FTS5. The triggers
-- keep the index in step with the rows.
CREATE TABLE IF NOT EXISTS search_docs (
  id         INTEGER PRIMARY KEY,
  key        TEXT NOT NULL UNIQUE,  -- the item's id, or "session:<id>"
  session_id TEXT NOT NULL,
  item_id    TEXT,                  -- NULL: the session's title and branch
  kind       TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS search_docs_session ON search_docs (session_id);
CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(
  title, body, content='search_docs', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER IF NOT EXISTS search_docs_ai AFTER INSERT ON search_docs BEGIN
  INSERT INTO search_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER IF NOT EXISTS search_docs_ad AFTER DELETE ON search_docs BEGIN
  INSERT INTO search_fts(search_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER IF NOT EXISTS search_docs_au AFTER UPDATE OF title, body ON search_docs BEGIN
  INSERT INTO search_fts(search_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO search_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
