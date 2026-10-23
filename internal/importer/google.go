package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// seekArray positions a decoder inside the array under a top-level key of
// an object ({"locations": [ ← here), skipping every other value whole.
func seekArray(dec *json.Decoder, key string) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return false, nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return false, err
		}
		if k, _ := tok.(string); k == key {
			next, err := dec.Token()
			if err != nil {
				return false, err
			}
			if d, ok := next.(json.Delim); ok && d == '[' {
				return true, nil
			}
			return false, nil
		}
		if err := skipNextValue(dec); err != nil {
			return false, err
		}
	}
	return false, nil
}

// skipNextValue skips one value (scalar, object or array).
func skipNextValue(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}

// e7 reads latitudeE7-style integers (sometimes strings, sometimes over 2^31 as negatives).
func e7(v json.Number) (float64, bool) {
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil {
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		n = int64(f)
	}
	if n > 1800000000 {
		n -= 4294967296
	}
	return float64(n) / 1e7, true
}

// ---- Records.json ----

type googleRecord struct {
	LatE7     json.Number `json:"latitudeE7"`
	LngE7     json.Number `json:"longitudeE7"`
	Accuracy  *float64    `json:"accuracy"`
	Altitude  *float64    `json:"altitude"`
	Velocity  *float64    `json:"velocity"`
	Heading   *float64    `json:"heading"`
	Timestamp string      `json:"timestamp"`
	TSms      string      `json:"timestampMs"`
	DeviceTag json.Number `json:"deviceTag"`
	Activity  []struct {
		Activity []struct {
			Type       string `json:"type"`
			Confidence int    `json:"confidence"`
		} `json:"activity"`
	} `json:"activity"`
}

func googleRecords(ctx context.Context, r io.Reader, b *batcher) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	ok, err := seekArray(dec, "locations")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no locations in this file")
	}
	for dec.More() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var g googleRecord
		if err := dec.Decode(&g); err != nil {
			return err
		}
		lat, ok1 := e7(g.LatE7)
		lng, ok2 := e7(g.LngE7)
		ts, ok3 := parseTime(g.Timestamp)
		if !ok3 {
			ts, ok3 = parseTime(g.TSms)
		}
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		p := Point{TS: ts, Lat: lat, Lng: lng, Alt: g.Altitude, Acc: g.Accuracy, Speed: g.Velocity, Course: g.Heading}
		if g.DeviceTag != "" {
			p.Tracker = "google:" + string(g.DeviceTag)
		}
		if len(g.Activity) > 0 && len(g.Activity[0].Activity) > 0 {
			p.Motion = googleMotion(g.Activity[0].Activity[0].Type)
		}
		if err := b.add(p); err != nil {
			return err
		}
	}
	return nil
}

func googleMotion(t string) string {
	switch strings.ToUpper(t) {
	case "STILL":
		return "stationary"
	case "WALKING", "ON_FOOT":
		return "walking"
	case "RUNNING":
		return "running"
	case "ON_BICYCLE", "CYCLING":
		return "cycling"
	case "IN_VEHICLE", "IN_PASSENGER_VEHICLE", "IN_CAR", "DRIVING", "IN_BUS", "IN_TRAIN", "IN_SUBWAY", "IN_TRAM", "MOTORCYCLING":
		return "driving"
	}
	return ""
}

// ---- Semantic Location History (monthly files) ----

type e7loc struct {
	LatE7   json.Number `json:"latitudeE7"`
	LngE7   json.Number `json:"longitudeE7"`
	LatE7b  json.Number `json:"latE7"`
	LngE7b  json.Number `json:"lngE7"`
	Name    string      `json:"name"`
	Address string      `json:"address"`
	Sem     string      `json:"semanticType"`
}

func (l e7loc) latLng() (float64, float64, bool) {
	a, ok1 := e7(l.LatE7)
	o, ok2 := e7(l.LngE7)
	if !ok1 || !ok2 {
		a, ok1 = e7(l.LatE7b)
		o, ok2 = e7(l.LngE7b)
	}
	return a, o, ok1 && ok2
}

type gDuration struct {
	Start   string `json:"startTimestamp"`
	End     string `json:"endTimestamp"`
	StartMs string `json:"startTimestampMs"`
	EndMs   string `json:"endTimestampMs"`
}

func (d gDuration) times() (int64, int64, bool) {
	s, ok1 := parseTime(d.Start)
	if !ok1 {
		s, ok1 = parseTime(d.StartMs)
	}
	e, ok2 := parseTime(d.End)
	if !ok2 {
		e, ok2 = parseTime(d.EndMs)
	}
	return s, e, ok1 && ok2 && e >= s
}

type semanticObject struct {
	Activity *struct {
		Start        e7loc     `json:"startLocation"`
		End          e7loc     `json:"endLocation"`
		Duration     gDuration `json:"duration"`
		ActivityType string    `json:"activityType"`
		Waypoints    *struct {
			Waypoints []e7loc `json:"waypoints"`
		} `json:"waypointPath"`
		Raw *struct {
			Points []struct {
				LatE7    json.Number `json:"latE7"`
				LngE7    json.Number `json:"lngE7"`
				TS       string      `json:"timestamp"`
				TSms     string      `json:"timestampMs"`
				Accuracy *float64    `json:"accuracyMeters"`
			} `json:"points"`
		} `json:"simplifiedRawPath"`
	} `json:"activitySegment"`
	Visit *struct {
		Location  e7loc       `json:"location"`
		Duration  gDuration   `json:"duration"`
		CenterLat json.Number `json:"centerLatE7"`
		CenterLng json.Number `json:"centerLngE7"`
	} `json:"placeVisit"`
}

func googleSemantic(ctx context.Context, r io.Reader, b *batcher, visitsOnly bool) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	ok, err := seekArray(dec, "timelineObjects")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no timelineObjects in this file")
	}
	for dec.More() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var o semanticObject
		if err := dec.Decode(&o); err != nil {
			return err
		}
		if v := o.Visit; v != nil {
			lat, lng, ok := v.Location.latLng()
			if !ok {
				lat, ok = e7(v.CenterLat)
				lng, _ = e7(v.CenterLng)
			}
			s, e, okT := v.Duration.times()
			if ok && okT {
				if err := b.visit(Visit{Start: s, End: e, Lat: lat, Lng: lng, Name: v.Location.Name, Address: v.Location.Address, Semantic: v.Location.Sem}); err != nil {
					return err
				}
				if !visitsOnly {
					_ = b.add(Point{TS: s, Lat: lat, Lng: lng})
					_ = b.add(Point{TS: e, Lat: lat, Lng: lng})
				}
			}
		}
		if a := o.Activity; a != nil && !visitsOnly {
			s, e, okT := a.Duration.times()
			if !okT {
				continue
			}
			motion := googleMotion(a.ActivityType)
			if lat, lng, ok := a.Start.latLng(); ok {
				_ = b.add(Point{TS: s, Lat: lat, Lng: lng, Motion: motion})
			}
			raw := 0
			if a.Raw != nil {
				for _, p := range a.Raw.Points {
					lat, ok1 := e7(p.LatE7)
					lng, ok2 := e7(p.LngE7)
					ts, ok3 := parseTime(p.TS)
					if !ok3 {
						ts, ok3 = parseTime(p.TSms)
					}
					if ok1 && ok2 && ok3 {
						raw++
						_ = b.add(Point{TS: ts, Lat: lat, Lng: lng, Acc: p.Accuracy, Motion: motion})
					}
				}
			}
			// Waypoints carry no times: spread them evenly over the activity.
			if raw == 0 && a.Waypoints != nil && len(a.Waypoints.Waypoints) > 0 {
				n := len(a.Waypoints.Waypoints)
				for i, w := range a.Waypoints.Waypoints {
					lat, lng, ok := w.latLng()
					if ok {
						ts := s + (e-s)*int64(i+1)/int64(n+1)
						_ = b.add(Point{TS: ts, Lat: lat, Lng: lng, Motion: motion})
					}
				}
			}
			if lat, lng, ok := a.End.latLng(); ok {
				if err := b.add(Point{TS: e, Lat: lat, Lng: lng, Motion: motion}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ---- the phone's Timeline export (Android "Timeline.json", iOS array) ----

type tlLatLng struct {
	LatLng string `json:"latLng"`
}

type tlSegment struct {
	Start        string `json:"startTime"`
	End          string `json:"endTime"`
	TimelinePath []struct {
		Point  string `json:"point"`
		Time   string `json:"time"`
		Offset string `json:"durationMinutesOffsetFromStartTime"`
	} `json:"timelinePath"`
	Visit *struct {
		Top struct {
			Semantic string          `json:"semanticType"`
			Location json.RawMessage `json:"placeLocation"`
		} `json:"topCandidate"`
	} `json:"visit"`
	Activity *struct {
		Start json.RawMessage `json:"start"`
		End   json.RawMessage `json:"end"`
		Top   struct {
			Type string `json:"type"`
		} `json:"topCandidate"`
	} `json:"activity"`
}

// rawLatLng reads {"latLng": "…"} or a bare "geo:…" string.
func rawLatLng(m json.RawMessage) (float64, float64, bool) {
	if len(m) == 0 {
		return 0, 0, false
	}
	var s string
	if json.Unmarshal(m, &s) == nil {
		return latLngString(s)
	}
	var o tlLatLng
	if json.Unmarshal(m, &o) == nil {
		return latLngString(o.LatLng)
	}
	return 0, 0, false
}

type tlRawSignal struct {
	Position *struct {
		LatLng   string   `json:"LatLng"`
		Accuracy *float64 `json:"accuracyMeters"`
		Altitude *float64 `json:"altitudeMeters"`
		Speed    *float64 `json:"speedMetersPerSecond"`
		TS       string   `json:"timestamp"`
	} `json:"position"`
}

func googleTimeline(ctx context.Context, r io.Reader, b *batcher) error {
	br, head := peekReader(r)
	dec := json.NewDecoder(br)
	dec.UseNumber()
	segment := func(sg tlSegment) error {
		s, ok1 := parseTime(sg.Start)
		e, ok2 := parseTime(sg.End)
		if v := sg.Visit; v != nil && ok1 && ok2 {
			if lat, lng, ok := rawLatLng(v.Top.Location); ok {
				if err := b.visit(Visit{Start: s, End: e, Lat: lat, Lng: lng, Semantic: v.Top.Semantic}); err != nil {
					return err
				}
				_ = b.add(Point{TS: s, Lat: lat, Lng: lng})
				_ = b.add(Point{TS: e, Lat: lat, Lng: lng})
			}
		}
		if a := sg.Activity; a != nil && ok1 && ok2 {
			motion := googleMotion(a.Top.Type)
			if motion == "" {
				motion = strings.ToLower(a.Top.Type)
			}
			if lat, lng, ok := rawLatLng(a.Start); ok {
				_ = b.add(Point{TS: s, Lat: lat, Lng: lng, Motion: motion})
			}
			if lat, lng, ok := rawLatLng(a.End); ok {
				_ = b.add(Point{TS: e, Lat: lat, Lng: lng, Motion: motion})
			}
		}
		for _, p := range sg.TimelinePath {
			lat, lng, ok := latLngString(p.Point)
			if !ok {
				continue
			}
			ts, okT := parseTime(p.Time)
			if !okT && ok1 && p.Offset != "" {
				if m, err := strconv.ParseFloat(p.Offset, 64); err == nil {
					ts, okT = s+int64(m*60), true
				}
			}
			if okT {
				if err := b.add(Point{TS: ts, Lat: lat, Lng: lng}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if strings.HasPrefix(strings.TrimLeft(string(head), "\ufeff \t\r\n"), "[") {
		if _, err := dec.Token(); err != nil {
			return err
		}
		for dec.More() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var sg tlSegment
			if err := dec.Decode(&sg); err != nil {
				return err
			}
			if err := segment(sg); err != nil {
				return err
			}
		}
		return nil
	}
	// {"semanticSegments": [...], "rawSignals": [...], ...} in either order.
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string)
		switch key {
		case "semanticSegments", "rawSignals":
			if _, err := dec.Token(); err != nil {
				return err
			}
			for dec.More() {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if key == "semanticSegments" {
					var sg tlSegment
					if err := dec.Decode(&sg); err != nil {
						return err
					}
					if err := segment(sg); err != nil {
						return err
					}
					continue
				}
				var rs tlRawSignal
				if err := dec.Decode(&rs); err != nil {
					return err
				}
				if p := rs.Position; p != nil {
					lat, lng, ok := latLngString(p.LatLng)
					ts, okT := parseTime(p.TS)
					if ok && okT {
						if err := b.add(Point{TS: ts, Lat: lat, Lng: lng, Acc: p.Accuracy, Alt: p.Altitude, Speed: p.Speed}); err != nil {
							return err
						}
					}
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
		default:
			if err := skipNextValue(dec); err != nil {
				return err
			}
		}
	}
	return nil
}
