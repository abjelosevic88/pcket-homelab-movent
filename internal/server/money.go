package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// ---- Pocket Money: where you paid ----
//
// Purchases that have a location (a scanned receipt's shop) are drawn on the
// map next to the visits recorded here, and a visit with a purchase inside
// it is named after the shop. Reached like Pocket Health: through a Pocket
// Core app link (transactions:read, acting as the person), directly with a
// URL + API token, or not at all.

const moneyScope = "transactions:read"

var moneyApp = struct{ slug, name, reason string }{"money", "Pocket Money", "Show where you paid on the map, and name the places you visited after the shop"}

func moneyUses() []core.Use {
	return []core.Use{{App: moneyApp.slug, Scopes: []string{moneyScope}, Reason: moneyApp.reason}}
}

func (s *Server) moneyMode(st store.Settings) string {
	switch st.MoneyMode {
	case modeCore, modeDirect, modeOff:
		return st.MoneyMode
	}
	if st.MoneyToken != "" || !s.core.Configured() {
		return modeDirect
	}
	return modeCore
}

func moneyLinkText(reason string) string {
	return strings.ReplaceAll(strings.ReplaceAll(linkReasonText(reason), "Pocket Health", "Pocket Money"), "Movement → Pocket Health", "Movement → Pocket Money")
}

// moneyFor connects to the person's Pocket Money.
func (s *Server) moneyFor(ctx context.Context, u *store.User, st store.Settings) (*healthClient, error) {
	switch s.moneyMode(st) {
	case modeOff:
		return nil, &notConnected{"Pocket Money is switched off in Settings → Connected apps"}
	case modeDirect:
		if st.MoneyToken == "" || st.MoneyURL == "" {
			return nil, &notConnected{"Pocket Money is not set up (Settings → Connected apps): add its address and a token, or switch to Pocket Core"}
		}
		return &healthClient{name: moneyApp.name, base: st.MoneyURL, token: st.MoneyToken, public: firstNonEmpty(st.MoneyPublicURL, st.MoneyURL), via: modeDirect, http: &http.Client{Timeout: 20 * time.Second}}, nil
	}
	if !s.core.Configured() {
		return nil, &notConnected{"Pocket Money through Pocket Core: there is no Pocket Core on this server; use a direct connection"}
	}
	tok, err := s.core.ActAs(ctx, u.Email, moneyApp.slug, moneyScope)
	if err != nil {
		var le *core.LinkError
		if errors.As(err, &le) {
			return nil, &notConnected{"Pocket Money is not set up: " + moneyLinkText(le.Reason)}
		}
		return nil, fmt.Errorf("Pocket Money through Pocket Core: %w", err)
	}
	if tok.URL == "" || tok.AccessToken == "" {
		return nil, errors.New("Pocket Money through Pocket Core: Core gave no address or token")
	}
	return &healthClient{name: moneyApp.name, base: strings.TrimRight(tok.URL, "/"), token: tok.AccessToken, public: strings.TrimRight(firstNonEmpty(tok.PublicURL, tok.URL), "/"), via: modeCore, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

// purchase is one located purchase as Pocket Money's /places gives it.
type purchase struct {
	ID           int64   `json:"id"`
	Date         string  `json:"date"`
	Time         string  `json:"time"`
	TS           int64   `json:"ts"`
	TimeKnown    bool    `json:"time_known"`
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	Kind         string  `json:"kind"`
	Payee        string  `json:"payee"`
	Description  string  `json:"description"`
	Amount       int64   `json:"amount"`
	Currency     string  `json:"currency"`
	Category     string  `json:"category"`
	CategoryIcon string  `json:"category_icon"`
	Shop         string  `json:"shop,omitempty"`
	Address      string  `json:"address,omitempty"`
	City         string  `json:"city,omitempty"`
	Items        int     `json:"items,omitempty"`
	URL          string  `json:"url"`
}

// handlePurchases lists the person's located purchases in a date range.
func (s *Server) handlePurchases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	if from == "" {
		from = to
	}
	mc, err := s.moneyFor(ctx, u, st)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": !isNotConnected(err), "detail": err.Error(), "items": []any{}})
		return
	}
	var out struct {
		Items []purchase `json:"items"`
	}
	if err := mc.get(ctx, "/api/v1/places", url.Values{"from": {from}, "to": {to}}, &out); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": true, "via": mc.via, "detail": err.Error(), "open_url": mc.public, "items": []any{}})
		return
	}
	if out.Items == nil {
		out.Items = []purchase{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "via": mc.via, "open_url": mc.public, "items": out.Items})
}

// handleMoneyStatus says how the person reaches Pocket Money and tests it.
func (s *Server) handleMoneyStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	mode := s.moneyMode(st)
	mc, err := s.moneyFor(ctx, u, st)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": false, "detail": err.Error(), "core_configured": s.core.Configured()})
		return
	}
	if r.Method == http.MethodPost {
		var out struct {
			Items []purchase `json:"items"`
		}
		q := url.Values{"from": {time.Now().AddDate(0, 0, -90).Format("2006-01-02")}, "to": {time.Now().Format("2006-01-02")}}
		if err := mc.get(ctx, "/api/v1/places", q, &out); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": false, "via": mc.via, "detail": err.Error(), "core_configured": s.core.Configured()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": true, "via": mc.via, "open_url": mc.public,
			"message": fmt.Sprintf("Pocket Money answers (%s): %d purchases with a place in the last 90 days", map[string]string{modeCore: "through Pocket Core", modeDirect: "direct connection"}[mc.via], len(out.Items)), "core_configured": s.core.Configured()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "connected": true, "via": mc.via, "open_url": mc.public, "core_configured": s.core.Configured()})
}
