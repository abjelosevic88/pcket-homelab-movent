package analyze

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// Engine rebuilds derived data for a person's time range.
type Engine struct {
	Store *store.Store
	// DefaultTZ is used when the person has not chosen a time zone.
	DefaultTZ *time.Location
}

// Location returns the person's time zone.
func (e *Engine) Location(st store.Settings) *time.Location {
	if st.Timezone != "" {
		if loc, err := time.LoadLocation(st.Timezone); err == nil {
			return loc
		}
	}
	if e.DefaultTZ != nil {
		return e.DefaultTZ
	}
	return time.Local
}

// Result reports what a rebuild produced.
type Result struct {
	From, To int64
	Tracks   int
	Visits   int
	Days     int
	NewStays []store.Visit // visits created by this rebuild
}

// Rebuild recomputes outliers, journeys, visits and day summaries for every
// point between from and to (unix seconds), widened so nothing is cut in half.
func (e *Engine) Rebuild(ctx context.Context, userID, from, to int64) (Result, error) {
	st, err := e.Store.GetSettings(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	prm := ParamsFrom(st)
	loc := e.Location(st)

	// Some context on both sides, then out to the edges of anything crossing.
	from -= 6 * 3600
	to += 6 * 3600
	from, to = e.Store.DerivedBounds(ctx, userID, from, to)

	raw, err := e.Store.Points(ctx, userID, store.PointQuery{From: from, To: to, IncludeOutliers: true})
	if err != nil {
		return Result{}, err
	}
	all := make([]P, len(raw))
	for i, r := range raw {
		all[i] = toP(r)
	}
	out := Outliers(all, prm)
	flags := map[int64]int{}
	clean := make([]P, 0, len(all))
	for i, p := range all {
		f := p.Flags &^ store.FlagOutlier
		if out[i] {
			f |= store.FlagOutlier
		} else {
			clean = append(clean, p)
		}
		if f != p.Flags {
			flags[p.ID] = f
		}
	}

	stays, moves := Segment(clean, prm)

	// Journeys, with hand-set modes carried over.
	overrides, err := e.Store.TrackOverrides(ctx, userID, from, to)
	if err != nil {
		return Result{}, err
	}
	tracks := make([]store.Track, 0, len(moves))
	for _, m := range moves {
		t := TrackFrom(m, prm.Modes)
		for _, o := range overrides {
			if overlap(t.StartTS, t.EndTS, o.StartTS, o.EndTS) > 0.5*float64(maxI(1, t.EndTS-t.StartTS)) {
				t.Mode, t.ModeManual = o.Mode, true
			}
		}
		tracks = append(tracks, t)
	}

	visits, keep, created, err := e.reconcileVisits(ctx, userID, from, to, stays, prm)
	if err != nil {
		return Result{}, err
	}
	rb := store.Rebuild{From: from, To: to, Tracks: tracks, Visits: visits, KeepIDs: keep, Flags: flags}
	if err := e.Store.ApplyRebuild(ctx, userID, rb); err != nil {
		return Result{}, err
	}
	days, err := e.Summarize(ctx, userID, from, to, loc)
	if err != nil {
		return Result{}, err
	}
	_ = e.Store.CleanupOrphanPlaces(ctx, userID)
	return Result{From: from, To: to, Tracks: len(tracks), Visits: len(visits), Days: days, NewStays: created}, nil
}

func toP(r store.Point) P {
	p := P{ID: r.ID, TS: r.TS, Lat: r.Lat, Lng: r.Lng, Alt: r.Altitude, Speed: r.Speed, Motion: r.Motion, Country: r.Country, Flags: r.Flags}
	if r.Accuracy != nil {
		p.Acc = *r.Accuracy
	}
	return p
}

func overlap(a1, a2, b1, b2 int64) float64 {
	lo, hi := maxI(a1, b1), minI(a2, b2)
	if hi <= lo {
		if a1 == a2 && b1 <= a1 && a1 <= b2 {
			return 1
		}
		return 0
	}
	return float64(hi - lo)
}

func maxI(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minI(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// reconcileVisits matches detected stays with the visits already stored:
// a stay that overlaps an existing detected visit updates it (keeping your
// confirmation, name and place); confirmed and declined visits nothing
// matches any more are kept; suggested ones disappear. New stays get a place:
// a saved place they fall in (confirmed straight away), else a known place
// nearby, else a new place to be named by the geocoder.
func (e *Engine) reconcileVisits(ctx context.Context, userID, from, to int64, stays []Stay, prm Params) ([]store.Visit, []int64, []store.Visit, error) {
	existing, err := e.Store.Visits(ctx, userID, store.VisitQuery{From: from, To: to})
	if err != nil {
		return nil, nil, nil, err
	}
	var manual []store.Visit
	var detected []store.Visit
	for _, v := range existing {
		if v.StartTS < from || v.StartTS > to {
			continue // outside the window, not ours to touch
		}
		if v.Manual {
			manual = append(manual, v)
		} else {
			detected = append(detected, v)
		}
	}
	used := map[int64]bool{}
	var out []store.Visit
	var created []store.Visit
	var keep []int64
	for _, s := range stays {
		// A visit you added by hand covers this stay already.
		covered := false
		for _, m := range manual {
			if overlap(s.Start, s.End, m.StartTS, m.EndTS) > 0.5*float64(maxI(1, s.End-s.Start)) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		var match *store.Visit
		best := 0.0
		for i := range detected {
			v := &detected[i]
			if used[v.ID] {
				continue
			}
			if geo.Haversine(v.Lat, v.Lng, s.Lat, s.Lng) > prm.VisitRadius*2 {
				continue
			}
			if ov := overlap(s.Start, s.End, v.StartTS, v.EndTS); ov > best {
				best, match = ov, v
			}
		}
		if match != nil {
			used[match.ID] = true
			v := *match
			v.StartTS, v.EndTS, v.Lat, v.Lng, v.Radius, v.Points = s.Start, s.End, s.Lat, s.Lng, s.Radius, s.Points
			if v.Status == store.VisitSuggested || v.PlaceID == nil {
				if pid, saved, err := e.placeFor(ctx, userID, s, prm); err == nil {
					v.PlaceID = &pid
					if saved && v.Status == store.VisitSuggested {
						v.Status = store.VisitConfirmed
					}
				}
			}
			v.Place = nil
			out = append(out, v)
			continue
		}
		v := store.Visit{UserID: userID, StartTS: s.Start, EndTS: s.End, Lat: s.Lat, Lng: s.Lng, Radius: s.Radius, Points: s.Points, Status: store.VisitSuggested}
		pid, saved, err := e.placeFor(ctx, userID, s, prm)
		if err != nil {
			return nil, nil, nil, err
		}
		v.PlaceID = &pid
		if saved {
			v.Status = store.VisitConfirmed
		}
		out = append(out, v)
		created = append(created, v)
	}
	for _, v := range detected {
		if !used[v.ID] && v.Status != store.VisitSuggested {
			keep = append(keep, v.ID)
		}
	}
	return out, keep, created, nil
}

// placeFor finds or creates the place of a stay; saved reports a saved place.
func (e *Engine) placeFor(ctx context.Context, userID int64, s Stay, prm Params) (int64, bool, error) {
	near, err := e.Store.PlacesNear(ctx, userID, s.Lat, s.Lng, math.Max(prm.VisitRadius*2, 500))
	if err != nil {
		return 0, false, err
	}
	type cand struct {
		p store.Place
		d float64
	}
	var saved, found []cand
	for _, p := range near {
		d := geo.Haversine(p.Lat, p.Lng, s.Lat, s.Lng)
		if p.Kind == store.PlaceSaved {
			if d <= p.Radius+math.Min(s.Radius, prm.VisitRadius)/2 {
				saved = append(saved, cand{p, d / math.Max(p.Radius, 1)})
			}
		} else if d <= math.Max(p.Radius, prm.VisitRadius*0.75) {
			found = append(found, cand{p, d})
		}
	}
	if len(saved) > 0 {
		sort.Slice(saved, func(i, j int) bool { return saved[i].d < saved[j].d })
		return saved[0].p.ID, true, nil
	}
	if len(found) > 0 {
		sort.Slice(found, func(i, j int) bool { return found[i].d < found[j].d })
		return found[0].p.ID, false, nil
	}
	p := &store.Place{UserID: userID, Kind: store.PlaceDiscovered, Lat: s.Lat, Lng: s.Lng, Radius: math.Round(math.Max(40, math.Min(prm.VisitRadius*0.75, s.Radius+25))),
		Country: geo.CountryAt(s.Lat, s.Lng)}
	if err := e.Store.CreatePlace(ctx, p); err != nil {
		return 0, false, err
	}
	return p.ID, false, nil
}

// Summarize recomputes the day rows for every local date touching [from, to].
func (e *Engine) Summarize(ctx context.Context, userID, from, to int64, loc *time.Location) (int, error) {
	d0 := time.Unix(from, 0).In(loc)
	d1 := time.Unix(to, 0).In(loc)
	dayStart := time.Date(d0.Year(), d0.Month(), d0.Day(), 0, 0, 0, 0, loc)
	dayEnd := time.Date(d1.Year(), d1.Month(), d1.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	pts, err := e.Store.SlimPoints(ctx, userID, dayStart.Unix(), dayEnd.Unix()-1, false)
	if err != nil {
		return 0, err
	}
	tracks, err := e.Store.Tracks(ctx, userID, dayStart.Unix(), dayEnd.Unix()-1, false)
	if err != nil {
		return 0, err
	}
	visits, err := e.Store.Visits(ctx, userID, store.VisitQuery{From: dayStart.Unix(), To: dayEnd.Unix() - 1, NoDecline: true})
	if err != nil {
		return 0, err
	}
	var home *store.Place
	if h, err := e.Store.HomePlace(ctx, userID); err == nil {
		home = h
	}
	type acc struct {
		day       store.Day
		seenC     map[string]bool
		seenPlace map[int64]bool
	}
	days := map[string]*acc{}
	var order []string
	get := func(t time.Time) *acc {
		key := t.In(loc).Format("2006-01-02")
		a, ok := days[key]
		if !ok {
			a = &acc{day: store.Day{Date: key, Countries: []string{}, Modes: map[string]float64{}, Places: []int64{}}, seenC: map[string]bool{}, seenPlace: map[int64]bool{}}
			days[key] = a
			order = append(order, key)
		}
		return a
	}
	for d := dayStart; d.Before(dayEnd); d = d.AddDate(0, 0, 1) {
		get(d)
	}
	// Which track each instant belongs to (tracks are sorted and disjoint).
	ti := 0
	trackAt := func(ts int64) int {
		for ti < len(tracks) && tracks[ti].EndTS < ts {
			ti++
		}
		if ti < len(tracks) && tracks[ti].StartTS <= ts {
			return ti
		}
		return -1
	}
	prevTrack := -1
	var prev *store.SlimPoint
	for i := range pts {
		p := &pts[i]
		a := get(time.Unix(p.TS, 0))
		a.day.Points++
		if a.day.FirstTS == 0 {
			a.day.FirstTS = p.TS
		}
		a.day.LastTS = p.TS
		if c := geo.CountryAt(p.Lat, p.Lng); c != "" && !a.seenC[c] {
			a.seenC[c] = true
			a.day.Countries = append(a.day.Countries, c)
		}
		if home != nil {
			if d := geo.Haversine(home.Lat, home.Lng, p.Lat, p.Lng); d > a.day.MaxFromHome {
				a.day.MaxFromHome = d
			}
		}
		tk := trackAt(p.TS)
		if prev != nil && tk >= 0 && tk == prevTrack {
			d := geo.Haversine(prev.Lat, prev.Lng, p.Lat, p.Lng)
			a.day.Distance += d
			a.day.Modes[tracks[tk].Mode] += d
			if dt := p.TS - prev.TS; dt > 0 && dt < 30*60 {
				a.day.MovingS += dt
			}
		} else if prev != nil {
			// Sparse history (a few points a day, as in old imports) has no
			// journeys between its points; a clear move between two of them
			// still counts, as the straight line — a lower bound.
			d := geo.Haversine(prev.Lat, prev.Lng, p.Lat, p.Lng)
			if dt := p.TS - prev.TS; d > 300 && dt > 0 && dt < 12*3600 {
				a.day.Distance += d
				a.day.Modes["unknown"] += d
			}
		}
		prev, prevTrack = p, tk
	}
	for _, v := range visits {
		if v.StartTS >= dayStart.Unix() && v.StartTS < dayEnd.Unix() {
			get(time.Unix(v.StartTS, 0)).day.Visits++
		}
		if v.PlaceID == nil {
			continue
		}
		// Every day the visit touches lists the place.
		vs := time.Unix(v.StartTS, 0).In(loc)
		for t := time.Date(vs.Year(), vs.Month(), vs.Day(), 0, 0, 0, 0, loc); t.Unix() <= v.EndTS && t.Before(dayEnd); t = t.AddDate(0, 0, 1) {
			if t.Before(dayStart) {
				continue
			}
			a := get(t)
			if !a.seenPlace[*v.PlaceID] {
				a.seenPlace[*v.PlaceID] = true
				a.day.Places = append(a.day.Places, *v.PlaceID)
			}
		}
	}
	out := make([]store.Day, 0, len(order))
	for _, k := range order {
		d := days[k].day
		for m, v := range d.Modes {
			d.Modes[m] = math.Round(v)
		}
		d.Distance = math.Round(d.Distance)
		out = append(out, d)
	}
	return len(out), e.Store.SaveDays(ctx, userID, out)
}
