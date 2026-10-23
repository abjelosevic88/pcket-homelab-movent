package importer

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// ---- GPX: track, route and way points with a time ----

func gpx(ctx context.Context, r io.Reader, b *batcher) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var cur *Point
	var field string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "trkpt", "rtept", "wpt":
				if ctx.Err() != nil {
					return ctx.Err()
				}
				cur = &Point{}
				for _, a := range t.Attr {
					v, _ := strconv.ParseFloat(a.Value, 64)
					switch a.Name.Local {
					case "lat":
						cur.Lat = v
					case "lon":
						cur.Lng = v
					}
				}
			default:
				field = t.Name.Local
			}
		case xml.CharData:
			if cur == nil || field == "" {
				continue
			}
			s := strings.TrimSpace(string(t))
			if s == "" {
				continue
			}
			v, numErr := strconv.ParseFloat(s, 64)
			switch field {
			case "time":
				cur.TS, _ = parseTime(s)
			case "ele":
				if numErr == nil {
					cur.Alt = fptr(v)
				}
			case "speed":
				if numErr == nil {
					cur.Speed = fptr(v)
				}
			case "course":
				if numErr == nil {
					cur.Course = fptr(v)
				}
			case "hdop":
				if numErr == nil && cur.Acc == nil {
					cur.Acc = fptr(v * 5)
				}
			case "accuracy", "hacc":
				if numErr == nil {
					cur.Acc = fptr(v)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "trkpt", "rtept", "wpt":
				if cur != nil && cur.TS > 0 {
					if err := b.add(*cur); err != nil {
						return err
					}
				}
				cur = nil
			}
			field = ""
		}
	}
}

// ---- KML: gx:Track (when + gx:coord) and timestamped placemarks ----

func kml(ctx context.Context, r io.Reader, b *batcher) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var whens []int64
	var coords [][3]float64
	var field string
	var pmWhen int64
	var pmCoord *[3]float64
	inPlacemark := false
	flushTrack := func() error {
		for i := 0; i < len(whens) && i < len(coords); i++ {
			c := coords[i]
			p := Point{TS: whens[i], Lng: c[0], Lat: c[1]}
			if c[2] != 0 {
				p.Alt = fptr(c[2])
			}
			if err := b.add(p); err != nil {
				return err
			}
		}
		whens, coords = whens[:0], coords[:0]
		return nil
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return flushTrack()
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			field = t.Name.Local
			if field == "Placemark" {
				inPlacemark, pmWhen, pmCoord = true, 0, nil
			}
		case xml.CharData:
			s := strings.TrimSpace(string(t))
			if s == "" {
				continue
			}
			switch field {
			case "when":
				if ts, ok := parseTime(s); ok {
					whens = append(whens, ts)
					if inPlacemark {
						pmWhen = ts
					}
				}
			case "coord":
				f := strings.Fields(s)
				if len(f) >= 2 {
					var c [3]float64
					c[0], _ = strconv.ParseFloat(f[0], 64)
					c[1], _ = strconv.ParseFloat(f[1], 64)
					if len(f) > 2 {
						c[2], _ = strconv.ParseFloat(f[2], 64)
					}
					coords = append(coords, c)
				}
			case "coordinates":
				f := strings.Split(strings.Fields(s)[0], ",")
				if len(f) >= 2 && inPlacemark {
					var c [3]float64
					c[0], _ = strconv.ParseFloat(f[0], 64)
					c[1], _ = strconv.ParseFloat(f[1], 64)
					if len(f) > 2 {
						c[2], _ = strconv.ParseFloat(f[2], 64)
					}
					pmCoord = &c
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "Track":
				if err := flushTrack(); err != nil {
					return err
				}
			case "Placemark":
				if pmCoord != nil && pmWhen > 0 && len(coords) == 0 {
					if err := b.add(Point{TS: pmWhen, Lng: pmCoord[0], Lat: pmCoord[1]}); err != nil {
						return err
					}
				}
				whens = whens[:0]
				inPlacemark = false
			}
			field = ""
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// ---- GeoJSON: Points with a time property; LineStrings with coordTimes ----

type gjFeature struct {
	Geometry *struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func nptr(v any) *float64 {
	if f, ok := num(v); ok {
		return &f
	}
	return nil
}

func propTime(p map[string]any) (int64, bool) {
	for _, k := range []string{"timestamp", "time", "tst", "date", "datetime", "recorded_at", "created_at"} {
		switch v := p[k].(type) {
		case string:
			if ts, ok := parseTime(v); ok {
				return ts, true
			}
		case json.Number:
			if f, err := v.Float64(); err == nil {
				if f > 1e11 {
					f /= 1000
				}
				return int64(f), f > 0
			}
		case float64:
			if v > 1e11 {
				v /= 1000
			}
			return int64(v), v > 0
		}
	}
	return 0, false
}

func geojson(ctx context.Context, r io.Reader, b *batcher) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	ok, err := seekArray(dec, "features")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no features in this GeoJSON")
	}
	for dec.More() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var f gjFeature
		if err := dec.Decode(&f); err != nil {
			return err
		}
		if f.Geometry == nil {
			continue
		}
		p := f.Properties
		switch f.Geometry.Type {
		case "Point":
			var c []float64
			if json.Unmarshal(f.Geometry.Coordinates, &c) != nil || len(c) < 2 {
				continue
			}
			ts, ok := propTime(p)
			if !ok {
				continue
			}
			pt := Point{TS: ts, Lng: c[0], Lat: c[1], Alt: nptr(p["altitude"]), Acc: nptr(firstOf(p, "accuracy", "horizontal_accuracy")),
				Course: nptr(firstOf(p, "course", "bearing", "heading"))}
			if len(c) > 2 && pt.Alt == nil {
				pt.Alt = fptr(c[2])
			}
			if sp := nptr(firstOf(p, "speed", "velocity")); sp != nil && *sp >= 0 {
				pt.Speed = sp
			}
			if bat, ok := num(firstOf(p, "battery", "battery_level")); ok && bat >= 0 {
				if bat <= 1 {
					bat *= 100
				}
				n := int(bat)
				pt.Battery = &n
			}
			if t, _ := firstOf(p, "tracker_id", "device_id").(string); t != "" {
				pt.Tracker = t
			}
			if err := b.add(pt); err != nil {
				return err
			}
		case "LineString", "MultiLineString":
			var lines [][][]float64
			if f.Geometry.Type == "LineString" {
				var l [][]float64
				if json.Unmarshal(f.Geometry.Coordinates, &l) != nil {
					continue
				}
				lines = [][][]float64{l}
			} else if json.Unmarshal(f.Geometry.Coordinates, &lines) != nil {
				continue
			}
			times := flatTimes(firstOf(p, "coordTimes", "times", "timestamps"))
			k := 0
			for _, l := range lines {
				for _, c := range l {
					if k < len(times) && len(c) >= 2 {
						pt := Point{TS: times[k], Lng: c[0], Lat: c[1]}
						if len(c) > 2 {
							pt.Alt = fptr(c[2])
						}
						if err := b.add(pt); err != nil {
							return err
						}
					}
					k++
				}
			}
		}
	}
	return nil
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// flatTimes reads ["t", …] or [["t", …], …] as unix seconds.
func flatTimes(v any) []int64 {
	var out []int64
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case string:
			ts, _ := parseTime(t)
			out = append(out, ts)
		case json.Number:
			f, _ := t.Float64()
			if f > 1e11 {
				f /= 1000
			}
			out = append(out, int64(f))
		}
	}
	walk(v)
	return out
}

// ---- OwnTracks recorder files: "<iso>\t<topic>\t<json>" per line ----

func ownTracksRec(ctx context.Context, r io.Reader, b *batcher) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	trimmed := false
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Text()
		if !trimmed {
			line = strings.TrimLeft(line, "\ufeff")
			trimmed = true
		}
		js := line
		if i := strings.Index(line, "{"); i >= 0 {
			js = line[i:]
		} else {
			continue
		}
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(js))
		dec.UseNumber()
		if dec.Decode(&m) != nil {
			continue
		}
		if t, _ := m["_type"].(string); t != "" && t != "location" {
			continue
		}
		lat, ok1 := num(m["lat"])
		lng, ok2 := num(m["lon"])
		ts, ok3 := num(m["tst"])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		p := Point{TS: int64(ts), Lat: lat, Lng: lng, Alt: nptr(m["alt"]), Acc: nptr(m["acc"]), Course: nptr(m["cog"])}
		if v, ok := num(m["vel"]); ok && v >= 0 {
			p.Speed = fptr(v / 3.6)
		}
		if bt, ok := num(m["batt"]); ok {
			n := int(bt)
			p.Battery = &n
		}
		if t, _ := m["tid"].(string); t != "" {
			p.Tracker = t
		}
		if err := b.add(p); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ---- CSV with a header naming lat/lon/time columns ----

func csvFile(ctx context.Context, r io.Reader, b *batcher) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		return err
	}
	if len(head) == 1 && strings.Contains(head[0], ";") {
		return errors.New("use commas as the CSV separator")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	find := func(names ...string) int {
		for _, n := range names {
			if i, ok := col[n]; ok {
				return i
			}
		}
		return -1
	}
	iLat := find("lat", "latitude")
	iLng := find("lon", "lng", "long", "longitude")
	iTS := find("timestamp", "time", "datetime", "date_time", "date", "tst", "recorded_at", "utc_time")
	iAlt := find("altitude", "alt", "elevation", "ele")
	iAcc := find("accuracy", "acc", "horizontal_accuracy")
	iSpd := find("speed", "velocity")
	iBat := find("battery", "batt")
	if iLat < 0 || iLng < 0 || iTS < 0 {
		return errors.New("the CSV needs latitude, longitude and time columns")
	}
	get := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec, err := cr.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			continue
		}
		lat, e1 := strconv.ParseFloat(get(rec, iLat), 64)
		lng, e2 := strconv.ParseFloat(get(rec, iLng), 64)
		ts, ok := parseTime(get(rec, iTS))
		if e1 != nil || e2 != nil || !ok {
			continue
		}
		p := Point{TS: ts, Lat: lat, Lng: lng, Alt: nptr(get(rec, iAlt)), Acc: nptr(get(rec, iAcc)), Speed: nptr(get(rec, iSpd))}
		if bt, ok := num(get(rec, iBat)); ok {
			n := int(bt)
			p.Battery = &n
		}
		if err := b.add(p); err != nil {
			return err
		}
	}
}
