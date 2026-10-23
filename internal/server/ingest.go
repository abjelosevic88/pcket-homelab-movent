package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

// ---- the one way in ----

// ingest stores positions for the caller: validates them, tags the country
// and device, drops points inside "drop" privacy zones, then wakes the
// rebuild, live views and notifications.
func (s *Server) ingest(ctx context.Context, u *store.User, d *store.Device, pts []store.Point, source string) (store.InsertResult, error) {
	now := time.Now().Unix()
	zones, _ := s.store.PrivacyZones(ctx, u.ID)
	clean := pts[:0]
	for _, p := range pts {
		if !geo.ValidCoord(p.Lat, p.Lng) || p.TS < 315532800 || p.TS > now+24*3600 { // 1980 .. tomorrow
			continue
		}
		drop := false
		for _, z := range zones {
			if z.DropPoints && geo.Haversine(z.Lat, z.Lng, p.Lat, p.Lng) <= z.Radius {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		p.UserID = u.ID
		if strings.HasPrefix(p.TrackerID, "pm_") {
			p.TrackerID = "" // a key used as the device identifier is not a name
		}
		if d != nil && p.DeviceID == nil {
			id := d.ID
			p.DeviceID = &id
		}
		if p.Source == "" {
			p.Source = source
		}
		p.Lat, p.Lng = round7(p.Lat), round7(p.Lng)
		p.Country = geo.CountryAt(p.Lat, p.Lng)
		clean = append(clean, p)
	}
	res, err := s.store.InsertPoints(ctx, u.ID, clean)
	res.Received = len(pts)
	if err != nil {
		return res, err
	}
	if d != nil {
		s.store.DeviceGotPoints(ctx, d.ID, res.Added, res.Last)
	}
	if res.Added > 0 {
		s.kickRebuild()
		if res.Last != nil && now-res.Last.TS < 3600 {
			s.publishLive(u.ID, res.Last, d)
			go s.onLivePoint(context.WithoutCancel(ctx), u, *res.Last)
		}
	}
	return res, nil
}

func round7(f float64) float64 { return math.Round(f*1e7) / 1e7 }

// ---- loose value parsing (trackers send numbers as numbers or strings) ----

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func numPtr(v any) *float64 {
	if f, ok := num(v); ok {
		return &f
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64, json.Number, int, int64:
		f, _ := num(x)
		return strconv.FormatFloat(f, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// parseTS reads a time: unix seconds or milliseconds (number or digits),
// or an ISO 8601 / RFC 3339 string with or without a zone (UTC assumed).
func parseTS(v any) (int64, bool) {
	if f, ok := num(v); ok {
		if _, isStr := v.(string); !isStr || !strings.ContainsAny(v.(string), "-:T") {
			if f > 1e11 {
				f /= 1000 // milliseconds
			}
			return int64(f), f > 0
		}
	}
	s := str(v)
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

func readJSON(r *http.Request, limit int64) (any, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func batteryPct(v any) *int {
	f, ok := num(v)
	if !ok || f < 0 {
		return nil
	}
	if f <= 1 && !isIntLike(v) {
		f *= 100 // 0..1 fraction
	}
	n := int(math.Round(math.Min(f, 100)))
	return &n
}

func isIntLike(v any) bool {
	s := str(v)
	return s != "" && !strings.Contains(s, ".") && s != "0" && s != "1"
}

// ---- GeoJSON features: Overland, the Dawarich apps, our own API ----

// featurePoint reads one GeoJSON Feature with Overland's property names.
func featurePoint(f map[string]any) (store.Point, bool) {
	g := obj(f["geometry"])
	coords, _ := g["coordinates"].([]any)
	props := obj(f["properties"])
	if len(coords) < 2 || props == nil {
		return store.Point{}, false
	}
	lng, ok1 := num(coords[0])
	lat, ok2 := num(coords[1])
	ts, ok3 := parseTS(first(props, "timestamp", "time", "tst"))
	if !ok1 || !ok2 || !ok3 {
		return store.Point{}, false
	}
	p := store.Point{TS: ts, Lat: lat, Lng: lng,
		Altitude: numPtr(first(props, "altitude", "alt")), Accuracy: numPtr(first(props, "horizontal_accuracy", "accuracy", "acc")),
		VerticalAccuracy: numPtr(first(props, "vertical_accuracy", "vac")), Speed: numPtr(first(props, "speed", "velocity")),
		Course: numPtr(first(props, "course", "bearing", "heading")), Battery: batteryPct(first(props, "battery_level", "battery")),
		BatteryState: str(first(props, "battery_state", "battery_status")), SSID: str(first(props, "wifi", "ssid")),
		TrackerID: str(first(props, "device_id", "tracker_id"))}
	if len(coords) >= 3 && p.Altitude == nil {
		p.Altitude = numPtr(coords[2])
	}
	if p.Speed != nil && *p.Speed < 0 {
		p.Speed = nil
	}
	if p.Course != nil && *p.Course < 0 {
		p.Course = nil
	}
	if p.Accuracy != nil && *p.Accuracy < 0 {
		p.Accuracy = nil
	}
	switch m := props["motion"].(type) {
	case []any:
		parts := make([]string, 0, len(m))
		for _, x := range m {
			if s := str(x); s != "" {
				parts = append(parts, s)
			}
		}
		p.Motion = strings.Join(parts, ",")
	case string:
		p.Motion = m
	}
	if a := str(props["activity"]); a != "" && p.Motion == "" {
		p.Motion = a
	}
	extra := map[string]any{}
	for _, k := range []string{"pauses", "deferred", "significant_change", "locations_in_payload", "desired_accuracy", "activity", "action", "unique_id"} {
		if v, ok := props[k]; ok {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		b, _ := json.Marshal(extra)
		p.Extra = string(b)
	}
	return p, true
}

func first(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

func featureList(body any) []map[string]any {
	var list []any
	switch b := body.(type) {
	case map[string]any:
		if l, ok := b["locations"].([]any); ok {
			list = l
		} else if l, ok := b["features"].([]any); ok {
			list = l
		} else if l, ok := b["points"].([]any); ok {
			list = l
		} else {
			list = []any{b}
		}
	case []any:
		list = b
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if m := obj(x); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// handleOverland takes Overland's batches (and anything else posting
// {"locations": [GeoJSON features]}).
func (s *Server) handleOverland(w http.ResponseWriter, r *http.Request) {
	body, err := readJSON(r, 64<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	var pts []store.Point
	for _, f := range featureList(body) {
		if p, ok := featurePoint(f); ok {
			pts = append(pts, p)
		}
	}
	if _, err := s.ingest(r.Context(), userFrom(r.Context()), deviceFrom(r.Context()), pts, "overland"); err != nil {
		s.fail(w, err, "store points")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"result": "ok"})
}

// handlePointsCreate is POST /api/v1/points as the Dawarich apps use it: the
// same GeoJSON batch; it answers with the stored points.
func (s *Server) handlePointsCreate(w http.ResponseWriter, r *http.Request) {
	body, err := readJSON(r, 64<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	var pts []store.Point
	feats := featureList(body)
	for _, f := range feats {
		if p, ok := featurePoint(f); ok {
			pts = append(pts, p)
		} else if p, ok := flatPoint(f); ok {
			pts = append(pts, p)
		}
	}
	if len(feats) > 0 && len(pts) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "Timestamp must be a date and time or Unix seconds")
		return
	}
	src := "dawarich"
	if d := deviceFrom(r.Context()); d != nil && d.Kind != "" && d.Kind != "dawarich" {
		src = d.Kind
	}
	if _, err := s.ingest(r.Context(), userFrom(r.Context()), deviceFrom(r.Context()), pts, src); err != nil {
		s.fail(w, err, "store points")
		return
	}
	data := make([]map[string]any, 0, len(pts))
	for _, p := range pts {
		data = append(data, map[string]any{"timestamp": p.TS, "latitude": strconv.FormatFloat(p.Lat, 'f', -1, 64), "longitude": strconv.FormatFloat(p.Lng, 'f', -1, 64), "lonlat": fmt.Sprintf("POINT (%v %v)", p.Lng, p.Lat)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// ---- flat objects: our simple API, GPSLogger, OsmAnd/Traccar, Home Assistant ----

// flatPoint reads {lat, lon, timestamp, …} under the usual aliases.
func flatPoint(m map[string]any) (store.Point, bool) {
	if loc := obj(m["location"]); loc != nil { // Traccar Client JSON (transistorsoft)
		return traccarJSON(m, loc)
	}
	lat, ok1 := num(first(m, "lat", "latitude"))
	lng, ok2 := num(first(m, "lon", "lng", "long", "longitude"))
	if !ok1 || !ok2 {
		return store.Point{}, false
	}
	tsv := first(m, "timestamp", "ts", "tst", "time", "recorded_at", "datetime", "date")
	ts, ok := parseTS(tsv)
	if tsv == nil {
		ts, ok = time.Now().Unix(), true // GPSLogger/HA without a time: now
	}
	if !ok {
		return store.Point{}, false
	}
	p := store.Point{TS: ts, Lat: lat, Lng: lng,
		Altitude: numPtr(first(m, "altitude", "alt", "elevation")), Accuracy: numPtr(first(m, "accuracy", "acc", "hdop_m", "horizontal_accuracy")),
		VerticalAccuracy: numPtr(first(m, "vertical_accuracy", "vac")), Course: numPtr(first(m, "course", "bearing", "heading", "dir", "cog")),
		Battery: batteryPct(first(m, "battery", "batt", "battery_level")), TrackerID: str(first(m, "tracker_id", "device_id", "device", "tid", "ser")),
		BatteryState: str(first(m, "battery_state", "battery_status")), Motion: str(first(m, "motion", "activity")), SSID: str(first(m, "ssid", "wifi"))}
	if sp := numPtr(first(m, "speed", "spd", "velocity_ms")); sp != nil {
		p.Speed = sp
	} else if kmh := numPtr(first(m, "vel", "speed_kmh")); kmh != nil {
		v := *kmh / 3.6
		p.Speed = &v
	}
	if p.Speed != nil && *p.Speed < 0 {
		p.Speed = nil
	}
	if p.BatteryState == "" {
		switch strings.ToLower(str(first(m, "charging", "ischarging", "charge", "is_charging"))) {
		case "true", "1":
			p.BatteryState = "charging"
		case "false", "0":
			p.BatteryState = "unplugged"
		}
	}
	return p, true
}

func traccarJSON(m, loc map[string]any) (store.Point, bool) {
	coords := obj(loc["coords"])
	if coords == nil {
		coords = loc
	}
	lat, ok1 := num(coords["latitude"])
	lng, ok2 := num(coords["longitude"])
	ts, ok3 := parseTS(loc["timestamp"])
	if !ok1 || !ok2 || !ok3 {
		return store.Point{}, false
	}
	p := store.Point{TS: ts, Lat: lat, Lng: lng, Altitude: numPtr(coords["altitude"]), Accuracy: numPtr(coords["accuracy"]), Speed: numPtr(coords["speed"]),
		Course: numPtr(coords["heading"]), TrackerID: str(first(m, "device_id", "id"))}
	if p.Speed != nil && *p.Speed < 0 {
		p.Speed = nil
	}
	if p.Course != nil && *p.Course < 0 {
		p.Course = nil
	}
	bat := obj(loc["battery"])
	if bat == nil {
		bat = obj(m["battery"])
	}
	if bat != nil {
		p.Battery = batteryPct(bat["level"])
		if c, ok := bat["is_charging"].(bool); ok {
			p.BatteryState = map[bool]string{true: "charging", false: "unplugged"}[c]
		}
	}
	act := obj(loc["activity"])
	if act == nil {
		act = obj(m["activity"])
	}
	if act != nil {
		p.Motion = str(act["type"])
	}
	return p, true
}

// handleLocations is the simple API for your own app: one object, an array,
// or {"locations": [...]}, each flat or a GeoJSON Feature.
func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	body, err := readJSON(r, 64<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	var pts []store.Point
	rejected := 0
	for _, f := range featureList(body) {
		if _, isFeature := f["geometry"]; isFeature {
			if p, ok := featurePoint(f); ok {
				pts = append(pts, p)
				continue
			}
		} else if p, ok := flatPoint(f); ok {
			pts = append(pts, p)
			continue
		}
		rejected++
	}
	res, err := s.ingest(r.Context(), userFrom(r.Context()), deviceFrom(r.Context()), pts, "api")
	if err != nil {
		s.fail(w, err, "store points")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": res.Received + rejected, "added": res.Added, "duplicates": len(pts) - res.Added, "rejected": rejected})
}

// paramsMap merges the query string, a form body and a JSON object body.
func paramsMap(r *http.Request) (map[string]any, error) {
	m := map[string]any{}
	add := func(v url.Values) {
		for k, vs := range v {
			if len(vs) > 0 {
				m[k] = vs[0]
			}
		}
	}
	add(r.URL.Query())
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		ct := r.Header.Get("Content-Type")
		switch {
		case strings.Contains(ct, "json"):
			body, err := readJSON(r, 8<<20)
			if err != nil {
				return nil, err
			}
			for k, v := range obj(body) {
				m[k] = v
			}
		case strings.Contains(ct, "form"):
			if err := r.ParseForm(); err == nil {
				add(r.PostForm)
			}
		default:
			b, _ := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
			if t := strings.TrimSpace(string(b)); strings.HasPrefix(t, "{") {
				var body map[string]any
				dec := json.NewDecoder(strings.NewReader(t))
				dec.UseNumber()
				if dec.Decode(&body) == nil {
					for k, v := range body {
						m[k] = v
					}
				}
			} else if v, err := url.ParseQuery(t); err == nil {
				add(v)
			}
		}
	}
	return m, nil
}

// handleOsmAnd speaks the OsmAnd protocol Traccar Client and many GPS
// trackers use (GET/POST ?id=&lat=&lon=&timestamp=&speed=<knots>…) and
// Traccar Client's newer JSON. The device identifier may be the key itself.
func (s *Server) handleOsmAnd(w http.ResponseWriter, r *http.Request) {
	m, err := paramsMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, ok := flatPoint(m)
	if !ok {
		writeErr(w, http.StatusBadRequest, "need lat, lon and timestamp")
		return
	}
	if obj(m["location"]) == nil {
		if kn := numPtr(m["speed"]); kn != nil { // OsmAnd speed is in knots
			v := *kn / 1.943844
			p.Speed = &v
		}
		if id := str(m["id"]); id != "" && !strings.HasPrefix(id, "pm_") {
			p.TrackerID = id
		} else {
			p.TrackerID = ""
		}
	}
	if _, err := s.ingest(r.Context(), userFrom(r.Context()), deviceFrom(r.Context()), []store.Point{p}, "osmand"); err != nil {
		s.fail(w, err, "store point")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleGPSLogger takes GPSLogger's custom URL (%LAT, %LON, %TIME, %ALT,
// %ACC, %SPD, %DIR, %BATT, %ISCHARGING, %SER) as query, form or JSON.
func (s *Server) handleGPSLogger(w http.ResponseWriter, r *http.Request) {
	m, err := paramsMap(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, ok := flatPoint(m)
	if !ok {
		writeErr(w, http.StatusBadRequest, "need lat and lon")
		return
	}
	if _, err := s.ingest(r.Context(), userFrom(r.Context()), deviceFrom(r.Context()), []store.Point{p}, "gpslogger"); err != nil {
		s.fail(w, err, "store point")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

// ---- OwnTracks ----

// handleOwnTracks takes OwnTracks HTTP mode messages. Locations are stored;
// the answer lists the people sharing their position with you as OwnTracks
// friends, so they show up on the OwnTracks map.
func (s *Server) handleOwnTracks(w http.ResponseWriter, r *http.Request) {
	body, err := readJSON(r, 8<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	msgs := []map[string]any{}
	switch b := body.(type) {
	case map[string]any:
		msgs = append(msgs, b)
	case []any:
		for _, x := range b {
			if m := obj(x); m != nil {
				msgs = append(msgs, m)
			}
		}
	}
	var pts []store.Point
	for _, m := range msgs {
		if t := str(m["_type"]); t != "location" && t != "" {
			continue
		}
		p, ok := ownTracksPoint(m)
		if ok {
			if p.TrackerID == "" {
				p.TrackerID = ownTracksTID(r)
			}
			pts = append(pts, p)
		}
	}
	u := userFrom(r.Context())
	if _, err := s.ingest(r.Context(), u, deviceFrom(r.Context()), pts, "owntracks"); err != nil {
		s.fail(w, err, "store points")
		return
	}
	writeJSON(w, http.StatusOK, s.ownTracksFriends(r.Context(), u))
}

func ownTracksTID(r *http.Request) string {
	if d := r.Header.Get("X-Limit-D"); d != "" {
		return d
	}
	return r.Header.Get("X-Limit-U")
}

func ownTracksPoint(m map[string]any) (store.Point, bool) {
	lat, ok1 := num(m["lat"])
	lng, ok2 := num(m["lon"])
	ts, ok3 := parseTS(m["tst"])
	if !ok1 || !ok2 || !ok3 {
		return store.Point{}, false
	}
	p := store.Point{TS: ts, Lat: lat, Lng: lng, Altitude: numPtr(m["alt"]), Accuracy: numPtr(m["acc"]), VerticalAccuracy: numPtr(m["vac"]),
		Course: numPtr(m["cog"]), TrackerID: str(m["tid"]), SSID: str(m["SSID"])}
	if v := numPtr(m["vel"]); v != nil && *v >= 0 { // km/h
		sp := *v / 3.6
		p.Speed = &sp
	}
	if b, ok := num(m["batt"]); ok && b >= 0 {
		n := int(b)
		p.Battery = &n
	}
	switch str(m["bs"]) {
	case "1":
		p.BatteryState = "unplugged"
	case "2":
		p.BatteryState = "charging"
	case "3":
		p.BatteryState = "full"
	}
	p.Connection = str(m["conn"])
	if mo := str(m["m"]); mo != "" {
		p.Motion = map[string]string{"1": "significant", "2": "move"}[mo]
	}
	extra := map[string]any{}
	for _, k := range []string{"t", "topic", "BSSID", "inregions", "p", "rad"} {
		if v, ok := m[k]; ok {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		b, _ := json.Marshal(extra)
		p.Extra = string(b)
	}
	return p, true
}

// ownTracksFriends formats the people who share their live position with u.
func (s *Server) ownTracksFriends(ctx context.Context, u *store.User) []map[string]any {
	out := []map[string]any{}
	shares, err := s.store.SharesWithViewer(ctx, u.ID)
	if err != nil {
		return out
	}
	for _, sh := range shares {
		p, err := s.store.LatestPoint(ctx, sh.OwnerID)
		if err != nil {
			continue
		}
		lat, lng := s.sharedPosition(ctx, sh, p.Lat, p.Lng)
		tid := initials(firstNonEmpty(sh.OwnerName, sh.OwnerEmail))
		topic := "owntracks/" + strings.SplitN(sh.OwnerEmail, "@", 2)[0] + "/movement"
		out = append(out,
			map[string]any{"_type": "card", "name": firstNonEmpty(sh.OwnerName, sh.OwnerEmail), "tid": tid, "topic": topic},
			map[string]any{"_type": "location", "lat": lat, "lon": lng, "tst": p.TS, "tid": tid, "topic": topic, "batt": p.Battery, "acc": p.Accuracy})
	}
	return out
}

func initials(name string) string {
	parts := strings.Fields(strings.NewReplacer("@", " ", ".", " ", "_", " ").Replace(name))
	out := ""
	for _, p := range parts {
		out += strings.ToUpper(string([]rune(p)[0]))
		if len(out) == 2 {
			break
		}
	}
	if out == "" {
		out = "?"
	}
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
