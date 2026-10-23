package analyze

import (
	"math"
	"testing"
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
	parts := splitModes(Move{Pts: pts})
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
	if parts := splitModes(Move{Pts: city}); len(parts) != 1 {
		t.Fatalf("city drive split into %d", len(parts))
	}
}
