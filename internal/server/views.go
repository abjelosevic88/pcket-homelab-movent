package server

import (
	"context"
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

// ---- shared shapes ----

func trackJSON(t store.Track, poly string) map[string]any {
	return map[string]any{"id": t.ID, "start_ts": t.StartTS, "end_ts": t.EndTS, "distance": math.Round(t.Distance), "points": t.Points,
		"avg_speed": round1(t.AvgSpeed * 3.6), "max_speed": round1(t.MaxSpeed * 3.6), "elevation_gain": math.Round(t.ElevationGain),
		"elevation_loss": math.Round(t.ElevationLoss), "mode": t.Mode, "mode_manual": t.ModeManual, "start": []float64{t.StartLat, t.StartLng},
		"end": []float64{t.EndLat, t.EndLng}, "polyline": poly, "countries": splitNonEmpty(t.Countries)}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func placeJSON(p *store.Place) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind, "category": p.Category, "color": p.Color, "lat": p.Lat, "lng": p.Lng,
		"radius": p.Radius, "address": p.Address, "city": p.City, "country": p.Country, "country_name": geo.CountryName(p.Country),
		"geocoded": p.Geocoded, "renamed": p.Renamed, "hidden": p.Hidden, "notes": p.Notes, "visits": p.Visits, "total_s": p.TotalS,
		"first_visit": p.FirstVisit, "last_visit": p.LastVisit}
}

func visitView(v store.Visit) map[string]any {
	return map[string]any{"id": v.ID, "start_ts": v.StartTS, "end_ts": v.EndTS, "lat": v.Lat, "lng": v.Lng, "radius": math.Round(v.Radius),
		"points": v.Points, "status": v.Status, "name": v.DisplayName(), "own_name": v.Name, "notes": v.Notes, "manual": v.Manual,
		"place_id": v.PlaceID, "place": placeJSON(v.Place)}
}

// rangeParams reads from/to as unix seconds or dates; default today.
func (s *Server) rangeParams(r *http.Request, loc *time.Location) (int64, int64) {
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from := today.Unix()
	to := today.AddDate(0, 0, 1).Unix() - 1
	parse := func(v string, end bool) (int64, bool) {
		if v == "" {
			return 0, false
		}
		if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
			if end {
				return t.AddDate(0, 0, 1).Unix() - 1, true
			}
			return t.Unix(), true
		}
		return parseTS(v)
	}
	if v, ok := parse(r.URL.Query().Get("from"), false); ok {
		from = v
	}
	if v, ok := parse(r.URL.Query().Get("to"), true); ok {
		to = v
	}
	if to < from {
		from, to = to, from
	}
	return from, to
}

func (s *Server) userLoc(ctx context.Context, userID int64) (store.Settings, *time.Location) {
	st, _ := s.store.GetSettings(ctx, userID)
	return st, s.engine.Location(st)
}

// ---- bootstrap ----

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, loc := s.userLoc(ctx, u.ID)
	first, last, count := s.store.PointBounds(ctx, u.ID)
	devices, _ := s.store.ListDevices(ctx, u.ID)
	for i := range devices {
		devices[i].APIKey = ""
	}
	home, _ := s.store.HomePlace(ctx, u.ID)
	shared, _ := s.store.SharesWithViewer(ctx, u.ID)
	firstDay, lastDay := s.store.DayBounds(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": u, "settings": st, "timezone": loc.String(), "version": Version, "base_url": s.publicURL(r),
		"points": count, "first_ts": first, "last_ts": last, "first_day": firstDay, "last_day": lastDay,
		"devices": devices, "home": placeJSON(home), "shared_with_me": shared, "pending": s.store.PendingDirty(ctx, u.ID),
		"geocoder": s.geocoder.Enabled(), "core": s.coreStatus(), "modes": modesList(),
	})
}

func modesList() []string {
	return []string{"walking", "running", "cycling", "driving", "bus", "train", "boat", "flying", "unknown"}
}

// publicURL is the address phones should use.
func (s *Server) publicURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- map ----

// simplifyFor re-simplifies a stored polyline for a long range, so a year
// of journeys is a few hundred kilobytes rather than many megabytes.
func simplifyFor(poly string, span int64) string {
	var tol float64
	switch {
	case span <= 3*86400:
		return poly
	case span <= 32*86400:
		tol = 12
	case span <= 370*86400:
		tol = 40
	default:
		tol = 120
	}
	return geo.EncodePolyline(geo.Simplify(geo.DecodePolyline(poly), tol))
}

// handleMap returns what the map draws for a range: journeys as encoded
// polylines, visits, totals, and — while new points are still waiting to be
// sorted into journeys — the raw route so nothing looks missing.
func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	_, loc := s.userLoc(ctx, u.ID)
	from, to := s.rangeParams(r, loc)
	s.writeMap(w, r, u.ID, from, to, true)
}

func (s *Server) writeMap(w http.ResponseWriter, r *http.Request, userID, from, to int64, withVisits bool) {
	ctx := r.Context()
	tracks, err := s.store.Tracks(ctx, userID, from, to, true)
	if err != nil {
		s.fail(w, err, "tracks")
		return
	}
	span := to - from
	outTracks := make([]map[string]any, 0, len(tracks))
	var dist float64
	modes := map[string]float64{}
	minLat, minLng, maxLat, maxLng := 90.0, 180.0, -90.0, -180.0
	grow := func(lat, lng float64) {
		minLat, maxLat = math.Min(minLat, lat), math.Max(maxLat, lat)
		minLng, maxLng = math.Min(minLng, lng), math.Max(maxLng, lng)
	}
	var lastTrackEnd int64
	for _, t := range tracks {
		outTracks = append(outTracks, trackJSON(t, simplifyFor(t.Polyline, span)))
		dist += t.Distance
		modes[t.Mode] += t.Distance
		for _, p := range geo.DecodePolyline(t.Polyline) {
			grow(p.Lat, p.Lng)
		}
		if t.EndTS > lastTrackEnd {
			lastTrackEnd = t.EndTS
		}
	}
	visits := []map[string]any{}
	if withVisits {
		vs, err := s.store.Visits(ctx, userID, store.VisitQuery{From: from, To: to, NoDecline: true})
		if err != nil {
			s.fail(w, err, "visits")
			return
		}
		for _, v := range vs {
			visits = append(visits, visitView(v))
			grow(v.Lat, v.Lng)
		}
	}
	// Points newer than the last journey that are not yet analysed: the raw line.
	pending := s.store.PendingDirty(ctx, userID)
	rawFrom := from
	if lastTrackEnd > from && !pending {
		rawFrom = lastTrackEnd
	}
	raw := ""
	if pending || len(tracks) == 0 || to-rawFrom < 2*86400 {
		if pts, err := s.store.SlimPoints(ctx, userID, rawFrom, to, false); err == nil && len(pts) > 1 {
			var line []geo.Pt
			for _, p := range pts {
				line = append(line, geo.Pt{Lat: p.Lat, Lng: p.Lng, TS: p.TS})
				grow(p.Lat, p.Lng)
			}
			tol := 5.0
			if span > 3*86400 {
				tol = 30
			}
			if pending || len(tracks) == 0 {
				raw = geo.EncodePolyline(geo.Simplify(line, tol))
			} else if len(line) > 1 && line[len(line)-1].TS-lastTrackEnd > 0 {
				raw = geo.EncodePolyline(geo.Simplify(line, tol))
			}
		}
	}
	var bounds any
	if minLat <= maxLat {
		bounds = []float64{minLat, minLng, maxLat, maxLng}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "tracks": outTracks, "visits": visits, "raw": raw, "pending": pending,
		"distance": math.Round(dist), "modes": modes, "bounds": bounds})
}

// handleMapPoints returns the points of a range as compact rows
// [id, lat, lng, ts, speed km/h, accuracy, altitude, outlier] — evenly
// thinned to `limit` (default 20000) so a whole year still draws.
func (s *Server) handleMapPoints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	_, loc := s.userLoc(ctx, u.ID)
	from, to := s.rangeParams(r, loc)
	limit := queryInt(r, "limit", 20000)
	if limit <= 0 || limit > 200000 {
		limit = 20000
	}
	withOutliers := queryBool(r, "outliers")
	var rows [][]any
	total := 0
	err := s.store.EachPoint(ctx, u.ID, store.PointQuery{From: from, To: to, IncludeOutliers: withOutliers}, func(p store.Point) error {
		total++
		var sp, acc, alt any
		if p.Speed != nil {
			sp = round1(*p.Speed * 3.6)
		}
		if p.Accuracy != nil {
			acc = math.Round(*p.Accuracy)
		}
		if p.Altitude != nil {
			alt = math.Round(*p.Altitude)
		}
		rows = append(rows, []any{p.ID, p.Lat, p.Lng, p.TS, sp, acc, alt, p.Flags & store.FlagOutlier})
		return nil
	})
	if err != nil {
		s.fail(w, err, "points")
		return
	}
	if len(rows) > limit {
		step := float64(len(rows)) / float64(limit)
		thin := make([][]any, 0, limit)
		for f := 0.0; int(f) < len(rows); f += step {
			thin = append(thin, rows[int(f)])
		}
		rows = thin
	}
	if rows == nil {
		rows = [][]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "points": rows})
}

// handleMapHeat bins points into ~cell-metre squares: [lat, lng, count].
// The heat map and the fog of war both draw from it.
func (s *Server) handleMapHeat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	_, loc := s.userLoc(ctx, u.ID)
	from, to := s.rangeParams(r, loc)
	if r.URL.Query().Get("from") == "" && r.URL.Query().Get("all") != "" {
		from, to = 0, time.Now().Unix()
	}
	cell := float64(queryInt(r, "cell", 150))
	if cell < 20 {
		cell = 20
	}
	pts, err := s.store.SlimPoints(ctx, u.ID, from, to, false)
	if err != nil {
		s.fail(w, err, "points")
		return
	}
	type key [2]int64
	type acc struct {
		lat, lng float64
		n        int
	}
	bins := map[key]*acc{}
	for _, p := range pts {
		a, b := geo.CellKey(p.Lat, p.Lng, cell)
		k := key{a, b}
		e := bins[k]
		if e == nil {
			e = &acc{}
			bins[k] = e
		}
		e.n++
		e.lat += (p.Lat - e.lat) / float64(e.n)
		e.lng += (p.Lng - e.lng) / float64(e.n)
	}
	out := make([][3]float64, 0, len(bins))
	for _, e := range bins {
		out = append(out, [3]float64{math.Round(e.lat*1e5) / 1e5, math.Round(e.lng*1e5) / 1e5, float64(e.n)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"cell": cell, "cells": out, "points": len(pts)})
}

// ---- one day ----

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	s.writeDay(w, r, u.ID, chi.URLParam(r, "date"), true)
}

func (s *Server) writeDay(w http.ResponseWriter, r *http.Request, userID int64, date string, full bool) {
	ctx := r.Context()
	st, loc := s.userLoc(ctx, userID)
	d0, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	from, to := d0.Unix(), d0.AddDate(0, 0, 1).Unix()-1
	tracks, err := s.store.Tracks(ctx, userID, from, to, true)
	if err != nil {
		s.fail(w, err, "tracks")
		return
	}
	visits, err := s.store.Visits(ctx, userID, store.VisitQuery{From: from, To: to})
	if err != nil {
		s.fail(w, err, "visits")
		return
	}
	type entry struct {
		ts int64
		v  map[string]any
	}
	var entries []entry
	for _, v := range visits {
		if v.Status == store.VisitDeclined && !full {
			continue
		}
		m := visitView(v)
		m["type"] = "visit"
		entries = append(entries, entry{v.StartTS, m})
	}
	for _, t := range tracks {
		m := trackJSON(t, t.Polyline)
		m["type"] = "journey"
		entries = append(entries, entry{t.StartTS, m})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts < entries[j].ts })
	list := make([]map[string]any, len(entries))
	for i, e := range entries {
		list[i] = e.v
	}
	var summary any
	if days, _ := s.store.Days(ctx, userID, date, date); len(days) == 1 {
		summary = days[0]
	}
	// Neighbouring days with data, for the arrows.
	var prev, next string
	_ = s.store.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(date),'') FROM days WHERE user_id = ? AND date < ?`, userID, date).Scan(&prev)
	_ = s.store.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(date),'') FROM days WHERE user_id = ? AND date > ?`, userID, date).Scan(&next)
	writeJSON(w, http.StatusOK, map[string]any{"date": date, "from": from, "to": to, "summary": summary, "entries": list, "prev": prev, "next": next,
		"unit": st.DistanceUnit, "pending": s.store.PendingDirty(ctx, userID)})
}

// ---- calendar ----

// handleCalendar lists every day with data in a year (or from/to dates).
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if y := r.URL.Query().Get("year"); y != "" {
		from, to = y+"-01-01", y+"-12-31"
	}
	days, err := s.store.Days(ctx, u.ID, from, to)
	if err != nil {
		s.fail(w, err, "days")
		return
	}
	writeJSON(w, http.StatusOK, days)
}

// ---- insights ----

type placeTime struct {
	Place  map[string]any `json:"place"`
	Visits int            `json:"visits"`
	TotalS int64          `json:"total_s"`
}

// handleInsights is the report for a range (a year by default): totals,
// months, modes, weekdays, countries and cities, top places, records, and
// how the time split between home, work, elsewhere and on the move.
func (s *Server) handleInsights(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	out, err := s.insights(ctx, u.ID, r.URL.Query().Get("year"), r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		s.fail(w, err, "insights")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) insights(ctx context.Context, userID int64, year, fromDate, toDate string) (map[string]any, error) {
	st, loc := s.userLoc(ctx, userID)
	if year == "" && fromDate == "" {
		year = strconv.Itoa(time.Now().In(loc).Year())
	}
	if year != "" {
		fromDate, toDate = year+"-01-01", year+"-12-31"
	}
	if toDate == "" {
		toDate = time.Now().In(loc).Format("2006-01-02")
	}
	f0, err := time.ParseInLocation("2006-01-02", fromDate, loc)
	if err != nil {
		return nil, errBad("from must be YYYY-MM-DD")
	}
	t0, err := time.ParseInLocation("2006-01-02", toDate, loc)
	if err != nil {
		return nil, errBad("to must be YYYY-MM-DD")
	}
	from, to := f0.Unix(), t0.AddDate(0, 0, 1).Unix()-1
	days, err := s.store.Days(ctx, userID, fromDate, toDate)
	if err != nil {
		return nil, err
	}
	visits, err := s.store.Visits(ctx, userID, store.VisitQuery{From: from, To: to, NoDecline: true})
	if err != nil {
		return nil, err
	}
	tracks, err := s.store.Tracks(ctx, userID, from, to, false)
	if err != nil {
		return nil, err
	}
	cities, _ := s.store.VisitedCities(ctx, userID, from, to)
	// First visit ever per place, to tell new places from old haunts.
	firstSeen := map[int64]int64{}
	if rows, err := s.store.DB.QueryContext(ctx, `SELECT place_id, MIN(start_ts) FROM visits WHERE user_id = ? AND status <> 'declined' AND place_id IS NOT NULL GROUP BY place_id`, userID); err == nil {
		for rows.Next() {
			var id, ts int64
			if rows.Scan(&id, &ts) == nil {
				firstSeen[id] = ts
			}
		}
		rows.Close()
	}
	// Countries: first ever day per country too.
	everCountry := map[string]string{}
	if rows, err := s.store.DB.QueryContext(ctx, `SELECT date, countries FROM days WHERE user_id = ? AND countries <> '' ORDER BY date`, userID); err == nil {
		for rows.Next() {
			var d, cs string
			if rows.Scan(&d, &cs) == nil {
				for _, c := range strings.Split(cs, ",") {
					if _, ok := everCountry[c]; !ok {
						everCountry[c] = d
					}
				}
			}
		}
		rows.Close()
	}

	type month struct {
		Month     string  `json:"month"`
		Distance  float64 `json:"distance"`
		Days      int     `json:"days"`
		Countries int     `json:"countries"`
		MovingS   int64   `json:"moving_s"`
		Visits    int     `json:"visits"`
	}
	months := map[string]*month{}
	for m := time.Date(f0.Year(), f0.Month(), 1, 0, 0, 0, 0, loc); !m.After(t0); m = m.AddDate(0, 1, 0) {
		k := m.Format("2006-01")
		months[k] = &month{Month: k}
	}
	monthCountries := map[string]map[string]bool{}
	weekday := make([]float64, 7)
	weekdayDays := make([]int, 7)
	modes := map[string]float64{}
	type country struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		Continent string `json:"continent"`
		Days      int    `json:"days"`
		First     string `json:"first"`
		Last      string `json:"last"`
		New       bool   `json:"new"`
	}
	countries := map[string]*country{}
	var corder []string
	var total float64
	var moving int64
	var points int
	var longestDay *store.Day
	var furthest *store.Day
	var mostCountries *store.Day
	streak, best, bestEnd := 0, 0, ""
	var prevDate time.Time
	for i := range days {
		d := &days[i]
		total += d.Distance
		moving += d.MovingS
		points += d.Points
		mk := d.Date[:7]
		if m := months[mk]; m != nil {
			m.Distance += d.Distance
			m.Days++
			m.MovingS += d.MovingS
			m.Visits += d.Visits
		}
		if monthCountries[mk] == nil {
			monthCountries[mk] = map[string]bool{}
		}
		for _, c := range d.Countries {
			monthCountries[mk][c] = true
			e := countries[c]
			if e == nil {
				info, _ := geo.CountryInfo(c)
				e = &country{Code: c, Name: firstNonEmpty(info.Name, c), Continent: info.Continent, First: d.Date, New: everCountry[c] >= fromDate}
				countries[c] = e
				corder = append(corder, c)
			}
			e.Days++
			e.Last = d.Date
		}
		for m, v := range d.Modes {
			modes[m] += v
		}
		dt, _ := time.ParseInLocation("2006-01-02", d.Date, loc)
		weekday[int(dt.Weekday())] += d.Distance
		weekdayDays[int(dt.Weekday())]++
		if longestDay == nil || d.Distance > longestDay.Distance {
			longestDay = d
		}
		if furthest == nil || d.MaxFromHome > furthest.MaxFromHome {
			furthest = d
		}
		if mostCountries == nil || len(d.Countries) > len(mostCountries.Countries) {
			mostCountries = d
		}
		if !prevDate.IsZero() && dt.Sub(prevDate) < 36*time.Hour {
			streak++
		} else {
			streak = 1
		}
		if streak > best {
			best, bestEnd = streak, d.Date
		}
		prevDate = dt
	}
	for k, cs := range monthCountries {
		if m := months[k]; m != nil {
			m.Countries = len(cs)
		}
	}
	monthList := make([]*month, 0, len(months))
	for _, m := range months {
		monthList = append(monthList, m)
	}
	sort.Slice(monthList, func(i, j int) bool { return monthList[i].Month < monthList[j].Month })
	for _, m := range monthList {
		m.Distance = math.Round(m.Distance)
	}
	countryList := make([]*country, 0, len(corder))
	for _, c := range corder {
		countryList = append(countryList, countries[c])
	}
	sort.SliceStable(countryList, func(i, j int) bool { return countryList[i].Days > countryList[j].Days })

	// Places and the time split.
	byPlace := map[int64]*placeTime{}
	var homeS, workS, otherS int64
	newPlaces := 0
	for _, v := range visits {
		dur := minI64(v.EndTS, to) - maxI64(v.StartTS, from)
		if dur < 0 {
			dur = 0
		}
		cat := ""
		if v.Place != nil {
			cat = v.Place.Category
			e := byPlace[v.Place.ID]
			if e == nil {
				e = &placeTime{Place: placeJSON(v.Place)}
				byPlace[v.Place.ID] = e
				if fs, ok := firstSeen[v.Place.ID]; ok && fs >= from {
					newPlaces++
				}
			}
			e.Visits++
			e.TotalS += dur
		}
		switch cat {
		case "home":
			homeS += dur
		case "work", "school":
			workS += dur
		default:
			otherS += dur
		}
	}
	top := make([]*placeTime, 0, len(byPlace))
	for _, p := range byPlace {
		top = append(top, p)
	}
	sort.Slice(top, func(i, j int) bool { return top[i].TotalS > top[j].TotalS })
	if len(top) > 15 {
		top = top[:15]
	}
	var movingTracks int64
	var longestTrack, fastestTrack *store.Track
	trackModes := map[string]int{}
	for i := range tracks {
		t := &tracks[i]
		movingTracks += t.EndTS - t.StartTS
		trackModes[t.Mode]++
		if longestTrack == nil || t.Distance > longestTrack.Distance {
			longestTrack = t
		}
		if t.Mode != "flying" && (fastestTrack == nil || t.MaxSpeed > fastestTrack.MaxSpeed) {
			fastestTrack = t
		}
	}
	// The split covers the time you were tracked: from the first point in
	// the range (not 1 January) to its end or now.
	start, end := from, to
	if len(days) > 0 && days[0].FirstTS > start {
		start = days[0].FirstTS
	}
	if now := time.Now().Unix(); end > now {
		end = now
	}
	span := end - start
	unknown := span - homeS - workS - otherS - movingTracks
	if unknown < 0 {
		unknown = 0
	}
	records := map[string]any{}
	if longestDay != nil && longestDay.Distance > 0 {
		records["longest_day"] = map[string]any{"date": longestDay.Date, "distance": math.Round(longestDay.Distance)}
	}
	if furthest != nil && furthest.MaxFromHome > 0 {
		records["furthest_from_home"] = map[string]any{"date": furthest.Date, "distance": math.Round(furthest.MaxFromHome)}
	}
	if mostCountries != nil && len(mostCountries.Countries) > 1 {
		records["most_countries"] = map[string]any{"date": mostCountries.Date, "countries": mostCountries.Countries}
	}
	if longestTrack != nil {
		records["longest_journey"] = trackJSON(*longestTrack, "")
	}
	if fastestTrack != nil && fastestTrack.MaxSpeed > 0 {
		records["fastest_journey"] = trackJSON(*fastestTrack, "")
	}
	if best > 1 {
		records["longest_streak"] = map[string]any{"days": best, "ended": bestEnd}
	}
	wd := make([]map[string]any, 7)
	for i := 0; i < 7; i++ {
		avg := 0.0
		if weekdayDays[i] > 0 {
			avg = weekday[i] / float64(weekdayDays[i])
		}
		wd[i] = map[string]any{"weekday": time.Weekday(i).String(), "distance": math.Round(weekday[i]), "days": weekdayDays[i], "avg": math.Round(avg)}
	}
	for k, v := range modes {
		modes[k] = math.Round(v)
	}
	return map[string]any{
		"from": fromDate, "to": toDate, "unit": st.DistanceUnit,
		"totals": map[string]any{"distance": math.Round(total), "days": len(days), "points": points, "moving_s": moving, "journeys": len(tracks),
			"visits": len(visits), "places": len(byPlace), "new_places": newPlaces, "countries": len(countries), "cities": len(cities)},
		"months": monthList, "modes": modes, "journeys_by_mode": trackModes, "weekdays": wd, "countries": countryList, "cities": cities,
		"top_places": top, "records": records,
		"time_split": map[string]int64{"home": homeS, "work": workS, "elsewhere": otherS, "moving": movingTracks, "unknown": unknown},
	}, nil
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---- countries (all time) ----

func (s *Server) handleCountries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	rows, err := s.store.DB.QueryContext(ctx, `SELECT date, countries FROM days WHERE user_id = ? AND countries <> '' ORDER BY date`, u.ID)
	if err != nil {
		s.fail(w, err, "countries")
		return
	}
	defer rows.Close()
	type c struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		Continent string `json:"continent"`
		Days      int    `json:"days"`
		First     string `json:"first"`
		Last      string `json:"last"`
		Years     []int  `json:"years"`
	}
	m := map[string]*c{}
	var order []string
	for rows.Next() {
		var d, cs string
		if rows.Scan(&d, &cs) != nil {
			continue
		}
		y, _ := strconv.Atoi(d[:4])
		for _, code := range strings.Split(cs, ",") {
			e := m[code]
			if e == nil {
				info, _ := geo.CountryInfo(code)
				e = &c{Code: code, Name: firstNonEmpty(info.Name, code), Continent: info.Continent, First: d}
				m[code] = e
				order = append(order, code)
			}
			e.Days++
			e.Last = d
			if len(e.Years) == 0 || e.Years[len(e.Years)-1] != y {
				e.Years = append(e.Years, y)
			}
		}
	}
	out := make([]*c, 0, len(order))
	continents := map[string]bool{}
	for _, k := range order {
		out = append(out, m[k])
		if m[k].Continent != "" {
			continents[m[k].Continent] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"countries": out, "continents": len(continents), "world": 195})
}

func (s *Server) handleCountryShape(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(chi.URLParam(r, "code"))
	shape := geo.CountryShape(code)
	if shape == nil {
		writeErr(w, http.StatusNotFound, "unknown country")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "name": geo.CountryName(code), "rings": shape})
}

// ---- now ----

// handleNow is "where am I": the newest point, the visit or journey it
// belongs to, every device's state, and today so far.
func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	_, loc := s.userLoc(ctx, u.ID)
	out := map[string]any{"point": nil, "visit": nil, "journey": nil}
	if p, err := s.store.LatestPoint(ctx, u.ID); err == nil {
		out["point"] = map[string]any{"lat": p.Lat, "lng": p.Lng, "ts": p.TS, "accuracy": p.Accuracy, "speed": p.Speed, "battery": p.Battery,
			"battery_state": p.BatteryState, "country": p.Country, "country_name": geo.CountryName(p.Country), "source": p.Source}
		if vs, _ := s.store.Visits(ctx, u.ID, store.VisitQuery{From: p.TS - 60, To: p.TS + 60, NoDecline: true}); len(vs) > 0 {
			out["visit"] = visitView(vs[len(vs)-1])
		} else if ts, _ := s.store.Tracks(ctx, u.ID, p.TS-60, p.TS+60, false); len(ts) > 0 {
			out["journey"] = trackJSON(ts[len(ts)-1], "")
		}
	}
	devices, _ := s.store.ListDevices(ctx, u.ID)
	for i := range devices {
		devices[i].APIKey = ""
	}
	out["devices"] = devices
	today := time.Now().In(loc).Format("2006-01-02")
	if d, _ := s.store.Days(ctx, u.ID, today, today); len(d) == 1 {
		out["today"] = d[0]
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- summaries for Pocket AI, Day and the MCP tools ----

// handleSummary describes a day (date=) or a range (from/to) in a compact
// form: distance, journeys with modes, the places in order, countries.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, loc := s.userLoc(ctx, u.ID)
	date := r.URL.Query().Get("date")
	var from, to int64
	if date != "" {
		d0, err := time.ParseInLocation("2006-01-02", date, loc)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
			return
		}
		from, to = d0.Unix(), d0.AddDate(0, 0, 1).Unix()-1
	} else {
		from, to = s.rangeParams(r, loc)
	}
	if to-from > 92*86400 {
		writeErr(w, http.StatusBadRequest, "at most 92 days; use /insights for longer")
		return
	}
	days, err := s.timelineDays(ctx, u.ID, from, to, loc, st.DistanceUnit)
	if err != nil {
		s.fail(w, err, "summary")
		return
	}
	var dist float64
	countries := map[string]bool{}
	sum, _ := s.store.Days(ctx, u.ID, time.Unix(from, 0).In(loc).Format("2006-01-02"), time.Unix(to, 0).In(loc).Format("2006-01-02"))
	var pts int
	for _, d := range sum {
		dist += d.Distance
		pts += d.Points
		for _, c := range d.Countries {
			countries[c] = true
		}
	}
	cl := make([]string, 0, len(countries))
	for c := range countries {
		cl = append(cl, geo.CountryName(c))
	}
	sort.Strings(cl)
	writeJSON(w, http.StatusOK, map[string]any{"from": isoTime(from), "to": isoTime(to), "timezone": loc.String(), "distance_km": round1(dist / 1000),
		"points": pts, "countries": cl, "days": days})
}

// handleWhere answers "where was I at …": the visit or journey at that
// instant, or the nearest recorded point.
func (s *Server) handleWhere(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	_, loc := s.userLoc(ctx, u.ID)
	at := r.URL.Query().Get("at")
	ts, ok := parseTS(at)
	if !ok {
		if t, err := time.ParseInLocation("2006-01-02T15:04", at, loc); err == nil {
			ts, ok = t.Unix(), true
		} else if t, err := time.ParseInLocation("2006-01-02 15:04", at, loc); err == nil {
			ts, ok = t.Unix(), true
		}
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "at must be a time (2026-10-20T14:30, ISO 8601 or unix seconds)")
		return
	}
	out := map[string]any{"at": isoTime(ts), "local": time.Unix(ts, 0).In(loc).Format("2006-01-02 15:04"), "visit": nil, "journey": nil, "nearest_point": nil}
	if vs, _ := s.store.Visits(ctx, u.ID, store.VisitQuery{From: ts, To: ts, NoDecline: true}); len(vs) > 0 {
		out["visit"] = visitView(vs[0])
	}
	if t, _ := s.store.Tracks(ctx, u.ID, ts, ts, false); len(t) > 0 {
		out["journey"] = trackJSON(t[0], "")
	}
	var best *store.Point
	for _, desc := range []bool{true, false} {
		q := store.PointQuery{To: ts, Desc: true, Limit: 1}
		if !desc {
			q = store.PointQuery{From: ts, Limit: 1}
		}
		if pts, _ := s.store.Points(ctx, u.ID, q); len(pts) == 1 {
			if best == nil || absI(pts[0].TS-ts) < absI(best.TS-ts) {
				p := pts[0]
				best = &p
			}
		}
	}
	if best != nil {
		out["nearest_point"] = map[string]any{"lat": best.Lat, "lng": best.Lng, "ts": best.TS, "seconds_away": absI(best.TS - ts), "country": geo.CountryName(best.Country)}
	}
	writeJSON(w, http.StatusOK, out)
}

func absI(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// handleSearch finds places (by name, address, city) and the visits there.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"places": []any{}})
		return
	}
	ps, err := s.store.ListPlaces(ctx, u.ID, store.PlaceQuery{Q: q, IncludeHidden: true, Limit: queryInt(r, "limit", 25)})
	if err != nil {
		s.fail(w, err, "search")
		return
	}
	out := make([]map[string]any, 0, len(ps))
	for i := range ps {
		out = append(out, placeJSON(&ps[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"places": out})
}

func (s *Server) handleGeocodeSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	res, err := s.geocoder.Search(r.Context(), q, 8)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
