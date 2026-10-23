package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"rsc.io/qr"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// Device kinds the setup guide knows.
var deviceKinds = map[string]string{
	"dawarich":      "Dawarich app (iOS / Android)",
	"owntracks":     "OwnTracks",
	"overland":      "Overland (iOS)",
	"gpslogger":     "GPSLogger (Android)",
	"traccar":       "Traccar Client / OsmAnd",
	"homeassistant": "Home Assistant",
	"custom":        "Your own app or script",
	"integration":   "Pocket Travel, Garage or another Dawarich client",
	"other":         "Other",
}

func defaultScope(kind string) string {
	switch kind {
	case "integration":
		return store.ScopeRead
	case "owntracks", "overland", "gpslogger", "traccar", "homeassistant":
		return store.ScopeTrack
	}
	return store.ScopeFull
}

func validScope(s string) bool {
	return s == store.ScopeTrack || s == store.ScopeRead || s == store.ScopeFull
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.store.ListDevices(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "list devices")
		return
	}
	for i := range ds {
		ds[i].APIKey = maskKey(ds[i].APIKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": ds, "kinds": deviceKinds})
}

func maskKey(k string) string {
	if len(k) <= 10 {
		return "••••"
	}
	return k[:7] + "…" + k[len(k)-4:]
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if _, ok := deviceKinds[in.Kind]; !ok {
		in.Kind = "other"
	}
	if !validScope(in.Scope) {
		in.Scope = defaultScope(in.Kind)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		in.Name = deviceKinds[in.Kind]
	}
	d := &store.Device{UserID: u.ID, Name: in.Name, Kind: in.Kind, Scope: in.Scope}
	if err := s.store.CreateDevice(ctx, d); err != nil {
		s.fail(w, err, "create device")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device": d, "setup": s.setupFor(r, d)})
}

func (s *Server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDevice(r.Context(), userFrom(r.Context()).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get device")
		return
	}
	d.APIKey = maskKey(d.APIKey)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := s.store.GetDevice(ctx, userFrom(ctx).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get device")
		return
	}
	var in struct {
		Name    *string `json:"name"`
		Kind    *string `json:"kind"`
		Scope   *string `json:"scope"`
		Revoked *bool   `json:"revoked"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		d.Name = strings.TrimSpace(*in.Name)
	}
	if in.Kind != nil {
		if _, ok := deviceKinds[*in.Kind]; ok {
			d.Kind = *in.Kind
		}
	}
	if in.Scope != nil && validScope(*in.Scope) {
		d.Scope = *in.Scope
	}
	if in.Revoked != nil {
		d.Revoked = *in.Revoked
	}
	if err := s.store.UpdateDevice(ctx, d); err != nil {
		s.fail(w, err, "update device")
		return
	}
	d.APIKey = maskKey(d.APIKey)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteDevice(r.Context(), userFrom(r.Context()).ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "delete device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotateDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if _, err := s.store.RotateDeviceKey(ctx, u.ID, idParam(r, "id")); err != nil {
		s.fail(w, err, "rotate key")
		return
	}
	d, _ := s.store.GetDevice(ctx, u.ID, idParam(r, "id"))
	writeJSON(w, http.StatusOK, map[string]any{"device": d, "setup": s.setupFor(r, d)})
}

// handleDeviceSetup shows the key and how to configure the app again.
func (s *Server) handleDeviceSetup(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDevice(r.Context(), userFrom(r.Context()).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": d, "setup": s.setupFor(r, d)})
}

// Setup is what a person needs to connect one app.
type Setup struct {
	Kind   string            `json:"kind"`
	Title  string            `json:"title"`
	Fields []SetupField      `json:"fields"`
	Steps  []string          `json:"steps"`
	QR     string            `json:"qr,omitempty"`   // what the QR code holds
	Link   string            `json:"link,omitempty"` // tap to configure on the phone
	Code   map[string]string `json:"code,omitempty"` // example snippets
	Notes  []string          `json:"notes,omitempty"`
}

// SetupField is one value to copy into the app.
type SetupField struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Copy  bool   `json:"copy"`
}

func (s *Server) setupFor(r *http.Request, d *store.Device) Setup {
	base := s.publicURL(r)
	key := d.APIKey
	st := Setup{Kind: d.Kind, Title: deviceKinds[d.Kind]}
	switch d.Kind {
	case "dawarich":
		st.Fields = []SetupField{{"Server URL", base, true}, {"API key", key, true}}
		st.Steps = []string{"Install Dawarich from the App Store or Google Play.", "On the sign-in screen choose your own server / API key.",
			"Enter the server URL and the API key below.", "Allow location access “Always” and start tracking."}
		st.QR = key
		st.Notes = []string{"The app checks the server with /api/v1/health and /api/v1/users/me and uploads batches to /api/v1/points, exactly as with Dawarich.",
			"Tracking settings you change in the app (mode, distance filter, batch size) are kept here too."}
	case "owntracks":
		u := base + "/api/v1/owntracks/points?api_key=" + url.QueryEscape(key)
		conf := map[string]any{"_type": "configuration", "mode": 3, "url": u, "deviceId": "phone", "tid": "ME", "monitoring": 1, "locatorDisplacement": 50,
			"locatorInterval": 60, "pubExtendedData": true, "auth": false}
		b, _ := json.Marshal(conf)
		st.Link = "owntracks:///config?inline=" + base64.StdEncoding.EncodeToString(b)
		st.QR = st.Link
		st.Fields = []SetupField{{"Mode", "HTTP", false}, {"URL", u, true}}
		st.Steps = []string{"Install OwnTracks.", "Scan the QR code with the phone's camera (or tap the link on the phone) and accept the configuration.",
			"Or by hand: Preferences → Connection → Mode HTTP, URL as below, no authentication.", "Set monitoring to Significant or Move."}
		st.Notes = []string{"People who share their location with you appear as friends on the OwnTracks map."}
	case "overland":
		endpoint := base + "/api/v1/overland/batches?api_key=" + url.QueryEscape(key)
		st.Link = "overland://setup?" + url.Values{"url": {base + "/api/v1/overland/batches"}, "token": {key}, "device_id": {"phone"}, "unique_id": {"yes"}}.Encode()
		st.QR = st.Link
		st.Fields = []SetupField{{"Receiver endpoint", endpoint, true}}
		st.Steps = []string{"Install Overland.", "Scan the QR code (or tap the link on the iPhone), or paste the receiver endpoint under Settings.",
			"Batch size 50–200, continuous or significant tracking.", "Start tracking."}
	case "gpslogger":
		u := base + "/gpslogger?api_key=" + url.QueryEscape(key) + "&lat=%LAT&lon=%LON&time=%TIME&alt=%ALT&acc=%ACC&spd=%SPD&dir=%DIR&batt=%BATT&ischarging=%ISCHARGING&ser=%SER"
		st.Fields = []SetupField{{"URL", u, true}, {"HTTP method", "GET", false}}
		st.Steps = []string{"Install GPSLogger from F-Droid or GitHub.", "Logging details → Log to custom URL → enable.", "Paste the URL below, method GET.", "Allow background location."}
	case "traccar":
		st.Fields = []SetupField{{"Server URL", base + "/osmand", true}, {"Device identifier", key, true}}
		st.Steps = []string{"Install Traccar Client (or any OsmAnd-protocol tracker).", "Set the server URL and use the key as the device identifier.",
			"Frequency 30–60 s, distance 25–50 m."}
		st.QR = key
		st.Notes = []string{"Newer Traccar Client versions that send JSON work too; if yours asks for a URL only, use " + base + "/osmand?api_key=" + key}
	case "homeassistant":
		st.Fields = []SetupField{{"URL", base + "/api/v1/locations", true}, {"Bearer token", key, true}}
		st.Code = map[string]string{"configuration.yaml": `rest_command:
  pocket_movement:
    url: "` + base + `/api/v1/locations"
    method: POST
    headers:
      Authorization: "Bearer ` + key + `"
    content_type: "application/json"
    payload: >
      {"lat": {{ state_attr('device_tracker.phone','latitude') }},
       "lon": {{ state_attr('device_tracker.phone','longitude') }},
       "accuracy": {{ state_attr('device_tracker.phone','gps_accuracy') | default(0) }},
       "battery": {{ state_attr('device_tracker.phone','battery_level') | default(0) }},
       "tracker_id": "home-assistant"}

automation:
  - alias: Send position to Pocket Movement
    trigger:
      - platform: state
        entity_id: device_tracker.phone
        attribute: latitude
    action:
      - service: rest_command.pocket_movement`}
		st.Steps = []string{"Add the rest_command and automation below to Home Assistant (replace device_tracker.phone).", "Restart Home Assistant."}
	case "integration":
		st.Fields = []SetupField{{"Dawarich URL", base, true}, {"API key", key, true}}
		if s.cfg.CoreAppURL != "" && s.cfg.CoreAppURL != base {
			st.Fields = append(st.Fields, SetupField{"Address from other containers", s.cfg.CoreAppURL, true})
		}
		st.Steps = []string{"Anything that talks to Dawarich can use this server: Pocket Travel, Pocket Garage, Pocket Core's Dawarich service, Home Assistant's Dawarich integration.",
			"Use the URL and key below where they ask for the Dawarich URL and API key."}
	default:
		st.Fields = []SetupField{{"Endpoint", base + "/api/v1/locations", true}, {"API key", key, true}}
		st.Code = map[string]string{
			"curl": `curl -X POST "` + base + `/api/v1/locations" \
  -H "Authorization: Bearer ` + key + `" \
  -H "Content-Type: application/json" \
  -d '[{"lat": 43.8563, "lon": 18.4131, "timestamp": "2026-10-23T08:30:00Z", "accuracy": 12, "speed": 1.4, "battery": 87}]'`,
			"fields": "lat, lon (or lng/longitude), timestamp (ISO 8601 or unix seconds/ms; default now), accuracy (m), altitude (m), speed (m/s) or vel (km/h), course, battery (0–100), battery_state, tracker_id, motion. One object, an array, or {\"locations\": [...]}; GeoJSON Point features work too. The answer says how many were added and how many were duplicates.",
		}
		st.Steps = []string{"Send positions with the key as a bearer token (or ?api_key=).", "Batches of up to thousands of points per request are fine; re-sending is harmless."}
	}
	return st
}

// handleDeviceQR renders the setup QR code as SVG.
func (s *Server) handleDeviceQR(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDevice(r.Context(), userFrom(r.Context()).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get device")
		return
	}
	st := s.setupFor(r, d)
	text := st.QR
	if text == "" {
		text = d.APIKey
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "qr: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	n := code.Size
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-4 -4 %d %d" shape-rendering="crispEdges"><rect x="-4" y="-4" width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n+8, n+8, n+8, n+8)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	_, _ = w.Write([]byte(b.String()))
}
