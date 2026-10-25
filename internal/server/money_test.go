package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Purchases come from Pocket Money's /places (here a direct connection to a
// stand-in), and connection settings survive a save.
func TestMoneyPurchases(t *testing.T) {
	s, u := newTestServer(t)
	money := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/places" || r.Header.Get("Authorization") != "Bearer pm_test" || r.URL.Query().Get("from") != "2026-10-25" {
			w.WriteHeader(403)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":7,"date":"2026-10-25","time":"10:27","ts":1792920420,"time_known":true,"lat":44.7624,"lng":17.1991,"kind":"expense","payee":"Konzum","amount":995,"currency":"BAM","category":"fasunga","shop":"Konzum","items":3,"url":"https://money.example/transactions/7"}],"home_currency":"BAM"}`))
	}))
	defer money.Close()
	call := func(method, path, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), userKey, u))
		rec := httptest.NewRecorder()
		switch {
		case strings.HasPrefix(path, "/api/v1/settings"):
			if method == "PUT" {
				s.handlePutSettings(rec, req)
			} else {
				s.handleGetSettings(rec, req)
			}
		case strings.HasPrefix(path, "/api/v1/purchases"):
			s.handlePurchases(rec, req)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	call("PUT", "/api/v1/settings", `{"money_mode":"direct","money_url":"`+money.URL+`/","money_token":"pm_test","health_mode":"off"}`)
	got := call("GET", "/api/v1/settings", "")["settings"].(map[string]any)
	if got["money_mode"] != "direct" || got["money_url"] != money.URL || got["health_mode"] != "off" || !strings.Contains(got["money_token"].(string), "••") {
		t.Fatalf("settings not kept: %+v", got)
	}
	// the masked token coming back from the form keeps the real one
	call("PUT", "/api/v1/settings", `{"money_token":"`+got["money_token"].(string)+`"}`)
	out := call("GET", "/api/v1/purchases?from=2026-10-25&to=2026-10-25", "")
	items, _ := out["items"].([]any)
	if out["connected"] != true || len(items) != 1 || items[0].(map[string]any)["shop"] != "Konzum" {
		t.Fatalf("purchases %+v", out)
	}
	call("PUT", "/api/v1/settings", `{"money_mode":"off"}`)
	if out := call("GET", "/api/v1/purchases", ""); out["connected"] != false {
		t.Fatalf("off: %+v", out)
	}
}
