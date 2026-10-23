package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

type memSink struct {
	pts    []Point
	visits []Visit
}

func (m *memSink) Points(_ context.Context, p []Point) error {
	m.pts = append(m.pts, p...)
	return nil
}

func (m *memSink) Visit(_ context.Context, v Visit) error {
	m.visits = append(m.visits, v)
	return nil
}

func run(t *testing.T, name, body string) *memSink {
	t.Helper()
	format := Detect(name, []byte(body))
	if format == "" {
		t.Fatalf("%s: format not detected", name)
	}
	s := &memSink{}
	if err := Run(context.Background(), format, strings.NewReader(body), s); err != nil {
		t.Fatalf("%s (%s): %v", name, format, err)
	}
	return s
}

func TestGoogleRecords(t *testing.T) {
	s := run(t, "Records.json", `{"locations": [
	  {"latitudeE7": 438563000, "longitudeE7": 184131000, "accuracy": 12, "timestamp": "2024-03-01T08:00:00.123Z", "velocity": 3, "activity": [{"activity": [{"type": "WALKING", "confidence": 80}]}]},
	  {"latitudeE7": 438564000, "longitudeE7": 184132000, "timestampMs": "1709280060000", "deviceTag": 12345},
	  {"latitudeE7": 4294967295, "longitudeE7": 184132000, "timestampMs": "1709280120000"}
	]}`)
	if len(s.pts) != 3 || s.pts[0].Lat != 43.8563 || s.pts[0].Motion != "walking" || s.pts[1].TS != 1709280060 || s.pts[1].Tracker != "google:12345" {
		t.Fatalf("records = %+v", s.pts)
	}
	if s.pts[2].Lat > 0 {
		t.Errorf("E7 overflow not handled: %v", s.pts[2].Lat)
	}
}

func TestGoogleSemantic(t *testing.T) {
	body := `{"timelineObjects": [
	  {"placeVisit": {"location": {"latitudeE7": 438563000, "longitudeE7": 184131000, "name": "Baščaršija", "address": "Sarajevo", "semanticType": "TYPE_HOME"},
	    "duration": {"startTimestamp": "2024-03-01T08:00:00Z", "endTimestamp": "2024-03-01T09:00:00Z"}}},
	  {"activitySegment": {"startLocation": {"latitudeE7": 438563000, "longitudeE7": 184131000}, "endLocation": {"latitudeE7": 438663000, "longitudeE7": 184331000},
	    "duration": {"startTimestamp": "2024-03-01T09:00:00Z", "endTimestamp": "2024-03-01T09:20:00Z"}, "activityType": "IN_PASSENGER_VEHICLE",
	    "waypointPath": {"waypoints": [{"latE7": 438600000, "lngE7": 184200000}, {"latE7": 438630000, "lngE7": 184280000}]}}}
	]}`
	s := run(t, "2024_MARCH.json", body)
	if len(s.visits) != 1 || s.visits[0].Name != "Baščaršija" || s.visits[0].End-s.visits[0].Start != 3600 {
		t.Fatalf("visits = %+v", s.visits)
	}
	// 2 visit edge points + start + 2 waypoints + end
	if len(s.pts) != 6 || s.pts[3].Motion != "driving" {
		t.Fatalf("points = %+v", s.pts)
	}
	if !(s.pts[2].TS < s.pts[3].TS && s.pts[3].TS < s.pts[4].TS && s.pts[4].TS < s.pts[5].TS) {
		t.Errorf("waypoint times not spread: %+v", s.pts)
	}
}

func TestGoogleTimelineAndroid(t *testing.T) {
	s := run(t, "Timeline.json", `{"semanticSegments": [
	  {"startTime": "2024-03-01T10:00:00.000+01:00", "endTime": "2024-03-01T11:00:00.000+01:00",
	   "visit": {"topCandidate": {"semanticType": "HOME", "placeLocation": {"latLng": "43.8563°, 18.4131°"}}}},
	  {"startTime": "2024-03-01T11:00:00.000+01:00", "endTime": "2024-03-01T12:00:00.000+01:00",
	   "timelinePath": [{"point": "43.8600°, 18.4200°", "time": "2024-03-01T11:10:00.000+01:00"}]}
	 ],
	 "rawSignals": [{"position": {"LatLng": "43.8610°, 18.4210°", "accuracyMeters": 9, "altitudeMeters": 530, "timestamp": "2024-03-01T11:15:00.000+01:00"}}],
	 "userLocationProfile": {"frequentPlaces": []}}`)
	if len(s.visits) != 1 || s.visits[0].Semantic != "HOME" || s.visits[0].Lat != 43.8563 {
		t.Fatalf("visits = %+v", s.visits)
	}
	if len(s.pts) != 4 {
		t.Fatalf("points = %+v", s.pts)
	}
}

func TestGoogleTimelineIOS(t *testing.T) {
	s := run(t, "location-history.json", `[
	  {"startTime": "2024-03-01T10:00:00.000+01:00", "endTime": "2024-03-01T11:00:00.000+01:00", "visit": {"topCandidate": {"semanticType": "Work", "placeLocation": "geo:43.856300,18.413100"}}},
	  {"startTime": "2024-03-01T11:00:00.000+01:00", "endTime": "2024-03-01T11:30:00.000+01:00", "activity": {"start": "geo:43.8563,18.4131", "end": "geo:43.87,18.43", "topCandidate": {"type": "walking"}}},
	  {"startTime": "2024-03-01T11:00:00.000+01:00", "endTime": "2024-03-01T13:00:00.000+01:00", "timelinePath": [{"point": "geo:43.86,18.42", "durationMinutesOffsetFromStartTime": "12"}]}
	]`)
	if len(s.visits) != 1 || len(s.pts) != 5 {
		t.Fatalf("visits %+v points %+v", s.visits, s.pts)
	}
	if s.pts[4].TS-s.pts[2].TS != 12*60 {
		t.Errorf("offset point at %d", s.pts[4].TS)
	}
}

func TestGPX(t *testing.T) {
	s := run(t, "ride.gpx", `<?xml version="1.0"?><gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>
	  <trkpt lat="43.85" lon="18.41"><ele>520</ele><time>2024-03-01T08:00:00Z</time><extensions><speed>4.2</speed></extensions></trkpt>
	  <trkpt lat="43.86" lon="18.42"><time>2024-03-01T08:01:00Z</time><hdop>1.2</hdop></trkpt>
	  <trkpt lat="43.87" lon="18.43"></trkpt>
	</trkseg></trk></gpx>`)
	if len(s.pts) != 2 || *s.pts[0].Alt != 520 || *s.pts[0].Speed != 4.2 || s.pts[1].Acc == nil {
		t.Fatalf("gpx = %+v", s.pts)
	}
}

func TestKML(t *testing.T) {
	s := run(t, "history.kml", `<?xml version="1.0"?><kml xmlns="http://www.opengis.net/kml/2.2" xmlns:gx="http://www.google.com/kml/ext/2.2"><Document><Placemark><gx:Track>
	  <when>2024-03-01T08:00:00Z</when><when>2024-03-01T08:01:00Z</when>
	  <gx:coord>18.41 43.85 520</gx:coord><gx:coord>18.42 43.86 0</gx:coord>
	</gx:Track></Placemark></Document></kml>`)
	if len(s.pts) != 2 || s.pts[0].Lng != 18.41 || s.pts[1].Lat != 43.86 {
		t.Fatalf("kml = %+v", s.pts)
	}
}

func TestGeoJSON(t *testing.T) {
	s := run(t, "export.geojson", `{"type": "FeatureCollection", "features": [
	  {"type": "Feature", "geometry": {"type": "Point", "coordinates": [18.41, 43.85]}, "properties": {"timestamp": 1709280000, "velocity": "2.5", "battery": 77, "tracker_id": "p1"}},
	  {"type": "Feature", "geometry": {"type": "LineString", "coordinates": [[18.41, 43.85], [18.42, 43.86]]}, "properties": {"coordTimes": ["2024-03-01T08:00:00Z", "2024-03-01T08:01:00Z"]}}
	]}`)
	if len(s.pts) != 3 || *s.pts[0].Speed != 2.5 || *s.pts[0].Battery != 77 || s.pts[0].Tracker != "p1" {
		t.Fatalf("geojson = %+v", s.pts)
	}
}

func TestOwnTracksRec(t *testing.T) {
	s := run(t, "2024-03.rec", "2024-03-01T08:00:00Z\t*                 \t{\"_type\":\"location\",\"lat\":43.85,\"lon\":18.41,\"tst\":1709280000,\"vel\":36,\"batt\":50}\n"+
		"2024-03-01T08:00:05Z\t*                 \t{\"_type\":\"lwt\"}\n")
	if len(s.pts) != 1 || *s.pts[0].Speed != 10 {
		t.Fatalf("rec = %+v", s.pts)
	}
}

func TestCSV(t *testing.T) {
	s := run(t, "points.csv", "time,latitude,longitude,altitude\n2024-03-01T08:00:00Z,43.85,18.41,520\n1709280060,43.86,18.42,\nbad,row,here,\n")
	if len(s.pts) != 2 || s.pts[1].TS != 1709280060 {
		t.Fatalf("csv = %+v", s.pts)
	}
}

func TestZipPrefersRecords(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Takeout/Location History/Records.json")
	w.Write([]byte(`{"locations": [{"latitudeE7": 438563000, "longitudeE7": 184131000, "timestamp": "2024-03-01T08:00:00Z"}]}`))
	w, _ = zw.Create("Takeout/Location History/Semantic Location History/2024/2024_MARCH.json")
	w.Write([]byte(`{"timelineObjects": [{"placeVisit": {"location": {"latitudeE7": 438563000, "longitudeE7": 184131000, "name": "Home"},
	  "duration": {"startTimestamp": "2024-03-01T08:00:00Z", "endTimestamp": "2024-03-01T09:00:00Z"}}}]}`))
	zw.Close()
	s := &memSink{}
	if err := RunZip(context.Background(), bytes.NewReader(buf.Bytes()), int64(buf.Len()), s, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.pts) != 1 || len(s.visits) != 1 {
		t.Fatalf("zip: %d points %d visits", len(s.pts), len(s.visits))
	}
}
