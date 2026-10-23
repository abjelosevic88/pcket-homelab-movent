package server

import (
	"encoding/json"
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

func newTestServer(t *testing.T) (*Server, *store.User) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", ImportsDir: dir + "/imports", AuthMode: "local", Geocoder: "off", SessionTTL: time.Hour, RebuildDelay: time.Second}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.DBPath, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(cfg, st, log, nil)
	if err := s.core.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	u := &store.User{Email: "me@example.com", Name: "Me", Role: "admin"}
	if err := st.CreateUser(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return s, u
}

// Movement speaks the Dawarich API, so one instance stands in for the
// Dawarich server the other is linked to.
func TestDawarichLinkPullsNewPoints(t *testing.T) {
	ctx := t.Context()
	src, su := newTestServer(t)
	key := &store.Device{UserID: su.ID, Name: "Link", Kind: "integration", Scope: store.ScopeRead}
	if err := src.store.CreateDevice(ctx, key); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(src.Router())
	defer ts.Close()
	now := time.Now().Unix()
	add := func(from, n int64) {
		t.Helper()
		pts := make([]store.Point, 0, n)
		for i := int64(0); i < n; i++ {
			pts = append(pts, store.Point{UserID: su.ID, TS: from + i*60, Lat: 44.77 + float64(i)/1e4, Lng: 17.19, Source: "dawarich"})
		}
		if _, err := src.store.InsertPoints(ctx, su.ID, pts); err != nil {
			t.Fatal(err)
		}
	}
	add(now-3*3600, 30)

	dst, du := newTestServer(t)
	dw, me, _, err := checkDawarich(ctx, ts.URL+"/", key.APIKey)
	if err != nil || me != "me@example.com" {
		t.Fatalf("check: %v %q", err, me)
	}
	if err := dst.store.SaveDawarichLink(ctx, &store.DawarichLink{UserID: du.ID, URL: dw.base, APIKey: dw.key, Account: me, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	sync := func() *store.DawarichLink {
		t.Helper()
		l, err := dst.store.GetDawarichLink(ctx, du.ID)
		if err != nil {
			t.Fatal(err)
		}
		dst.syncDawarich(ctx, *l)
		l, _ = dst.store.GetDawarichLink(ctx, du.ID)
		if l.LastError != "" {
			t.Fatalf("sync error: %s", l.LastError)
		}
		return l
	}
	l := sync()
	if l.LastAdded != 30 || l.CursorTS != now-3*3600+29*60 {
		t.Fatalf("first pull: added %d cursor %d", l.LastAdded, l.CursorTS)
	}
	// the overlap re-reads the last hours but adds only what is new
	add(now-600, 5)
	if l = sync(); l.LastAdded != 5 || l.PointsAdded != 35 {
		t.Fatalf("second pull: added %d total %d", l.LastAdded, l.PointsAdded)
	}
	if l = sync(); l.LastAdded != 0 {
		t.Fatalf("third pull added %d", l.LastAdded)
	}
	var n int
	_ = dst.store.DB.QueryRow(`SELECT COUNT(*) FROM points WHERE user_id = ? AND source = 'dawarich'`, du.ID).Scan(&n)
	if n != 35 {
		t.Fatalf("points here: %d", n)
	}

	// a refused key is reported and keeps the cursor
	_ = dst.store.SaveDawarichLink(ctx, &store.DawarichLink{UserID: du.ID, URL: dw.base, APIKey: "wrong", Enabled: true, CursorTS: 42})
	bad, _ := dst.store.GetDawarichLink(ctx, du.ID)
	dst.syncDawarich(ctx, *bad)
	bad, _ = dst.store.GetDawarichLink(ctx, du.ID)
	if bad.LastError == "" || bad.CursorTS != 42 {
		t.Fatalf("bad key: %+v", bad)
	}
}

// A link through Pocket Core asks Core for the address and key on every pull
// and keeps no key itself; switching it off in Core stops it.
func TestDawarichLinkThroughCore(t *testing.T) {
	ctx := t.Context()
	src, su := newTestServer(t)
	key := &store.Device{UserID: su.ID, Name: "Link", Kind: "integration", Scope: store.ScopeRead}
	if err := src.store.CreateDevice(ctx, key); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(src.Router())
	defer ts.Close()
	now := time.Now().Unix()
	if _, err := src.store.InsertPoints(ctx, su.ID, []store.Point{{UserID: su.ID, TS: now - 600, Lat: 44.77, Lng: 17.19, Source: "dawarich"}}); err != nil {
		t.Fatal(err)
	}
	off := false
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ User, Type string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/api/v1/services/credential" || in.Type != "dawarich" || in.User != "me@example.com" || off {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"switched off in Pocket Core","reason":"off_admin"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"url": ts.URL, "key": key.APIKey, "source": "box"})
	}))
	defer coreSrv.Close()

	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, DBPath: dir + "/t.db", ImportsDir: dir + "/imports", AuthMode: "local", Geocoder: "off", SessionTTL: time.Hour, RebuildDelay: time.Second, CoreURL: coreSrv.URL}
	st, err := store.Open(cfg.DBPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dst := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	du := &store.User{Email: "me@example.com", Name: "Me", Role: "admin"}
	if err := st.CreateUser(ctx, du); err != nil {
		t.Fatal(err)
	}

	dw, me, _, err := dst.pickDawarich(ctx, du.Email, "core", "", "")
	if err != nil || me != "me@example.com" || dw.base != ts.URL {
		t.Fatalf("pick through core: %v %q", err, me)
	}
	if err := st.SaveDawarichLink(ctx, &store.DawarichLink{UserID: du.ID, URL: dw.base, Account: me, Enabled: true, Source: "core"}); err != nil {
		t.Fatal(err)
	}
	l, _ := st.GetDawarichLink(ctx, du.ID)
	if l.APIKey != "" || l.Source != "core" {
		t.Fatalf("stored link: %+v", l)
	}
	dst.syncDawarich(ctx, *l)
	if l, _ = st.GetDawarichLink(ctx, du.ID); l.LastError != "" || l.LastAdded != 1 {
		t.Fatalf("pull through core: %+v", l)
	}
	if st := dst.dawarichCoreStatus(ctx, du.Email); st["available"] != true || st["url"] != ts.URL {
		t.Fatalf("status: %+v", st)
	}

	// switched off in Core: settings see it at once, pulls within the SDK's one-minute cache
	off = true
	if st := dst.dawarichCoreStatus(ctx, du.Email); st["available"] != false || !strings.Contains(st["error"].(string), "switched off in Pocket Core") {
		t.Fatalf("switched off: %+v", st)
	}
	if _, _, _, err := dst.pickDawarich(ctx, "someone@else", "core", "", ""); err == nil {
		t.Fatal("someone else got a Dawarich")
	}
}
