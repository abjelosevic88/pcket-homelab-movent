package store

import (
	"context"
	"math"
	"strings"
	"time"
)

const placeCols = `p.id, p.user_id, p.name, p.kind, p.category, p.color, p.lat, p.lng, p.radius, p.address, p.city, p.country, p.geocoded, p.renamed, p.hidden, p.notes, p.created_at, p.updated_at`

func scanPlace(row interface{ Scan(...any) error }, extra ...any) (*Place, error) {
	var p Place
	var renamed, hidden int
	dst := []any{&p.ID, &p.UserID, &p.Name, &p.Kind, &p.Category, &p.Color, &p.Lat, &p.Lng, &p.Radius, &p.Address, &p.City, &p.Country, &p.Geocoded, &renamed, &hidden, &p.Notes, &p.CreatedAt, &p.UpdatedAt}
	if err := row.Scan(append(dst, extra...)...); err != nil {
		return nil, notFound(err)
	}
	p.Renamed, p.Hidden = renamed == 1, hidden == 1
	return &p, nil
}

// PlaceQuery filters places.
type PlaceQuery struct {
	Kind          string // saved | discovered | ""
	Q             string
	IncludeHidden bool
	OnlyVisited   bool
	Sort          string // visits | time | recent | name
	Limit         int
}

// ListPlaces lists a person's places with how often and how long they were there
// (confirmed and suggested visits; declined ones do not count).
func (s *Store) ListPlaces(ctx context.Context, userID int64, q PlaceQuery) ([]Place, error) {
	w := []string{"p.user_id = ?"}
	args := []any{userID}
	if q.Kind != "" {
		w = append(w, "p.kind = ?")
		args = append(args, q.Kind)
	}
	if !q.IncludeHidden {
		w = append(w, "p.hidden = 0")
	}
	if q.Q != "" {
		w = append(w, "(p.name LIKE ? OR p.address LIKE ? OR p.city LIKE ?)")
		like := "%" + q.Q + "%"
		args = append(args, like, like, like)
	}
	having := ""
	if q.OnlyVisited {
		having = " HAVING COUNT(v.id) > 0"
	}
	order := "visits DESC, total DESC"
	switch q.Sort {
	case "time":
		order = "total DESC"
	case "recent":
		order = "last DESC"
	case "name":
		order = "p.name COLLATE NOCASE"
	}
	sqlq := `SELECT ` + placeCols + `, COUNT(v.id) AS visits, COALESCE(SUM(v.end_ts - v.start_ts),0) AS total, COALESCE(MIN(v.start_ts),0), COALESCE(MAX(v.end_ts),0) AS last
		FROM places p LEFT JOIN visits v ON v.place_id = p.id AND v.status <> 'declined'
		WHERE ` + strings.Join(w, " AND ") + ` GROUP BY p.id` + having + ` ORDER BY ` + order
	if q.Limit > 0 {
		sqlq += ` LIMIT ` + itoa(q.Limit)
	}
	rows, err := s.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Place{}
	for rows.Next() {
		var p *Place
		var visits int
		var total, first, last int64
		if p, err = scanPlace(rows, &visits, &total, &first, &last); err != nil {
			return nil, err
		}
		p.Visits, p.TotalS, p.FirstVisit, p.LastVisit = visits, total, first, last
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPlace returns one of a person's places with its visit totals.
func (s *Store) GetPlace(ctx context.Context, userID, id int64) (*Place, error) {
	var visits int
	var total, first, last int64
	p, err := scanPlace(s.DB.QueryRowContext(ctx, `SELECT `+placeCols+`, COUNT(v.id), COALESCE(SUM(v.end_ts - v.start_ts),0), COALESCE(MIN(v.start_ts),0), COALESCE(MAX(v.end_ts),0)
		FROM places p LEFT JOIN visits v ON v.place_id = p.id AND v.status <> 'declined' WHERE p.id = ? AND p.user_id = ? GROUP BY p.id`, id, userID), &visits, &total, &first, &last)
	if err != nil {
		return nil, err
	}
	p.Visits, p.TotalS, p.FirstVisit, p.LastVisit = visits, total, first, last
	return p, nil
}

// CreatePlace inserts a place.
func (s *Store) CreatePlace(ctx context.Context, p *Place) error {
	now := time.Now().Unix()
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Kind == "" {
		p.Kind = PlaceDiscovered
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO places (user_id, name, kind, category, color, lat, lng, radius, address, city, country, geocoded, renamed, hidden, notes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.UserID, p.Name, p.Kind, p.Category, p.Color, p.Lat, p.Lng, p.Radius, p.Address, p.City, p.Country, p.Geocoded,
		boolInt(p.Renamed), boolInt(p.Hidden), p.Notes, now, now)
	if err != nil {
		return err
	}
	p.ID, _ = r.LastInsertId()
	return nil
}

// UpdatePlace saves a place.
func (s *Store) UpdatePlace(ctx context.Context, p *Place) error {
	p.UpdatedAt = time.Now().Unix()
	_, err := s.DB.ExecContext(ctx, `UPDATE places SET name = ?, kind = ?, category = ?, color = ?, lat = ?, lng = ?, radius = ?, address = ?, city = ?, country = ?, geocoded = ?, renamed = ?, hidden = ?, notes = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`, p.Name, p.Kind, p.Category, p.Color, p.Lat, p.Lng, p.Radius, p.Address, p.City, p.Country, p.Geocoded, boolInt(p.Renamed), boolInt(p.Hidden), p.Notes, p.UpdatedAt, p.ID, p.UserID)
	return err
}

// DeletePlace removes a place; its visits stay, without a place.
func (s *Store) DeletePlace(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM places WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// MergePlaces moves every visit of `from` to `into` and deletes `from`.
func (s *Store) MergePlaces(ctx context.Context, userID, into int64, from []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range from {
		if id == into {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE visits SET place_id = ? WHERE place_id = ? AND user_id = ?`, into, id, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM places WHERE id = ? AND user_id = ?`, id, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PlacesNear returns a person's places whose centre is within `within`
// metres of a point (rough box first, exact distance by the caller).
func (s *Store) PlacesNear(ctx context.Context, userID int64, lat, lng, within float64) ([]Place, error) {
	dLat := within / 110540
	dLng := within / (111320 * math.Max(0.05, math.Cos(lat*math.Pi/180)))
	rows, err := s.DB.QueryContext(ctx, `SELECT `+placeCols+` FROM places p WHERE p.user_id = ? AND p.lat BETWEEN ? AND ? AND p.lng BETWEEN ? AND ?`,
		userID, lat-dLat, lat+dLat, lng-dLng, lng+dLng)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Place{}
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PlacesToGeocode returns places still waiting for a name.
func (s *Store) PlacesToGeocode(ctx context.Context, limit int) ([]Place, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+placeCols+` FROM places p WHERE p.geocoded = 0 ORDER BY p.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Place{}
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SetPlaceGeocode stores what the geocoder said; the name only when the
// person has not named the place.
func (s *Store) SetPlaceGeocode(ctx context.Context, id int64, name, address, city, country string, status int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE places SET name = CASE WHEN renamed = 1 OR ? = '' THEN name ELSE ? END, address = ?, city = ?, country = CASE WHEN ? = '' THEN country ELSE ? END, geocoded = ?, updated_at = ? WHERE id = ?`,
		name, name, address, city, country, country, status, time.Now().Unix(), id)
	return err
}

// HomePlace returns the saved place in category "home", if any.
func (s *Store) HomePlace(ctx context.Context, userID int64) (*Place, error) {
	return scanPlace(s.DB.QueryRowContext(ctx, `SELECT `+placeCols+` FROM places p WHERE p.user_id = ? AND p.category = 'home' ORDER BY p.kind = 'saved' DESC, p.id LIMIT 1`, userID))
}

// CleanupOrphanPlaces deletes discovered places nothing points at any more.
func (s *Store) CleanupOrphanPlaces(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM places WHERE user_id = ? AND kind = 'discovered' AND renamed = 0 AND category = '' AND notes = '' AND NOT EXISTS (SELECT 1 FROM visits v WHERE v.place_id = places.id)`, userID)
	return err
}

// Cities lists the distinct (city, country) of visited places with first and
// last visit and the number of visits.
type CityVisit struct {
	City    string `json:"city"`
	Country string `json:"country"`
	Visits  int    `json:"visits"`
	First   int64  `json:"first"`
	Last    int64  `json:"last"`
	TotalS  int64  `json:"total_s"`
}

// VisitedCities lists cities from visit places in a time range (0 = all time).
func (s *Store) VisitedCities(ctx context.Context, userID, from, to int64) ([]CityVisit, error) {
	if to == 0 {
		to = math.MaxInt64
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT p.city, p.country, COUNT(v.id), MIN(v.start_ts), MAX(v.end_ts), SUM(v.end_ts - v.start_ts)
		FROM visits v JOIN places p ON p.id = v.place_id
		WHERE v.user_id = ? AND v.status <> 'declined' AND v.end_ts >= ? AND v.start_ts <= ? AND p.city <> ''
		GROUP BY p.city, p.country ORDER BY MIN(v.start_ts)`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CityVisit{}
	for rows.Next() {
		var c CityVisit
		if err := rows.Scan(&c.City, &c.Country, &c.Visits, &c.First, &c.Last, &c.TotalS); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
