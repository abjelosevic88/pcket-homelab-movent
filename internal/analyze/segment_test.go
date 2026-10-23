package analyze

import (
	"math"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

func fp(v float64) *float64 { return &v }

// Walking round a block for ten minutes stays within the visit radius but is
// no visit: the phone says it is moving.
func TestWalkingLoopIsNoStay(t *testing.T) {
	prm := Params{VisitRadius: 100, MinStay: 300, TrackGap: 1800, MaxSpeed: 100}
	var pts []P
	for k := 0; k < 90; k++ { // every 7 s, a 70 m circle at 1.5 m/s
		a := float64(k) * 1.5 * 7 / 70
		pts = append(pts, P{TS: int64(k * 7), Lat: 44.77 + 70*math.Sin(a)/111000, Lng: 17.19 + 70*math.Cos(a)/79000, Acc: 4, Speed: fp(1.5)})
	}
	if stays, _ := Segment(pts, prm); len(stays) != 0 {
		t.Fatalf("a walking loop became %d stays", len(stays))
	}
	// the same spot, phone lying still: a stay
	for k := range pts {
		pts[k].Lat, pts[k].Lng, pts[k].Speed = 44.77, 17.19, fp(0.1)
	}
	if stays, _ := Segment(pts, prm); len(stays) != 1 {
		t.Fatalf("still phone: %d stays", len(stays))
	}
}

// Walking to the car and driving off is two journeys that meet at the car.
func TestWalkThenDriveSplits(t *testing.T) {
	var pts []P
	lat, ts := 44.77, int64(0)
	for k := 0; k < 30; k++ { // 3.5 min on foot
		pts = append(pts, P{TS: ts, Lat: lat, Lng: 17.19, Speed: fp(1.4)})
		lat += 1.4 * 7 / 111000
		ts += 7
	}
	for k := 0; k < 30; k++ { // 3 min by car
		pts = append(pts, P{TS: ts, Lat: lat, Lng: 17.19, Speed: fp(10)})
		lat += 10 * 6 / 111000
		ts += 6
	}
	parts := splitModes(Move{Pts: pts}, ModeRules{}.filled())
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if parts[0].Pts[len(parts[0].Pts)-1].TS != parts[1].Pts[0].TS {
		t.Fatal("the parts do not meet")
	}
	// stop-and-go traffic stays one drive
	var city []P
	for k := 0; k < 60; k++ {
		v := 9.0
		if k%12 < 4 {
			v = 0.5 // red light
		}
		city = append(city, P{TS: int64(k * 6), Lat: 44.77 + float64(k)*50/111000, Lng: 17.19, Speed: fp(v)})
	}
	if parts := splitModes(Move{Pts: city}, ModeRules{}.filled()); len(parts) != 1 {
		t.Fatalf("city drive split into %d", len(parts))
	}
}

// line makes a straight journey north, one point every 10 s, at the speeds (m/s) given in turn.
func line(n int, speed func(k int) float64) Move {
	var pts []P
	lat := 44.77
	for k := 0; k < n; k++ {
		v := speed(k)
		pts = append(pts, P{TS: int64(k * 10), Lat: lat, Lng: 17.19, Speed: fp(v), Country: "BA"})
		lat += v * 10 / 111000
	}
	return Move{Pts: pts}
}

// A mode turned off is never the answer: slow traffic goes to driving, a walk
// with a jumpy GPS back to walking.
func TestModesTurnedOffHandOver(t *testing.T) {
	on := ModeRules{}.filled()
	noBike := RulesFrom(store.Settings{ModesOff: []string{"cycling"}})
	traffic := line(60, func(k int) float64 { return []float64{3.5, 4.5, 5.5}[k%3] }) // 13–20 km/h, no stops
	jumpy := line(60, func(k int) float64 { return []float64{1.3, 1.4, 1.5, 1.4, 3.2, 1.3, 3.5}[k%7] })
	for _, c := range []struct {
		name string
		m    Move
		r    ModeRules
		want string
	}{
		{"traffic, bike on", traffic, on, "cycling"},
		{"traffic, no bike", traffic, noBike, "driving"},
		{"jumpy walk, bike on", jumpy, on, "cycling"},
		{"jumpy walk, no bike", jumpy, noBike, "walking"},
		{"traffic, no bike or car", traffic, RulesFrom(store.Settings{ModesOff: []string{"cycling", "driving"}}), "bus"},
		{"walk, everything off", jumpy, RulesFrom(store.Settings{ModesOff: store.SwitchableModes}), "unknown"},
	} {
		if got := TrackFrom(c.m, c.r).Mode; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// a faster walking pace makes the jumpy walk a walk with the bike on too
	brisk := RulesFrom(store.Settings{ModeSpeeds: store.ModeSpeeds{WalkTop: 13}})
	if got := TrackFrom(jumpy, brisk).Mode; got != "walking" {
		t.Errorf("walking top 13 km/h: %s", got)
	}
	if brisk.CycleTop != 32 {
		t.Errorf("unset speeds should be the defaults, cycle top = %v", brisk.CycleTop)
	}
}

// An hour at the gate, then Sarajevo → Vienna: with the phone tracking in the
// air, and with it off until landing. Both are one flight, not spikes and not
// a hole in the day.
func TestFlights(t *testing.T) {
	prm := ParamsFrom(store.Settings{})
	sjjLat, sjjLng, vieLat, vieLng := 43.82, 18.33, 48.11, 16.57
	gate := func(lat, lng float64, from int64) []P {
		var pts []P
		for k := int64(0); k < 30; k++ {
			pts = append(pts, P{TS: from + k*120, Lat: lat, Lng: lng, Acc: 10, Speed: fp(0)})
		}
		return pts
	}
	flights := func(pts []P) []store.Track {
		clean := pts[:0:0]
		out := Outliers(pts, prm)
		for i, p := range pts {
			if !out[i] {
				clean = append(clean, p)
			}
		}
		_, moves := Segment(clean, prm)
		var fl []store.Track
		for _, m := range moves {
			if tr := TrackFrom(m, prm.Modes); tr.Mode == "flying" {
				fl = append(fl, tr)
			}
		}
		return fl
	}
	dep := int64(3600)
	// tracked: a point a minute for 50 minutes, ~490 km at ~590 km/h
	pts := gate(sjjLat, sjjLng, 0)
	for k := int64(1); k < 50; k++ {
		f := float64(k) / 50
		pts = append(pts, P{TS: dep + k*60, Lat: sjjLat + (vieLat-sjjLat)*f, Lng: sjjLng + (vieLng-sjjLng)*f, Acc: 20, Speed: fp(165)})
	}
	pts = append(pts, gate(vieLat, vieLng, dep+50*60)...)
	if fl := flights(pts); len(fl) != 1 || fl[0].Distance < 400000 {
		t.Fatalf("tracked flight: %+v", fl)
	}
	// phone off from the gate to the arrival hall
	pts = append(gate(sjjLat, sjjLng, 0), gate(vieLat, vieLng, dep+50*60)...)
	if fl := flights(pts); len(fl) != 1 {
		t.Fatalf("phone-off flight: %d flights", len(fl))
	}
	// a drive with the phone off is still not a journey of its own
	pts = append(gate(sjjLat, sjjLng, 0), gate(43.34, 17.81, dep+2*3600)...) // Mostar, 2 h
	if _, moves := Segment(pts, prm); len(moves) != 0 {
		t.Fatalf("phone-off drive became %d journeys", len(moves))
	}
}

// A single wild point that comes straight back is still a spike.
func TestSpikeStillDropped(t *testing.T) {
	prm := ParamsFrom(store.Settings{})
	var pts []P
	for k := int64(0); k < 10; k++ {
		pts = append(pts, P{TS: k * 30, Lat: 43.85, Lng: 18.41, Acc: 10})
	}
	pts[5].Lat = 44.85 // 110 km away for one fix
	if out := Outliers(pts, prm); !out[5] {
		t.Fatal("spike kept")
	}
}
