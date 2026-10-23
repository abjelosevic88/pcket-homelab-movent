package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// InsertResult says what a batch did.
type InsertResult struct {
	Received int    `json:"received"`
	Added    int    `json:"added"`
	MinTS    int64  `json:"-"`
	MaxTS    int64  `json:"-"`
	Last     *Point `json:"-"` // newest added point
}

// InsertPoints stores a batch in one transaction, skipping exact duplicates
// (same person, instant and position), and marks the covered time range for
// rebuilding. Points must carry UserID; Country is filled by the caller.
func (s *Store) InsertPoints(ctx context.Context, userID int64, pts []Point) (InsertResult, error) {
	res := InsertResult{Received: len(pts)}
	if len(pts) == 0 {
		return res, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO points (user_id, device_id, import_id, ts, lat, lng, altitude, accuracy, vertical_accuracy, speed, course, battery, battery_state, motion, connection, ssid, tracker_id, source, country, flags, extra)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return res, err
	}
	defer stmt.Close()
	for i := range pts {
		p := &pts[i]
		r, err := stmt.ExecContext(ctx, userID, p.DeviceID, p.ImportID, p.TS, p.Lat, p.Lng, p.Altitude, p.Accuracy, p.VerticalAccuracy, p.Speed, p.Course, p.Battery,
			nullStr(p.BatteryState), nullStr(p.Motion), nullStr(p.Connection), nullStr(p.SSID), nullStr(p.TrackerID), p.Source, p.Country, p.Flags, nullStr(p.Extra))
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Added++
			if res.MinTS == 0 || p.TS < res.MinTS {
				res.MinTS = p.TS
			}
			if p.TS > res.MaxTS {
				res.MaxTS = p.TS
				res.Last = p
			}
		}
	}
	if res.Added > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO dirty (user_id, from_ts, to_ts, queued_at) VALUES (?, ?, ?, ?)`, userID, res.MinTS, res.MaxTS, time.Now().Unix()); err != nil {
			return res, err
		}
	}
	return res, tx.Commit()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const pointCols = `id, user_id, device_id, import_id, ts, lat, lng, altitude, accuracy, vertical_accuracy, speed, course, battery, battery_state, motion, connection, ssid, tracker_id, source, country, flags, extra`

func scanPoint(row interface{ Scan(...any) error }) (Point, error) {
	var p Point
	var bs, mo, co, ss, ti, ex sql.NullString
	err := row.Scan(&p.ID, &p.UserID, &p.DeviceID, &p.ImportID, &p.TS, &p.Lat, &p.Lng, &p.Altitude, &p.Accuracy, &p.VerticalAccuracy, &p.Speed, &p.Course, &p.Battery,
		&bs, &mo, &co, &ss, &ti, &p.Source, &p.Country, &p.Flags, &ex)
	p.BatteryState, p.Motion, p.Connection, p.SSID, p.TrackerID, p.Extra = bs.String, mo.String, co.String, ss.String, ti.String, ex.String
	return p, err
}

// PointQuery selects points.
type PointQuery struct {
	From, To        int64 // unix seconds, inclusive; 0 = open
	Desc            bool
	Limit, Offset   int
	IncludeOutliers bool
	DeviceID        int64
}

func (q PointQuery) where(userID int64) (string, []any) {
	w := []string{"user_id = ?"}
	args := []any{userID}
	if q.From > 0 {
		w = append(w, "ts >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		w = append(w, "ts <= ?")
		args = append(args, q.To)
	}
	if !q.IncludeOutliers {
		w = append(w, "(flags & 1) = 0")
	}
	if q.DeviceID > 0 {
		w = append(w, "device_id = ?")
		args = append(args, q.DeviceID)
	}
	return strings.Join(w, " AND "), args
}

// Points lists points.
func (s *Store) Points(ctx context.Context, userID int64, q PointQuery) ([]Point, error) {
	w, args := q.where(userID)
	sqlq := `SELECT ` + pointCols + ` FROM points WHERE ` + w + ` ORDER BY ts`
	if q.Desc {
		sqlq += ` DESC`
	}
	sqlq += `, id`
	if q.Limit > 0 {
		sqlq += fmt.Sprintf(` LIMIT %d OFFSET %d`, q.Limit, q.Offset)
	}
	rows, err := s.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Point{}
	for rows.Next() {
		p, err := scanPoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EachPoint streams points in time order without holding them all.
func (s *Store) EachPoint(ctx context.Context, userID int64, q PointQuery, fn func(Point) error) error {
	w, args := q.where(userID)
	rows, err := s.DB.QueryContext(ctx, `SELECT `+pointCols+` FROM points WHERE `+w+` ORDER BY ts, id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPoint(rows)
		if err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CountPoints counts points matching a query.
func (s *Store) CountPoints(ctx context.Context, userID int64, q PointQuery) (int64, error) {
	w, args := q.where(userID)
	var n int64
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM points WHERE `+w, args...).Scan(&n)
	return n, err
}

// SlimPoint is what maps need.
type SlimPoint struct {
	TS    int64
	Lat   float64
	Lng   float64
	Speed *float64
	Acc   *float64
	Alt   *float64
	Flags int
}

// SlimPoints returns the bare positions in a range, oldest first.
func (s *Store) SlimPoints(ctx context.Context, userID int64, from, to int64, includeOutliers bool) ([]SlimPoint, error) {
	q := `SELECT ts, lat, lng, speed, accuracy, altitude, flags FROM points WHERE user_id = ? AND ts >= ? AND ts <= ?`
	if !includeOutliers {
		q += ` AND (flags & 1) = 0`
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY ts`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SlimPoint, 0, 1024)
	for rows.Next() {
		var p SlimPoint
		if err := rows.Scan(&p.TS, &p.Lat, &p.Lng, &p.Speed, &p.Acc, &p.Alt, &p.Flags); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LatestPoint returns the newest point of a person (outliers excluded).
func (s *Store) LatestPoint(ctx context.Context, userID int64) (*Point, error) {
	p, err := scanPoint(s.DB.QueryRowContext(ctx, `SELECT `+pointCols+` FROM points WHERE user_id = ? AND (flags & 1) = 0 ORDER BY ts DESC LIMIT 1`, userID))
	if err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// GetPoint returns one of a person's points.
func (s *Store) GetPoint(ctx context.Context, userID, id int64) (*Point, error) {
	p, err := scanPoint(s.DB.QueryRowContext(ctx, `SELECT `+pointCols+` FROM points WHERE user_id = ? AND id = ?`, userID, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// PointBounds returns the first and last timestamps and the count.
func (s *Store) PointBounds(ctx context.Context, userID int64) (first, last, count int64) {
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(ts),0), COALESCE(MAX(ts),0), COUNT(1) FROM points WHERE user_id = ?`, userID).Scan(&first, &last, &count)
	return
}

// DeletePoints removes points by id and marks their range for rebuilding.
func (s *Store) DeletePoints(ctx context.Context, userID int64, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{userID}
	for _, id := range ids {
		args = append(args, id)
	}
	var minTS, maxTS sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(ts), MAX(ts) FROM points WHERE user_id = ? AND id IN (`+ph+`)`, args...).Scan(&minTS, &maxTS); err != nil {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM points WHERE user_id = ? AND id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	if n > 0 && minTS.Valid {
		if _, err := tx.ExecContext(ctx, `INSERT INTO dirty (user_id, from_ts, to_ts, queued_at) VALUES (?, ?, ?, ?)`, userID, minTS.Int64, maxTS.Int64, time.Now().Unix()); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// DeletePointsRange removes every point in [from, to] (optionally of one import).
func (s *Store) DeletePointsRange(ctx context.Context, userID, from, to, importID int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := `DELETE FROM points WHERE user_id = ? AND ts >= ? AND ts <= ?`
	args := []any{userID, from, to}
	if importID > 0 {
		q += ` AND import_id = ?`
		args = append(args, importID)
	}
	r, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO dirty (user_id, from_ts, to_ts, queued_at) VALUES (?, ?, ?, ?)`, userID, from, to, time.Now().Unix()); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// SetPointFlags updates flags for many points (outlier marking).
func (s *Store) SetPointFlags(ctx context.Context, userID int64, flags map[int64]int) error {
	if len(flags) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE points SET flags = ? WHERE id = ? AND user_id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for id, f := range flags {
		if _, err := stmt.ExecContext(ctx, f, id, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkDirty queues a range for rebuilding.
func (s *Store) MarkDirty(ctx context.Context, userID, from, to int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO dirty (user_id, from_ts, to_ts, queued_at) VALUES (?, ?, ?, ?)`, userID, from, to, time.Now().Unix())
	return err
}

// DirtyRange is a merged range waiting for a rebuild.
type DirtyRange struct {
	UserID   int64
	From, To int64
}

// TakeDirty returns one person's waiting ranges merged into one and clears
// them; ok is false when nothing waits. Ranges queued in the last `settle`
// are left alone so a phone uploading every few seconds is not rebuilt each time.
func (s *Store) TakeDirty(ctx context.Context, settle time.Duration) (DirtyRange, bool, error) {
	var d DirtyRange
	cut := time.Now().Add(-settle).Unix()
	err := s.DB.QueryRowContext(ctx, `SELECT user_id, MIN(from_ts), MAX(to_ts) FROM dirty WHERE user_id = (SELECT user_id FROM dirty GROUP BY user_id HAVING MAX(queued_at) <= ? ORDER BY MIN(queued_at) LIMIT 1) GROUP BY user_id`, cut).Scan(&d.UserID, &d.From, &d.To)
	if err == sql.ErrNoRows {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM dirty WHERE user_id = ? AND queued_at <= ?`, d.UserID, cut)
	return d, true, err
}

// PendingDirty reports whether a person has ranges waiting.
func (s *Store) PendingDirty(ctx context.Context, userID int64) bool {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM dirty WHERE user_id = ?`, userID).Scan(&n)
	return n > 0
}

// MonthCount is how many points fall in one month.
type MonthCount struct {
	Year  int   `json:"year"`
	Month int   `json:"month"`
	Count int64 `json:"count"`
}

// TrackedMonths lists the months with data from the day summaries.
func (s *Store) TrackedMonths(ctx context.Context, userID int64) ([]MonthCount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT CAST(substr(date,1,4) AS INTEGER), CAST(substr(date,6,2) AS INTEGER), SUM(points) FROM days WHERE user_id = ? AND points > 0 GROUP BY 1, 2 ORDER BY 1, 2`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonthCount{}
	for rows.Next() {
		var m MonthCount
		if err := rows.Scan(&m.Year, &m.Month, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UsersWithPoints lists every person that has at least one point.
func (s *Store) UsersWithPoints(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users u WHERE EXISTS (SELECT 1 FROM points p WHERE p.user_id = u.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
