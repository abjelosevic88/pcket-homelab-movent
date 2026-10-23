package server

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/analyze"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// handleListPlaces lists places with visit counts (?kind=saved|discovered,
// q=, sort=visits|time|recent|name, visited=1, hidden=1, limit=).
func (s *Server) handleListPlaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	q := store.PlaceQuery{Kind: r.URL.Query().Get("kind"), Q: r.URL.Query().Get("q"), Sort: r.URL.Query().Get("sort"),
		IncludeHidden: queryBool(r, "hidden"), OnlyVisited: queryBool(r, "visited"), Limit: queryInt(r, "limit", 0)}
	ps, err := s.store.ListPlaces(ctx, u.ID, q)
	if err != nil {
		s.fail(w, err, "list places")
		return
	}
	out := make([]map[string]any, 0, len(ps))
	for i := range ps {
		m := placeJSON(&ps[i])
		m["latitude"], m["longitude"] = ps[i].Lat, ps[i].Lng // Dawarich clients
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPlace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	p, err := s.store.GetPlace(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get place")
		return
	}
	visits, _ := s.store.Visits(ctx, u.ID, store.VisitQuery{PlaceID: p.ID, Desc: true, Limit: 200})
	vs := make([]map[string]any, 0, len(visits))
	// By hour of day and weekday, for "when do I usually come here".
	hours := make([]int64, 24)
	weekdays := make([]int64, 7)
	_, loc := s.userLoc(ctx, u.ID)
	for _, v := range visits {
		vs = append(vs, visitView(v))
		if v.Status == store.VisitDeclined {
			continue
		}
		for t := v.StartTS; t < v.EndTS; t += 900 {
			lt := time.Unix(t, 0).In(loc)
			hours[lt.Hour()] += 900
			weekdays[int(lt.Weekday())] += 900
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"place": placeJSON(p), "visits": vs, "hours": hours, "weekdays": weekdays})
}

type placeInput struct {
	Name     *string  `json:"name"`
	Kind     *string  `json:"kind"`
	Category *string  `json:"category"`
	Color    *string  `json:"color"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
	Radius   *float64 `json:"radius"`
	Address  *string  `json:"address"`
	Hidden   *bool    `json:"hidden"`
	Notes    *string  `json:"notes"`
}

func (in placeInput) apply(p *store.Place) error {
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
		p.Renamed = p.Name != ""
	}
	if in.Kind != nil {
		if *in.Kind != store.PlaceSaved && *in.Kind != store.PlaceDiscovered {
			return errBad("kind must be saved or discovered")
		}
		p.Kind = *in.Kind
	}
	if in.Category != nil {
		p.Category = strings.ToLower(strings.TrimSpace(*in.Category))
	}
	if in.Color != nil {
		p.Color = *in.Color
	}
	if in.Lat != nil && in.Lng != nil {
		if !geo.ValidCoord(*in.Lat, *in.Lng) {
			return errBad("invalid coordinates")
		}
		p.Lat, p.Lng = *in.Lat, *in.Lng
		p.Country = geo.CountryAt(p.Lat, p.Lng)
	}
	if in.Radius != nil {
		p.Radius = math.Max(15, math.Min(5000, *in.Radius))
	}
	if in.Address != nil {
		p.Address = *in.Address
	}
	if in.Hidden != nil {
		p.Hidden = *in.Hidden
	}
	if in.Notes != nil {
		p.Notes = *in.Notes
	}
	return nil
}

// handleCreatePlace saves a place (home, work, the gym…): visits inside it
// are named and confirmed automatically from now on and back in time.
func (s *Server) handleCreatePlace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in placeInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Lat == nil || in.Lng == nil {
		writeErr(w, http.StatusBadRequest, "lat and lng are required")
		return
	}
	p := &store.Place{UserID: u.ID, Kind: store.PlaceSaved, Radius: 100}
	if err := in.apply(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Name == "" {
		p.Name = capitalize(p.Category)
	}
	if p.Category == "home" {
		s.demoteOtherHomes(ctx, u.ID, 0)
	}
	if err := s.store.CreatePlace(ctx, p); err != nil {
		s.fail(w, err, "create place")
		return
	}
	if p.Name == "" {
		s.kickGeocode()
	}
	s.afterSavedPlaceChange(ctx, u.ID, p)
	writeJSON(w, http.StatusCreated, placeJSON(p))
}

func (s *Server) demoteOtherHomes(ctx context.Context, userID, keep int64) {
	_, _ = s.store.DB.ExecContext(ctx, `UPDATE places SET category = '' WHERE user_id = ? AND category = 'home' AND id <> ?`, userID, keep)
}

func (s *Server) handleUpdatePlace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	p, err := s.store.GetPlace(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get place")
		return
	}
	before := *p
	var in placeInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := in.apply(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Category == "home" && before.Category != "home" {
		s.demoteOtherHomes(ctx, u.ID, p.ID)
	}
	if p.Name == "" && in.Name != nil {
		p.Renamed, p.Geocoded = false, 0 // cleared: let the geocoder name it again
		s.kickGeocode()
	}
	if err := s.store.UpdatePlace(ctx, p); err != nil {
		s.fail(w, err, "update place")
		return
	}
	geometry := p.Lat != before.Lat || p.Lng != before.Lng || p.Radius != before.Radius || p.Kind != before.Kind
	if geometry && (p.Kind == store.PlaceSaved || before.Kind == store.PlaceSaved) {
		s.afterSavedPlaceChange(ctx, u.ID, p)
	} else if p.Category != before.Category || p.Hidden != before.Hidden {
		go s.resummarize(context.WithoutCancel(ctx), u.ID, 1, time.Now().Unix())
	}
	p, _ = s.store.GetPlace(ctx, u.ID, p.ID)
	writeJSON(w, http.StatusOK, placeJSON(p))
}

func (s *Server) handleDeletePlace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	p, err := s.store.GetPlace(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get place")
		return
	}
	if err := s.store.DeletePlace(ctx, u.ID, p.ID); err != nil {
		s.fail(w, err, "delete place")
		return
	}
	if p.Kind == store.PlaceSaved && p.Visits > 0 {
		_ = s.store.MarkDirty(ctx, u.ID, p.FirstVisit, p.LastVisit)
		s.kickRebuild()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMergePlaces folds other places into this one: {"ids": [..]}.
func (s *Server) handleMergePlaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	into := idParam(r, "id")
	if _, err := s.store.GetPlace(ctx, u.ID, into); err != nil {
		s.fail(w, err, "get place")
		return
	}
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &in); err != nil || len(in.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids are required")
		return
	}
	if err := s.store.MergePlaces(ctx, u.ID, into, in.IDs); err != nil {
		s.fail(w, err, "merge places")
		return
	}
	go s.resummarize(context.WithoutCancel(ctx), u.ID, 1, time.Now().Unix())
	p, _ := s.store.GetPlace(ctx, u.ID, into)
	writeJSON(w, http.StatusOK, placeJSON(p))
}

// afterSavedPlaceChange re-sorts the visits a saved place covers.
func (s *Server) afterSavedPlaceChange(ctx context.Context, userID int64, p *store.Place) {
	// Every detected visit within reach goes back through detection.
	vs, err := s.store.Visits(ctx, userID, store.VisitQuery{})
	if err != nil {
		return
	}
	var lo, hi int64
	for _, v := range vs {
		if geo.Haversine(v.Lat, v.Lng, p.Lat, p.Lng) <= p.Radius+250 {
			if lo == 0 || v.StartTS < lo {
				lo = v.StartTS
			}
			if v.EndTS > hi {
				hi = v.EndTS
			}
			if !v.Manual && v.Status == store.VisitSuggested {
				id := p.ID
				v.PlaceID = &id
				if p.Kind == store.PlaceSaved && geo.Haversine(v.Lat, v.Lng, p.Lat, p.Lng) <= p.Radius {
					v.Status = store.VisitConfirmed
				}
				v.Place = nil
				_ = s.store.UpdateVisit(ctx, &v)
			}
		}
	}
	if lo > 0 {
		go s.resummarize(context.WithoutCancel(ctx), userID, lo, hi)
	}
}

// handleBulkVisits confirms or declines many visits: {"ids": [], "status": ""}.
func (s *Server) handleBulkVisits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if err := decode(r, &in); err != nil || len(in.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids and status are required")
		return
	}
	if in.Status != store.VisitConfirmed && in.Status != store.VisitDeclined && in.Status != store.VisitSuggested {
		writeErr(w, http.StatusBadRequest, "status must be confirmed, declined or suggested")
		return
	}
	var lo, hi int64
	n := 0
	for _, id := range in.IDs {
		v, err := s.store.GetVisit(ctx, u.ID, id)
		if err != nil {
			continue
		}
		v.Status, v.Place = in.Status, nil
		if s.store.UpdateVisit(ctx, v) == nil {
			n++
			if lo == 0 || v.StartTS < lo {
				lo = v.StartTS
			}
			hi = maxI64(hi, v.EndTS)
		}
	}
	if n > 0 {
		go s.resummarize(context.WithoutCancel(ctx), u.ID, lo, hi)
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": n})
}

// ---- journeys ----

func (s *Server) handleGetTrack(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTrack(r.Context(), userFrom(r.Context()).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get journey")
		return
	}
	writeJSON(w, http.StatusOK, trackJSON(*t, t.Polyline))
}

// handleUpdateTrack corrects a journey's mode: {"mode": "train"}.
func (s *Server) handleUpdateTrack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decode(r, &in); err != nil || !analyze.ValidMode(in.Mode) {
		writeErr(w, http.StatusBadRequest, "mode must be one of "+strings.Join(analyze.Modes, ", "))
		return
	}
	if err := s.store.SetTrackMode(ctx, u.ID, idParam(r, "id"), in.Mode); err != nil {
		s.fail(w, err, "set mode")
		return
	}
	t, err := s.store.GetTrack(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get journey")
		return
	}
	go s.resummarize(context.WithoutCancel(ctx), u.ID, t.StartTS, t.EndTS)
	writeJSON(w, http.StatusOK, trackJSON(*t, t.Polyline))
}

// handleTrackPoints returns a journey's points with speed and altitude, for
// the speed/elevation chart.
func (s *Server) handleTrackPoints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	t, err := s.store.GetTrack(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get journey")
		return
	}
	pts, err := s.store.SlimPoints(ctx, u.ID, t.StartTS, t.EndTS, false)
	if err != nil {
		s.fail(w, err, "points")
		return
	}
	rows := make([][]any, 0, len(pts))
	var cum float64
	for i, p := range pts {
		if i > 0 {
			cum += geo.Haversine(pts[i-1].Lat, pts[i-1].Lng, p.Lat, p.Lng)
		}
		var sp, alt any
		if p.Speed != nil {
			sp = round1(*p.Speed * 3.6)
		} else if i > 0 && p.TS > pts[i-1].TS {
			sp = round1(geo.Haversine(pts[i-1].Lat, pts[i-1].Lng, p.Lat, p.Lng) / float64(p.TS-pts[i-1].TS) * 3.6)
		}
		if p.Alt != nil {
			alt = math.Round(*p.Alt)
		}
		rows = append(rows, []any{p.TS, p.Lat, p.Lng, sp, alt, math.Round(cum)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"journey": trackJSON(*t, ""), "points": rows})
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
