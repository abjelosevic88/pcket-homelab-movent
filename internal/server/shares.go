package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// ---- sharing with people on this box and by link ----

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	mine, err := s.store.SharesByOwner(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "shares")
		return
	}
	withMe, _ := s.store.SharesWithViewer(ctx, u.ID)
	base := s.publicURL(r)
	out := make([]map[string]any, 0, len(mine))
	for _, sh := range mine {
		m := shareJSON(sh)
		if sh.Token != "" {
			m["url"] = base + "/s/" + sh.Token
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mine": out, "with_me": withMe})
}

func shareJSON(sh store.Share) map[string]any {
	return map[string]any{"id": sh.ID, "viewer_id": sh.ViewerID, "viewer_name": sh.ViewerName, "viewer_email": sh.ViewerEmail, "token": sh.Token,
		"name": sh.Name, "level": sh.Level, "precision_m": sh.PrecisionM, "expires_at": sh.ExpiresAt, "created_at": sh.CreatedAt,
		"expired": sh.ExpiresAt > 0 && sh.ExpiresAt < time.Now().Unix(), "link": sh.ViewerID == nil}
}

type shareInput struct {
	ViewerID   *int64  `json:"viewer_id"`
	Link       bool    `json:"link"`
	Name       *string `json:"name"`
	Level      *string `json:"level"`
	PrecisionM *int    `json:"precision_m"`
	Hours      *int    `json:"hours"` // 0 = never expires
}

func (in shareInput) apply(sh *store.Share) error {
	if in.Name != nil {
		sh.Name = strings.TrimSpace(*in.Name)
	}
	if in.Level != nil {
		if *in.Level != "live" && *in.Level != "history" {
			return errBad("level must be live or history")
		}
		sh.Level = *in.Level
	}
	if in.PrecisionM != nil {
		sh.PrecisionM = max(0, min(50000, *in.PrecisionM))
	}
	if in.Hours != nil {
		if *in.Hours <= 0 {
			sh.ExpiresAt = 0
		} else {
			sh.ExpiresAt = time.Now().Add(time.Duration(*in.Hours) * time.Hour).Unix()
		}
	}
	return nil
}

// handleCreateShare shares your position with a person on this box
// (viewer_id) or makes a link anyone can open (link: true; tailnet only
// unless the server is public), optionally rounded and expiring.
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in shareInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	sh := &store.Share{OwnerID: u.ID, Level: "live"}
	if err := in.apply(sh); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Link {
		sh.Token = store.RandomToken(16)
		if sh.ExpiresAt == 0 && in.Hours == nil {
			sh.ExpiresAt = time.Now().Add(24 * time.Hour).Unix()
		}
	} else {
		if in.ViewerID == nil || *in.ViewerID == u.ID {
			writeErr(w, http.StatusBadRequest, "choose someone to share with, or make a link")
			return
		}
		if _, err := s.store.GetUser(ctx, *in.ViewerID); err != nil {
			writeErr(w, http.StatusBadRequest, "no such person")
			return
		}
		if prev, err := s.store.ShareFor(ctx, u.ID, *in.ViewerID); err == nil {
			_ = s.store.DeleteShare(ctx, u.ID, prev.ID)
		}
		sh.ViewerID = in.ViewerID
	}
	if err := s.store.CreateShare(ctx, sh); err != nil {
		s.fail(w, err, "create share")
		return
	}
	m := shareJSON(*sh)
	if sh.Token != "" {
		m["url"] = s.publicURL(r) + "/s/" + sh.Token
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	mine, err := s.store.SharesByOwner(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "shares")
		return
	}
	id := idParam(r, "id")
	var sh *store.Share
	for i := range mine {
		if mine[i].ID == id {
			sh = &mine[i]
		}
	}
	if sh == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	var in shareInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := in.apply(sh); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateShare(ctx, sh); err != nil {
		s.fail(w, err, "update share")
		return
	}
	writeJSON(w, http.StatusOK, shareJSON(*sh))
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteShare(r.Context(), userFrom(r.Context()).ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete share")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sharedPosition applies the share's rounding and the owner's privacy
// zones (inside one, the zone's centre is shown instead).
func (s *Server) sharedPosition(ctx context.Context, sh store.Share, lat, lng float64) (float64, float64) {
	zones, _ := s.store.PrivacyZones(ctx, sh.OwnerID)
	for _, z := range zones {
		if geo.Haversine(z.Lat, z.Lng, lat, lng) <= z.Radius {
			lat, lng = z.Lat, z.Lng
			break
		}
	}
	if sh.PrecisionM > 0 {
		step := float64(sh.PrecisionM) / 111320
		lat = math.Round(lat/step) * step
		lng = math.Round(lng/step) * step
	}
	return math.Round(lat*1e6) / 1e6, math.Round(lng*1e6) / 1e6
}

// livePerson is one person's current position as others may see it.
func (s *Server) livePerson(ctx context.Context, sh store.Share) map[string]any {
	out := map[string]any{"user_id": sh.OwnerID, "name": firstNonEmpty(sh.OwnerName, sh.OwnerEmail), "email": sh.OwnerEmail, "level": sh.Level,
		"precision_m": sh.PrecisionM, "expires_at": sh.ExpiresAt, "point": nil, "place": nil}
	p, err := s.store.LatestPoint(ctx, sh.OwnerID)
	if err != nil {
		return out
	}
	lat, lng := s.sharedPosition(ctx, sh, p.Lat, p.Lng)
	out["point"] = map[string]any{"lat": lat, "lng": lng, "ts": p.TS, "battery": p.Battery, "battery_state": p.BatteryState,
		"accuracy": math.Max(ptrF(p.Accuracy), float64(sh.PrecisionM)), "country": geo.CountryName(p.Country)}
	if vs, _ := s.store.Visits(ctx, sh.OwnerID, store.VisitQuery{From: p.TS - 60, To: p.TS + 60, NoDecline: true}); len(vs) > 0 && sh.PrecisionM == 0 {
		v := vs[len(vs)-1]
		out["place"] = map[string]any{"name": v.DisplayName(), "since": v.StartTS}
	}
	return out
}

func ptrF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// handleFamily lists everyone sharing with you and where they are.
func (s *Server) handleFamily(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	shares, err := s.store.SharesWithViewer(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "family")
		return
	}
	out := make([]map[string]any, 0, len(shares))
	for _, sh := range shares {
		out = append(out, s.livePerson(ctx, sh))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFamilyDay shows a day of someone who shares their history with you.
func (s *Server) handleFamilyDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	owner := idParam(r, "userID")
	sh, err := s.store.ShareFor(ctx, owner, u.ID)
	if err != nil || sh.Level != "history" {
		writeErr(w, http.StatusForbidden, "they do not share their history with you")
		return
	}
	s.writeDay(w, r, owner, chi.URLParam(r, "date"), false)
}

// handleSharedView is the public side of a share link.
func (s *Server) handleSharedView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sh, err := s.store.ShareByToken(ctx, chi.URLParam(r, "token"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "this link has expired or was stopped")
		return
	}
	out := s.livePerson(ctx, *sh)
	delete(out, "email")
	if sh.Name != "" {
		out["title"] = sh.Name
	}
	if sh.Level == "history" && sh.PrecisionM == 0 {
		st, loc := s.userLoc(ctx, sh.OwnerID)
		_ = st
		now := time.Now().In(loc)
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		tracks, _ := s.store.Tracks(ctx, sh.OwnerID, day.Unix(), now.Unix(), true)
		lines := make([]string, 0, len(tracks))
		for _, t := range tracks {
			lines = append(lines, t.Polyline)
		}
		out["today"] = lines
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// ---- live updates (server-sent events) ----

// publishLive tells everyone watching (the person, people they share with)
// about a new position.
func (s *Server) publishLive(userID int64, p *store.Point, d *store.Device) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if len(s.live) == 0 {
		return
	}
	ev := map[string]any{"user_id": userID, "lat": p.Lat, "lng": p.Lng, "ts": p.TS, "speed": p.Speed, "accuracy": p.Accuracy, "battery": p.Battery}
	if d != nil {
		ev["device_id"] = d.ID
	}
	b, _ := json.Marshal(ev)
	for ch := range s.live[userID] {
		select {
		case ch <- b:
		default:
		}
	}
}

// handleLiveStream streams your own new positions, and those of people
// sharing with you (rounded as they chose), as server-sent events.
func (s *Server) handleLiveStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	shares, _ := s.store.SharesWithViewer(ctx, u.ID)
	byOwner := map[int64]store.Share{}
	watch := []int64{u.ID}
	for _, sh := range shares {
		byOwner[sh.OwnerID] = sh
		watch = append(watch, sh.OwnerID)
	}
	ch := make(chan []byte, 16)
	s.liveMu.Lock()
	for _, id := range watch {
		if s.live[id] == nil {
			s.live[id] = map[chan []byte]struct{}{}
		}
		s.live[id][ch] = struct{}{}
	}
	s.liveMu.Unlock()
	defer func() {
		s.liveMu.Lock()
		for _, id := range watch {
			delete(s.live[id], ch)
			if len(s.live[id]) == 0 {
				delete(s.live, id)
			}
		}
		s.liveMu.Unlock()
	}()
	fmt.Fprintf(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case b := <-ch:
			var ev map[string]any
			_ = json.Unmarshal(b, &ev)
			owner := int64(ev["user_id"].(float64))
			if sh, ok := byOwner[owner]; ok && owner != u.ID {
				if sh.ExpiresAt > 0 && sh.ExpiresAt < time.Now().Unix() {
					continue
				}
				lat, lng := s.sharedPosition(ctx, sh, ev["lat"].(float64), ev["lng"].(float64))
				ev = map[string]any{"user_id": owner, "lat": lat, "lng": lng, "ts": ev["ts"], "battery": ev["battery"]}
				b, _ = json.Marshal(ev)
			}
			fmt.Fprintf(w, "event: point\ndata: %s\n\n", b)
			fl.Flush()
		}
	}
}

// osmandKey lets Traccar Client's JSON mode authenticate with the device
// identifier in the body (it has no other place for a key).
func (s *Server) osmandKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if credential(r) == "" && r.Body != nil && r.Method == http.MethodPost && strings.Contains(r.Header.Get("Content-Type"), "json") {
			b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
			if err == nil {
				var m map[string]any
				if json.Unmarshal(b, &m) == nil {
					if id := str(first(m, "device_id", "id")); strings.HasPrefix(id, "pm_") {
						r.Header.Set("X-API-Key", id)
					}
				}
				r.Body = io.NopCloser(bytes.NewReader(b))
			}
		}
		next.ServeHTTP(w, r)
	})
}
