package store

import (
	"context"
	"time"
)

const deviceCols = `id, user_id, name, kind, scope, api_key, tracker_id, last_seen_at, last_point_at, last_lat, last_lng, last_battery, last_battery_state, last_ip, user_agent, points, revoked, created_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var revoked int
	if err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Kind, &d.Scope, &d.APIKey, &d.TrackerID, &d.LastSeenAt, &d.LastPointAt, &d.LastLat, &d.LastLng, &d.LastBattery,
		&d.LastBatteryState, &d.LastIP, &d.UserAgent, &d.Points, &revoked, &d.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	d.Revoked = revoked == 1
	return &d, nil
}

// NewDeviceKey returns a fresh key. Dawarich keys are 32 hex characters; ours
// are longer and carry a prefix so they are recognisable in a config file.
func NewDeviceKey() string { return "pm_" + RandomToken(20) }

// CreateDevice adds a device with a new key.
func (s *Store) CreateDevice(ctx context.Context, d *Device) error {
	if d.APIKey == "" {
		d.APIKey = NewDeviceKey()
	}
	d.CreatedAt = time.Now().Unix()
	r, err := s.DB.ExecContext(ctx, `INSERT INTO devices (user_id, name, kind, scope, api_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`, d.UserID, d.Name, d.Kind, d.Scope, d.APIKey, d.CreatedAt)
	if err != nil {
		return conflict(err)
	}
	d.ID, _ = r.LastInsertId()
	return nil
}

// ListDevices lists a person's devices, newest activity first.
func (s *Store) ListDevices(ctx context.Context, userID int64) ([]Device, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE user_id = ? ORDER BY revoked, last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// GetDevice returns one of a person's devices.
func (s *Store) GetDevice(ctx context.Context, userID, id int64) (*Device, error) {
	return scanDevice(s.DB.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = ? AND user_id = ?`, id, userID))
}

// DeviceByKey resolves an API key (revoked keys are not found).
func (s *Store) DeviceByKey(ctx context.Context, key string) (*Device, error) {
	return scanDevice(s.DB.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE api_key = ? AND revoked = 0`, key))
}

// UpdateDevice saves name, kind and scope.
func (s *Store) UpdateDevice(ctx context.Context, d *Device) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE devices SET name = ?, kind = ?, scope = ?, revoked = ? WHERE id = ? AND user_id = ?`, d.Name, d.Kind, d.Scope, boolInt(d.Revoked), d.ID, d.UserID)
	return err
}

// RotateDeviceKey gives a device a new key; the old one stops working.
func (s *Store) RotateDeviceKey(ctx context.Context, userID, id int64) (string, error) {
	key := NewDeviceKey()
	r, err := s.DB.ExecContext(ctx, `UPDATE devices SET api_key = ?, revoked = 0 WHERE id = ? AND user_id = ?`, key, id, userID)
	if err != nil {
		return "", err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// DeleteDevice removes a device; its points stay.
func (s *Store) DeleteDevice(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// TouchDevice records that a device called in.
func (s *Store) TouchDevice(ctx context.Context, id int64, ip, ua string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE devices SET last_seen_at = ?, last_ip = ?, user_agent = CASE WHEN ? = '' THEN user_agent ELSE ? END WHERE id = ?`, time.Now().Unix(), ip, ua, ua, id)
}

// DeviceGotPoints records the newest point a device sent.
func (s *Store) DeviceGotPoints(ctx context.Context, id int64, added int, last *Point) {
	if last == nil {
		_, _ = s.DB.ExecContext(ctx, `UPDATE devices SET points = points + ? WHERE id = ?`, added, id)
		return
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE devices SET points = points + ?,
		last_lat = CASE WHEN ? >= last_point_at THEN ? ELSE last_lat END,
		last_lng = CASE WHEN ? >= last_point_at THEN ? ELSE last_lng END,
		last_battery = CASE WHEN ? >= last_point_at AND ? IS NOT NULL THEN ? ELSE last_battery END,
		last_battery_state = CASE WHEN ? >= last_point_at AND ? <> '' THEN ? ELSE last_battery_state END,
		tracker_id = CASE WHEN ? <> '' THEN ? ELSE tracker_id END,
		last_point_at = MAX(last_point_at, ?) WHERE id = ?`,
		added, last.TS, last.Lat, last.TS, last.Lng, last.TS, last.Battery, last.Battery, last.TS, last.BatteryState, last.BatteryState,
		last.TrackerID, last.TrackerID, last.TS, id)
}
