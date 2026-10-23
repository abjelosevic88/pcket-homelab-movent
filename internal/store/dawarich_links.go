package store

import (
	"context"
	"time"
)

// DawarichLink keeps pulling new points from a Dawarich server.
type DawarichLink struct {
	UserID      int64  `json:"-"`
	URL         string `json:"url"`
	APIKey      string `json:"-"`
	Account     string `json:"account"`
	Source      string `json:"source"` // direct | core
	Enabled     bool   `json:"enabled"`
	CursorTS    int64  `json:"cursor_ts"`
	LastSyncAt  int64  `json:"last_sync_at"`
	LastAdded   int64  `json:"last_added"`
	PointsAdded int64  `json:"points_added"`
	LastError   string `json:"last_error"`
	CreatedAt   int64  `json:"created_at"`
}

const linkCols = `user_id, url, api_key, account, enabled, cursor_ts, last_sync_at, last_added, points_added, last_error, created_at, source`

func scanLink(row interface{ Scan(...any) error }) (*DawarichLink, error) {
	var l DawarichLink
	var en int
	if err := row.Scan(&l.UserID, &l.URL, &l.APIKey, &l.Account, &en, &l.CursorTS, &l.LastSyncAt, &l.LastAdded, &l.PointsAdded, &l.LastError, &l.CreatedAt, &l.Source); err != nil {
		return nil, notFound(err)
	}
	l.Enabled = en == 1
	return &l, nil
}

// GetDawarichLink returns a person's link.
func (s *Store) GetDawarichLink(ctx context.Context, userID int64) (*DawarichLink, error) {
	return scanLink(s.DB.QueryRowContext(ctx, `SELECT `+linkCols+` FROM dawarich_links WHERE user_id = ?`, userID))
}

// EnabledDawarichLinks lists the links to pull from.
func (s *Store) EnabledDawarichLinks(ctx context.Context) ([]DawarichLink, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+linkCols+` FROM dawarich_links WHERE enabled = 1 ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DawarichLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// SaveDawarichLink creates or replaces a person's link (new address or key),
// starting over from its cursor and counters.
func (s *Store) SaveDawarichLink(ctx context.Context, l *DawarichLink) error {
	l.CreatedAt = time.Now().Unix()
	if l.Source == "" {
		l.Source = "direct"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO dawarich_links (user_id, url, api_key, account, enabled, cursor_ts, created_at, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET url = excluded.url, api_key = excluded.api_key, account = excluded.account, enabled = excluded.enabled, source = excluded.source,
		cursor_ts = excluded.cursor_ts, last_sync_at = 0, last_added = 0, points_added = 0, last_error = '', created_at = excluded.created_at`,
		l.UserID, l.URL, l.APIKey, l.Account, boolInt(l.Enabled), l.CursorTS, l.CreatedAt, l.Source)
	return err
}

// SetDawarichLinkEnabled pauses or resumes a link.
func (s *Store) SetDawarichLinkEnabled(ctx context.Context, userID int64, on bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE dawarich_links SET enabled = ? WHERE user_id = ?`, boolInt(on), userID)
	return err
}

// FinishDawarichSync records one pull: the new cursor and what it added, or
// why it failed (the cursor then stays where it was).
func (s *Store) FinishDawarichSync(ctx context.Context, userID, cursor, added int64, syncErr error) error {
	msg := ""
	if syncErr != nil {
		msg = syncErr.Error()
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE dawarich_links SET cursor_ts = MAX(cursor_ts, ?), last_sync_at = ?, last_added = ?, points_added = points_added + ?, last_error = ? WHERE user_id = ?`,
		cursor, time.Now().Unix(), added, added, msg, userID)
	return err
}

// DeleteDawarichLink stops pulling; the points already here stay.
func (s *Store) DeleteDawarichLink(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM dawarich_links WHERE user_id = ?`, userID)
	return err
}
