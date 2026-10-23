package analyze

import (
	"context"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

var (
	home = [2]float64{43.8563, 18.4131}
	work = [2]float64{43.8563, 18.5371} // ~10 km east
)

func lerp(a, b [2]float64, f float64) (float64, float64) {
	return a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f
}

// day builds one realistic day in Sarajevo time.
func day(t0 time.Time, uid int64) []store.Point {
	var pts []store.Point
	add := func(ts time.Time, lat, lng, acc float64, speed *float64) {
		a := acc
		pts = append(pts, store.Point{UserID: uid, TS: ts.Unix(), Lat: lat, Lng: lng, Accuracy: &a, Speed: speed, Country: geo.CountryAt(lat, lng), Source: "test"})
	}
	// Night at home: a phone in significant-change mode says almost nothing.
	add(t0.Add(30*time.Minute), home[0]+0.0001, home[1], 15, nil)
	add(t0.Add(3*time.Hour), home[0], home[1]+0.0002, 20, nil)
	add(t0.Add(7*time.Hour+40*time.Minute), home[0]-0.0001, home[1], 10, nil)
	// Drive to work 08:00–08:12 at ~50 km/h, a point every 20 s.
	start := t0.Add(8 * time.Hour)
	sp := 13.9
	for i := 0; i <= 36; i++ {
		lat, lng := lerp(home, work, float64(i)/36)
		add(start.Add(time.Duration(i*20)*time.Second), lat, lng, 8, &sp)
	}
	// One GPS spike mid-way.
	add(start.Add(6*time.Minute+10*time.Second), 44.2, 18.9, 30, nil)
	// At work 08:12–17:00, a point every 10 minutes.
	for m := 12; m <= 9*60; m += 10 {
		add(start.Add(time.Duration(m)*time.Minute), work[0]+0.0002*math.Sin(float64(m)), work[1]+0.0002*math.Cos(float64(m)), 12, nil)
	}
	// Drive home 17:05–17:20.
	back := t0.Add(17*time.Hour + 5*time.Minute)
	for i := 0; i <= 45; i++ {
		lat, lng := lerp(work, home, float64(i)/45)
		add(back.Add(time.Duration(i*20)*time.Second), lat, lng, 8, &sp)
	}
	// Evening at home.
	add(t0.Add(19*time.Hour), home[0], home[1], 10, nil)
	add(t0.Add(23*time.Hour), home[0], home[1]+0.0001, 10, nil)
	return pts
}

func TestRebuildDay(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u := &store.User{Email: "a@b.c", Name: "A", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Sarajevo")
	t0 := time.Date(2026, 10, 20, 0, 0, 0, 0, loc)
	// Home is a saved place: visits there are confirmed straight away.
	if err := st.CreatePlace(ctx, &store.Place{UserID: u.ID, Name: "Home", Kind: store.PlaceSaved, Category: "home", Lat: home[0], Lng: home[1], Radius: 80}); err != nil {
		t.Fatal(err)
	}
	pts := day(t0, u.ID)
	res, err := st.InsertPoints(ctx, u.ID, pts)
	if err != nil || res.Added != len(pts) {
		t.Fatalf("insert: %v %+v", err, res)
	}
	// Same batch again: nothing new.
	if res2, _ := st.InsertPoints(ctx, u.ID, pts); res2.Added != 0 {
		t.Fatalf("duplicates added: %d", res2.Added)
	}
	e := &Engine{Store: st, DefaultTZ: loc}
	r, err := e.Rebuild(ctx, u.ID, res.MinTS, res.MaxTS)
	if err != nil {
		t.Fatal(err)
	}
	tracks, _ := st.Tracks(ctx, u.ID, 0, 0, true)
	visits, _ := st.Visits(ctx, u.ID, store.VisitQuery{})
	for _, v := range visits {
		t.Logf("visit %s–%s %s place=%v status=%s pts=%d", time.Unix(v.StartTS, 0).In(loc).Format("15:04"), time.Unix(v.EndTS, 0).In(loc).Format("15:04"), v.DisplayName(), v.PlaceID, v.Status, v.Points)
	}
	for _, tr := range tracks {
		t.Logf("track %s–%s %.1f km %s avg %.0f km/h", time.Unix(tr.StartTS, 0).In(loc).Format("15:04"), time.Unix(tr.EndTS, 0).In(loc).Format("15:04"), tr.Distance/1000, tr.Mode, tr.AvgSpeed*3.6)
	}
	t.Logf("result %+v", r)
	if len(visits) != 3 {
		t.Errorf("want 3 visits (home, work, home), got %d", len(visits))
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 journeys, got %d", len(tracks))
	}
	for _, tr := range tracks {
		if tr.Mode != "driving" {
			t.Errorf("mode = %s, want driving", tr.Mode)
		}
		if tr.Distance < 9000 || tr.Distance > 11500 {
			t.Errorf("distance = %.0f", tr.Distance)
		}
	}
	if visits[0].Status != store.VisitConfirmed || visits[1].Status != store.VisitSuggested {
		t.Errorf("statuses %s %s", visits[0].Status, visits[1].Status)
	}
	// The morning stay at home lasts until shortly before leaving.
	if end := time.Unix(visits[0].EndTS, 0).In(loc); end.Hour() != 7 && end.Hour() != 8 {
		t.Errorf("home stay ends %s", end)
	}
	days, _ := st.Days(ctx, u.ID, "", "")
	if len(days) != 1 {
		t.Fatalf("days = %+v", days)
	}
	d := days[0]
	if d.Date != "2026-10-20" || d.Distance < 18000 || d.Distance > 23000 || len(d.Countries) != 1 || d.Countries[0] != "BA" {
		t.Errorf("day = %+v", d)
	}
	n, _ := st.CountPoints(ctx, u.ID, store.PointQuery{})
	all, _ := st.CountPoints(ctx, u.ID, store.PointQuery{IncludeOutliers: true})
	if all-n != 1 {
		t.Errorf("outliers = %d, want the one spike", all-n)
	}

	// Confirm the work visit and rename it; a rebuild keeps both.
	work := visits[1]
	work.Status, work.Name = store.VisitConfirmed, "Office"
	if err := st.UpdateVisit(ctx, &work); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Rebuild(ctx, u.ID, res.MinTS, res.MaxTS); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetVisit(ctx, u.ID, work.ID)
	if again == nil || again.Status != store.VisitConfirmed || again.Name != "Office" {
		t.Errorf("confirmed visit lost: %+v", again)
	}
	if v2, _ := st.Visits(ctx, u.ID, store.VisitQuery{}); len(v2) != 3 {
		t.Errorf("visits after rebuild = %d", len(v2))
	}
}
