package store

import (
	"context"
	"strconv"
)

// DefaultTitle is the app title until someone changes it.
const DefaultTitle = "Pocket Movement"

// Settings are the box-wide settings plus one person's preferences.
type Settings struct {
	// Box-wide (admins).
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`
	// Personal.
	Timezone     string `json:"timezone"`      // IANA; "" = the server's
	DistanceUnit string `json:"distance_unit"` // km | mi
	MapTiles     string `json:"map_tiles"`
	RouteColor   string `json:"route_color"` // speed | mode | plain
	// Detection.
	VisitRadius     float64 `json:"visit_radius"`      // metres a stay may wander
	VisitMinMinutes int     `json:"visit_min_minutes"` // shortest stay that counts
	TrackGapMinutes int     `json:"track_gap_minutes"` // a silence longer than this, while moving, ends a journey
	MaxAccuracy     float64 `json:"max_accuracy"`      // points less accurate than this (m) are left out of routes; 0 keeps all
	MaxSpeedKmh     float64 `json:"max_speed_kmh"`     // a jump faster than this is an outlier, unless it is a flight
	// Notifications through Pocket Core.
	NotifyStaleHours int  `json:"notify_stale_hours"` // no position for this long → event; 0 = never
	NotifyCountries  bool `json:"notify_countries"`
}

// DefaultSettings are what a new person starts with.
func DefaultSettings() Settings {
	return Settings{Title: DefaultTitle, DistanceUnit: "km", MapTiles: "osm", RouteColor: "speed",
		VisitRadius: 100, VisitMinMinutes: 5, TrackGapMinutes: 30, MaxAccuracy: 150, MaxSpeedKmh: 400,
		NotifyStaleHours: 12, NotifyCountries: true}
}

var globalKeys = map[string]bool{"title": true, "setup_complete": true}

// GetSettings loads the box-wide settings plus userID's personal ones, with
// defaults applied. userID 0 returns the box-wide settings and defaults.
func (s *Store) GetSettings(ctx context.Context, userID int64) (Settings, error) {
	st := DefaultSettings()
	kv := map[string]string{}
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil && globalKeys[k] {
			kv[k] = v
		}
	}
	rows.Close()
	if userID > 0 {
		rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
		if err != nil {
			return st, err
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err == nil && !globalKeys[k] {
				kv[k] = v
			}
		}
		rows.Close()
	}
	num := func(v string, dst *float64) {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			*dst = f
		}
	}
	integer := func(v string, dst *int) {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			*dst = n
		}
	}
	for k, v := range kv {
		switch k {
		case "title":
			if v != "" {
				st.Title = v
			}
		case "setup_complete":
			st.SetupComplete = v == "1"
		case "timezone":
			st.Timezone = v
		case "distance_unit":
			if v == "mi" {
				st.DistanceUnit = "mi"
			}
		case "map_tiles":
			if v != "" {
				st.MapTiles = v
			}
		case "route_color":
			if v != "" {
				st.RouteColor = v
			}
		case "visit_radius":
			num(v, &st.VisitRadius)
		case "visit_min_minutes":
			integer(v, &st.VisitMinMinutes)
		case "track_gap_minutes":
			integer(v, &st.TrackGapMinutes)
		case "max_accuracy":
			num(v, &st.MaxAccuracy)
		case "max_speed_kmh":
			num(v, &st.MaxSpeedKmh)
		case "notify_stale_hours":
			integer(v, &st.NotifyStaleHours)
		case "notify_countries":
			st.NotifyCountries = v != "0"
		}
	}
	return st, nil
}

func personalKV(st Settings) map[string]string {
	b := map[bool]string{true: "1", false: "0"}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return map[string]string{
		"timezone":           st.Timezone,
		"distance_unit":      st.DistanceUnit,
		"map_tiles":          st.MapTiles,
		"route_color":        st.RouteColor,
		"visit_radius":       f(st.VisitRadius),
		"visit_min_minutes":  strconv.Itoa(st.VisitMinMinutes),
		"track_gap_minutes":  strconv.Itoa(st.TrackGapMinutes),
		"max_accuracy":       f(st.MaxAccuracy),
		"max_speed_kmh":      f(st.MaxSpeedKmh),
		"notify_stale_hours": strconv.Itoa(st.NotifyStaleHours),
		"notify_countries":   b[st.NotifyCountries],
	}
}

// SaveUserSettings persists userID's personal settings.
func (s *Store) SaveUserSettings(ctx context.Context, userID int64, st Settings) error {
	if userID <= 0 {
		return ErrNotFound
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range personalKV(st) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?) ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveGlobalSettings persists the box-wide settings.
func (s *Store) SaveGlobalSettings(ctx context.Context, st Settings) error {
	b := map[bool]string{true: "1", false: "0"}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{"title": st.Title, "setup_complete": b[st.SetupComplete]} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetState reads one small per-person state value.
func (s *Store) GetState(ctx context.Context, userID int64, key string) string {
	var v string
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM user_state WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	return v
}

// SetState writes one small per-person state value.
func (s *Store) SetState(ctx context.Context, userID int64, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_state (user_id, key, value) VALUES (?, ?, ?) ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, key, value)
	return err
}

// ---- generic cache ----

// CacheGet returns a cached value and when it was fetched.
func (s *Store) CacheGet(ctx context.Context, key string) (string, string, bool) {
	var v, at string
	if err := s.DB.QueryRowContext(ctx, `SELECT value, fetched_at FROM cache WHERE key = ?`, key).Scan(&v, &at); err != nil {
		return "", "", false
	}
	return v, at, true
}

// CachePut stores a value.
func (s *Store) CachePut(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO cache (key, value, fetched_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, fetched_at = excluded.fetched_at`, key, value, Now())
	return err
}

// CacheDelete removes every key with the prefix.
func (s *Store) CacheDelete(ctx context.Context, prefix string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM cache WHERE key LIKE ? || '%'`, prefix)
	return err
}

// BoxValue reads one raw box-wide value ("" when unset).
func (s *Store) BoxValue(ctx context.Context, key string) string {
	var v string
	_ = s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v
}

// SetBoxValue stores one raw box-wide value.
func (s *Store) SetBoxValue(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
