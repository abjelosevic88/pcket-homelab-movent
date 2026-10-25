package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// ---- Pocket Health: workouts, drawn from the positions recorded here ----
//
// Each person chooses how Movement reaches their Pocket Health: through a
// Pocket Core app link (a five-minute token acting as that person, nothing
// to paste), directly with their own URL + API token, or not at all.

const healthScope = "health:read"

var healthApp = struct{ slug, name, reason string }{"health", "Pocket Health", "Show your workouts on the map, drawn from the positions recorded while they ran"}

// healthUses is the manifest's Uses.
func healthUses() []core.Use {
	return []core.Use{{App: healthApp.slug, Scopes: []string{healthScope}, Reason: healthApp.reason}}
}

const (
	modeCore   = "core"
	modeDirect = "direct"
	modeOff    = "off"
)

// healthMode is the mode in effect: the chosen one, else direct when a token
// was entered, else Pocket Core when Movement is connected to one, else direct.
func (s *Server) healthMode(st store.Settings) string {
	switch st.HealthMode {
	case modeCore, modeDirect, modeOff:
		return st.HealthMode
	}
	if st.HealthToken != "" || !s.core.Configured() {
		return modeDirect
	}
	return modeCore
}

type notConnected struct{ msg string }

func (e *notConnected) Error() string { return e.msg }

func isNotConnected(err error) bool {
	var nc *notConnected
	return errors.As(err, &nc)
}

type healthClient struct {
	base, token, public, via string
	name                     string // the app, for errors ("" = Pocket Health)
	http                     *http.Client
}

func linkReasonText(reason string) string {
	switch reason {
	case "off_person":
		return "you switched the Movement → Pocket Health link off in Pocket Core (Settings → Apps acting for me)"
	case "off_admin":
		return "the Movement → Pocket Health link is switched off in Pocket Core (Links)"
	case "target_missing":
		return "Pocket Health is not registered with Pocket Core"
	case "not_offered":
		return "this Pocket Health version does not accept app links yet"
	case "not_declared":
		return "Pocket Core has not seen this Movement version yet; it re-registers within a minute of starting"
	case "not_in_audience":
		return "you do not have access to Pocket Health in Pocket Core"
	case "unknown_user":
		return "your email is not a person in Pocket Core"
	}
	return reason
}

// healthFor connects to the person's Pocket Health.
func (s *Server) healthFor(ctx context.Context, u *store.User, st store.Settings) (*healthClient, error) {
	switch s.healthMode(st) {
	case modeOff:
		return nil, &notConnected{"Pocket Health is switched off in Settings → Connected apps"}
	case modeDirect:
		if st.HealthToken == "" || st.HealthURL == "" {
			return nil, &notConnected{"Pocket Health is not set up (Settings → Connected apps): add its address and a token, or switch to Pocket Core"}
		}
		return &healthClient{base: strings.TrimRight(st.HealthURL, "/"), token: st.HealthToken, public: strings.TrimRight(firstNonEmpty(st.HealthPublicURL, st.HealthURL), "/"), via: modeDirect, http: &http.Client{Timeout: 20 * time.Second}}, nil
	}
	if !s.core.Configured() {
		return nil, &notConnected{"Pocket Health through Pocket Core: there is no Pocket Core on this server; use a direct connection"}
	}
	tok, err := s.core.ActAs(ctx, u.Email, healthApp.slug, healthScope)
	if err != nil {
		var le *core.LinkError
		if errors.As(err, &le) {
			return nil, &notConnected{"Pocket Health is not set up: " + linkReasonText(le.Reason)}
		}
		return nil, fmt.Errorf("Pocket Health through Pocket Core: %w", err)
	}
	if tok.URL == "" || tok.AccessToken == "" {
		return nil, errors.New("Pocket Health through Pocket Core: Core gave no address or token")
	}
	return &healthClient{base: strings.TrimRight(tok.URL, "/"), token: tok.AccessToken, public: strings.TrimRight(firstNonEmpty(tok.PublicURL, tok.URL), "/"), via: modeCore, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

func (c *healthClient) get(ctx context.Context, path string, q url.Values, v any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketMovement/"+Version)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&e)
		name := firstNonEmpty(c.name, healthApp.name)
		if e.Error != "" {
			return fmt.Errorf("%s answered %d: %s", name, res.StatusCode, e.Error)
		}
		return fmt.Errorf("%s answered %d", name, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(v)
}

// healthWorkout is one workout from Pocket Health.
type healthWorkout struct {
	ID         int64   `json:"id"`
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	StartAt    string  `json:"start_at"`
	EndAt      string  `json:"end_at"`
	DurationS  int     `json:"duration_s"`
	DistanceM  float64 `json:"distance_m"`
	EnergyKcal float64 `json:"energy_kcal"`
	AvgHR      float64 `json:"avg_hr"`
	MaxHR      float64 `json:"max_hr"`
	ElevationM float64 `json:"elevation_m"`
}

// workoutView is a workout with the route recorded here.
type workoutView struct {
	ID            int64      `json:"id"`
	Kind          string     `json:"kind"`
	Name          string     `json:"name"`
	StartTS       int64      `json:"start_ts"`
	EndTS         int64      `json:"end_ts"`
	DurationS     int        `json:"duration_s"`
	DistanceM     float64    `json:"distance_m"`
	EnergyKcal    float64    `json:"energy_kcal"`
	AvgHR         float64    `json:"avg_hr"`
	MaxHR         float64    `json:"max_hr"`
	ElevationM    float64    `json:"elevation_m"`
	Polyline      string     `json:"polyline"`
	RouteDistance float64    `json:"route_distance"`
	RoutePoints   int        `json:"route_points"`
	Start         [2]float64 `json:"start"`
	End           [2]float64 `json:"end"`
}

func parseWorkoutTimes(w healthWorkout) (int64, int64, bool) {
	start, err := time.Parse(time.RFC3339, w.StartAt)
	if err != nil {
		return 0, 0, false
	}
	end := start.Add(time.Duration(w.DurationS) * time.Second)
	if e, err := time.Parse(time.RFC3339, w.EndAt); err == nil && e.After(start) {
		end = e
	}
	return start.Unix(), end.Unix(), true
}

// workoutRoute returns the polyline recorded between start and end (a minute
// of slack each side), cached a day per workout.
func (s *Server) workoutRoute(ctx context.Context, uid int64, w healthWorkout, start, end int64) (string, float64, int, [2]float64, [2]float64) {
	key := fmt.Sprintf("wroute:%d:%d:%d", uid, w.ID, start)
	type cached struct {
		Poly   string     `json:"p"`
		Dist   float64    `json:"d"`
		N      int        `json:"n"`
		Start  [2]float64 `json:"s"`
		End    [2]float64 `json:"e"`
		Cached string     `json:"at"`
	}
	if v, at, ok := s.store.CacheGet(ctx, key); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < 24*time.Hour {
			var c cached
			if json.Unmarshal([]byte(v), &c) == nil {
				return c.Poly, c.Dist, c.N, c.Start, c.End
			}
		}
	}
	pts, err := s.store.SlimPoints(ctx, uid, start-60, end+60, false)
	if err != nil || len(pts) < 2 {
		_ = s.store.CachePut(ctx, key, `{"p":"","d":0,"n":0}`)
		return "", 0, len(pts), [2]float64{}, [2]float64{}
	}
	line := make([]geo.Pt, 0, len(pts))
	dist := 0.0
	for i, p := range pts {
		if i > 0 {
			d := geo.Haversine(pts[i-1].Lat, pts[i-1].Lng, p.Lat, p.Lng)
			if d < 3 {
				continue
			}
			dist += d
		}
		line = append(line, geo.Pt{Lat: p.Lat, Lng: p.Lng, TS: p.TS})
	}
	poly := geo.EncodePolyline(line)
	c := cached{Poly: poly, Dist: dist, N: len(pts), Start: [2]float64{line[0].Lat, line[0].Lng}, End: [2]float64{line[len(line)-1].Lat, line[len(line)-1].Lng}}
	if b, err := json.Marshal(c); err == nil {
		_ = s.store.CachePut(ctx, key, string(b))
	}
	return poly, dist, len(pts), c.Start, c.End
}

// handleListWorkouts lists the person's workouts in a date range with their routes.
func (s *Server) handleListWorkouts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	if from == "" {
		from = time.Now().AddDate(0, 0, -29).Format("2006-01-02")
	}
	hc, err := s.healthFor(ctx, u, st)
	if err != nil {
		if isNotConnected(err) {
			writeJSON(w, http.StatusOK, map[string]any{"connected": false, "detail": err.Error(), "workouts": []any{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"connected": true, "detail": err.Error(), "workouts": []any{}})
		return
	}
	var list []healthWorkout
	if err := hc.get(ctx, "/api/v1/workouts", url.Values{"from": {from}, "to": {to}, "limit": {"500"}}, &list); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": true, "via": hc.via, "detail": err.Error(), "open_url": hc.public, "workouts": []any{}})
		return
	}
	out := make([]workoutView, 0, len(list))
	for _, hw := range list {
		start, end, ok := parseWorkoutTimes(hw)
		if !ok {
			continue
		}
		v := workoutView{ID: hw.ID, Kind: hw.Kind, Name: hw.Name, StartTS: start, EndTS: end, DurationS: hw.DurationS, DistanceM: hw.DistanceM, EnergyKcal: hw.EnergyKcal, AvgHR: hw.AvgHR, MaxHR: hw.MaxHR, ElevationM: hw.ElevationM}
		v.Polyline, v.RouteDistance, v.RoutePoints, v.Start, v.End = s.workoutRoute(ctx, u.ID, hw, start, end)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTS > out[j].StartTS })
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "via": hc.via, "open_url": hc.public, "workouts": out})
}

// handleWorkoutPoints returns the positions of one workout, point by point.
func (s *Server) handleWorkoutPoints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	hc, err := s.healthFor(ctx, u, st)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var hw healthWorkout
	if err := hc.get(ctx, fmt.Sprintf("/api/v1/workouts/%d", idParam(r, "id")), nil, &hw); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	start, end, ok := parseWorkoutTimes(hw)
	if !ok {
		writeErr(w, http.StatusBadGateway, "the workout has no start time")
		return
	}
	pts, err := s.store.SlimPoints(ctx, u.ID, start-60, end+60, false)
	if err != nil {
		s.fail(w, err, "points")
		return
	}
	rows := make([][]any, 0, len(pts))
	for i, p := range pts {
		var sp any
		if p.Speed != nil {
			sp = round1(*p.Speed * 3.6)
		} else if i > 0 && p.TS > pts[i-1].TS {
			sp = round1(geo.Haversine(pts[i-1].Lat, pts[i-1].Lng, p.Lat, p.Lng) / float64(p.TS-pts[i-1].TS) * 3.6)
		}
		rows = append(rows, []any{p.TS, p.Lat, p.Lng, sp})
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": rows})
}

// handleHealthStatus says how the person reaches Pocket Health and tests it.
func (s *Server) handleHealthStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	mode := s.healthMode(st)
	hc, err := s.healthFor(ctx, u, st)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": false, "detail": err.Error(), "core_configured": s.core.Configured()})
		return
	}
	if r.Method == http.MethodPost {
		var list []healthWorkout
		q := url.Values{"from": {time.Now().AddDate(0, 0, -30).Format("2006-01-02")}, "to": {time.Now().Format("2006-01-02")}, "limit": {"500"}}
		if err := hc.get(ctx, "/api/v1/workouts", q, &list); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": false, "via": hc.via, "detail": err.Error(), "core_configured": s.core.Configured()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": true, "via": hc.via, "open_url": hc.public, "message": fmt.Sprintf("Pocket Health answers (%s): %d workouts in the last 30 days", map[string]string{modeCore: "through Pocket Core", modeDirect: "direct connection"}[hc.via], len(list)), "core_configured": s.core.Configured()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": true, "via": hc.via, "open_url": hc.public, "core_configured": s.core.Configured()})
}
