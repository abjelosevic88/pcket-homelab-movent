// Package server wires the HTTP API (the native one and the Dawarich- and
// tracker-compatible ones), auth, background jobs and the embedded SPA.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/analyze"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/config"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// Version is injected at build time.
var Version = "dev"

// DawarichVersion is what the Dawarich-compatible API reports, so clients
// that check for a minimum server version are happy.
const DawarichVersion = "1.16.0"

// Server holds dependencies.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	log      *slog.Logger
	webFS    fs.FS
	started  time.Time
	requests atomic.Int64
	errors   atomic.Int64
	engine   *analyze.Engine
	geocoder *geo.Geocoder

	oidcMu sync.Mutex
	oidc   *oidcProvider

	core    *core.Client
	coreMu  sync.Mutex
	coreOK  bool
	coreErr string

	rebuildKick  chan struct{}
	importKick   chan struct{}
	geocodeKick  chan struct{}
	dawarichKick chan struct{}

	// live position subscribers (server-sent events), per person.
	liveMu sync.Mutex
	live   map[int64]map[chan []byte]struct{}
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	return &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(),
		engine:      &analyze.Engine{Store: st, DefaultTZ: time.Local},
		geocoder:    geo.NewGeocoder(cfg.Geocoder, cfg.GeocoderURL, cfg.UserAgent),
		oidc:        &oidcProvider{issuerURL: cfg.OIDCIssuer},
		core:        core.New(cfg.CoreURL, core.Options{DB: st.DB, TokenFile: cfg.DataDir + "/core.token", UserAgent: cfg.UserAgent}),
		rebuildKick: make(chan struct{}, 1), importKick: make(chan struct{}, 1), geocodeKick: make(chan struct{}, 1), dawarichKick: make(chan struct{}, 1),
		live: map[int64]map[chan []byte]struct{}{}}
}

// Bootstrap seeds the first admin (if configured) and demo data, and starts
// the background jobs.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.core.Start(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(s.cfg.ImportsDir, 0o755); err != nil {
		return fmt.Errorf("imports dir: %w", err)
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		st, _ := s.store.GetSettings(ctx, 0)
		st.SetupComplete = true
		if err := s.store.SaveGlobalSettings(ctx, st); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	if s.cfg.AdminAPIKey != "" {
		if err := s.seedAdminKey(ctx); err != nil {
			return err
		}
	}
	if s.cfg.DemoData {
		if err := s.seedDemo(ctx); err != nil {
			return err
		}
	}
	_ = s.store.PurgeSessions(ctx)
	_ = s.store.RequeueRunning(ctx)
	s.rebuildIfAnalysisChanged(ctx)
	go s.rebuildLoop(ctx)
	go s.importLoop(ctx)
	go s.geocodeLoop(ctx)
	go s.watchLoop(ctx)
	go s.dawarichLoop(ctx)
	if s.core.Configured() {
		go s.coreLoop(ctx)
	}
	return nil
}

// seedAdminKey gives the first admin a device with ADMIN_API_KEY, once.
func (s *Server) seedAdminKey(ctx context.Context) error {
	if d, err := s.store.DeviceByKey(ctx, s.cfg.AdminAPIKey); err == nil && d != nil {
		return nil
	}
	var admin int64
	_ = s.store.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(id),0) FROM users WHERE role = 'admin'`).Scan(&admin)
	if admin == 0 {
		return nil
	}
	if len(s.cfg.AdminAPIKey) < 16 {
		return errors.New("ADMIN_API_KEY must be at least 16 characters")
	}
	d := &store.Device{UserID: admin, Name: "Integrations (from ADMIN_API_KEY)", Kind: "integration", Scope: store.ScopeFull, APIKey: s.cfg.AdminAPIKey}
	if err := s.store.CreateDevice(ctx, d); err != nil {
		return err
	}
	s.log.Info("device key created from ADMIN_API_KEY")
	return nil
}

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)

	r.Get("/pocket-ask.js", s.core.AskWidget("movement"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	// Tracker endpoints at the paths their apps expect (no /api prefix needed).
	r.Group(func(r chi.Router) {
		r.Use(s.osmandKey, s.requireTracker)
		r.HandleFunc("/osmand", s.handleOsmAnd)
		r.HandleFunc("/gpslogger", s.handleGPSLogger)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Compress(5))
		r.Use(middleware.Timeout(10 * time.Minute))

		// Dawarich: public liveness with its version headers.
		r.Get("/health", s.handleDawarichHealth)

		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/setup", s.handleSetup)
		r.Get("/auth/status", s.handleAuthStatus)
		r.Get("/auth/oidc/start", s.handleOIDCStart)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)

		// A share link: the live position (and maybe today) without signing in.
		r.Get("/shared/{token}", s.handleSharedView)

		// ---- sending positions: any device key with the track scope ----
		r.Group(func(r chi.Router) {
			r.Use(s.osmandKey, s.requireTracker)
			r.Post("/owntracks/points", s.handleOwnTracks)
			r.Post("/traccar/points", s.handleOsmAnd)
			r.Post("/overland/batches", s.handleOverland)
			r.Post("/points", s.handlePointsCreate)
			r.Post("/points/create", s.handlePointsCreate)
			r.Post("/locations", s.handleLocations)
			r.HandleFunc("/osmand", s.handleOsmAnd)
			r.HandleFunc("/gpslogger", s.handleGPSLogger)
		})

		// Tracker apps read their own account to check a key.
		r.Group(func(r chi.Router) {
			r.Use(s.readerOrTracker)
			r.Get("/users/me", s.handleDawarichMe)
			r.Get("/settings/mobile", s.handleMobileSettings)
			r.Patch("/settings/mobile", s.handleMobileSettings)
		})

		// ---- reading: the Dawarich API (device keys with read scope, or a session) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireReader)
			r.Get("/points", s.handleDawarichPoints)
			r.Get("/points/tracked_months", s.handleTrackedMonths)
			r.Delete("/points/bulk_destroy", s.handleBulkDeletePoints)
			r.Delete("/points/{id}", s.handleDeletePoint)
			r.Get("/visits", s.handleListVisits)
			r.Get("/visits/possible_places", s.handleVisitPossiblePlaces)
			r.Get("/visits/{id}", s.handleGetVisit)
			r.Get("/visits/{id}/possible_places", s.handleVisitPossiblePlaces)
			r.Put("/visits/{id}", s.handleUpdateVisit)
			r.Patch("/visits/{id}", s.handleUpdateVisit)
			r.Delete("/visits/{id}", s.handleDeleteVisit)
			r.Post("/visits", s.handleCreateVisit)
			r.Get("/areas", s.handleListAreas)
			r.Post("/areas", s.handleCreateArea)
			r.Delete("/areas/{id}", s.handleDeleteArea)
			r.Get("/places", s.handleListPlaces)
			r.Get("/stats", s.handleDawarichStats)
			r.Get("/countries/visited_cities", s.handleVisitedCities)
			r.Get("/countries/borders", s.handleCountryBorders)
			r.Get("/timeline", s.handleDawarichTimeline)
			r.Get("/tracks", s.handleDawarichTracks)
			r.Get("/settings", s.handleDawarichSettings)
			r.Patch("/settings", s.handleDawarichSettingsUpdate)
			r.Get("/photos", s.handleDawarichPhotos)
		})

		// ---- the app itself: a signed-in person (session, API token or Core) ----
		r.Group(func(r chi.Router) {
			r.Use(s.requireUser)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Put("/auth/me", s.handleUpdateMe)
			r.Get("/bootstrap", s.handleBootstrap)
			r.Get("/now", s.handleNow)
			r.Get("/live", s.handleLiveStream)

			r.Get("/map", s.handleMap)
			r.Get("/map/points", s.handleMapPoints)
			r.Get("/map/heat", s.handleMapHeat)
			r.Get("/day/{date}", s.handleDay)
			r.Get("/calendar", s.handleCalendar)
			r.Get("/insights", s.handleInsights)
			r.Get("/countries", s.handleCountries)
			r.Get("/countries/{code}/shape", s.handleCountryShape)
			r.Get("/summary", s.handleSummary)
			r.Get("/where", s.handleWhere)
			r.Get("/search", s.handleSearch)
			r.Get("/geocode", s.handleGeocodeSearch)

			r.Post("/places", s.handleCreatePlace)
			r.Get("/places/{id}", s.handleGetPlace)
			r.Put("/places/{id}", s.handleUpdatePlace)
			r.Delete("/places/{id}", s.handleDeletePlace)
			r.Post("/places/{id}/merge", s.handleMergePlaces)
			r.Post("/visits/bulk", s.handleBulkVisits)

			r.Get("/journeys/{id}", s.handleGetTrack)
			r.Patch("/journeys/{id}", s.handleUpdateTrack)
			r.Get("/journeys/{id}/points", s.handleTrackPoints)

			r.Get("/devices", s.handleListDevices)
			r.Post("/devices", s.handleCreateDevice)
			r.Get("/devices/{id}", s.handleGetDevice)
			r.Put("/devices/{id}", s.handleUpdateDevice)
			r.Delete("/devices/{id}", s.handleDeleteDevice)
			r.Post("/devices/{id}/rotate", s.handleRotateDevice)
			r.Get("/devices/{id}/setup", s.handleDeviceSetup)
			r.Get("/devices/{id}/qr.svg", s.handleDeviceQR)

			r.Get("/imports", s.handleListImports)
			r.Post("/imports", s.handleUploadImport)
			r.Post("/imports/dawarich", s.handleImportDawarich)
			r.Delete("/imports/{id}", s.handleDeleteImport)
			r.Get("/dawarich-link", s.handleGetDawarichLink)
			r.Put("/dawarich-link", s.handleSaveDawarichLink)
			r.Patch("/dawarich-link", s.handlePatchDawarichLink)
			r.Post("/dawarich-link/sync", s.handleSyncDawarichLink)
			r.Delete("/dawarich-link", s.handleDeleteDawarichLink)
			r.Get("/export", s.handleExport)
			r.Post("/rebuild", s.handleRebuild)
			r.Delete("/history", s.handleDeleteHistory)

			r.Get("/shares", s.handleListShares)
			r.Post("/shares", s.handleCreateShare)
			r.Put("/shares/{id}", s.handleUpdateShare)
			r.Delete("/shares/{id}", s.handleDeleteShare)
			r.Get("/family", s.handleFamily)
			r.Get("/family/{userID}/day/{date}", s.handleFamilyDay)

			r.Get("/privacy-zones", s.handleListZones)
			r.Post("/privacy-zones", s.handleSaveZone)
			r.Put("/privacy-zones/{id}", s.handleSaveZone)
			r.Delete("/privacy-zones/{id}", s.handleDeleteZone)

			r.Get("/account/settings", s.handleGetSettings)
			r.Put("/account/settings", s.handlePutSettings)
			r.Get("/system", s.handleSystemInfo)
			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Put("/users/{id}", s.handleUpdateUser)
			r.Delete("/users/{id}", s.handleDeleteUser)
			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleDeleteToken)
			r.Get("/backup", s.handleBackup)
		})
	})

	r.NotFound(s.handleSPA)
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	case errors.Is(err, context.Canceled):
		return
	}
	s.log.Error(what, "err", err)
	writeErr(w, http.StatusInternalServerError, what+": "+err.Error())
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<20)).Decode(v)
}

// decodeKeys decodes JSON into v and also reports which top-level keys were
// present, so updates can tell "not sent" from "sent empty".
func decodeKeys(r *http.Request, v any) (map[string]bool, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys, json.Unmarshal(b, v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil {
		return n
	}
	return def
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_movement_info Build info.\n# TYPE pocket_movement_info gauge\npocket_movement_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_movement_uptime_seconds Process uptime.\n# TYPE pocket_movement_uptime_seconds gauge\npocket_movement_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_movement_http_requests_total HTTP requests served.\n# TYPE pocket_movement_http_requests_total counter\npocket_movement_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_movement_http_errors_total HTTP 5xx responses.\n# TYPE pocket_movement_http_errors_total counter\npocket_movement_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_movement_rows Rows per table.\n# TYPE pocket_movement_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_movement_rows{table=%q} %d\n", k, v)
	}
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}
