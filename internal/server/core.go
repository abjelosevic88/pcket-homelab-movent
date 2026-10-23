package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// ---- Pocket Core ----

// Topics Movement publishes. Exact positions never go on the bus: places by
// name, countries, times.
var publishes = []string{"movement.visit_detected", "movement.country_entered", "movement.tracking_stopped", "movement.tracking_resumed"}

// linkOffers are what other Pocket apps may read here for a person.
var linkOffers = []core.Offer{
	{Scope: "movement:read", Name: "See where you have been", Description: "Your timeline (journeys, visits, places), day summaries, reports and where you were at a given time."},
	{Scope: "movement:points", Name: "Read your recorded positions", Description: "The raw location points of a time range (as Dawarich's API), for routes on trips and vehicle mileage."},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scope   string // "" = any linked app
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(summary|where|insights|calendar|countries|search|now|map|timeline|visits|places|tracks|stats)$`), "movement:read"},
	{"GET", regexp.MustCompile(`^/api/v1/(day/[0-9-]+|places/\d+|journeys/\d+|visits/\d+)$`), "movement:read"},
	{"GET", regexp.MustCompile(`^/api/v1/(points|map/points|points/tracked_months|users/me|health)$`), "movement:points"},
}

func linkAllows(scopes []string, r *http.Request) bool {
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.scope {
				return true
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token and maps it to the person it acts for.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, err
	}
	if !in.Active || !in.Delegated() || in.User == nil || in.User.Email == "" {
		return nil, http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, errLinkScope
	}
	return s.coreIdentityUser(ctx, in)
}

// coreLoop registers with Core on start (retrying until it answers) and
// re-registers every 30 minutes so version and last-seen stay fresh.
func (s *Server) coreLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		err := s.registerWithCore(ctx)
		s.coreMu.Lock()
		s.coreOK, s.coreErr = err == nil, ""
		if err != nil {
			s.coreErr = err.Error()
		}
		s.coreMu.Unlock()
		if err != nil {
			s.log.Warn("pocket core: registration failed, retrying", "err", err, "in", wait.String())
		} else {
			wait = 30 * time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil && wait < 5*time.Minute {
			wait *= 2
		}
	}
}

func (s *Server) registerWithCore(ctx context.Context) error {
	m := core.Manifest{
		Slug: "movement", Name: "Pocket Movement",
		Description: "Your location history, kept at home: where you went, how and how far, the places you stop and the countries you have been — from your phone's own tracker app or the Dawarich app.",
		Icon:        "/favicon.svg", Version: Version,
		URL: s.cfg.CoreAppURL, PublicURL: s.cfg.BaseURL, HealthPath: "/healthz", EnvPrefix: "MOVEMENT",
		Publishes: publishes,
		Offers:    linkOffers,
	}
	if s.cfg.BaseURL != "" {
		m.OIDC = &core.OIDCRequest{RedirectURIs: []string{s.oidcCallbackURL()}}
	}
	reg, err := s.core.Register(ctx, s.cfg.CoreRegistrationKey, m)
	if err != nil {
		return err
	}
	if reg.Token != "" {
		s.log.Info("pocket core: registered, app token stored", "core", s.cfg.CoreURL)
	} else {
		s.log.Debug("pocket core: re-registered", "core", s.cfg.CoreURL)
	}
	return nil
}

func (s *Server) coreStatus() map[string]any {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return map[string]any{"configured": s.core.Configured(), "url": s.cfg.CoreURL, "registered": s.coreOK, "error": s.coreErr}
}

// event queues a Core event about a person (no-op without Core). The
// payload gets user_email and a link to the page.
func (s *Server) event(ctx context.Context, userID int64, topic, subject string, payload map[string]any) {
	if !s.core.Configured() {
		return
	}
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return
	}
	payload["user_email"] = u.Email
	payload["owner_email"] = u.Email
	if p, ok := payload["path"].(string); ok {
		if s.cfg.BaseURL != "" {
			payload["url"] = s.cfg.BaseURL + p
		}
		delete(payload, "path")
	}
	if err := core.QueueEvent(ctx, s.store.DB, topic, subject, payload); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("pocket core: queue event", "topic", topic, "err", err)
	}
}
