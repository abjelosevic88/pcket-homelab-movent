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
}

// ParamsFrom reads the detection settings.
func ParamsFrom(st store.Settings) Params {
	p := Params{VisitRadius: st.VisitRadius, MinStay: int64(st.VisitMinMinutes) * 60, TrackGap: int64(st.TrackGapMinutes) * 60,
		MaxAccuracy: st.MaxAccuracy, MaxSpeed: st.MaxSpeedKmh / 3.6}
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
func Segment(pts []P, prm Params) ([]Stay, []Move) {
	var stays []Stay
	n := len(pts)
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
		start, end := pts[i].TS, pts[j-1].TS
		// The silence after the cluster.
		if j < n {
			gap := pts[j].TS - end
			d := geo.Haversine(lat, lng, pts[j].Lat, pts[j].Lng)
			if gap > 10*60 && d/float64(gap) < 0.5 {
				travel := int64(d / 5) // ~18 km/h to get to the next point
				if e := pts[j].TS - travel; e > end {
					end = e
				}
			}
		}
		if end-start >= prm.MinStay {
			var r float64
			for k := i; k < j; k++ {
				r = math.Max(r, geo.Haversine(lat, lng, pts[k].Lat, pts[k].Lng))
			}
			stays = append(stays, Stay{Start: start, End: end, Lat: lat, Lng: lng, Radius: r, Points: j - i, first: i, last: j - 1})
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
	// A "journey" that never left the neighbourhood is noise.
	kept := moves[:0]
	for _, m := range moves {
		if span(m.Pts) >= prm.VisitRadius*1.5 {
			kept = append(kept, m)
		}
	}
	return stays, kept
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
func TrackFrom(m Move) store.Track {
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
	t.Mode = classify(t, speeds, pts, motions)
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
// (iOS/Android activity) and whether the way was over water.
func classify(t store.Track, speeds []float64, pts []P, motions map[string]int) string {
	dur := float64(t.EndTS - t.StartTS)
	if dur <= 0 {
		return "unknown"
	}
	med := percentile(speeds, 0.5) * 3.6
	p85 := percentile(speeds, 0.85) * 3.6
	avg := t.AvgSpeed * 3.6
	direct := geo.Haversine(t.StartLat, t.StartLng, t.EndLat, t.EndLng)
	// A long, fast hop — with or without points in between — is a flight.
	if (direct > 150000 && direct/dur*3.6 > 180) || p85 > 300 {
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
		switch best {
		case "driving", "automotive", "in_vehicle":
			if p85 > 140 && t.Distance > 40000 {
				return "train"
			}
			return "driving"
		case "walking", "on_foot":
			if med < 9 {
				return "walking"
			}
		case "running":
			return "running"
		case "cycling", "on_bicycle":
			return "cycling"
		}
	}
	// Mostly over water at boat speeds.
	sea := 0
	for _, p := range pts {
		if p.Country == "" {
			sea++
		}
	}
	if len(pts) >= 4 && sea*10 >= len(pts)*6 && p85 < 80 {
		return "boat"
	}
	switch {
	case p85 < 8 && med < 6.5:
		return "walking"
	case p85 < 15 && med >= 7 && med < 14 && dur < 4*3600 && t.Distance < 45000:
		return "running"
	case p85 < 32 && med < 24:
		return "cycling"
	case p85 > 160 && t.Distance > 40000:
		return "train"
	case avg < 30 && p85 < 70 && stopsOften(pts):
		return "bus"
	default:
		return "driving"
	}
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
