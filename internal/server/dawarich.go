package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// The Dawarich API (v1.16), so the Dawarich apps, Pocket Travel, Pocket
// Garage, Pocket Core's connector and Home Assistant integrations work
// against this server unchanged. Shapes follow Dawarich's serializers; our
// own fields are added alongside, never in place of theirs.

func (s *Server) dawarichHeaders(w http.ResponseWriter, authed bool) {
	msg := "Hey, I'm alive!"
	if authed {
		msg = "Hey, I'm alive and authenticated!"
	}
	w.Header().Set("X-Dawarich-Response", msg)
	w.Header().Set("X-Dawarich-Version", DawarichVersion)
	w.Header().Set("X-Pocket-Movement-Version", Version)
}

func (s *Server) handleDawarichHealth(w http.ResponseWriter, r *http.Request) {
	u, _, _ := s.resolve(r)
	s.dawarichHeaders(w, u != nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func isoTime(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// tsParam reads start_at / end_at as unix seconds or a date/time.
func tsParam(r *http.Request, name string, def int64) int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	if ts, ok := parseTS(v); ok {
		return ts
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		if name == "end_at" || name == "to" {
			return t.AddDate(0, 0, 1).Unix() - 1
		}
		return t.Unix()
	}
	return def
}

func (s *Server) handleDawarichMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	s.dawarichHeaders(w, true)
	created := u.CreatedAt
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id": u.ID, "email": u.Email, "name": u.Name, "theme": "dark", "created_at": created, "updated_at": created,
			"settings": s.dawarichSettings(ctx, u, st),
		},
		"features": map[string]any{"family": true, "immich": false, "photoprism": false, "reverse_geocoding": s.geocoder.Enabled(), "self_hosted": true},
	})
}

func (s *Server) dawarichSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	tz := st.Timezone
	if tz == "" {
		tz = s.engine.Location(st).String()
	}
	return map[string]any{
		"timezone": tz, "maps": map[string]any{"distance_unit": st.DistanceUnit}, "distance_unit": st.DistanceUnit,
		"fog_of_war_meters": 100, "meters_between_routes": 500, "minutes_between_routes": st.TrackGapMinutes,
		"time_threshold_minutes": st.VisitMinMinutes, "merge_threshold_minutes": 20, "preferred_map_layer": st.MapTiles,
		"speed_colored_routes": st.RouteColor == "speed", "points_rendering_mode": "raw", "live_map_enabled": true, "route_opacity": 0.6,
		"visits_suggestions_enabled": true, "speed_color_scale": "", "fog_of_war_threshold": 100, "globe_projection": false,
		"immich_url": nil, "photoprism_url": nil,
	}
}

func (s *Server) handleDawarichSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	s.dawarichHeaders(w, true)
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.dawarichSettings(ctx, u, st), "status": "success"})
}

func (s *Server) handleDawarichSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Settings map[string]any `json:"settings"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	if v, ok := num(in.Settings["minutes_between_routes"]); ok && v >= 5 {
		st.TrackGapMinutes = int(v)
	}
	if v, ok := num(in.Settings["time_threshold_minutes"]); ok && v >= 1 {
		st.VisitMinMinutes = int(v)
	}
	if v := str(in.Settings["distance_unit"]); v == "km" || v == "mi" {
		st.DistanceUnit = v
	}
	if v := str(in.Settings["timezone"]); v != "" {
		if _, err := time.LoadLocation(v); err == nil {
			st.Timezone = v
		}
	}
	if err := s.store.SaveUserSettings(ctx, u.ID, st); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Settings updated", "settings": s.dawarichSettings(ctx, u, st), "status": "success"})
}

// handleMobileSettings stores the tracking settings the Dawarich app syncs
// (tracking mode, filters, batch size); Devices shows them too.
func (s *Server) handleMobileSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	cur := map[string]any{}
	_ = json.Unmarshal([]byte(s.store.GetState(ctx, u.ID, "mobile_settings")), &cur)
	updated := s.store.GetState(ctx, u.ID, "mobile_settings_at")
	if r.Method == http.MethodPatch || r.Method == http.MethodPut {
		var in struct {
			Settings map[string]any `json:"settings"`
		}
		if err := decode(r, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		allowed := map[string]bool{"tracking_mode": true, "tracking_visits": true, "track_visits_independently": true, "auto_start": true, "distance_filter": true,
			"time_filter": true, "track_break": true, "accuracy": true, "show_background_location_indicator": true, "upload_automatically": true,
			"upload_all_on_tracking_stop": true, "batch_size": true}
		for k, v := range in.Settings {
			if allowed[k] {
				cur[k] = v
			}
		}
		b, _ := json.Marshal(cur)
		updated = time.Now().UTC().Format(time.RFC3339)
		_ = s.store.SetState(ctx, u.ID, "mobile_settings", string(b))
		_ = s.store.SetState(ctx, u.ID, "mobile_settings_at", updated)
	}
	var at any
	if updated != "" {
		at = updated
	}
	s.dawarichHeaders(w, true)
	writeJSON(w, http.StatusOK, map[string]any{"settings": cur, "updated_at": at, "capabilities": map[string]any{}, "status": "success"})
}

// ---- points ----

func dawarichPoint(p store.Point) map[string]any {
	m := map[string]any{
		"id": p.ID, "latitude": strconv.FormatFloat(p.Lat, 'f', -1, 64), "longitude": strconv.FormatFloat(p.Lng, 'f', -1, 64),
		"timestamp": p.TS, "altitude": p.Altitude, "accuracy": p.Accuracy, "vertical_accuracy": p.VerticalAccuracy,
		"velocity": speedStr(p.Speed), "course": p.Course, "battery": p.Battery, "battery_status": nullable(p.BatteryState),
		"tracker_id": nullable(p.TrackerID), "ssid": nullable(p.SSID), "connection": connName(p.Connection), "mode": nil, "motion_data": motionData(p.Motion),
		"country": nullable(p.Country), "country_name": nullable(geo.CountryName(p.Country)), "city": nil, "anomaly": p.Flags&store.FlagOutlier != 0,
		"lonlat": "POINT (" + strconv.FormatFloat(p.Lng, 'f', -1, 64) + " " + strconv.FormatFloat(p.Lat, 'f', -1, 64) + ")",
		"source": p.Source, "device_id": p.DeviceID,
	}
	return m
}

func speedStr(v *float64) any {
	if v == nil {
		return nil
	}
	return strconv.FormatFloat(math.Round(*v*10)/10, 'f', -1, 64)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func connName(c string) any {
	switch c {
	case "w":
		return "wifi"
	case "m":
		return "mobile"
	case "o":
		return "offline"
	case "":
		return nil
	}
	return c
}

func motionData(m string) any {
	if m == "" {
		return map[string]any{}
	}
	return map[string]any{"motion": strings.Split(m, ",")}
}

// handleDawarichPoints is GET /api/v1/points: start_at, end_at (default now),
// order (desc), page, per_page (100, max 10000), slim=true, X-Current-Page
// and X-Total-Pages headers.
func (s *Server) handleDawarichPoints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	q := store.PointQuery{From: tsParam(r, "start_at", 0), To: tsParam(r, "end_at", time.Now().Unix()), Desc: r.URL.Query().Get("order") != "asc",
		IncludeOutliers: queryBool(r, "include_anomalies")}
	perPage := queryInt(r, "per_page", 100)
	if perPage <= 0 {
		perPage = 100
	}
	if perPage > 10000 {
		perPage = 10000
	}
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	total, err := s.store.CountPoints(ctx, u.ID, q)
	if err != nil {
		s.fail(w, err, "count points")
		return
	}
	q.Limit, q.Offset = perPage, (page-1)*perPage
	pts, err := s.store.Points(ctx, u.ID, q)
	if err != nil {
		s.fail(w, err, "list points")
		return
	}
	pages := (total + int64(perPage) - 1) / int64(perPage)
	w.Header().Set("X-Current-Page", strconv.Itoa(page))
	w.Header().Set("X-Total-Pages", strconv.FormatInt(pages, 10))
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	s.dawarichHeaders(w, true)
	slim := r.URL.Query().Get("slim") == "true"
	out := make([]map[string]any, 0, len(pts))
	for _, p := range pts {
		if slim {
			out = append(out, map[string]any{"id": p.ID, "latitude": strconv.FormatFloat(p.Lat, 'f', -1, 64), "longitude": strconv.FormatFloat(p.Lng, 'f', -1, 64),
				"timestamp": p.TS, "velocity": speedStr(p.Speed), "country_name": nullable(geo.CountryName(p.Country)), "tracker_id": nullable(p.TrackerID)})
		} else {
			out = append(out, dawarichPoint(p))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTrackedMonths(w http.ResponseWriter, r *http.Request) {
	months, err := s.store.TrackedMonths(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "tracked months")
		return
	}
	// Dawarich: [{"year": 2024, "months": ["Jan", "Feb"]}]
	byYear := map[int][]string{}
	var years []int
	for _, m := range months {
		if _, ok := byYear[m.Year]; !ok {
			years = append(years, m.Year)
		}
		byYear[m.Year] = append(byYear[m.Year], time.Month(m.Month).String()[:3])
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	out := make([]map[string]any, 0, len(years))
	for _, y := range years {
		out = append(out, map[string]any{"year": y, "months": byYear[y]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeletePoint(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.DeletePoints(r.Context(), userFrom(r.Context()).ID, []int64{idParam(r, "id")}); err != nil {
		s.fail(w, err, "delete point")
		return
	}
	s.kickRebuild()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Point deleted successfully"})
}

func (s *Server) handleBulkDeletePoints(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PointIDs []json.Number `json:"point_ids"`
	}
	if err := decode(r, &in); err != nil || len(in.PointIDs) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "No points selected")
		return
	}
	if len(in.PointIDs) > 5000 {
		writeErr(w, http.StatusUnprocessableEntity, "Too many points (max 5000)")
		return
	}
	ids := make([]int64, 0, len(in.PointIDs))
	for _, n := range in.PointIDs {
		if id, err := n.Int64(); err == nil {
			ids = append(ids, id)
		}
	}
	n, err := s.store.DeletePoints(r.Context(), userFrom(r.Context()).ID, ids)
	if err != nil {
		s.fail(w, err, "delete points")
		return
	}
	s.kickRebuild()
	writeJSON(w, http.StatusOK, map[string]any{"message": "Points were successfully destroyed", "count": n})
}

// ---- visits ----

// visitJSON is Dawarich's visit plus ours.
func visitJSON(v store.Visit) map[string]any {
	place := map[string]any{"latitude": v.Lat, "longitude": v.Lng, "id": nil, "address": nil}
	out := map[string]any{
		"id": v.ID, "area_id": nil, "user_id": v.UserID, "started_at": isoTime(v.StartTS), "ended_at": isoTime(v.EndTS),
		"duration": (v.EndTS - v.StartTS) / 60, "name": v.DisplayName(), "status": v.Status, "confidence": nil, "confidence_band": nil,
		"start_ts": v.StartTS, "end_ts": v.EndTS, "lat": v.Lat, "lng": v.Lng, "radius": v.Radius, "points": v.Points,
		"own_name": v.Name, "notes": v.Notes, "manual": v.Manual, "place_id": v.PlaceID,
	}
	if v.Place != nil {
		p := v.Place
		place = map[string]any{"latitude": p.Lat, "longitude": p.Lng, "id": p.ID, "address": nullable(p.Address), "name": p.Name, "kind": p.Kind,
			"category": p.Category, "city": p.City, "country": p.Country, "radius": p.Radius, "color": p.Color}
		if p.Kind == store.PlaceSaved {
			out["area_id"] = p.ID
		}
	}
	out["place"] = place
	return out
}

func (s *Server) handleListVisits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	q := store.VisitQuery{From: tsParam(r, "start_at", tsParam(r, "from", 0)), To: tsParam(r, "end_at", tsParam(r, "to", 0)), Status: r.URL.Query().Get("status"),
		Desc: r.URL.Query().Get("order") == "desc", Limit: queryInt(r, "limit", 0)}
	if pid := queryInt(r, "place_id", 0); pid > 0 {
		q.PlaceID = int64(pid)
	}
	// Dawarich's selection box: visits whose place lies inside a bounding box.
	minLat, okA := num(r.URL.Query().Get("selection[sw_lat]"))
	minLng, okB := num(r.URL.Query().Get("selection[sw_lng]"))
	maxLat, okC := num(r.URL.Query().Get("selection[ne_lat]"))
	maxLng, okD := num(r.URL.Query().Get("selection[ne_lng]"))
	vs, err := s.store.Visits(ctx, u.ID, q)
	if err != nil {
		s.fail(w, err, "list visits")
		return
	}
	out := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		if okA && okB && okC && okD && (v.Lat < minLat || v.Lat > maxLat || v.Lng < minLng || v.Lng > maxLng) {
			continue
		}
		out = append(out, visitJSON(v))
	}
	s.dawarichHeaders(w, true)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetVisit(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.GetVisit(r.Context(), userFrom(r.Context()).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get visit")
		return
	}
	writeJSON(w, http.StatusOK, visitJSON(*v))
}

// handleUpdateVisit confirms, declines, renames, moves to another place or
// retimes a visit (Dawarich sends {"visit": {...}}; a flat body works too).
func (s *Server) handleUpdateVisit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	v, err := s.store.GetVisit(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get visit")
		return
	}
	body, err := readJSON(r, 1<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	m := obj(body)
	if inner := obj(m["visit"]); inner != nil {
		m = inner
	}
	if err := s.applyVisitChanges(ctx, u, v, m); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	v, _ = s.store.GetVisit(ctx, u.ID, v.ID)
	writeJSON(w, http.StatusOK, visitJSON(*v))
}

func (s *Server) applyVisitChanges(ctx context.Context, u *store.User, v *store.Visit, m map[string]any) error {
	retimed := false
	if st := str(m["status"]); st != "" {
		switch st {
		case store.VisitSuggested, store.VisitConfirmed, store.VisitDeclined:
			v.Status = st
		default:
			return errBad("status must be suggested, confirmed or declined")
		}
	}
	if _, ok := m["name"]; ok {
		name := str(m["name"])
		// Naming a visit that has a discovered place names the place, so
		// every visit there gets the name (that is what people mean).
		if v.PlaceID != nil && name != "" && !truthy(m["only_this"]) {
			if p, err := s.store.GetPlace(ctx, u.ID, *v.PlaceID); err == nil {
				p.Name, p.Renamed = name, true
				_ = s.store.UpdatePlace(ctx, p)
				name = ""
			}
		}
		v.Name = name
		if v.Status == store.VisitSuggested {
			v.Status = store.VisitConfirmed
		}
	}
	if _, ok := m["notes"]; ok {
		v.Notes = str(m["notes"])
	}
	if pid, ok := num(m["place_id"]); ok {
		id := int64(pid)
		if id <= 0 {
			v.PlaceID = nil
		} else if _, err := s.store.GetPlace(ctx, u.ID, id); err != nil {
			return errBad("no such place")
		} else {
			v.PlaceID = &id
			if v.Status == store.VisitSuggested {
				v.Status = store.VisitConfirmed
			}
		}
	}
	if ts, ok := parseTS(first(m, "started_at", "start_ts")); ok {
		v.StartTS, retimed = ts, true
	}
	if ts, ok := parseTS(first(m, "ended_at", "end_ts")); ok {
		v.EndTS, retimed = ts, true
	}
	if v.EndTS < v.StartTS {
		return errBad("the visit ends before it starts")
	}
	if retimed {
		v.Manual = true // hand-timed: rebuilds leave it alone
	}
	v.Place = nil
	if err := s.store.UpdateVisit(ctx, v); err != nil {
		return err
	}
	if retimed {
		_ = s.store.MarkDirty(ctx, u.ID, v.StartTS, v.EndTS)
		s.kickRebuild()
	} else {
		go s.resummarize(context.WithoutCancel(ctx), u.ID, v.StartTS, v.EndTS)
	}
	return nil
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

func errBad(msg string) error { return badRequest(msg) }

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "1" || x == "true"
	}
	f, ok := num(v)
	return ok && f != 0
}

// handleCreateVisit adds a visit by hand: {started_at, ended_at, latitude,
// longitude | place_id, name}.
func (s *Server) handleCreateVisit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	body, err := readJSON(r, 1<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	m := obj(body)
	if inner := obj(m["visit"]); inner != nil {
		m = inner
	}
	start, ok1 := parseTS(first(m, "started_at", "start_ts"))
	end, ok2 := parseTS(first(m, "ended_at", "end_ts"))
	if !ok1 || !ok2 || end < start {
		writeErr(w, http.StatusUnprocessableEntity, "started_at and ended_at are required")
		return
	}
	v := &store.Visit{UserID: u.ID, StartTS: start, EndTS: end, Status: store.VisitConfirmed, Manual: true, Name: str(m["name"]), Notes: str(m["notes"])}
	if pid, ok := num(m["place_id"]); ok && pid > 0 {
		p, err := s.store.GetPlace(ctx, u.ID, int64(pid))
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "no such place")
			return
		}
		v.PlaceID, v.Lat, v.Lng = &p.ID, p.Lat, p.Lng
	} else {
		lat, okA := num(first(m, "latitude", "lat"))
		lng, okB := num(first(m, "longitude", "lng", "lon"))
		if !okA || !okB || !geo.ValidCoord(lat, lng) {
			writeErr(w, http.StatusUnprocessableEntity, "a place_id or latitude and longitude are required")
			return
		}
		v.Lat, v.Lng = lat, lng
		p := &store.Place{UserID: u.ID, Name: v.Name, Renamed: v.Name != "", Kind: store.PlaceDiscovered, Lat: lat, Lng: lng, Radius: 75, Country: geo.CountryAt(lat, lng)}
		if err := s.store.CreatePlace(ctx, p); err == nil {
			v.PlaceID = &p.ID
			v.Name = ""
			s.kickGeocode()
		}
	}
	if err := s.store.CreateVisit(ctx, v); err != nil {
		s.fail(w, err, "create visit")
		return
	}
	go s.resummarize(context.WithoutCancel(ctx), u.ID, v.StartTS, v.EndTS)
	v, _ = s.store.GetVisit(ctx, u.ID, v.ID)
	writeJSON(w, http.StatusCreated, visitJSON(*v))
}

func (s *Server) handleDeleteVisit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	v, err := s.store.GetVisit(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get visit")
		return
	}
	// A detected visit comes back on the next rebuild unless declined.
	if !v.Manual {
		v.Status = store.VisitDeclined
		v.Place = nil
		err = s.store.UpdateVisit(ctx, v)
	} else {
		err = s.store.DeleteVisit(ctx, u.ID, v.ID)
	}
	if err != nil {
		s.fail(w, err, "delete visit")
		return
	}
	go s.resummarize(context.WithoutCancel(ctx), u.ID, v.StartTS, v.EndTS)
	w.WriteHeader(http.StatusNoContent)
}

// handleVisitPossiblePlaces lists the places near a visit (or a point) to
// choose from: your own first, then what the geocoder knows there.
func (s *Server) handleVisitPossiblePlaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var lat, lng float64
	if id := chi.URLParam(r, "id"); id != "" {
		v, err := s.store.GetVisit(ctx, u.ID, idParam(r, "id"))
		if err != nil {
			s.fail(w, err, "get visit")
			return
		}
		lat, lng = v.Lat, v.Lng
	} else {
		var ok1, ok2 bool
		lat, ok1 = num(r.URL.Query().Get("lat"))
		lng, ok2 = num(r.URL.Query().Get("lng"))
		if !ok1 || !ok2 {
			writeErr(w, http.StatusBadRequest, "lat and lng are required")
			return
		}
	}
	near, err := s.store.PlacesNear(ctx, u.ID, lat, lng, 400)
	if err != nil {
		s.fail(w, err, "places")
		return
	}
	sort.Slice(near, func(i, j int) bool {
		return geo.Haversine(lat, lng, near[i].Lat, near[i].Lng) < geo.Haversine(lat, lng, near[j].Lat, near[j].Lng)
	})
	out := make([]map[string]any, 0, len(near))
	for _, p := range near {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "latitude": p.Lat, "longitude": p.Lng, "lat": p.Lat, "lng": p.Lng, "kind": p.Kind,
			"category": p.Category, "address": p.Address, "distance": math.Round(geo.Haversine(lat, lng, p.Lat, p.Lng))})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- areas = saved places ----

func areaJSON(p store.Place) map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "latitude": strconv.FormatFloat(p.Lat, 'f', -1, 64), "longitude": strconv.FormatFloat(p.Lng, 'f', -1, 64),
		"radius": p.Radius, "category": p.Category, "color": p.Color}
}

func (s *Server) handleListAreas(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListPlaces(r.Context(), userFrom(r.Context()).ID, store.PlaceQuery{Kind: store.PlaceSaved, IncludeHidden: true, Sort: "name"})
	if err != nil {
		s.fail(w, err, "list areas")
		return
	}
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, areaJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateArea(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	body, err := readJSON(r, 1<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	m := obj(body)
	if inner := obj(m["area"]); inner != nil {
		m = inner
	}
	lat, ok1 := num(m["latitude"])
	lng, ok2 := num(m["longitude"])
	radius, _ := num(m["radius"])
	if !ok1 || !ok2 || str(m["name"]) == "" {
		writeErr(w, http.StatusUnprocessableEntity, "name, latitude and longitude are required")
		return
	}
	if radius <= 0 {
		radius = 100
	}
	p := &store.Place{UserID: u.ID, Name: str(m["name"]), Renamed: true, Kind: store.PlaceSaved, Lat: lat, Lng: lng, Radius: radius, Country: geo.CountryAt(lat, lng)}
	if err := s.store.CreatePlace(ctx, p); err != nil {
		s.fail(w, err, "create area")
		return
	}
	s.afterSavedPlaceChange(ctx, u.ID, p)
	writeJSON(w, http.StatusCreated, areaJSON(*p))
}

func (s *Server) handleDeleteArea(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if err := s.store.DeletePlace(ctx, u.ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete area")
		return
	}
	_ = s.store.MarkDirty(ctx, u.ID, 1, time.Now().Unix())
	s.kickRebuild()
	writeJSON(w, http.StatusOK, map[string]string{"message": "Area was successfully deleted"})
}

// ---- stats ----

var monthNames = []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"}

func (s *Server) handleDawarichStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	days, err := s.store.Days(ctx, u.ID, "", "")
	if err != nil {
		s.fail(w, err, "stats")
		return
	}
	cities, _ := s.store.VisitedCities(ctx, u.ID, 0, 0)
	type yr struct {
		dist      float64
		months    [12]float64
		countries map[string]bool
		cities    map[string]bool
	}
	years := map[int]*yr{}
	allCountries := map[string]bool{}
	var total float64
	var points int64
	for _, d := range days {
		y, _ := strconv.Atoi(d.Date[:4])
		m, _ := strconv.Atoi(d.Date[5:7])
		e := years[y]
		if e == nil {
			e = &yr{countries: map[string]bool{}, cities: map[string]bool{}}
			years[y] = e
		}
		e.dist += d.Distance
		e.months[m-1] += d.Distance
		total += d.Distance
		points += int64(d.Points)
		for _, c := range d.Countries {
			e.countries[c] = true
			allCountries[c] = true
		}
	}
	allCities := map[string]bool{}
	for _, c := range cities {
		key := c.Country + "/" + c.City
		allCities[key] = true
		for y := time.Unix(c.First, 0).Year(); y <= time.Unix(c.Last, 0).Year(); y++ {
			if e := years[y]; e != nil {
				e.cities[key] = true
			}
		}
	}
	var ys []int
	for y := range years {
		ys = append(ys, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ys)))
	yearly := make([]map[string]any, 0, len(ys))
	for _, y := range ys {
		e := years[y]
		monthly := map[string]int64{}
		for i, v := range e.months {
			monthly[monthNames[i]] = int64(v / 1000)
		}
		yearly = append(yearly, map[string]any{"year": y, "totalDistanceKm": int64(e.dist / 1000), "totalCountriesVisited": len(e.countries),
			"totalCitiesVisited": len(e.cities), "monthlyDistanceKm": monthly})
	}
	writeJSON(w, http.StatusOK, map[string]any{"totalDistanceKm": int64(total / 1000), "totalPointsTracked": points, "totalReverseGeocodedPoints": 0,
		"totalCountriesVisited": len(allCountries), "totalCitiesVisited": len(allCities), "yearlyStats": yearly})
}

// handleVisitedCities answers Dawarich's countries/visited_cities: per
// country the cities you stopped in, with how long.
func (s *Server) handleVisitedCities(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	from, to := tsParam(r, "start_at", 0), tsParam(r, "end_at", time.Now().Unix())
	cities, err := s.store.VisitedCities(ctx, u.ID, from, to)
	if err != nil {
		s.fail(w, err, "visited cities")
		return
	}
	byCountry := map[string][]map[string]any{}
	var order []string
	for _, c := range cities {
		name := firstNonEmpty(geo.CountryName(c.Country), c.Country)
		if _, ok := byCountry[name]; !ok {
			order = append(order, name)
		}
		byCountry[name] = append(byCountry[name], map[string]any{"city": c.City, "points": c.Visits, "timestamp": c.Last, "stayed_for": c.TotalS / 60})
	}
	data := make([]map[string]any, 0, len(order))
	for _, c := range order {
		data = append(data, map[string]any{"country": c, "cities": byCountry[c]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) handleCountryBorders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"type": "FeatureCollection", "features": []any{}})
}

func (s *Server) handleDawarichPhotos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// ---- timeline and tracks ----

// handleDawarichTimeline is GET /api/v1/timeline?start_at&end_at (≤ 31 days):
// per local day a summary and the visits and journeys in order.
func (s *Server) handleDawarichTimeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if r.URL.Query().Get("start_at") == "" || r.URL.Query().Get("end_at") == "" {
		writeErr(w, http.StatusBadRequest, "start_at and end_at are required")
		return
	}
	from, to := tsParam(r, "start_at", 0), tsParam(r, "end_at", 0)
	if to-from > 31*86400+3600 {
		writeErr(w, http.StatusBadRequest, "Date range cannot exceed 31 days")
		return
	}
	st, _ := s.store.GetSettings(ctx, u.ID)
	unit := firstNonEmpty(r.URL.Query().Get("distance_unit"), st.DistanceUnit)
	days, err := s.timelineDays(ctx, u.ID, from, to, s.engine.Location(st), unit)
	if err != nil {
		s.fail(w, err, "timeline")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

func distIn(m float64, unit string) float64 {
	if unit == "mi" {
		return math.Round(m/1609.344*100) / 100
	}
	return math.Round(m/1000*100) / 100
}

// timelineDays builds Dawarich timeline days for [from, to].
func (s *Server) timelineDays(ctx context.Context, userID, from, to int64, loc *time.Location, unit string) ([]map[string]any, error) {
	visits, err := s.store.Visits(ctx, userID, store.VisitQuery{From: from, To: to, NoDecline: true})
	if err != nil {
		return nil, err
	}
	tracks, err := s.store.Tracks(ctx, userID, from, to, false)
	if err != nil {
		return nil, err
	}
	type bucket struct {
		entries []map[string]any
		sort    []int64
		dist    float64
		moving  int64
		still   int64
		modes   map[string]float64
		sugg    int
		conf    int
		places  map[int64]bool
	}
	days := map[string]*bucket{}
	var order []string
	get := func(ts int64) *bucket {
		k := time.Unix(ts, 0).In(loc).Format("2006-01-02")
		b := days[k]
		if b == nil {
			b = &bucket{modes: map[string]float64{}, places: map[int64]bool{}}
			days[k] = b
			order = append(order, k)
		}
		return b
	}
	for _, v := range visits {
		if v.StartTS < from || v.StartTS > to {
			continue
		}
		b := get(v.StartTS)
		e := map[string]any{"type": "visit", "visit_id": v.ID, "name": v.DisplayName(), "editable_name": v.DisplayName(), "status": v.Status,
			"confidence_band": nil, "place_id": v.PlaceID, "point_count": v.Points, "tags": []any{}, "started_at": isoTime(v.StartTS), "ended_at": isoTime(v.EndTS),
			"duration": (v.EndTS - v.StartTS) / 60, "latitude": v.Lat, "longitude": v.Lng, "area": nil}
		if v.Place != nil {
			e["place"] = map[string]any{"id": v.Place.ID, "name": v.Place.Name, "lat": v.Place.Lat, "lng": v.Place.Lng, "address": v.Place.Address, "city": v.Place.City, "country": v.Place.Country}
			if v.Place.Kind == store.PlaceSaved {
				e["area"] = map[string]any{"id": v.Place.ID, "name": v.Place.Name, "lat": v.Place.Lat, "lng": v.Place.Lng, "radius": v.Place.Radius}
			}
			b.places[v.Place.ID] = true
		} else {
			e["place"] = nil
		}
		b.entries = append(b.entries, e)
		b.sort = append(b.sort, v.StartTS)
		b.still += (v.EndTS - v.StartTS) / 60
		if v.Status == store.VisitConfirmed {
			b.conf++
		} else {
			b.sugg++
		}
	}
	for _, t := range tracks {
		if t.StartTS < from || t.StartTS > to {
			continue
		}
		b := get(t.StartTS)
		dur := (t.EndTS - t.StartTS) / 60
		e := map[string]any{"type": "journey", "track_id": t.ID, "started_at": isoTime(t.StartTS), "ended_at": isoTime(t.EndTS), "duration": dur,
			"distance": distIn(t.Distance, unit), "distance_unit": unit, "dominant_mode": t.Mode, "avg_speed": math.Round(t.AvgSpeed*3.6*10) / 10,
			"elevation_gain": math.Round(t.ElevationGain), "point_count": t.Points, "latitude": t.StartLat, "longitude": t.StartLng,
			"end_latitude": t.EndLat, "end_longitude": t.EndLng, "countries": splitNonEmpty(t.Countries)}
		b.entries = append(b.entries, e)
		b.sort = append(b.sort, t.StartTS)
		b.dist += t.Distance
		b.moving += dur
		b.modes[t.Mode] += distIn(t.Distance, unit)
	}
	sort.Strings(order)
	out := make([]map[string]any, 0, len(order))
	for _, k := range order {
		b := days[k]
		idx := make([]int, len(b.entries))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool { return b.sort[idx[i]] < b.sort[idx[j]] })
		entries := make([]map[string]any, len(idx))
		for i, j := range idx {
			entries[i] = b.entries[j]
		}
		out = append(out, map[string]any{"date": k, "summary": map[string]any{"total_distance": distIn(b.dist, unit), "distance_unit": unit,
			"places_visited": len(b.places), "time_moving_minutes": b.moving, "time_stationary_minutes": b.still, "mode_distances": b.modes,
			"suggested_count": b.sugg, "confirmed_count": b.conf}, "entries": entries})
	}
	return out, nil
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// handleDawarichTracks lists journeys as GeoJSON features with a LineString.
func (s *Server) handleDawarichTracks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	from, to := tsParam(r, "start_at", time.Now().Add(-24*time.Hour).Unix()), tsParam(r, "end_at", time.Now().Unix())
	tracks, err := s.store.Tracks(ctx, u.ID, from, to, true)
	if err != nil {
		s.fail(w, err, "tracks")
		return
	}
	feats := make([]map[string]any, 0, len(tracks))
	for _, t := range tracks {
		coords := [][]float64{}
		for _, p := range geo.DecodePolyline(t.Polyline) {
			coords = append(coords, []float64{p.Lng, p.Lat})
		}
		feats = append(feats, map[string]any{"type": "Feature", "geometry": map[string]any{"type": "LineString", "coordinates": coords},
			"properties": map[string]any{"id": t.ID, "start_at": isoTime(t.StartTS), "end_at": isoTime(t.EndTS), "distance": math.Round(t.Distance),
				"avg_speed": math.Round(t.AvgSpeed*3.6*10) / 10, "duration": t.EndTS - t.StartTS, "dominant_mode": t.Mode, "elevation_gain": t.ElevationGain}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "FeatureCollection", "features": feats})
}
