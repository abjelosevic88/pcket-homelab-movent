package server

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// seedDemo fills an empty box with two weeks of a made-up life in Sarajevo
// (home, office, a café, the gym) and a weekend in Mostar, so every screen
// has something to show. Only when nobody has points yet.
func (s *Server) seedDemo(ctx context.Context) error {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return err
	}
	var u *store.User
	if len(users) == 0 {
		u = &store.User{Email: "admin@localhost", Name: "Demo", Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
	} else {
		u = &users[0]
	}
	if _, _, n := s.store.PointBounds(ctx, u.ID); n > 0 {
		return nil
	}
	home := [2]float64{43.8519, 18.3866}
	office := [2]float64{43.8590, 18.4210}
	cafe := [2]float64{43.8597, 18.4310}
	gym := [2]float64{43.8487, 18.3631}
	mostar := [2]float64{43.3373, 17.8152}
	for _, p := range []store.Place{
		{Name: "Home", Category: "home", Lat: home[0], Lng: home[1]},
		{Name: "Office", Category: "work", Lat: office[0], Lng: office[1]},
	} {
		p.UserID, p.Kind, p.Radius, p.Renamed, p.Geocoded, p.Country = u.ID, store.PlaceSaved, 90, true, 1, "BA"
		if err := s.store.CreatePlace(ctx, &p); err != nil {
			return err
		}
	}
	rng := rand.New(rand.NewPCG(7, 11))
	loc, _ := time.LoadLocation("Europe/Sarajevo")
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	day0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -14)
	var pts []store.Point
	jitter := func() float64 { return (rng.Float64() - 0.5) * 0.0004 }
	stay := func(at [2]float64, from, to time.Time, every time.Duration) {
		for t := from; t.Before(to); t = t.Add(every) {
			acc := 10 + rng.Float64()*15
			pts = append(pts, store.Point{TS: t.Unix(), Lat: at[0] + jitter(), Lng: at[1] + jitter(), Accuracy: &acc, Source: "demo"})
		}
	}
	move := func(a, b [2]float64, start time.Time, kmh float64) time.Time {
		d := math.Hypot((b[0]-a[0])*110540, (b[1]-a[1])*111320*math.Cos(a[0]*math.Pi/180))
		dur := time.Duration(d / (kmh / 3.6) * float64(time.Second))
		steps := int(dur / (15 * time.Second))
		if steps < 2 {
			steps = 2
		}
		sp := kmh / 3.6
		// A gentle curve so routes do not look ruled.
		bend := (rng.Float64() - 0.5) * 0.004
		for i := 0; i <= steps; i++ {
			f := float64(i) / float64(steps)
			lat := a[0] + (b[0]-a[0])*f + math.Sin(f*math.Pi)*bend
			lng := a[1] + (b[1]-a[1])*f - math.Sin(f*math.Pi)*bend
			acc := 5 + rng.Float64()*6
			v := sp * (0.8 + rng.Float64()*0.4)
			alt := 520 + 30*math.Sin(f*6)
			pts = append(pts, store.Point{TS: start.Add(time.Duration(f * float64(dur))).Unix(), Lat: lat, Lng: lng, Accuracy: &acc, Speed: &v, Altitude: &alt, Source: "demo"})
		}
		return start.Add(dur)
	}
	for d := 0; d <= 14; d++ {
		day := day0.AddDate(0, 0, d)
		wd := day.Weekday()
		at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
		switch {
		case wd == time.Saturday && d < 10:
			stay(home, at(0, 10), at(8, 30), 3*time.Hour)
			t := move(home, mostar, at(9, 0), 75)
			stay(mostar, t.Add(2*time.Minute), at(18, 0), 10*time.Minute)
		case wd == time.Sunday && d < 10:
			stay(mostar, at(0, 10), at(14, 0), 2*time.Hour)
			t := move(mostar, home, at(14, 30), 80)
			stay(home, t.Add(2*time.Minute), at(23, 50), time.Hour)
		case wd == time.Saturday || wd == time.Sunday:
			stay(home, at(0, 10), at(10, 30), 3*time.Hour)
			t := move(home, gym, at(11, 0), 14)
			stay(gym, t.Add(time.Minute), at(12, 30), 10*time.Minute)
			t = move(gym, home, at(12, 35), 15)
			stay(home, t.Add(time.Minute), at(23, 50), 2*time.Hour)
		default:
			stay(home, at(0, 10), at(7, 45), 3*time.Hour)
			t := move(home, office, at(8, 0), 32)
			stay(office, t.Add(time.Minute), at(12, 30), 15*time.Minute)
			t = move(office, cafe, at(12, 35), 5)
			stay(cafe, t.Add(time.Minute), at(13, 20), 8*time.Minute)
			t = move(cafe, office, at(13, 25), 5)
			stay(office, t.Add(time.Minute), at(17, 0), 15*time.Minute)
			t = move(office, home, at(17, 10), 28)
			stay(home, t.Add(time.Minute), at(23, 50), 2*time.Hour)
		}
	}
	var keep []store.Point
	for _, p := range pts {
		if p.TS <= now.Unix() {
			keep = append(keep, p)
		}
	}
	res, err := s.ingest(ctx, u, nil, keep, "demo")
	if err != nil {
		return err
	}
	if res.Added > 0 {
		if _, err := s.engine.Rebuild(ctx, u.ID, res.MinTS, res.MaxTS); err != nil {
			return err
		}
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM dirty WHERE user_id = ?`, u.ID)
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE places SET name = 'Café Tito', geocoded = 1, renamed = 1, city = 'Sarajevo' WHERE user_id = ? AND kind = 'discovered' AND abs(lat - ?) < 0.001 AND abs(lng - ?) < 0.001`, u.ID, cafe[0], cafe[1])
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE places SET name = 'Gym', geocoded = 1, renamed = 1, city = 'Sarajevo' WHERE user_id = ? AND kind = 'discovered' AND abs(lat - ?) < 0.001 AND abs(lng - ?) < 0.001`, u.ID, gym[0], gym[1])
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE places SET name = 'Stari Most', geocoded = 1, renamed = 1, city = 'Mostar' WHERE user_id = ? AND kind = 'discovered' AND abs(lat - ?) < 0.002 AND abs(lng - ?) < 0.002`, u.ID, mostar[0], mostar[1])
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE places SET city = 'Sarajevo', geocoded = 1 WHERE user_id = ? AND city = ''`, u.ID)
	}
	s.log.Info("demo data created", "points", res.Added)
	return nil
}
