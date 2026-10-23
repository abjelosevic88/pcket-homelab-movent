package store

import (
	"context"
	"database/sql"
	"time"
)

const importCols = `id, user_id, name, format, status, file_path, size, source_url, source_key, points_read, points_added, visits_added, first_ts, last_ts, error, progress, created_at, finished_at`

func scanImport(row interface{ Scan(...any) error }) (*Import, error) {
	var i Import
	if err := row.Scan(&i.ID, &i.UserID, &i.Name, &i.Format, &i.Status, &i.FilePath, &i.Size, &i.SourceURL, &i.SourceKey, &i.PointsRead, &i.PointsAdded, &i.VisitsAdded,
		&i.FirstTS, &i.LastTS, &i.Error, &i.Progress, &i.CreatedAt, &i.FinishedAt); err != nil {
		return nil, notFound(err)
	}
	return &i, nil
}

// CreateImport queues an import.
func (s *Store) CreateImport(ctx context.Context, i *Import) error {
	i.CreatedAt = time.Now().Unix()
	i.Status = "queued"
	r, err := s.DB.ExecContext(ctx, `INSERT INTO imports (user_id, name, format, status, file_path, size, source_url, source_key, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.UserID, i.Name, i.Format, i.Status, i.FilePath, i.Size, i.SourceURL, i.SourceKey, i.CreatedAt)
	if err != nil {
		return err
	}
	i.ID, _ = r.LastInsertId()
	return nil
}

// ListImports lists a person's imports, newest first.
func (s *Store) ListImports(ctx context.Context, userID int64) ([]Import, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+importCols+` FROM imports WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Import{}
	for rows.Next() {
		i, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// GetImport returns one import.
func (s *Store) GetImport(ctx context.Context, userID, id int64) (*Import, error) {
	return scanImport(s.DB.QueryRowContext(ctx, `SELECT `+importCols+` FROM imports WHERE id = ? AND user_id = ?`, id, userID))
}

// NextImport claims the oldest queued import.
func (s *Store) NextImport(ctx context.Context) (*Import, error) {
	i, err := scanImport(s.DB.QueryRowContext(ctx, `SELECT `+importCols+` FROM imports WHERE status = 'queued' ORDER BY id LIMIT 1`))
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE imports SET status = 'running' WHERE id = ?`, i.ID)
	i.Status = "running"
	return i, err
}

// RequeueRunning puts imports a restart interrupted back in the queue.
func (s *Store) RequeueRunning(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE imports SET status = 'queued' WHERE status = 'running'`)
	return err
}

// SaveImportProgress records counters while an import runs.
func (s *Store) SaveImportProgress(ctx context.Context, i *Import) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE imports SET format = ?, points_read = ?, points_added = ?, visits_added = ?, first_ts = ?, last_ts = ?, progress = ? WHERE id = ?`,
		i.Format, i.PointsRead, i.PointsAdded, i.VisitsAdded, i.FirstTS, i.LastTS, i.Progress, i.ID)
	return err
}

// FinishImport marks an import done or failed.
func (s *Store) FinishImport(ctx context.Context, i *Import, err error) error {
	i.Status, i.Error, i.FinishedAt = "done", "", time.Now().Unix()
	if err != nil {
		i.Status, i.Error = "failed", err.Error()
	} else {
		i.Progress = 1
	}
	_, e := s.DB.ExecContext(ctx, `UPDATE imports SET format = ?, status = ?, error = ?, points_read = ?, points_added = ?, visits_added = ?, first_ts = ?, last_ts = ?, progress = ?, finished_at = ?, source_key = '' WHERE id = ?`,
		i.Format, i.Status, i.Error, i.PointsRead, i.PointsAdded, i.VisitsAdded, i.FirstTS, i.LastTS, i.Progress, i.FinishedAt, i.ID)
	return e
}

// DeleteImport removes the import record; withPoints also removes its points.
func (s *Store) DeleteImport(ctx context.Context, userID, id int64, withPoints bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if withPoints {
		var a, b sql.NullInt64
		_ = tx.QueryRowContext(ctx, `SELECT MIN(ts), MAX(ts) FROM points WHERE import_id = ? AND user_id = ?`, id, userID).Scan(&a, &b)
		if _, err := tx.ExecContext(ctx, `DELETE FROM points WHERE import_id = ? AND user_id = ?`, id, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM visits WHERE manual = 1 AND user_id = ? AND notes = ?`, userID, importNote(id)); err != nil {
			return err
		}
		if a.Valid {
			if _, err := tx.ExecContext(ctx, `INSERT INTO dirty (user_id, from_ts, to_ts, queued_at) VALUES (?, ?, ?, ?)`, userID, a.Int64, b.Int64, time.Now().Unix()); err != nil {
				return err
			}
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE points SET import_id = NULL WHERE import_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM imports WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func importNote(id int64) string { return "import:" + itoa(int(id)) }

// ImportNote tags visits an import brought (Google's place visits) so
// deleting the import can take them away again.
func ImportNote(id int64) string { return importNote(id) }

// ---- shares ----

const shareCols = `s.id, s.owner_id, s.viewer_id, COALESCE(s.token,''), s.name, s.level, s.precision_m, s.expires_at, s.created_at,
	o.name, o.email, COALESCE(v.name,''), COALESCE(v.email,'')`

func scanShare(row interface{ Scan(...any) error }) (*Share, error) {
	var sh Share
	if err := row.Scan(&sh.ID, &sh.OwnerID, &sh.ViewerID, &sh.Token, &sh.Name, &sh.Level, &sh.PrecisionM, &sh.ExpiresAt, &sh.CreatedAt,
		&sh.OwnerName, &sh.OwnerEmail, &sh.ViewerName, &sh.ViewerEmail); err != nil {
		return nil, notFound(err)
	}
	return &sh, nil
}

const shareJoin = `SELECT ` + shareCols + ` FROM shares s JOIN users o ON o.id = s.owner_id LEFT JOIN users v ON v.id = s.viewer_id`

// SharesByOwner lists what a person shares.
func (s *Store) SharesByOwner(ctx context.Context, ownerID int64) ([]Share, error) {
	return s.shares(ctx, shareJoin+` WHERE s.owner_id = ? ORDER BY s.id DESC`, ownerID)
}

// SharesWithViewer lists what others share with a person (live ones only).
func (s *Store) SharesWithViewer(ctx context.Context, viewerID int64) ([]Share, error) {
	return s.shares(ctx, shareJoin+` WHERE s.viewer_id = ? AND (s.expires_at = 0 OR s.expires_at > ?) ORDER BY o.name`, viewerID, time.Now().Unix())
}

func (s *Store) shares(ctx context.Context, q string, args ...any) ([]Share, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sh)
	}
	return out, rows.Err()
}

// ShareByToken resolves a share link that has not expired.
func (s *Store) ShareByToken(ctx context.Context, token string) (*Share, error) {
	return scanShare(s.DB.QueryRowContext(ctx, shareJoin+` WHERE s.token = ? AND (s.expires_at = 0 OR s.expires_at > ?)`, token, time.Now().Unix()))
}

// ShareFor returns the share from owner to viewer, if any is live.
func (s *Store) ShareFor(ctx context.Context, ownerID, viewerID int64) (*Share, error) {
	return scanShare(s.DB.QueryRowContext(ctx, shareJoin+` WHERE s.owner_id = ? AND s.viewer_id = ? AND (s.expires_at = 0 OR s.expires_at > ?) ORDER BY s.id DESC LIMIT 1`, ownerID, viewerID, time.Now().Unix()))
}

// CreateShare inserts a share.
func (s *Store) CreateShare(ctx context.Context, sh *Share) error {
	sh.CreatedAt = time.Now().Unix()
	var tok any
	if sh.Token != "" {
		tok = sh.Token
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO shares (owner_id, viewer_id, token, name, level, precision_m, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sh.OwnerID, sh.ViewerID, tok, sh.Name, sh.Level, sh.PrecisionM, sh.ExpiresAt, sh.CreatedAt)
	if err != nil {
		return conflict(err)
	}
	sh.ID, _ = r.LastInsertId()
	return nil
}

// UpdateShare changes level, precision, name and expiry.
func (s *Store) UpdateShare(ctx context.Context, sh *Share) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE shares SET name = ?, level = ?, precision_m = ?, expires_at = ? WHERE id = ? AND owner_id = ?`, sh.Name, sh.Level, sh.PrecisionM, sh.ExpiresAt, sh.ID, sh.OwnerID)
	return err
}

// DeleteShare stops sharing.
func (s *Store) DeleteShare(ctx context.Context, ownerID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM shares WHERE id = ? AND owner_id = ?`, id, ownerID)
	return err
}

// ---- privacy zones ----

// PrivacyZones lists a person's zones.
func (s *Store) PrivacyZones(ctx context.Context, userID int64) ([]PrivacyZone, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, name, lat, lng, radius, drop_points, created_at FROM privacy_zones WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PrivacyZone{}
	for rows.Next() {
		var z PrivacyZone
		var drop int
		if err := rows.Scan(&z.ID, &z.UserID, &z.Name, &z.Lat, &z.Lng, &z.Radius, &drop, &z.CreatedAt); err != nil {
			return nil, err
		}
		z.DropPoints = drop == 1
		out = append(out, z)
	}
	return out, rows.Err()
}

// SavePrivacyZone inserts or updates a zone.
func (s *Store) SavePrivacyZone(ctx context.Context, z *PrivacyZone) error {
	if z.ID > 0 {
		_, err := s.DB.ExecContext(ctx, `UPDATE privacy_zones SET name = ?, lat = ?, lng = ?, radius = ?, drop_points = ? WHERE id = ? AND user_id = ?`, z.Name, z.Lat, z.Lng, z.Radius, boolInt(z.DropPoints), z.ID, z.UserID)
		return err
	}
	z.CreatedAt = time.Now().Unix()
	r, err := s.DB.ExecContext(ctx, `INSERT INTO privacy_zones (user_id, name, lat, lng, radius, drop_points, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, z.UserID, z.Name, z.Lat, z.Lng, z.Radius, boolInt(z.DropPoints), z.CreatedAt)
	if err != nil {
		return err
	}
	z.ID, _ = r.LastInsertId()
	return nil
}

// DeletePrivacyZone removes a zone.
func (s *Store) DeletePrivacyZone(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM privacy_zones WHERE id = ? AND user_id = ?`, id, userID)
	return err
}
