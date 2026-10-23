package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, loc := s.userLoc(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"settings": st, "effective_timezone": loc.String(), "server_timezone": time.Local.String(), "is_admin": u.Role == "admin"})
}

// handlePutSettings saves preferences; changed detection settings rebuild
// the whole history in the background.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	before := st
	keys, err := decodeKeys(r, &st)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if st.Timezone != "" {
		if _, err := time.LoadLocation(st.Timezone); err != nil {
			writeErr(w, http.StatusBadRequest, "unknown time zone "+st.Timezone)
			return
		}
	}
	if st.DistanceUnit != "mi" {
		st.DistanceUnit = "km"
	}
	switch st.RouteColor {
	case "speed", "mode", "plain":
	default:
		st.RouteColor = "speed"
	}
	st.VisitRadius = clampF(st.VisitRadius, 25, 500)
	st.VisitMinMinutes = clampI(st.VisitMinMinutes, 1, 240)
	st.TrackGapMinutes = clampI(st.TrackGapMinutes, 5, 24*60)
	if st.MaxAccuracy != 0 {
		st.MaxAccuracy = clampF(st.MaxAccuracy, 10, 5000)
	}
	st.MaxSpeedKmh = clampF(st.MaxSpeedKmh, 50, 2000)
	st.ModesOff = store.CleanModesOff(st.ModesOff)
	st.ModeSpeeds = clampSpeeds(st.ModeSpeeds)
	st.NotifyStaleHours = clampI(st.NotifyStaleHours, 0, 24*14)
	if err := s.store.SaveUserSettings(ctx, u.ID, st); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if keys["title"] && u.Role == "admin" && strings.TrimSpace(st.Title) != "" {
		g, _ := s.store.GetSettings(ctx, 0)
		g.Title = strings.TrimSpace(st.Title)
		_ = s.store.SaveGlobalSettings(ctx, g)
	}
	rebuild := st.VisitRadius != before.VisitRadius || st.VisitMinMinutes != before.VisitMinMinutes || st.TrackGapMinutes != before.TrackGapMinutes ||
		st.MaxAccuracy != before.MaxAccuracy || st.MaxSpeedKmh != before.MaxSpeedKmh || st.Timezone != before.Timezone ||
		strings.Join(st.ModesOff, ",") != strings.Join(before.ModesOff, ",") || st.ModeSpeeds != before.ModeSpeeds
	if rebuild {
		s.rebuildAll(ctx, u.ID)
	}
	st, _ = s.store.GetSettings(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"settings": st, "rebuilding": rebuild})
}

// clampSpeeds keeps the mode speeds (km/h) believable and in order: a
// typical speed never above its top speed. Zero is the default.
func clampSpeeds(m store.ModeSpeeds) store.ModeSpeeds {
	d := store.DefaultModeSpeeds()
	c := func(v *float64, def, lo, hi float64) {
		if *v <= 0 {
			*v = def
		}
		*v = math.Round(clampF(*v, lo, hi)*10) / 10
	}
	c(&m.WalkTop, d.WalkTop, 3, 20)
	c(&m.WalkTypical, d.WalkTypical, 2, m.WalkTop)
	c(&m.RunTop, d.RunTop, 8, 30)
	c(&m.RunTypicalMax, d.RunTypicalMax, 5, m.RunTop)
	c(&m.RunTypicalMin, d.RunTypicalMin, 3, m.RunTypicalMax)
	c(&m.CycleTop, d.CycleTop, 10, 60)
	c(&m.CycleTypical, d.CycleTypical, 5, m.CycleTop)
	c(&m.BusTop, d.BusTop, 20, 150)
	c(&m.BusAvg, d.BusAvg, 5, m.BusTop)
	c(&m.TrainTop, d.TrainTop, 60, 400)
	c(&m.BoatTop, d.BoatTop, 20, 150)
	c(&m.FlyTop, d.FlyTop, 150, 1200)
	c(&m.RideFrom, d.RideFrom, 10, 80)
	return m
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// analysisVersion goes up whenever visit and journey detection changes, so
// everyone's history is worked out again once, with the new rules, on start.
// 2: walking loops are no visits, walk/drive splits, stays over a long
// silence elsewhere end within 12 h. 3: transport modes follow the person's
// settings (modes turned off hand their journeys to the nearest one in use).
const analysisVersion = "3"

func (s *Server) rebuildIfAnalysisChanged(ctx context.Context) {
	if s.store.BoxValue(ctx, "analysis_version") == analysisVersion {
		return
	}
	users, err := s.store.UsersWithPoints(ctx)
	if err != nil {
		s.log.Warn("analysis version: users", "err", err)
		return
	}
	for _, id := range users {
		s.rebuildAll(ctx, id)
	}
	if err := s.store.SetBoxValue(ctx, "analysis_version", analysisVersion); err == nil {
		s.log.Info("detection changed: rebuilding history", "version", analysisVersion, "people", len(users))
	}
}

// rebuildAll queues the person's whole history for a rebuild.
func (s *Server) rebuildAll(ctx context.Context, userID int64) {
	first, last, _ := s.store.PointBounds(ctx, userID)
	if last == 0 {
		return
	}
	// In month-long slices so the map stays usable while it runs.
	for from := first; from <= last; from += 31 * 86400 {
		_ = s.store.MarkDirty(ctx, userID, from, minI64(from+31*86400, last))
	}
	s.kickRebuild()
}

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	from, to := tsParam(r, "from", 0), tsParam(r, "to", 0)
	if from > 0 && to > from {
		_ = s.store.MarkDirty(ctx, u.ID, from, to)
		s.kickRebuild()
	} else {
		s.rebuildAll(ctx, u.ID)
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// handleDeleteHistory deletes points in a range (from/to required; all=1 for everything).
func (s *Server) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	from, to := tsParam(r, "from", 0), tsParam(r, "to", 0)
	if queryBool(r, "all") {
		from, to = 0, time.Now().Unix()+86400*2
	}
	if to <= from {
		writeErr(w, http.StatusBadRequest, "from and to are required")
		return
	}
	n, err := s.store.DeletePointsRange(ctx, u.ID, from, to, 0)
	if err != nil {
		s.fail(w, err, "delete history")
		return
	}
	if queryBool(r, "all") {
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM visits WHERE user_id = ?`, u.ID)
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM tracks WHERE user_id = ?`, u.ID)
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM days WHERE user_id = ?`, u.ID)
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM dirty WHERE user_id = ?`, u.ID)
	}
	s.kickRebuild()
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

// ---- privacy zones ----

func (s *Server) handleListZones(w http.ResponseWriter, r *http.Request) {
	zs, err := s.store.PrivacyZones(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "zones")
		return
	}
	writeJSON(w, http.StatusOK, zs)
}

func (s *Server) handleSaveZone(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var z store.PrivacyZone
	if err := decode(r, &z); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	z.UserID = u.ID
	z.ID = idParam(r, "id")
	if !geo.ValidCoord(z.Lat, z.Lng) {
		writeErr(w, http.StatusBadRequest, "lat and lng are required")
		return
	}
	z.Radius = clampF(z.Radius, 50, 5000)
	if err := s.store.SavePrivacyZone(ctx, &z); err != nil {
		s.fail(w, err, "save zone")
		return
	}
	writeJSON(w, http.StatusOK, z)
}

func (s *Server) handleDeleteZone(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePrivacyZone(r.Context(), userFrom(r.Context()).ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete zone")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- system ----

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var size int64
	if fi, err := os.Stat(s.cfg.DBPath); err == nil {
		size = fi.Size()
	}
	if fi, err := os.Stat(s.cfg.DBPath + "-wal"); err == nil {
		size += fi.Size()
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	out := map[string]any{"version": Version, "dawarich_api": DawarichVersion, "uptime": time.Since(s.started).Round(time.Second).String(),
		"db_size": size, "go": runtime.Version(), "memory": mem.Alloc, "auth_mode": s.cfg.AuthMode, "geocoder": s.cfg.Geocoder,
		"geocoder_url": s.geocoder.BaseURL, "core": s.coreStatus(), "base_url": s.publicURL(r)}
	if u := userFrom(ctx); u != nil && u.Role == "admin" {
		out["counts"] = s.store.Counts(ctx)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBackup streams a consistent copy of the database (admins).
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	dst := filepath.Join(os.TempDir(), fmt.Sprintf("pocket-movement-%d.db", time.Now().UnixNano()))
	if err := s.store.Backup(r.Context(), dst); err != nil {
		s.fail(w, err, "backup")
		return
	}
	defer os.Remove(dst)
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-movement-`+time.Now().Format("20060102-150405")+`.db"`)
	http.ServeFile(w, r, dst)
}
