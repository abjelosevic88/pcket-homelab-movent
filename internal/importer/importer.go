// Package importer reads location history files: Google Takeout (Records.json,
// Semantic Location History, the phone's Timeline export on Android and
// iOS), GPX, KML, GeoJSON (Dawarich exports too), OwnTracks .rec, CSV, and
// ZIP archives holding any of these. Everything streams, so a multi-gigabyte
// Records.json never sits in memory.
package importer

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Point is one imported position (the store fills user, country, flags).
type Point struct {
	TS       int64
	Lat, Lng float64
	Alt      *float64
	Acc      *float64
	Speed    *float64 // m/s
	Course   *float64
	Battery  *int
	Motion   string
	Tracker  string
}

// Visit is a stay a source already knew about (Google's place visits).
type Visit struct {
	Start, End int64
	Lat, Lng   float64
	Name       string
	Address    string
	Semantic   string // HOME, WORK, … when the source says
}

// Sink receives what a file holds.
type Sink interface {
	Points(ctx context.Context, pts []Point) error
	Visit(ctx context.Context, v Visit) error
}

// Formats.
const (
	GoogleRecords  = "google_records"
	GoogleSemantic = "google_semantic"
	GoogleTimeline = "google_timeline"
	GPX            = "gpx"
	KML            = "kml"
	GeoJSON        = "geojson"
	OwnTracks      = "owntracks"
	CSV            = "csv"
	ZIP            = "zip"
)

// Names for the UI.
var Names = map[string]string{
	GoogleRecords: "Google Takeout Records.json", GoogleSemantic: "Google Semantic Location History", GoogleTimeline: "Google Timeline (phone export)",
	GPX: "GPX", KML: "KML", GeoJSON: "GeoJSON", OwnTracks: "OwnTracks .rec", CSV: "CSV", ZIP: "ZIP archive",
}

var recLine = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ\t`)

// Detect guesses the format from the name and the first bytes.
func Detect(name string, head []byte) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".gpx":
		return GPX
	case ".kml":
		return KML
	case ".zip", ".kmz":
		return ZIP
	case ".rec":
		return OwnTracks
	case ".csv":
		return CSV
	}
	h := string(head)
	t := strings.TrimLeft(h, "\ufeff \t\r\n")
	switch {
	case strings.HasPrefix(t, "PK"):
		return ZIP
	case strings.HasPrefix(t, "<"):
		if strings.Contains(h, "<gpx") {
			return GPX
		}
		if strings.Contains(h, "<kml") {
			return KML
		}
	case recLine.MatchString(t):
		return OwnTracks
	case strings.HasPrefix(t, "{") || strings.HasPrefix(t, "["):
		switch {
		case strings.Contains(h, "timelineObjects"):
			return GoogleSemantic
		case strings.Contains(h, "semanticSegments") || strings.Contains(h, "rawSignals") || strings.Contains(h, "timelinePath") ||
			(strings.HasPrefix(t, "[") && strings.Contains(h, "startTime") && (strings.Contains(h, "\"visit\"") || strings.Contains(h, "\"activity\""))):
			return GoogleTimeline
		case strings.Contains(h, "latitudeE7") || strings.Contains(h, "\"locations\""):
			return GoogleRecords
		case strings.Contains(h, "FeatureCollection") || strings.Contains(h, "\"Feature\""):
			return GeoJSON
		case strings.Contains(h, "\"_type\""):
			return OwnTracks
		}
	}
	if strings.Contains(strings.ToLower(strings.SplitN(t, "\n", 2)[0]), "lat") {
		return CSV
	}
	return ""
}

// Run imports one stream of a known format.
func Run(ctx context.Context, format string, r io.Reader, sink Sink) error {
	b := &batcher{sink: sink, ctx: ctx}
	var err error
	switch format {
	case GoogleRecords:
		err = googleRecords(ctx, r, b)
	case GoogleSemantic:
		err = googleSemantic(ctx, r, b, false)
	case GoogleTimeline:
		err = googleTimeline(ctx, r, b)
	case GPX:
		err = gpx(ctx, r, b)
	case KML:
		err = kml(ctx, r, b)
	case GeoJSON:
		err = geojson(ctx, r, b)
	case OwnTracks:
		err = ownTracksRec(ctx, r, b)
	case CSV:
		err = csvFile(ctx, r, b)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
	if err != nil {
		return err
	}
	return b.flush()
}

// RunZip imports every recognised file of an archive. When it holds
// Records.json, Google's semantic files only add their place visits (the
// raw records are the better route).
func RunZip(ctx context.Context, ra io.ReaderAt, size int64, sink Sink, progress func(done, total int)) error {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return fmt.Errorf("not a readable zip: %w", err)
	}
	hasRecords := false
	var files []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(filepath.Base(f.Name), ".") || strings.Contains(f.Name, "__MACOSX") {
			continue
		}
		files = append(files, f)
		if strings.EqualFold(filepath.Base(f.Name), "Records.json") {
			hasRecords = true
		}
	}
	found := 0
	for i, f := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		br := bufio.NewReaderSize(rc, 1<<16)
		head, _ := br.Peek(4096)
		format := Detect(f.Name, head)
		if strings.HasSuffix(strings.ToLower(f.Name), ".kml") {
			format = KML
		}
		b := &batcher{sink: sink, ctx: ctx}
		switch format {
		case "", ZIP:
		case GoogleSemantic:
			found++
			err = googleSemantic(ctx, br, b, hasRecords)
		default:
			found++
			err = Run(ctx, format, br, sink)
		}
		if err == nil {
			err = b.flush()
		}
		rc.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		if progress != nil {
			progress(i+1, len(files))
		}
	}
	if found == 0 {
		return errors.New("the archive holds no location files this importer knows")
	}
	return nil
}

// batcher groups points into batches of 2000 for the sink.
type batcher struct {
	ctx  context.Context
	sink Sink
	buf  []Point
}

func (b *batcher) add(p Point) error {
	if p.TS <= 0 || p.Lat < -90 || p.Lat > 90 || p.Lng < -180 || p.Lng > 180 {
		return nil
	}
	b.buf = append(b.buf, p)
	if len(b.buf) >= 2000 {
		return b.flush()
	}
	return nil
}

func (b *batcher) visit(v Visit) error {
	if v.End <= v.Start {
		return nil
	}
	return b.sink.Visit(b.ctx, v)
}

func (b *batcher) flush() error {
	if len(b.buf) == 0 {
		return nil
	}
	err := b.sink.Points(b.ctx, b.buf)
	b.buf = b.buf[:0]
	return err
}

// ---- small parsers shared by the formats ----

func parseTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	allDigits := true
	for _, c := range s {
		if c < '0' || c > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		var n int64
		fmt.Sscan(s, &n)
		if n > 1e11 {
			n /= 1000
		}
		return n, n > 0
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700", "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05", "2006/01/02 15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// latLngString reads "45.123°, 18.456°", "geo:45.123,18.456" or "45.1,18.4".
func latLngString(s string) (float64, float64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "geo:")
	s = strings.ReplaceAll(s, "°", "")
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	var lat, lng float64
	if _, err := fmt.Sscan(strings.TrimSpace(parts[0]), &lat); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscan(strings.TrimSpace(parts[1]), &lng); err != nil {
		return 0, 0, false
	}
	return lat, lng, true
}

func fptr(f float64) *float64 { return &f }

// peekReader lets a parser look at the start of a stream.
func peekReader(r io.Reader) (*bufio.Reader, []byte) {
	br := bufio.NewReaderSize(r, 1<<16)
	head, _ := br.Peek(4096)
	return br, bytes.Clone(head)
}
