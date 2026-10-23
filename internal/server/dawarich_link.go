package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// A Dawarich link keeps pulling new points from a Dawarich server, so a
// phone can go on reporting there (and Travel, Garage… reading there) while
// everything also shows up here.
const (
	dawarichSyncEvery = 5 * time.Minute
	// Each pull goes back this far behind the newest point it has seen, for
	// phones that upload a buffered batch late. Points already here are
	// skipped, so the overlap costs only bandwidth.
	dawarichLookback = 6 * time.Hour
	dawarichPerPage  = 5000
)

// serviceUses are the third-party services Movement can reach through Pocket
// Core, by Core catalog type.
var serviceUses = []core.ServiceUse{
	{Type: "dawarich", Reason: "Your location history from Dawarich, copied once or kept in sync"},
}

func (s *Server) kickDawarich() { kick(s.dawarichKick) }

// dawarichFromCore asks Pocket Core for the person's Dawarich (address and
// key). fresh skips the SDK's cache, for settings and the first check.
func (s *Server) dawarichFromCore(ctx context.Context, email string, fresh bool) (*dawarichClient, error) {
	if !s.core.Configured() {
		return nil, errors.New("Pocket Core is not set up on this server")
	}
	get := s.core.ServiceFor
	if fresh {
		get = s.core.ServiceForFresh
	}
	cred, err := get(ctx, email, "dawarich")
	if err != nil {
		var le *core.LinkError
		if errors.As(err, &le) {
			return nil, errors.New("Dawarich through Pocket Core: " + le.Message)
		}
		return nil, fmt.Errorf("Dawarich through Pocket Core: %w", err)
	}
	if cred.URL == "" || cred.Key == "" {
		return nil, errors.New("Pocket Core has no Dawarich address and API key for you")
	}
	return &dawarichClient{base: strings.TrimRight(cred.URL, "/"), key: cred.Key, http: &http.Client{Timeout: 2 * time.Minute}}, nil
}

// pickDawarich checks the Dawarich a request names: through Core
// (source "core") or the typed address and key.
func (s *Server) pickDawarich(ctx context.Context, email, source, rawURL, key string) (*dawarichClient, string, int, error) {
	if source != "core" {
		return checkDawarich(ctx, rawURL, key)
	}
	dw, err := s.dawarichFromCore(ctx, email, true)
	if err != nil {
		return nil, "", http.StatusBadGateway, err
	}
	me, err := dw.me(ctx)
	if err != nil {
		return nil, "", http.StatusBadGateway, errors.New("cannot use the Dawarich from Pocket Core: " + err.Error())
	}
	return dw, me, 0, nil
}

// dawarichLoop pulls every enabled link every few minutes, one at a time.
func (s *Server) dawarichLoop(ctx context.Context) {
	t := time.NewTicker(dawarichSyncEvery)
	defer t.Stop()
	for {
		links, err := s.store.EnabledDawarichLinks(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("dawarich links", "err", err)
		}
		for _, l := range links {
			if ctx.Err() != nil {
				return
			}
			s.syncDawarich(ctx, l)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.dawarichKick:
		}
	}
}

// syncDawarich pulls the points of one link that are new since its cursor.
func (s *Server) syncDawarich(ctx context.Context, l store.DawarichLink) {
	u, err := s.store.GetUser(ctx, l.UserID)
	if err != nil {
		return
	}
	dw := &dawarichClient{base: l.URL, key: l.APIKey, http: &http.Client{Timeout: 2 * time.Minute}}
	if l.Source == "core" {
		if dw, err = s.dawarichFromCore(ctx, u.Email, false); err != nil {
			if err := s.store.FinishDawarichSync(context.WithoutCancel(ctx), l.UserID, l.CursorTS, 0, err); err != nil {
				s.log.Warn("dawarich sync: save", "user", l.UserID, "err", err)
			}
			s.log.Warn("dawarich sync failed", "user", l.UserID, "err", err)
			return
		}
	}
	now := time.Now().Unix()
	from := max(l.CursorTS-int64(dawarichLookback/time.Second), 0)
	cursor, added := l.CursorTS, int64(0)
	var syncErr error
	for page := 1; ; page++ {
		var rows []map[string]any
		h, err := dw.get(ctx, "/api/v1/points", url.Values{"start_at": {time.Unix(from, 0).UTC().Format(time.RFC3339)},
			"end_at": {time.Unix(now+48*3600, 0).UTC().Format(time.RFC3339)}, "order": {"asc"}, "per_page": {strconv.Itoa(dawarichPerPage)}, "page": {strconv.Itoa(page)}}, &rows)
		if err != nil {
			syncErr = err
			break
		}
		pts := make([]store.Point, 0, len(rows))
		for _, row := range rows {
			p, ok := parseDawarichPoint(row)
			if !ok {
				continue
			}
			// a clock running ahead must not push the cursor past real points
			cursor = max(cursor, min(p.TS, now))
			pts = append(pts, store.Point{TS: p.TS, Lat: p.Lat, Lng: p.Lng, Altitude: p.Alt, Accuracy: p.Acc, Speed: p.Speed, Course: p.Course,
				Battery: p.Battery, Motion: p.Motion, TrackerID: p.Tracker})
		}
		res, err := s.ingest(ctx, u, nil, pts, "dawarich")
		if err != nil {
			syncErr = err
			break
		}
		added += int64(res.Added)
		total, _ := strconv.Atoi(h.Get("X-Total-Pages"))
		if len(rows) == 0 || (total > 0 && page >= total) || (total == 0 && len(rows) < dawarichPerPage) {
			break
		}
	}
	if ctx.Err() != nil {
		syncErr = nil // shutting down, not the link's fault
	}
	if err := s.store.FinishDawarichSync(context.WithoutCancel(ctx), l.UserID, cursor, added, syncErr); err != nil {
		s.log.Warn("dawarich sync: save", "user", l.UserID, "err", err)
	}
	if syncErr != nil {
		s.log.Warn("dawarich sync failed", "user", l.UserID, "url", l.URL, "err", syncErr)
	} else if added > 0 {
		s.log.Info("dawarich sync", "user", l.UserID, "added", added)
	}
}

// ---- API ----

func (s *Server) handleGetDawarichLink(w http.ResponseWriter, r *http.Request) {
	l, err := s.store.GetDawarichLink(r.Context(), userFrom(r.Context()).ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err, "get link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": l, "every_minutes": int(dawarichSyncEvery / time.Minute), "core": s.dawarichCoreStatus(r.Context(), userFrom(r.Context()).Email)})
}

// dawarichCoreStatus says whether Pocket Core can hand this person a
// Dawarich, and which one (never the key).
func (s *Server) dawarichCoreStatus(ctx context.Context, email string) map[string]any {
	if !s.core.Configured() {
		return map[string]any{"configured": false}
	}
	dw, err := s.dawarichFromCore(ctx, email, true)
	if err != nil {
		return map[string]any{"configured": true, "available": false, "error": err.Error()}
	}
	return map[string]any{"configured": true, "available": true, "url": dw.base}
}

// handleSaveDawarichLink links (or re-links) a Dawarich server. With
// copy_history it also queues a full copy (areas, visits, every point) and
// the link takes over from now; without, it starts after the newest point
// already here.
func (s *Server) handleSaveDawarichLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Source      string `json:"source"` // direct (default) | core
		URL         string `json:"url"`
		APIKey      string `json:"api_key"`
		CopyHistory bool   `json:"copy_history"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	dw, me, code, err := s.pickDawarich(ctx, u.Email, in.Source, in.URL, in.APIKey)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	l := &store.DawarichLink{UserID: u.ID, URL: dw.base, APIKey: dw.key, Account: me, Enabled: true, Source: "direct"}
	if in.Source == "core" {
		l.APIKey, l.Source = "", "core" // asked from Core on every pull
	}
	if in.CopyHistory {
		if _, err := s.queueDawarichImport(ctx, u.ID, dw, me); err != nil {
			s.fail(w, err, "queue import")
			return
		}
		l.CursorTS = time.Now().Unix()
	} else if p, err := s.store.LatestPoint(ctx, u.ID); err == nil {
		l.CursorTS = p.TS
	}
	if err := s.store.SaveDawarichLink(ctx, l); err != nil {
		s.fail(w, err, "save link")
		return
	}
	s.kickDawarich()
	l, _ = s.store.GetDawarichLink(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"link": l, "every_minutes": int(dawarichSyncEvery / time.Minute), "core": s.dawarichCoreStatus(ctx, u.Email)})
}

// handlePatchDawarichLink pauses or resumes the link.
func (s *Server) handlePatchDawarichLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(r, &in); err != nil || in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "send {\"enabled\": true|false}")
		return
	}
	if _, err := s.store.GetDawarichLink(ctx, u.ID); err != nil {
		s.fail(w, err, "get link")
		return
	}
	if err := s.store.SetDawarichLinkEnabled(ctx, u.ID, *in.Enabled); err != nil {
		s.fail(w, err, "update link")
		return
	}
	if *in.Enabled {
		s.kickDawarich()
	}
	l, _ := s.store.GetDawarichLink(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"link": l, "every_minutes": int(dawarichSyncEvery / time.Minute)})
}

// handleSyncDawarichLink pulls now instead of at the next tick.
func (s *Server) handleSyncDawarichLink(w http.ResponseWriter, r *http.Request) {
	l, err := s.store.GetDawarichLink(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "get link")
		return
	}
	if !l.Enabled {
		writeErr(w, http.StatusConflict, "the link is paused")
		return
	}
	s.kickDawarich()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleDeleteDawarichLink(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDawarichLink(r.Context(), userFrom(r.Context()).ID); err != nil {
		s.fail(w, err, "delete link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
