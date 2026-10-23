-- A standing link to a Dawarich server: new points there are pulled in
-- every few minutes, so a phone can keep reporting to Dawarich.
CREATE TABLE dawarich_links (
  user_id      INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  url          TEXT NOT NULL,
  api_key      TEXT NOT NULL,
  account      TEXT NOT NULL DEFAULT '',   -- the Dawarich account's email
  enabled      INTEGER NOT NULL DEFAULT 1,
  cursor_ts    INTEGER NOT NULL DEFAULT 0, -- newest point timestamp pulled
  last_sync_at INTEGER NOT NULL DEFAULT 0,
  last_added   INTEGER NOT NULL DEFAULT 0,
  points_added INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
