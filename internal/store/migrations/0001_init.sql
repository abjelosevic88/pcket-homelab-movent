CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'admin',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

CREATE TABLE cache (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  fetched_at TEXT NOT NULL
);

-- A device is anything that sends positions: a phone app (Dawarich,
-- OwnTracks, Overland, GPSLogger, Traccar…), a script, or an integration
-- that only reads (Pocket Travel, Home Assistant). Each has its own key so
-- one can be revoked without re-pairing the others.
-- scope: track = send points (and what tracker apps need to sign in),
--        read  = read history (integrations), full = both.
CREATE TABLE devices (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'other',
  scope         TEXT NOT NULL DEFAULT 'full',
  api_key       TEXT NOT NULL UNIQUE,
  tracker_id    TEXT NOT NULL DEFAULT '',      -- OwnTracks tid / Overland device_id / Traccar id seen last
  last_seen_at  INTEGER NOT NULL DEFAULT 0,    -- unix seconds, any request
  last_point_at INTEGER NOT NULL DEFAULT 0,    -- unix seconds, newest point timestamp
  last_lat      REAL,
  last_lng      REAL,
  last_battery  INTEGER,
  last_battery_state TEXT NOT NULL DEFAULT '',
  last_ip       TEXT NOT NULL DEFAULT '',
  user_agent    TEXT NOT NULL DEFAULT '',
  points        INTEGER NOT NULL DEFAULT 0,
  revoked       INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);
CREATE INDEX devices_user ON devices(user_id);

-- Points: the raw history. ts is unix seconds (UTC). Coordinates are WGS84.
-- Optional fields stay NULL when the sender did not report them. flags bit 1
-- = outlier (kept, left out of routes and stats). One row per person,
-- instant and position: re-sending or re-importing a point is a no-op.
CREATE TABLE points (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_id     INTEGER,
  import_id     INTEGER,
  ts            INTEGER NOT NULL,
  lat           REAL NOT NULL,
  lng           REAL NOT NULL,
  altitude      REAL,
  accuracy      REAL,
  vertical_accuracy REAL,
  speed         REAL,            -- m/s
  course        REAL,            -- degrees
  battery       INTEGER,         -- 0..100
  battery_state TEXT,            -- unplugged | charging | full | unknown
  motion        TEXT,            -- e.g. "driving", "walking,stationary"
  connection    TEXT,            -- w (wifi) | m (mobile) | o (offline)
  ssid          TEXT,
  tracker_id    TEXT,
  source        TEXT NOT NULL DEFAULT '',  -- owntracks | overland | dawarich | gpslogger | osmand | api | import
  country       TEXT NOT NULL DEFAULT '',  -- ISO alpha-2 from the embedded borders
  flags         INTEGER NOT NULL DEFAULT 0,
  extra         TEXT                       -- small JSON with anything else worth keeping
);
CREATE UNIQUE INDEX points_unique ON points(user_id, ts, lat, lng);
CREATE INDEX points_user_ts ON points(user_id, ts);
CREATE INDEX points_import ON points(import_id) WHERE import_id IS NOT NULL;

-- Ranges whose derived data (journeys, visits, day summaries) must be rebuilt.
CREATE TABLE dirty (
  user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  from_ts   INTEGER NOT NULL,
  to_ts     INTEGER NOT NULL,
  queued_at INTEGER NOT NULL
);
CREATE INDEX dirty_user ON dirty(user_id);

-- Places: somewhere you stop. Saved places (home, work, the gym) are made by
-- you with a name and radius and win over everything detected; discovered
-- places are created by visit detection and named by reverse geocoding until
-- you rename them. A place can be hidden from the map and reports.
CREATE TABLE places (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'discovered',  -- saved | discovered
  category    TEXT NOT NULL DEFAULT '',            -- home | work | food | shop | … (free)
  color       TEXT NOT NULL DEFAULT '',
  lat         REAL NOT NULL,
  lng         REAL NOT NULL,
  radius      REAL NOT NULL DEFAULT 75,
  address     TEXT NOT NULL DEFAULT '',
  city        TEXT NOT NULL DEFAULT '',
  country     TEXT NOT NULL DEFAULT '',            -- ISO alpha-2
  geocoded    INTEGER NOT NULL DEFAULT 0,          -- 0 pending, 1 done, 2 failed
  renamed     INTEGER NOT NULL DEFAULT 0,          -- the name is yours; geocoding never overwrites it
  hidden      INTEGER NOT NULL DEFAULT 0,
  notes       TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX places_user_geo ON places(user_id, lat, lng);

-- Visits: a stay at one spot. Detection writes suggested visits; you confirm,
-- decline or rename them, and that survives every rebuild of the range.
CREATE TABLE visits (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  place_id    INTEGER REFERENCES places(id) ON DELETE SET NULL,
  start_ts    INTEGER NOT NULL,
  end_ts      INTEGER NOT NULL,
  lat         REAL NOT NULL,
  lng         REAL NOT NULL,
  radius      REAL NOT NULL DEFAULT 0,
  points      INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'suggested',   -- suggested | confirmed | declined
  name        TEXT NOT NULL DEFAULT '',            -- your name for this one visit, else the place's
  notes       TEXT NOT NULL DEFAULT '',
  manual      INTEGER NOT NULL DEFAULT 0,          -- added or edited by hand: rebuilds keep it as is
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX visits_user_time ON visits(user_id, start_ts);
CREATE INDEX visits_place ON visits(place_id);

-- Journeys: movement between two stays, with a transport mode guessed from
-- speed (walking, running, cycling, driving, train, flying) you can correct.
CREATE TABLE tracks (
  id             INTEGER PRIMARY KEY,
  user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  start_ts       INTEGER NOT NULL,
  end_ts         INTEGER NOT NULL,
  distance       REAL NOT NULL DEFAULT 0,          -- metres
  points         INTEGER NOT NULL DEFAULT 0,
  avg_speed      REAL NOT NULL DEFAULT 0,          -- m/s
  max_speed      REAL NOT NULL DEFAULT 0,
  elevation_gain REAL NOT NULL DEFAULT 0,
  elevation_loss REAL NOT NULL DEFAULT 0,
  mode           TEXT NOT NULL DEFAULT 'unknown',
  mode_manual    INTEGER NOT NULL DEFAULT 0,
  start_lat      REAL NOT NULL,
  start_lng      REAL NOT NULL,
  end_lat        REAL NOT NULL,
  end_lng        REAL NOT NULL,
  polyline       TEXT NOT NULL DEFAULT '',         -- simplified, encoded
  countries      TEXT NOT NULL DEFAULT '',         -- comma separated ISO codes
  created_at     INTEGER NOT NULL
);
CREATE INDEX tracks_user_time ON tracks(user_id, start_ts);

-- A journey's mode set by hand, kept across rebuilds (matched by overlap).
CREATE TABLE track_overrides (
  user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  start_ts INTEGER NOT NULL,
  end_ts   INTEGER NOT NULL,
  mode     TEXT NOT NULL,
  PRIMARY KEY (user_id, start_ts)
);

-- One row per person and local day: what the reports and the calendar read.
CREATE TABLE days (
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  date          TEXT NOT NULL,                     -- YYYY-MM-DD in the person's time zone
  points        INTEGER NOT NULL DEFAULT 0,
  distance      REAL NOT NULL DEFAULT 0,           -- metres
  moving_s      INTEGER NOT NULL DEFAULT 0,
  first_ts      INTEGER NOT NULL DEFAULT 0,
  last_ts       INTEGER NOT NULL DEFAULT 0,
  countries     TEXT NOT NULL DEFAULT '',          -- comma separated, in order of arrival
  modes         TEXT NOT NULL DEFAULT '{}',        -- JSON {mode: metres}
  visits        INTEGER NOT NULL DEFAULT 0,
  places        TEXT NOT NULL DEFAULT '',          -- comma separated place ids
  max_from_home REAL NOT NULL DEFAULT 0,           -- metres from the home place
  PRIMARY KEY (user_id, date)
);

-- Imports run in the background; the file stays until the import finishes.
CREATE TABLE imports (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  format       TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'queued',     -- queued | running | done | failed
  file_path    TEXT NOT NULL DEFAULT '',
  size         INTEGER NOT NULL DEFAULT 0,
  source_url   TEXT NOT NULL DEFAULT '',           -- pulled from another Dawarich server
  source_key   TEXT NOT NULL DEFAULT '',
  points_read  INTEGER NOT NULL DEFAULT 0,
  points_added INTEGER NOT NULL DEFAULT 0,
  visits_added INTEGER NOT NULL DEFAULT 0,
  first_ts     INTEGER NOT NULL DEFAULT 0,
  last_ts      INTEGER NOT NULL DEFAULT 0,
  error        TEXT NOT NULL DEFAULT '',
  progress     REAL NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  finished_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX imports_user ON imports(user_id);

-- Sharing: a person lets another account on this box see where they are
-- (live) or also their history; a link shares the live position with anyone
-- holding it until it expires.
CREATE TABLE shares (
  id           INTEGER PRIMARY KEY,
  owner_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  viewer_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
  token        TEXT UNIQUE,
  name         TEXT NOT NULL DEFAULT '',
  level        TEXT NOT NULL DEFAULT 'live',       -- live | history
  precision_m  INTEGER NOT NULL DEFAULT 0,         -- 0 exact, else rounded to this many metres
  expires_at   INTEGER NOT NULL DEFAULT 0,         -- 0 never
  created_at   INTEGER NOT NULL
);
CREATE INDEX shares_owner ON shares(owner_id);
CREATE INDEX shares_viewer ON shares(viewer_id);

-- Privacy zones: points inside never reach shared views or exports, and can
-- be dropped on arrival.
CREATE TABLE privacy_zones (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name        TEXT NOT NULL DEFAULT '',
  lat         REAL NOT NULL,
  lng         REAL NOT NULL,
  radius      REAL NOT NULL DEFAULT 200,
  drop_points INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);

-- Small per-person state (last notified country, tracking stopped…) so
-- events go out once.
CREATE TABLE user_state (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);
