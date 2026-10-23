package server

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Server) kickRebuild() { kick(s.rebuildKick) }
func (s *Server) kickImport()  { kick(s.importKick) }
func (s *Server) kickGeocode() { kick(s.geocodeKick) }

// rebuildLoop turns new points into journeys, visits and day summaries.
// Ranges settle for REBUILD_DELAY first, so a phone uploading every few
// seconds is analysed once a batch is complete, not on every request.
func (s *Server) rebuildLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.RebuildDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.rebuildKick:
			select { // let the burst finish
			case <-ctx.Done():
				return
			case <-time.After(s.cfg.RebuildDelay):
			}
		}
		for i := 0; i < 1000; i++ {
			d, ok, err := s.store.TakeDirty(ctx, s.cfg.RebuildDelay)
			if err != nil {
				s.log.Warn("rebuild: take dirty", "err", err)
				break
			}
			if !ok {
				break
			}
			start := time.Now()
			res, err := s.engine.Rebuild(ctx, d.UserID, d.From, d.To)
			if err != nil {
				s.log.Error("rebuild failed", "user", d.UserID, "err", err)
				_ = s.store.MarkDirty(ctx, d.UserID, d.From, d.To)
				break
			}
			s.log.Debug("rebuilt", "user", d.UserID, "from", time.Unix(res.From, 0).Format(time.RFC3339), "to", time.Unix(res.To, 0).Format(time.RFC3339),
				"tracks", res.Tracks, "visits", res.Visits, "days", res.Days, "took", time.Since(start).String())
			if len(res.NewStays) > 0 {
				s.kickGeocode()
				s.announceVisits(ctx, d.UserID, res.NewStays)
			}
		}
	}
}

// resummarize recomputes day rows after a visit or place edit.
func (s *Server) resummarize(ctx context.Context, userID, from, to int64) {
	_, loc := s.userLoc(ctx, userID)
	if _, err := s.engine.Summarize(ctx, userID, from, to, loc); err != nil {
		s.log.Warn("summarize", "err", err)
	}
}

// geocodeLoop names new places, politely (one request a second on the
// public Nominatim), with answers cached on a ~25 m grid.
func (s *Server) geocodeLoop(ctx context.Context) {
	if !s.geocoder.Enabled() {
		return
	}
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		for {
			places, err := s.store.PlacesToGeocode(ctx, 25)
			if err != nil || len(places) == 0 {
				break
			}
			for _, p := range places {
				if ctx.Err() != nil {
					return
				}
				s.geocodePlace(ctx, p)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.geocodeKick:
		}
	}
}

func (s *Server) geocodePlace(ctx context.Context, p store.Place) {
	a, b := geo.CellKey(p.Lat, p.Lng, 25)
	key := fmt.Sprintf("geo:%d:%d", a, b)
	var res *geo.Place
	if v, _, ok := s.store.CacheGet(ctx, key); ok {
		res = &geo.Place{}
		if jsonUnmarshal([]byte(v), res) != nil {
			res = nil
		}
	}
	if res == nil {
		r, err := s.geocoder.Reverse(ctx, p.Lat, p.Lng)
		if err != nil {
			s.log.Debug("geocode failed", "place", p.ID, "err", err)
			_ = s.store.SetPlaceGeocode(ctx, p.ID, "", "", "", "", 2)
			return
		}
		res = r
		if b, err := jsonMarshal(res); err == nil {
			_ = s.store.CachePut(ctx, key, string(b))
		}
	}
	country := res.CountryCode
	if country == "" {
		country = geo.CountryAt(p.Lat, p.Lng)
	}
	_ = s.store.SetPlaceGeocode(ctx, p.ID, res.Name, res.Address, res.City, country, 1)
}

// importLoop runs queued imports one at a time.
func (s *Server) importLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		for {
			imp, err := s.store.NextImport(ctx)
			if err != nil {
				break
			}
			s.runImport(ctx, imp)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.importKick:
		}
	}
}

// watchLoop notices phones that went quiet.
func (s *Server) watchLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		users, err := s.store.UsersWithPoints(ctx)
		if err != nil {
			continue
		}
		for _, uid := range users {
			st, _ := s.store.GetSettings(ctx, uid)
			if st.NotifyStaleHours <= 0 || s.store.GetState(ctx, uid, "stale") == "1" {
				continue
			}
			p, err := s.store.LatestPoint(ctx, uid)
			if err != nil {
				continue
			}
			age := time.Since(time.Unix(p.TS, 0))
			if age < time.Duration(st.NotifyStaleHours)*time.Hour || age > 30*24*time.Hour {
				continue
			}
			_ = s.store.SetState(ctx, uid, "stale", "1")
			hours := int(math.Round(age.Hours()))
			s.event(ctx, uid, "movement.tracking_stopped", "tracking:"+strconv.FormatInt(uid, 10), map[string]any{
				"title": fmt.Sprintf("No location for %d hours", hours), "last_point_at": isoTime(p.TS), "silent_hours": hours, "path": "/devices"})
		}
	}
}

// onLivePoint reacts to a fresh position: tracking resumed, a new country.
func (s *Server) onLivePoint(ctx context.Context, u *store.User, p store.Point) {
	if s.store.GetState(ctx, u.ID, "stale") == "1" {
		_ = s.store.SetState(ctx, u.ID, "stale", "0")
		s.event(ctx, u.ID, "movement.tracking_resumed", "tracking:"+strconv.FormatInt(u.ID, 10), map[string]any{"title": "Location tracking is back", "path": "/"})
	}
	// No country from the air: the welcome comes on landing.
	airborne := (p.Speed != nil && *p.Speed > 70) || (p.Altitude != nil && *p.Altitude > 5000)
	if p.Country == "" || airborne {
		return
	}
	prev := s.store.GetState(ctx, u.ID, "country")
	if prev == p.Country {
		return
	}
	_ = s.store.SetState(ctx, u.ID, "country", p.Country)
	st, _ := s.store.GetSettings(ctx, u.ID)
	if prev == "" || !st.NotifyCountries {
		return
	}
	name := geo.CountryName(p.Country)
	s.event(ctx, u.ID, "movement.country_entered", "country:"+p.Country, map[string]any{"title": "Welcome to " + name, "country": p.Country,
		"country_name": name, "from_country": prev, "from_country_name": geo.CountryName(prev), "at": isoTime(p.TS), "path": "/"})
}

// announceVisits publishes new visits that are recent (not an import's).
func (s *Server) announceVisits(ctx context.Context, userID int64, vs []store.Visit) {
	cut := time.Now().Add(-6 * time.Hour).Unix()
	for _, v := range vs {
		if v.EndTS < cut {
			continue
		}
		name := ""
		if v.PlaceID != nil {
			if p, err := s.store.GetPlace(ctx, userID, *v.PlaceID); err == nil {
				name = p.Name
			}
		}
		s.event(ctx, userID, "movement.visit_detected", "visit:"+strconv.FormatInt(v.StartTS, 10), map[string]any{
			"title": "Stopped at " + firstNonEmpty(name, "a new place"), "place": name, "started_at": isoTime(v.StartTS), "ended_at": isoTime(v.EndTS),
			"duration_minutes": (v.EndTS - v.StartTS) / 60, "status": v.Status, "path": "/timeline/" + time.Unix(v.StartTS, 0).Format("2006-01-02")})
	}
}
