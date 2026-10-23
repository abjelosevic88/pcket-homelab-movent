package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/config"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// The wire formats phones and other Dawarich clients depend on.
func TestTrackerAndDawarichCompat(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", ImportsDir: dir + "/imports", AuthMode: "local", Geocoder: "off", SessionTTL: time.Hour, RebuildDelay: time.Second}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, log, nil)
	if err := s.core.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Router())
	defer ts.Close()
	ctx := t.Context()
	u := &store.User{Email: "me@example.com", Name: "Me", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	full := &store.Device{UserID: u.ID, Name: "Phone", Kind: "dawarich", Scope: store.ScopeFull}
	track := &store.Device{UserID: u.ID, Name: "Tracker", Kind: "owntracks", Scope: store.ScopeTrack}
	read := &store.Device{UserID: u.ID, Name: "Travel", Kind: "integration", Scope: store.ScopeRead}
	for _, d := range []*store.Device{full, track, read} {
		if err := st.CreateDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Unix()
	do := func(method, path, body string, hdr ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	expect := func(res *http.Response, body string, code int, want string) {
		t.Helper()
		if res.StatusCode != code || !strings.Contains(body, want) {
			t.Fatalf("%s %s → %d %s (want %d with %q)", res.Request.Method, res.Request.URL.Path, res.StatusCode, body, code, want)
		}
	}

	// health: no key, Dawarich's headers
	res, body := do("GET", "/api/v1/health", "")
	expect(res, body, 200, `"ok"`)
	if res.Header.Get("X-Dawarich-Version") == "" || !strings.Contains(res.Header.Get("X-Dawarich-Response"), "alive") {
		t.Fatalf("health headers %v", res.Header)
	}
	// users/me answers a tracker key too (apps check their key with it)
	res, body = do("GET", "/api/v1/users/me?api_key="+track.APIKey, "")
	expect(res, body, 200, `"email":"me@example.com"`)
	res, body = do("GET", "/api/v1/users/me", "")
	expect(res, body, 401, "unauthorized")

	// The Dawarich app's batch
	feat := func(lat, lng float64, ts int64) string {
		return fmt.Sprintf(`{"type":"Feature","geometry":{"type":"Point","coordinates":[%v,%v]},"properties":{"timestamp":%d,"speed":3.2,"horizontal_accuracy":8,"battery_level":0.81,"battery_state":"unplugged","motion":["walking"]}}`, lng, lat, ts)
	}
	res, body = do("POST", "/api/v1/points?api_key="+full.APIKey, `{"locations":[`+feat(43.85, 18.41, now-900)+`,`+feat(43.851, 18.411, now-890)+`]}`)
	expect(res, body, 200, `"data"`)
	// Overland, bearer key
	res, body = do("POST", "/api/v1/overland/batches", `{"locations":[`+feat(43.852, 18.412, now-880)+`]}`, "Authorization", "Bearer "+full.APIKey)
	expect(res, body, 201, `"result":"ok"`)
	// OwnTracks with basic auth; answers a JSON array
	ot := fmt.Sprintf(`{"_type":"location","lat":43.853,"lon":18.413,"tst":%d,"acc":10,"vel":4,"batt":77,"bs":2,"tid":"ME"}`, now-870)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/owntracks/points", strings.NewReader(ot))
	req.SetBasicAuth("me", track.APIKey)
	req.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 200 || !strings.HasPrefix(strings.TrimSpace(string(b2)), "[") {
		t.Fatalf("owntracks → %d %s", r2.StatusCode, b2)
	}
	// OsmAnd with the key as the device id, speed in knots
	res, body = do("GET", fmt.Sprintf("/osmand?id=%s&lat=43.854&lon=18.414&timestamp=%d&speed=10&batt=60", track.APIKey, now-860), "")
	expect(res, body, 200, "")
	// GPSLogger
	res, body = do("GET", fmt.Sprintf("/gpslogger?api_key=%s&lat=43.855&lon=18.415&time=%s&acc=5", track.APIKey, time.Unix(now-850, 0).UTC().Format(time.RFC3339)), "")
	expect(res, body, 200, "ok")
	// our own API: duplicates and junk are counted, not stored
	res, body = do("POST", "/api/v1/locations?api_key="+full.APIKey, fmt.Sprintf(`[{"lat":43.856,"lon":18.416,"timestamp":%d},{"lat":43.856,"lon":18.416,"timestamp":%d},{"nope":1}]`, now-840, now-840))
	expect(res, body, 200, `"added":1`)
	// a read-only key cannot send, a track-only key cannot read history
	res, body = do("POST", "/api/v1/locations?api_key="+read.APIKey, `[{"lat":1,"lon":1}]`)
	expect(res, body, 403, "read")
	res, body = do("GET", "/api/v1/points?api_key="+track.APIKey, "")
	expect(res, body, 403, "send points")
	// a device key is not a person: the app's own API stays closed to it
	res, body = do("GET", "/api/v1/devices?api_key="+full.APIKey, "")
	expect(res, body, 403, "")

	// Dawarich reads, as Pocket Travel does them
	res, body = do("GET", fmt.Sprintf("/api/v1/points?api_key=%s&start_at=%s&end_at=%s&order=asc&slim=true&per_page=3&page=1", read.APIKey,
		time.Unix(now-3600, 0).UTC().Format(time.RFC3339), time.Unix(now, 0).UTC().Format(time.RFC3339)), "")
	expect(res, body, 200, `"latitude":"43.85"`)
	if res.Header.Get("X-Total-Pages") != "3" {
		t.Fatalf("X-Total-Pages = %q, body %s", res.Header.Get("X-Total-Pages"), body)
	}
	var pts []map[string]any
	_ = json.Unmarshal([]byte(body), &pts)
	if len(pts) != 3 || pts[0]["timestamp"].(float64) != float64(now-900) {
		t.Fatalf("slim points %v", pts)
	}
	n, _ := st.CountPoints(ctx, u.ID, store.PointQuery{IncludeOutliers: true})
	if n != 7 {
		t.Fatalf("stored %d points, want 7", n)
	}
	// battery and tracker carried over from OwnTracks
	devs, _ := st.ListDevices(ctx, u.ID)
	for _, d := range devs {
		if d.ID == track.ID && (d.LastBattery == nil || d.LastPointAt == 0) {
			t.Fatalf("tracker device not updated: %+v", d)
		}
	}
	// the timeline and stats answer in Dawarich's shapes
	if _, err := s.engine.Rebuild(ctx, u.ID, now-1000, now); err != nil {
		t.Fatal(err)
	}
	res, body = do("GET", fmt.Sprintf("/api/v1/timeline?api_key=%s&start_at=%s&end_at=%s", read.APIKey, time.Unix(now-86400, 0).UTC().Format(time.RFC3339), time.Unix(now, 0).UTC().Format(time.RFC3339)), "")
	expect(res, body, 200, `"days"`)
	res, body = do("GET", "/api/v1/stats?api_key="+read.APIKey, "")
	expect(res, body, 200, `"totalDistanceKm"`)
	res, body = do("GET", "/api/v1/visits?api_key="+read.APIKey+"&start_at=2000-01-01T00:00:00Z&end_at=2100-01-01T00:00:00Z", "")
	expect(res, body, 200, "[")
	res, body = do("GET", "/api/v1/points/tracked_months?api_key="+read.APIKey, "")
	expect(res, body, 200, `"year"`)
}
