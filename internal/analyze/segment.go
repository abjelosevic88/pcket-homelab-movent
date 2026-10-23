// Package analyze turns raw positions into a timeline: it drops GPS spikes,
// finds where you stayed (visits) and how you moved between them (journeys,
// with a guessed transport mode), and summarises every day.
package analyze

import (
	"math"
	"sort"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// Params tune detection; they come from the person's settings.
type Params struct {
	VisitRadius float64 // metres
	MinStay     int64   // seconds
	TrackGap    int64   // seconds
	MaxAccuracy float64 // metres, 0 = keep all
	MaxSpeed    float64 // m/s
	Modes       ModeRules
}

// ModeRules is how a journey gets its transport mode: the speeds (km/h) that
// tell the modes apart and the modes the person never uses.
type ModeRules struct {
	store.ModeSpeeds
	Off map[string]bool
}

// RulesFrom reads the mode settings; a missing or zero speed is the default.
func RulesFrom(st store.Settings) ModeRules {
	r := ModeRules{ModeSpeeds: st.ModeSpeeds, Off: map[string]bool{}}
	for _, m := range st.ModesOff {
		r.Off[m] = true
	}
	return r.filled()
}

func (r ModeRules) filled() ModeRules {
	d := store.DefaultModeSpeeds()
	for _, f := range []struct {
		v   *float64
		def float64
	}{{&r.WalkTop, d.WalkTop}, {&r.WalkTypical, d.WalkTypical}, {&r.RunTop, d.RunTop},
		{&r.RunTypicalMin, d.RunTypicalMin}, {&r.RunTypicalMax, d.RunTypicalMax}, {&r.CycleTop, d.CycleTop}, {&r.CycleTypical, d.CycleTypical},
		{&r.BusAvg, d.BusAvg}, {&r.BusTop, d.BusTop}, {&r.TrainTop, d.TrainTop}, {&r.BoatTop, d.BoatTop}, {&r.FlyTop, d.FlyTop}, {&r.RideFrom, d.RideFrom}} {
		if *f.v <= 0 {
			*f.v = f.def
		}
	}
	return r
}

func (r ModeRules) on(mode string) bool { return !r.Off[mode] }

// ParamsFrom reads the detection settings.
func ParamsFrom(st store.Settings) Params {
	p := Params{VisitRadius: st.VisitRadius, MinStay: int64(st.VisitMinMinutes) * 60, TrackGap: int64(st.TrackGapMinutes) * 60,
		MaxAccuracy: st.MaxAccuracy, MaxSpeed: st.MaxSpeedKmh / 3.6, Modes: RulesFrom(st)}
	if p.VisitRadius < 20 {
		p.VisitRadius = 100
	}
	if p.MinStay < 60 {
		p.MinStay = 5 * 60
	}
	if p.TrackGap < 5*60 {
		p.TrackGap = 30 * 60
	}
	if p.MaxSpeed < 10 {
		p.MaxSpeed = 400 / 3.6
	}
	return p
}

// P is the point shape the analysis works on.
type P struct {
	ID       int64
	TS       int64
	Lat, Lng float64
	Acc      float64 // 0 = unknown
	Alt      *float64
	Speed    *float64
	Motion   string
	Country  string
	Flags    int
}

func (p P) pt() geo.Pt { return geo.Pt{Lat: p.Lat, Lng: p.Lng, TS: p.TS} }

// Outliers decides each point's outlier flag: too inaccurate, or a spike
// (far from both neighbours, which agree with each other). A long jump that
// does not come back — a flight with the phone off — is not a spike.
func Outliers(pts []P, prm Params) map[int]bool {
	out := map[int]bool{}
	for i := range pts {
		if prm.MaxAccuracy > 0 && pts[i].Acc > prm.MaxAccuracy {
			out[i] = true
		}
	}
	// Spikes, judged among the accurate points only.
	idx := make([]int, 0, len(pts))
	for i := range pts {
		if !out[i] {
			idx = append(idx, i)
		}
	}
	speed := func(a, b P) float64 {
		dt := float64(b.TS - a.TS)
		d := geo.Haversine(a.Lat, a.Lng, b.Lat, b.Lng)
		if dt <= 0 {
			if d < 50 {
				return 0
			}
			return math.Inf(1)
		}
		return d / dt
	}
	for k := 1; k+1 < len(idx); k++ {
		a, b, c := pts[idx[k-1]], pts[idx[k]], pts[idx[k+1]]
		dab := geo.Haversine(a.Lat, a.Lng, b.Lat, b.Lng)
		dbc := geo.Haversine(b.Lat, b.Lng, c.Lat, c.Lng)
		dac := geo.Haversine(a.Lat, a.Lng, c.Lat, c.Lng)
		if dab < 200 || dbc < 200 {
			continue
		}
		if speed(a, b) > prm.MaxSpeed && speed(b, c) > prm.MaxSpeed {
			out[idx[k]] = true
			continue
		}
		// An excursion several times longer than the direct way, in a short
		// time, is a multipath glitch (cities, tunnels).
		if dab+dbc > 6*dac+1000 && c.TS-a.TS < 300 && speed(a, b) > 30 {
			out[idx[k]] = true
		}
	}
	// Two identical timestamps: keep the more accurate one.
	for k := 1; k < len(idx); k++ {
		a, b := pts[idx[k-1]], pts[idx[k]]
		if a.TS == b.TS && !out[idx[k-1]] && !out[idx[k]] {
			if a.Acc > b.Acc && b.Acc > 0 {
				out[idx[k-1]] = true
			} else if geo.Haversine(a.Lat, a.Lng, b.Lat, b.Lng) > 20 {
				out[idx[k]] = true
			}
		}
	}
	return out
}

// Stay is a detected visit.
type Stay struct {
	Start, End int64
	Lat, Lng   float64
	Radius     float64
	Points     int
	first      int // index range in the cleaned slice
	last       int
}

// Move is a detected journey.
type Move struct {
	Pts []P
}

// Segment splits cleaned, time-ordered points into stays and moves.
//
// A stay is a run of points that stay within VisitRadius of their running
// centre for at least MinStay. Phones in "significant change" mode report
// almost nothing while still, so a long silence followed by a point close by
// also counts as staying put: the stay lasts until the next point minus the
// time it would take to get there.
//
// Walking round and round a block also stays within the radius, so the ends
// of a cluster where the phone says it is moving are trimmed off (they belong
// to the journeys either side), and a cluster that is mostly moving is no stay.
func Segment(pts []P, prm Params) ([]Stay, []Move) {
	var stays []Stay
	n := len(pts)
	moving := movingFlags(pts)
	i := 0
	for i < n {
		lat, lng := pts[i].Lat, pts[i].Lng
		cnt := 1.0
		j := i + 1
		for j < n {
			d := geo.Haversine(lat, lng, pts[j].Lat, pts[j].Lng)
			allow := prm.VisitRadius
			if pts[j].Acc > 0 {
				allow += math.Min(pts[j].Acc, prm.VisitRadius) / 2
			}
			if d > allow {
				break
			}
			cnt++
			lat += (pts[j].Lat - lat) / cnt
			lng += (pts[j].Lng - lng) / cnt
			j++
		}
		// Trim the walking in and out; recentre on what is left.
		a, b := i, j
		for a < b && moving[a] {
			a++
		}
		for b > a && moving[b-1] {
			b--
		}
		if b <= a || movingShare(moving[a:b]) > 0.5 {
			i++
			continue
		}
		if a != i || b != j {
			lat, lng = 0, 0
			for k := a; k < b; k++ {
				lat += pts[k].Lat
				lng += pts[k].Lng
			}
			lat, lng = lat/float64(b-a), lng/float64(b-a)
		}
		start, end := pts[a].TS, pts[b-1].TS
		// The silence after the cluster (not when the phone walked away first).
		if j < n && b == j {
			gap := pts[j].TS - end
			d := geo.Haversine(lat, lng, pts[j].Lat, pts[j].Lng)
			// Not when the phone was driving into the silence; and unless the
			// next point is back at the spot, at most 12 h: a single point
			// before a long gap is not a stay of months.
			driving := pts[b-1].Speed != nil && *pts[b-1].Speed > 2
			if gap > 10*60 && d/float64(gap) < 0.5 && !driving {
				travel := int64(d / 5) // ~18 km/h to get to the next point
				e := pts[j].TS - travel
				if d > prm.VisitRadius*2 {
					e = min(e, end+12*3600)
				}
				if e > end {
					end = e
				}
			}
		}
		if end-start >= prm.MinStay {
			var r float64
			for k := a; k < b; k++ {
				r = math.Max(r, geo.Haversine(lat, lng, pts[k].Lat, pts[k].Lng))
			}
			stays = append(stays, Stay{Start: start, End: end, Lat: lat, Lng: lng, Radius: r, Points: b - a, first: a, last: b - 1})
			i = j
			continue
		}
		i++
	}
	stays = mergeStays(stays, pts, prm)

	// Moves: everything between stays, from the last point of one stay to the
	// first of the next so the line on the map is unbroken; a silence longer
	// than TrackGap while moving starts a new journey.
	var moves []Move
	emit := func(a, b int) { // inclusive indices
		if b <= a {
			return
		}
		seg := []P{pts[a]}
		for k := a + 1; k <= b; k++ {
			if pts[k].TS-pts[k-1].TS > prm.TrackGap {
				if len(seg) > 1 {
					moves = append(moves, Move{Pts: seg})
				}
				seg = nil
			}
			seg = append(seg, pts[k])
		}
		if len(seg) > 1 {
			moves = append(moves, Move{Pts: seg})
		}
	}
	prev := 0
	if len(stays) == 0 {
		emit(0, n-1)
	} else {
		if stays[0].first > 0 {
			emit(0, stays[0].first)
		}
		for k := 0; k+1 < len(stays); k++ {
			emit(stays[k].last, stays[k+1].first)
		}
		prev = stays[len(stays)-1].last
		if prev < n-1 {
			emit(prev, n-1)
		}
	}
	// A "journey" that never left the neighbourhood is noise. One that walks
	// to the car and drives off is two journeys.
	var kept []Move
	rules := prm.Modes.filled()
	for _, m := range moves {
		if span(m.Pts) >= prm.VisitRadius*1.5 {
			kept = append(kept, splitModes(m, rules)...)
		}
	}
	return stays, kept
}

// Seconds a change between on foot and a vehicle has to last.
const modeMinRun = 60

// splitModes cuts a journey where it changes from on foot to a vehicle or
// back (walking to the car, getting off the bus), so each part gets its own
// mode. The parts share the point where they meet, keeping the line whole.
func splitModes(m Move, r ModeRules) []Move {
	pts := m.Pts
	n := len(pts)
	if n < 8 {
		return []Move{m}
	}
	// Speeds (m/s): a point is on foot below walking's top speed, in a
	// vehicle from a bit above it; in between is left to the neighbours.
	footMax := r.WalkTop / 3.6
	vehicleMin := math.Max(3.5, footMax*1.2)
	rideFrom := r.RideFrom / 3.6
	// 0 = unknown, 1 = on foot, 2 = vehicle; a reported speed, else to the next point.
	cls := make([]int, n)
	for k := range pts {
		var v float64 = -1
		if pts[k].Speed != nil && *pts[k].Speed >= 0 {
			v = *pts[k].Speed
		} else if k+1 < n {
			if dt := float64(pts[k+1].TS - pts[k].TS); dt > 0 && dt <= 120 {
				v = geo.Haversine(pts[k].Lat, pts[k].Lng, pts[k+1].Lat, pts[k+1].Lng) / dt
			}
		}
		switch {
		case v < 0:
		case v < footMax:
			cls[k] = 1
		case v >= vehicleMin:
			cls[k] = 2
		}
	}
	// Majority over a window of 7 smooths a red light or a jog.
	sm := make([]int, n)
	for k := range pts {
		var f, c int
		for o := max(0, k-3); o <= min(n-1, k+3); o++ {
			switch cls[o] {
			case 1:
				f++
			case 2:
				c++
			}
		}
		switch {
		case c > f:
			sm[k] = 2
		case f > c:
			sm[k] = 1
		case k > 0:
			sm[k] = sm[k-1]
		}
	}
	// Runs of one kind.
	type run struct{ a, b, kind int } // inclusive
	var runs []run
	for k := 0; k < n; k++ {
		if len(runs) > 0 && (runs[len(runs)-1].kind == sm[k] || sm[k] == 0) {
			runs[len(runs)-1].b = k
			continue
		}
		runs = append(runs, run{k, k, sm[k]})
	}
	// Only a real walk next to a real drive is a change: on foot for two
	// minutes at walking pace, in a vehicle for a minute with real speed.
	// Stop-and-go traffic and red lights stay part of the drive.
	speedsOf := func(r run) []float64 {
		var v []float64
		for k := r.a; k <= r.b; k++ {
			if pts[k].Speed != nil && *pts[k].Speed >= 0 {
				v = append(v, *pts[k].Speed)
			}
		}
		sort.Float64s(v)
		return v
	}
	valid := func(r run) bool {
		dur := pts[r.b].TS - pts[r.a].TS
		v := speedsOf(r)
		switch r.kind {
		case 1:
			return dur >= 2*modeMinRun && (len(v) < 3 || (percentile(v, 0.5) <= footMax*0.9 && percentile(v, 0.85) <= footMax*1.18))
		case 2:
			return dur >= modeMinRun && (len(v) < 3 || percentile(v, 0.85) >= rideFrom)
		}
		return false
	}
	var out []run
	for _, r := range runs {
		if !valid(r) {
			r.kind = 0
		}
		if len(out) > 0 && (out[len(out)-1].kind == r.kind || r.kind == 0) {
			out[len(out)-1].b = r.b
			continue
		}
		if len(out) > 0 && out[len(out)-1].kind == 0 {
			// what came before was unclear: it goes with this run
			r.a = out[len(out)-1].a
			out = out[:len(out)-1]
			if len(out) > 0 && out[len(out)-1].kind == r.kind {
				out[len(out)-1].b = r.b
				continue
			}
		}
		out = append(out, r)
	}
	if len(out) < 2 {
		return []Move{m}
	}
	parts := make([]Move, 0, len(out))
	for k, r := range out {
		b := r.b
		if k+1 < len(out) {
			b = out[k+1].a // share the meeting point
		}
		parts = append(parts, Move{Pts: pts[r.a : b+1]})
	}
	return parts
}

// stayMoving is the speed (m/s) from which a point counts as moving: a slow
// walk is ~1.1 m/s, while a phone lying still reports (or jitters at) well under.
const stayMoving = 1.0

// movingFlags marks the points where the phone was moving: its own speed when
// it reports one, else the speed to a neighbour less than a minute away (sparse
// "significant change" points never count as moving, as before).
func movingFlags(pts []P) []bool {
	out := make([]bool, len(pts))
	for k, p := range pts {
		if p.Speed != nil && *p.Speed >= 0 {
			out[k] = *p.Speed >= stayMoving
			continue
		}
		for _, o := range []int{k - 1, k + 1} {
			if o < 0 || o >= len(pts) {
				continue
			}
			dt := math.Abs(float64(pts[o].TS - p.TS))
			if dt <= 0 || dt > 60 {
				continue
			}
			d := geo.Haversine(p.Lat, p.Lng, pts[o].Lat, pts[o].Lng) - math.Max(p.Acc, pts[o].Acc)
			if d/dt >= stayMoving*1.2 {
				out[k] = true
			}
		}
	}
	return out
}

func movingShare(m []bool) float64 {
	if len(m) == 0 {
		return 0
	}
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return float64(n) / float64(len(m))
}

// span is the largest distance from the first point.
func span(pts []P) float64 {
	var m float64
	for _, p := range pts[1:] {
		m = math.Max(m, geo.Haversine(pts[0].Lat, pts[0].Lng, p.Lat, p.Lng))
	}
	return m
}

// mergeStays joins consecutive stays at the same spot separated by a short
// excursion (stepping out to the car, GPS wandering).
func mergeStays(stays []Stay, pts []P, prm Params) []Stay {
	if len(stays) < 2 {
		return stays
	}
	out := []Stay{stays[0]}
	for _, s := range stays[1:] {
		last := &out[len(out)-1]
		d := geo.Haversine(last.Lat, last.Lng, s.Lat, s.Lng)
		if d <= prm.VisitRadius*1.2 && s.Start-last.End <= 20*60 {
			// No point in between went far away.
			far := false
			for k := last.last + 1; k < s.first; k++ {
				if geo.Haversine(last.Lat, last.Lng, pts[k].Lat, pts[k].Lng) > prm.VisitRadius*3 {
					far = true
					break
				}
			}
			if !far {
				w1, w2 := float64(last.Points), float64(s.Points)
				last.Lat = (last.Lat*w1 + s.Lat*w2) / (w1 + w2)
				last.Lng = (last.Lng*w1 + s.Lng*w2) / (w1 + w2)
				last.End = s.End
				last.Points += s.Points + (s.first - last.last - 1)
				last.Radius = math.Max(last.Radius, s.Radius+d)
				last.last = s.last
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// Modes a journey can have.
var Modes = []string{"walking", "running", "cycling", "driving", "bus", "train", "boat", "flying", "unknown"}

// ValidMode reports whether m is a known mode.
func ValidMode(m string) bool {
	for _, x := range Modes {
		if x == m {
			return true
		}
	}
	return false
}

// TrackFrom measures a move and guesses how it was made.
func TrackFrom(m Move, r ModeRules) store.Track {
	pts := m.Pts
	first, last := pts[0], pts[len(pts)-1]
	t := store.Track{StartTS: first.TS, EndTS: last.TS, Points: len(pts), StartLat: first.Lat, StartLng: first.Lng, EndLat: last.Lat, EndLng: last.Lng}
	var speeds []float64
	var movingS int64
	countries := []string{}
	seenC := map[string]bool{}
	var prevAlt *float64
	motions := map[string]int{}
	for k, p := range pts {
		if p.Country != "" && !seenC[p.Country] {
			seenC[p.Country] = true
			countries = append(countries, p.Country)
		}
		if p.Motion != "" {
			for _, mo := range strings.Split(p.Motion, ",") {
				motions[strings.TrimSpace(mo)]++
			}
		}
		if p.Alt != nil {
			if prevAlt != nil {
				if d := *p.Alt - *prevAlt; d >= 3 {
					t.ElevationGain += d
					prevAlt = p.Alt
				} else if d <= -3 {
					t.ElevationLoss -= d
					prevAlt = p.Alt
				}
			} else {
				prevAlt = p.Alt
			}
		}
		if k == 0 {
			continue
		}
		a := pts[k-1]
		d := geo.Haversine(a.Lat, a.Lng, p.Lat, p.Lng)
		t.Distance += d
		dt := p.TS - a.TS
		if dt > 0 {
			sp := d / float64(dt)
			if p.Speed != nil && *p.Speed >= 0 && dt < 120 {
				sp = *p.Speed
			}
			speeds = append(speeds, sp)
			if sp > 0.4 {
				movingS += dt
			}
		}
	}
	dur := t.EndTS - t.StartTS
	if movingS == 0 || movingS > dur {
		movingS = dur
	}
	if movingS > 0 {
		t.AvgSpeed = t.Distance / float64(movingS)
	}
	sort.Float64s(speeds)
	if len(speeds) > 0 {
		t.MaxSpeed = speeds[int(float64(len(speeds)-1)*0.95)]
	}
	t.Countries = strings.Join(countries, ",")
	t.Mode = classify(t, speeds, pts, motions, r.filled())
	tol := 4.0
	if t.Distance > 50000 {
		tol = 15
	}
	simp := make([]geo.Pt, len(pts))
	for k, p := range pts {
		simp[k] = p.pt()
	}
	t.Polyline = geo.EncodePolyline(geo.Simplify(simp, tol))
	return t
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*q)]
}

// classify guesses the transport mode from speeds, what the phone said
// (iOS/Android activity) and whether the way was over water. Modes the person
// turned off are never the answer: their journeys go to the next best one.
func classify(t store.Track, speeds []float64, pts []P, motions map[string]int, r ModeRules) string {
	dur := float64(t.EndTS - t.StartTS)
	if dur <= 0 {
		return "unknown"
	}
	med := percentile(speeds, 0.5) * 3.6
	p85 := percentile(speeds, 0.85) * 3.6
	avg := t.AvgSpeed * 3.6
	direct := geo.Haversine(t.StartLat, t.StartLng, t.EndLat, t.EndLng)
	// A long, fast hop — with or without points in between — is a flight.
	if r.on("flying") && ((direct > 150000 && direct/dur*3.6 > 180) || p85 > r.FlyTop) {
		return "flying"
	}
	// The phone's own activity guess, when it gave one for most points.
	best, bestN, total := "", 0, 0
	for k, n := range motions {
		total += n
		if n > bestN && k != "stationary" && k != "unknown" && k != "" {
			best, bestN = k, n
		}
	}
	if total > 0 && bestN*2 >= total {
		guess := ""
		switch best {
		case "driving", "automotive", "in_vehicle":
			guess = "driving"
			if p85 > r.TrainTop*0.875 && t.Distance > 40000 && r.on("train") {
				guess = "train"
			}
		case "walking", "on_foot":
			if med < 9 {
				guess = "walking"
			}
		case "running":
			guess = "running"
		case "cycling", "on_bicycle":
			guess = "cycling"
		}
		if guess != "" && r.on(guess) {
			return guess
		}
	}
	// Mostly over water at boat speeds.
	sea := 0
	for _, p := range pts {
		if p.Country == "" {
			sea++
		}
	}
	if r.on("boat") && len(pts) >= 4 && sea*10 >= len(pts)*6 && p85 < r.BoatTop {
		return "boat"
	}
	// On foot and by bike, slowest first. A journey that fits a mode turned
	// off goes to a slower one when its typical speed suits it (a walk with a
	// jumpy GPS), otherwise on to the faster modes (a car in slow traffic).
	fits := map[string]bool{
		"walking": p85 < r.WalkTop && med < r.WalkTypical,
		"running": p85 < r.RunTop && med >= r.RunTypicalMin && med < r.RunTypicalMax && dur < 4*3600 && t.Distance < 45000,
		"cycling": p85 < r.CycleTop && med < r.CycleTypical,
	}
	slower := func(k int) string {
		if k > 0 && r.on("walking") && med < r.WalkTop {
			return "walking"
		}
		if k > 1 && r.on("running") && med >= r.RunTypicalMin && med < r.RunTypicalMax {
			return "running"
		}
		return ""
	}
	for k, m := range []string{"walking", "running", "cycling"} {
		if !fits[m] {
			continue
		}
		if r.on(m) {
			return m
		}
		if s := slower(k); s != "" {
			return s
		}
	}
	switch {
	case r.on("train") && p85 > r.TrainTop && t.Distance > 40000:
		return "train"
	case r.on("bus") && avg < r.BusAvg && p85 < r.BusTop && stopsOften(pts):
		return "bus"
	}
	for _, m := range []string{"driving", "bus", "train", "cycling", "running", "walking"} {
		if r.on(m) {
			return m
		}
	}
	return "unknown"
}

// stopsOften is true when a vehicle journey pauses every few hundred metres
// for a short while: a city bus rather than a car.
func stopsOften(pts []P) bool {
	if len(pts) < 20 {
		return false
	}
	stops := 0
	var dist float64
	for k := 1; k < len(pts); k++ {
		a, b := pts[k-1], pts[k]
		d := geo.Haversine(a.Lat, a.Lng, b.Lat, b.Lng)
		dist += d
		dt := b.TS - a.TS
		if dt >= 20 && dt <= 120 && d < 15 {
			stops++
		}
	}
	return dist > 2000 && float64(stops) >= dist/600
}
