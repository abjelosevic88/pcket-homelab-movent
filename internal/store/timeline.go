package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ---- visits ----

const visitCols = `v.id, v.user_id, v.place_id, v.start_ts, v.end_ts, v.lat, v.lng, v.radius, v.points, v.status, v.name, v.notes, v.manual, v.created_at, v.updated_at`

func scanVisitPlace(row interface{ Scan(...any) error }) (*Visit, error) {
	var v Visit
	var manual int
	var pid sql.NullInt64
	var pname, pkind, pcat, pcolor, paddr, pcity, pcountry sql.NullString
	var plat, plng, pradius sql.NullFloat64
	var phidden, prenamed sql.NullInt64
	if err := row.Scan(&v.ID, &v.UserID, &v.PlaceID, &v.StartTS, &v.EndTS, &v.Lat, &v.Lng, &v.Radius, &v.Points, &v.Status, &v.Name, &v.Notes, &manual, &v.CreatedAt, &v.UpdatedAt,
		&pid, &pname, &pkind, &pcat, &pcolor, &plat, &plng, &pradius, &paddr, &pcity, &pcountry, &phidden, &prenamed); err != nil {
		return nil, notFound(err)
	}
	v.Manual = manual == 1
	if pid.Valid {
		v.Place = &Place{ID: pid.Int64, UserID: v.UserID, Name: pname.String, Kind: pkind.String, Category: pcat.String, Color: pcolor.String, Lat: plat.Float64, Lng: plng.Float64,
			Radius: pradius.Float64, Address: paddr.String, City: pcity.String, Country: pcountry.String, Hidden: phidden.Int64 == 1, Renamed: prenamed.Int64 == 1}
	}
	return &v, nil
}

const visitJoin = `SELECT ` + visitCols + `, p.id, p.name, p.kind, p.category, p.color, p.lat, p.lng, p.radius, p.address, p.city, p.country, p.hidden, p.renamed
	FROM visits v LEFT JOIN places p ON p.id = v.place_id`

// VisitQuery filters visits.
type VisitQuery struct {
	From, To  int64 // overlapping [From, To]; 0 = open
	Status    string
	PlaceID   int64
	Desc      bool
	Limit     int
	NoDecline bool
}

// Visits lists a person's visits.
func (s *Store) Visits(ctx context.Context, userID int64, q VisitQuery) ([]Visit, error) {
	w := []string{"v.user_id = ?"}
	args := []any{userID}
	if q.From > 0 {
		w = append(w, "v.end_ts >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		w = append(w, "v.start_ts <= ?")
		args = append(args, q.To)
	}
	if q.Status != "" {
		w = append(w, "v.status = ?")
		args = append(args, q.Status)
	}
	if q.NoDecline {
		w = append(w, "v.status <> 'declined'")
	}
	if q.PlaceID > 0 {
		w = append(w, "v.place_id = ?")
		args = append(args, q.PlaceID)
	}
	sqlq := visitJoin + ` WHERE ` + strings.Join(w, " AND ") + ` ORDER BY v.start_ts`
	if q.Desc {
		sqlq += ` DESC`
	}
	if q.Limit > 0 {
		sqlq += ` LIMIT ` + strconv.Itoa(q.Limit)
	}
	rows, err := s.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Visit{}
	for rows.Next() {
		v, err := scanVisitPlace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetVisit returns one visit with its place.
func (s *Store) GetVisit(ctx context.Context, userID, id int64) (*Visit, error) {
	return scanVisitPlace(s.DB.QueryRowContext(ctx, visitJoin+` WHERE v.id = ? AND v.user_id = ?`, id, userID))
}

// CreateVisit inserts a visit.
func (s *Store) CreateVisit(ctx context.Context, v *Visit) error {
	return s.createVisit(ctx, s.DB, v)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) createVisit(ctx context.Context, db execer, v *Visit) error {
	now := time.Now().Unix()
	v.CreatedAt, v.UpdatedAt = now, now
	if v.Status == "" {
		v.Status = VisitSuggested
	}
	r, err := db.ExecContext(ctx, `INSERT INTO visits (user_id, place_id, start_ts, end_ts, lat, lng, radius, points, status, name, notes, manual, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, v.UserID, v.PlaceID, v.StartTS, v.EndTS, v.Lat, v.Lng, v.Radius, v.Points, v.Status, v.Name, v.Notes, boolInt(v.Manual), now, now)
	if err != nil {
		return err
	}
	v.ID, _ = r.LastInsertId()
	return nil
}

// UpdateVisit saves a visit.
func (s *Store) UpdateVisit(ctx context.Context, v *Visit) error {
	return s.updateVisit(ctx, s.DB, v)
}

func (s *Store) updateVisit(ctx context.Context, db execer, v *Visit) error {
	v.UpdatedAt = time.Now().Unix()
	_, err := db.ExecContext(ctx, `UPDATE visits SET place_id = ?, start_ts = ?, end_ts = ?, lat = ?, lng = ?, radius = ?, points = ?, status = ?, name = ?, notes = ?, manual = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		v.PlaceID, v.StartTS, v.EndTS, v.Lat, v.Lng, v.Radius, v.Points, v.Status, v.Name, v.Notes, boolInt(v.Manual), v.UpdatedAt, v.ID, v.UserID)
	return err
}

// DeleteVisit removes a visit.
func (s *Store) DeleteVisit(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM visits WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// ---- tracks ----

const trackCols = `id, user_id, start_ts, end_ts, distance, points, avg_speed, max_speed, elevation_gain, elevation_loss, mode, mode_manual, start_lat, start_lng, end_lat, end_lng, polyline, countries, created_at`

func scanTrack(row interface{ Scan(...any) error }) (*Track, error) {
	var t Track
	var manual int
	if err := row.Scan(&t.ID, &t.UserID, &t.StartTS, &t.EndTS, &t.Distance, &t.Points, &t.AvgSpeed, &t.MaxSpeed, &t.ElevationGain, &t.ElevationLoss, &t.Mode, &manual,
		&t.StartLat, &t.StartLng, &t.EndLat, &t.EndLng, &t.Polyline, &t.Countries, &t.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	t.ModeManual = manual == 1
	return &t, nil
}

// Tracks lists journeys overlapping [from, to] (0 = open).
func (s *Store) Tracks(ctx context.Context, userID, from, to int64, withPolyline bool) ([]Track, error) {
	cols := trackCols
	if !withPolyline {
		cols = strings.Replace(cols, "polyline", "''", 1)
	}
	q := `SELECT ` + cols + ` FROM tracks WHERE user_id = ?`
	args := []any{userID}
	if from > 0 {
		q += ` AND end_ts >= ?`
		args = append(args, from)
	}
	if to > 0 {
		q += ` AND start_ts <= ?`
		args = append(args, to)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY start_ts`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Track{}
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// GetTrack returns one journey.
func (s *Store) GetTrack(ctx context.Context, userID, id int64) (*Track, error) {
	return scanTrack(s.DB.QueryRowContext(ctx, `SELECT `+trackCols+` FROM tracks WHERE id = ? AND user_id = ?`, id, userID))
}

// SetTrackMode corrects a journey's transport mode and remembers it across rebuilds.
func (s *Store) SetTrackMode(ctx context.Context, userID, id int64, mode string) error {
	t, err := s.GetTrack(ctx, userID, id)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE tracks SET mode = ?, mode_manual = 1 WHERE id = ?`, mode, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM track_overrides WHERE user_id = ? AND end_ts >= ? AND start_ts <= ?`, userID, t.StartTS, t.EndTS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO track_overrides (user_id, start_ts, end_ts, mode) VALUES (?, ?, ?, ?)`, userID, t.StartTS, t.EndTS, mode); err != nil {
		return err
	}
	return tx.Commit()
}

// TrackOverride is a mode set by hand.
type TrackOverride struct {
	StartTS, EndTS int64
	Mode           string
}

// TrackOverrides lists hand-set modes overlapping a range.
func (s *Store) TrackOverrides(ctx context.Context, userID, from, to int64) ([]TrackOverride, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT start_ts, end_ts, mode FROM track_overrides WHERE user_id = ? AND end_ts >= ? AND start_ts <= ?`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackOverride
	for rows.Next() {
		var o TrackOverride
		if err := rows.Scan(&o.StartTS, &o.EndTS, &o.Mode); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DerivedBounds widens [from, to] to cover any journey or detected visit that
// crosses its edges, so a rebuild never cuts one in half.
func (s *Store) DerivedBounds(ctx context.Context, userID, from, to int64) (int64, int64) {
	for i := 0; i < 4; i++ {
		var a, b sql.NullInt64
		_ = s.DB.QueryRowContext(ctx, `SELECT MIN(s), MAX(e) FROM (
			SELECT start_ts s, end_ts e FROM tracks WHERE user_id = ? AND end_ts >= ? AND start_ts <= ?
			UNION ALL SELECT start_ts, end_ts FROM visits WHERE user_id = ? AND manual = 0 AND end_ts >= ? AND start_ts <= ?)`,
			userID, from, to, userID, from, to).Scan(&a, &b)
		changed := false
		if a.Valid && a.Int64 < from {
			from, changed = a.Int64, true
		}
		if b.Valid && b.Int64 > to {
			to, changed = b.Int64, true
		}
		if !changed {
			break
		}
	}
	return from, to
}

// Rebuild is the new derived data for a window.
type Rebuild struct {
	From, To int64
	Tracks   []Track
	// Visits to insert (ID 0) or update (ID set); KeepIDs are existing visits
	// left untouched; every other non-manual visit in the window is deleted.
	Visits  []Visit
	KeepIDs []int64
	Flags   map[int64]int
}

// ApplyRebuild swaps a window's journeys, detected visits and day summaries
// in one transaction.
func (s *Store) ApplyRebuild(ctx context.Context, userID int64, rb Rebuild) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracks WHERE user_id = ? AND start_ts >= ? AND start_ts <= ?`, userID, rb.From, rb.To); err != nil {
		return err
	}
	keep := map[int64]bool{}
	for _, id := range rb.KeepIDs {
		keep[id] = true
	}
	for _, v := range rb.Visits {
		if v.ID > 0 {
			keep[v.ID] = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM visits WHERE user_id = ? AND manual = 0 AND start_ts >= ? AND start_ts <= ?`, userID, rb.From, rb.To)
	if err != nil {
		return err
	}
	var drop []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil && !keep[id] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	for _, id := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM visits WHERE id = ?`, id); err != nil {
			return err
		}
	}
	for i := range rb.Visits {
		v := &rb.Visits[i]
		v.UserID = userID
		if v.ID > 0 {
			if err := s.updateVisit(ctx, tx, v); err != nil {
				return err
			}
		} else if err := s.createVisit(ctx, tx, v); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	for _, t := range rb.Tracks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tracks (user_id, start_ts, end_ts, distance, points, avg_speed, max_speed, elevation_gain, elevation_loss, mode, mode_manual, start_lat, start_lng, end_lat, end_lng, polyline, countries, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, userID, t.StartTS, t.EndTS, t.Distance, t.Points, t.AvgSpeed, t.MaxSpeed, t.ElevationGain, t.ElevationLoss,
			t.Mode, boolInt(t.ModeManual), t.StartLat, t.StartLng, t.EndLat, t.EndLng, t.Polyline, t.Countries, now); err != nil {
			return err
		}
	}
	if len(rb.Flags) > 0 {
		stmt, err := tx.PrepareContext(ctx, `UPDATE points SET flags = ? WHERE id = ? AND user_id = ?`)
		if err != nil {
			return err
		}
		for id, f := range rb.Flags {
			if _, err := stmt.ExecContext(ctx, f, id, userID); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()
	}
	return tx.Commit()
}

// ---- days ----

func upsertDay(ctx context.Context, db execer, userID int64, d Day) error {
	if d.Points == 0 && d.Visits == 0 {
		_, err := db.ExecContext(ctx, `DELETE FROM days WHERE user_id = ? AND date = ?`, userID, d.Date)
		return err
	}
	modes, _ := json.Marshal(d.Modes)
	places := make([]string, 0, len(d.Places))
	for _, id := range d.Places {
		places = append(places, strconv.FormatInt(id, 10))
	}
	_, err := db.ExecContext(ctx, `INSERT INTO days (user_id, date, points, distance, moving_s, first_ts, last_ts, countries, modes, visits, places, max_from_home)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(user_id, date) DO UPDATE SET points = excluded.points, distance = excluded.distance, moving_s = excluded.moving_s,
		first_ts = excluded.first_ts, last_ts = excluded.last_ts, countries = excluded.countries, modes = excluded.modes, visits = excluded.visits, places = excluded.places, max_from_home = excluded.max_from_home`,
		userID, d.Date, d.Points, d.Distance, d.MovingS, d.FirstTS, d.LastTS, strings.Join(d.Countries, ","), string(modes), d.Visits, strings.Join(places, ","), d.MaxFromHome)
	return err
}

// SaveDays writes day summaries (a day without data is removed).
func (s *Store) SaveDays(ctx context.Context, userID int64, days []Day) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, d := range days {
		if err := upsertDay(ctx, tx, userID, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Days lists day summaries between two dates (inclusive, YYYY-MM-DD; "" = open).
func (s *Store) Days(ctx context.Context, userID int64, from, to string) ([]Day, error) {
	q := `SELECT date, points, distance, moving_s, first_ts, last_ts, countries, modes, visits, places, max_from_home FROM days WHERE user_id = ?`
	args := []any{userID}
	if from != "" {
		q += ` AND date >= ?`
		args = append(args, from)
	}
	if to != "" {
		q += ` AND date <= ?`
		args = append(args, to)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY date`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Day{}
	for rows.Next() {
		var d Day
		var countries, modes, places string
		if err := rows.Scan(&d.Date, &d.Points, &d.Distance, &d.MovingS, &d.FirstTS, &d.LastTS, &countries, &modes, &d.Visits, &places, &d.MaxFromHome); err != nil {
			return nil, err
		}
		d.Countries = splitList(countries)
		d.Modes = map[string]float64{}
		_ = json.Unmarshal([]byte(modes), &d.Modes)
		for _, p := range splitList(places) {
			if id, err := strconv.ParseInt(p, 10, 64); err == nil {
				d.Places = append(d.Places, id)
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DayBounds returns the first and last summarised dates.
func (s *Store) DayBounds(ctx context.Context, userID int64) (string, string) {
	var a, b sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT MIN(date), MAX(date) FROM days WHERE user_id = ?`, userID).Scan(&a, &b)
	return a.String, b.String
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}
