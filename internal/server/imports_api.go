package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/abjelosevic88/pocket-homelab-movement/internal/geo"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/importer"
	"github.com/abjelosevic88/pocket-homelab-movement/internal/store"
)

func (s *Server) handleListImports(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListImports(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err, "imports")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": list, "formats": importer.Names})
}

// handleUploadImport stores an uploaded file (multipart "file", or the raw
// body with ?name=) and queues it. Files go straight to disk, so a
// multi-gigabyte Takeout is fine.
func (s *Server) handleUploadImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	var src io.Reader
	name := r.URL.Query().Get("name")
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad upload")
			return
		}
		for {
			part, err := mr.NextPart()
			if err != nil {
				writeErr(w, http.StatusBadRequest, "no file in the upload")
				return
			}
			if part.FormName() == "file" {
				name = firstNonEmpty(part.FileName(), name)
				src = part
				break
			}
		}
	} else {
		src = r.Body
	}
	if name == "" {
		name = "upload"
	}
	name = filepath.Base(name)
	if err := os.MkdirAll(s.cfg.ImportsDir, 0o755); err != nil {
		s.fail(w, err, "imports dir")
		return
	}
	path := filepath.Join(s.cfg.ImportsDir, fmt.Sprintf("%d-%s-%s", u.ID, store.RandomToken(6), strings.ReplaceAll(name, " ", "_")))
	f, err := os.Create(path)
	if err != nil {
		s.fail(w, err, "save upload")
		return
	}
	n, err := io.Copy(f, src)
	f.Close()
	if err != nil {
		os.Remove(path)
		writeErr(w, http.StatusBadRequest, "upload failed: "+err.Error())
		return
	}
	head := make([]byte, 4096)
	if fh, err := os.Open(path); err == nil {
		k, _ := io.ReadFull(fh, head)
		head = head[:k]
		fh.Close()
	}
	format := importer.Detect(name, head)
	if format == "" {
		os.Remove(path)
		writeErr(w, http.StatusUnprocessableEntity, "this does not look like a location file: Google Takeout (Records.json, Semantic Location History, Timeline.json), GPX, KML, GeoJSON, OwnTracks .rec, CSV or a ZIP of them")
		return
	}
	imp := &store.Import{UserID: u.ID, Name: name, Format: format, FilePath: path, Size: n}
	if err := s.store.CreateImport(ctx, imp); err != nil {
		os.Remove(path)
		s.fail(w, err, "queue import")
		return
	}
	s.kickImport()
	writeJSON(w, http.StatusAccepted, imp)
}

// handleImportDawarich queues a pull of everything from a Dawarich server:
// saved areas, confirmed visits and every point, through its API.
func (s *Server) handleImportDawarich(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	var in struct {
		Source string `json:"source"` // direct (default) | core
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	dw, me, code, err := s.pickDawarich(ctx, u.Email, in.Source, in.URL, in.APIKey)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	imp, err := s.queueDawarichImport(ctx, u.ID, dw, me)
	if err != nil {
		s.fail(w, err, "queue import")
		return
	}
	writeJSON(w, http.StatusAccepted, imp)
}

// checkDawarich normalises a Dawarich address and checks the key against
// it, returning the account's email (or an HTTP status and a message).
func checkDawarich(ctx context.Context, rawURL, key string) (*dawarichClient, string, int, error) {
	base := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	pu, err := url.Parse(base)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" || strings.TrimSpace(key) == "" {
		return nil, "", http.StatusBadRequest, errors.New("the Dawarich URL (http… or https…) and an API key are required")
	}
	dw := &dawarichClient{base: base, key: strings.TrimSpace(key), http: &http.Client{Timeout: 2 * time.Minute}}
	me, err := dw.me(ctx)
	if err != nil {
		return nil, "", http.StatusBadGateway, errors.New("cannot use that Dawarich: " + err.Error())
	}
	return dw, me, 0, nil
}

func (s *Server) queueDawarichImport(ctx context.Context, userID int64, dw *dawarichClient, me string) (*store.Import, error) {
	pu, _ := url.Parse(dw.base)
	imp := &store.Import{UserID: userID, Name: "Dawarich " + pu.Host + " (" + me + ")", Format: "dawarich", SourceURL: dw.base, SourceKey: dw.key}
	if err := s.store.CreateImport(ctx, imp); err != nil {
		return nil, err
	}
	s.kickImport()
	return imp, nil
}

func (s *Server) handleDeleteImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	imp, err := s.store.GetImport(ctx, u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "get import")
		return
	}
	if imp.Status == "running" {
		writeErr(w, http.StatusConflict, "the import is still running")
		return
	}
	if err := s.store.DeleteImport(ctx, u.ID, imp.ID, queryBool(r, "points")); err != nil {
		s.fail(w, err, "delete import")
		return
	}
	if imp.FilePath != "" {
		os.Remove(imp.FilePath)
	}
	s.kickRebuild()
	w.WriteHeader(http.StatusNoContent)
}

// ---- running an import ----

// importSink stores what a file holds for one person.
type importSink struct {
	s       *Server
	imp     *store.Import
	user    int64
	zones   []store.PrivacyZone
	read    atomic.Int64
	added   atomic.Int64
	visits  atomic.Int64
	last    time.Time
	homeSet bool
	workSet bool
}

func (k *importSink) Points(ctx context.Context, pts []importer.Point) error {
	batch := make([]store.Point, 0, len(pts))
	impID := k.imp.ID
	for _, p := range pts {
		k.read.Add(1)
		if !geo.ValidCoord(p.Lat, p.Lng) || p.TS < 315532800 || p.TS > time.Now().Unix()+86400 {
			continue
		}
		drop := false
		for _, z := range k.zones {
			if z.DropPoints && geo.Haversine(z.Lat, z.Lng, p.Lat, p.Lng) <= z.Radius {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		sp := store.Point{UserID: k.user, ImportID: &impID, TS: p.TS, Lat: round7(p.Lat), Lng: round7(p.Lng), Altitude: p.Alt, Accuracy: p.Acc, Speed: p.Speed,
			Course: p.Course, Battery: p.Battery, Motion: p.Motion, TrackerID: p.Tracker, Source: "import"}
		sp.Country = geo.CountryAt(sp.Lat, sp.Lng)
		batch = append(batch, sp)
		if k.imp.FirstTS == 0 || p.TS < k.imp.FirstTS {
			k.imp.FirstTS = p.TS
		}
		if p.TS > k.imp.LastTS {
			k.imp.LastTS = p.TS
		}
	}
	res, err := k.s.store.InsertPoints(ctx, k.user, batch)
	if err != nil {
		return err
	}
	k.added.Add(int64(res.Added))
	k.save(ctx, false)
	return nil
}

// Visit keeps a stay the source named (Google's place visits) as a
// confirmed visit you can still edit; HOME and WORK become saved places
// when you have none yet.
func (k *importSink) Visit(ctx context.Context, v importer.Visit) error {
	s := k.s
	var placeID int64
	sem := strings.ToUpper(v.Semantic)
	if (sem == "HOME" && !k.homeSet) || (sem == "WORK" && !k.workSet) || sem == "INFERRED_HOME" || sem == "INFERRED_WORK" {
		cat := map[string]string{"HOME": "home", "INFERRED_HOME": "home", "WORK": "work", "INFERRED_WORK": "work"}[sem]
		near, _ := s.store.PlacesNear(ctx, k.user, v.Lat, v.Lng, 150)
		for _, p := range near {
			if p.Category == cat {
				placeID = p.ID
			}
		}
		if placeID == 0 {
			if existing, err := s.store.ListPlaces(ctx, k.user, store.PlaceQuery{Kind: store.PlaceSaved, IncludeHidden: true}); err == nil {
				has := false
				for _, p := range existing {
					if p.Category == cat {
						has = true
					}
				}
				if !has {
					p := &store.Place{UserID: k.user, Name: capitalize(cat), Renamed: true, Kind: store.PlaceSaved, Category: cat, Lat: v.Lat, Lng: v.Lng, Radius: 100,
						Address: v.Address, Country: geo.CountryAt(v.Lat, v.Lng), Geocoded: 0}
					if s.store.CreatePlace(ctx, p) == nil {
						placeID = p.ID
					}
				}
			}
		}
		if cat == "home" {
			k.homeSet = true
		} else {
			k.workSet = true
		}
	}
	if placeID == 0 {
		near, _ := s.store.PlacesNear(ctx, k.user, v.Lat, v.Lng, 60)
		best := math.MaxFloat64
		for _, p := range near {
			if d := geo.Haversine(p.Lat, p.Lng, v.Lat, v.Lng); d < best && d <= math.Max(p.Radius, 50) {
				best, placeID = d, p.ID
			}
		}
	}
	if placeID == 0 {
		// The geocoder still fills in the city and address; a name from the
		// source is kept (renamed).
		p := &store.Place{UserID: k.user, Name: v.Name, Renamed: v.Name != "", Kind: store.PlaceDiscovered, Lat: v.Lat, Lng: v.Lng, Radius: 60, Address: v.Address,
			Country: geo.CountryAt(v.Lat, v.Lng)}
		if err := s.store.CreatePlace(ctx, p); err != nil {
			return err
		}
		placeID = p.ID
	}
	visit := &store.Visit{UserID: k.user, PlaceID: &placeID, StartTS: v.Start, EndTS: v.End, Lat: v.Lat, Lng: v.Lng, Status: store.VisitConfirmed,
		Manual: true, Notes: store.ImportNote(k.imp.ID)}
	if err := s.store.CreateVisit(ctx, visit); err != nil {
		return err
	}
	k.visits.Add(1)
	s.kickGeocode()
	return nil
}

func (k *importSink) save(ctx context.Context, force bool) {
	if !force && time.Since(k.last) < 2*time.Second {
		return
	}
	k.last = time.Now()
	k.imp.PointsRead, k.imp.PointsAdded, k.imp.VisitsAdded = k.read.Load(), k.added.Load(), k.visits.Load()
	_ = k.s.store.SaveImportProgress(ctx, k.imp)
}

// countingReader reports progress through a file.
type countingReader struct {
	r    io.Reader
	n    int64
	size int64
	imp  *store.Import
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.size > 0 {
		c.imp.Progress = math.Min(0.99, float64(c.n)/float64(c.size))
	}
	return n, err
}

func (s *Server) runImport(ctx context.Context, imp *store.Import) {
	start := time.Now()
	zones, _ := s.store.PrivacyZones(ctx, imp.UserID)
	sink := &importSink{s: s, imp: imp, user: imp.UserID, zones: zones}
	s.log.Info("import started", "id", imp.ID, "format", imp.Format, "name", imp.Name)
	var err error
	switch imp.Format {
	case "dawarich":
		err = s.importFromDawarich(ctx, imp, sink)
	case importer.ZIP:
		var f *os.File
		if f, err = os.Open(imp.FilePath); err == nil {
			err = importer.RunZip(ctx, f, imp.Size, sink, func(done, total int) {
				imp.Progress = float64(done) / float64(total)
				sink.save(ctx, true)
			})
			f.Close()
		}
	default:
		var f *os.File
		if f, err = os.Open(imp.FilePath); err == nil {
			cr := &countingReader{r: bufio.NewReaderSize(f, 1<<20), size: imp.Size, imp: imp}
			err = importer.Run(ctx, imp.Format, cr, sink)
			f.Close()
		}
	}
	if errors.Is(err, context.Canceled) {
		return // shutting down; it runs again on the next start
	}
	sink.save(ctx, true)
	if err == nil && sink.read.Load() == 0 && sink.visits.Load() == 0 {
		err = errors.New("no positions found in this file")
	}
	_ = s.store.FinishImport(ctx, imp, err)
	if err == nil && imp.FilePath != "" {
		os.Remove(imp.FilePath)
	}
	if imp.FirstTS > 0 {
		_ = s.store.MarkDirty(ctx, imp.UserID, imp.FirstTS, imp.LastTS)
		s.kickRebuild()
	}
	s.log.Info("import finished", "id", imp.ID, "read", imp.PointsRead, "added", imp.PointsAdded, "visits", imp.VisitsAdded, "err", err, "took", time.Since(start).Round(time.Second).String())
}

// ---- pulling from another Dawarich ----

type dawarichClient struct {
	base, key string
	http      *http.Client
}

func (d *dawarichClient) get(ctx context.Context, path string, q url.Values, v any) (http.Header, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_key", d.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("the API key was refused (401)")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", res.StatusCode, path)
	}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	return res.Header, dec.Decode(v)
}

func (d *dawarichClient) me(ctx context.Context) (string, error) {
	var out struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if _, err := d.get(ctx, "/api/v1/users/me", nil, &out); err != nil {
		return "", err
	}
	return out.User.Email, nil
}

func (s *Server) importFromDawarich(ctx context.Context, imp *store.Import, sink *importSink) error {
	dw := &dawarichClient{base: imp.SourceURL, key: imp.SourceKey, http: &http.Client{Timeout: 5 * time.Minute}}
	if dw.key == "" {
		return errors.New("the API key is gone (the import was restarted); start it again")
	}
	// Areas become saved places.
	var areas []map[string]any
	if _, err := dw.get(ctx, "/api/v1/areas", nil, &areas); err == nil {
		existing, _ := s.store.ListPlaces(ctx, imp.UserID, store.PlaceQuery{Kind: store.PlaceSaved, IncludeHidden: true})
		for _, a := range areas {
			lat, ok1 := num(a["latitude"])
			lng, ok2 := num(a["longitude"])
			if !ok1 || !ok2 {
				continue
			}
			dup := false
			for _, p := range existing {
				if geo.Haversine(p.Lat, p.Lng, lat, lng) < 30 {
					dup = true
				}
			}
			if dup {
				continue
			}
			radius, _ := num(a["radius"])
			p := &store.Place{UserID: imp.UserID, Name: str(a["name"]), Renamed: true, Kind: store.PlaceSaved, Lat: lat, Lng: lng, Radius: math.Max(radius, 30),
				Country: geo.CountryAt(lat, lng)}
			if strings.EqualFold(p.Name, "home") {
				p.Category = "home"
			} else if strings.EqualFold(p.Name, "work") || strings.EqualFold(p.Name, "office") {
				p.Category = "work"
			}
			_ = s.store.CreatePlace(ctx, p)
		}
	}
	// Every point, oldest first.
	var total int64 = -1
	for page := 1; ; page++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var rows []map[string]any
		h, err := dw.get(ctx, "/api/v1/points", url.Values{"start_at": {"1970-01-01T00:00:00Z"}, "end_at": {time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)},
			"order": {"asc"}, "per_page": {"5000"}, "page": {strconv.Itoa(page)}}, &rows)
		if err != nil {
			return fmt.Errorf("points page %d: %w", page, err)
		}
		if total < 0 {
			total, _ = strconv.ParseInt(h.Get("X-Total-Pages"), 10, 64)
		}
		pts := make([]importer.Point, 0, len(rows))
		for _, row := range rows {
			if p, ok := parseDawarichPoint(row); ok {
				pts = append(pts, p)
			}
		}
		if err := sink.Points(ctx, pts); err != nil {
			return err
		}
		if total > 0 {
			imp.Progress = math.Min(0.95, float64(page)/float64(total))
		}
		if len(rows) == 0 || (total >= 0 && int64(page) >= total) {
			break
		}
	}
	// Visits you confirmed there keep their names here.
	if imp.FirstTS > 0 {
		for from := time.Unix(imp.FirstTS, 0).UTC(); from.Unix() <= imp.LastTS; from = from.AddDate(0, 0, 30) {
			var vs []map[string]any
			q := url.Values{"start_at": {from.Format(time.RFC3339)}, "end_at": {from.AddDate(0, 0, 30).Format(time.RFC3339)}}
			if _, err := dw.get(ctx, "/api/v1/visits", q, &vs); err != nil {
				break
			}
			for _, v := range vs {
				if str(v["status"]) != "confirmed" {
					continue
				}
				st, ok1 := parseTS(v["started_at"])
				en, ok2 := parseTS(v["ended_at"])
				place := obj(v["place"])
				lat, ok3 := num(place["latitude"])
				lng, ok4 := num(place["longitude"])
				if ok1 && ok2 && ok3 && ok4 {
					_ = sink.Visit(ctx, importer.Visit{Start: st, End: en, Lat: lat, Lng: lng, Name: str(v["name"])})
				}
			}
		}
	}
	return nil
}

// parseDawarichPoint reads one row of Dawarich's /api/v1/points.
func parseDawarichPoint(row map[string]any) (importer.Point, bool) {
	lat, ok1 := num(row["latitude"])
	lng, ok2 := num(row["longitude"])
	ts, ok3 := num(row["timestamp"])
	if !ok1 || !ok2 || !ok3 {
		return importer.Point{}, false
	}
	p := importer.Point{TS: int64(ts), Lat: lat, Lng: lng, Alt: numPtr(row["altitude"]), Acc: numPtr(row["accuracy"]), Course: numPtr(row["course"]),
		Tracker: str(row["tracker_id"])}
	if v := numPtr(row["velocity"]); v != nil && *v >= 0 {
		p.Speed = v
	}
	if b, ok := num(row["battery"]); ok && b >= 0 {
		n := int(b)
		p.Battery = &n
	}
	if md := obj(row["motion_data"]); md != nil {
		if m, ok := md["motion"].([]any); ok && len(m) > 0 {
			p.Motion = str(m[0])
		}
	}
	return p, true
}

// ---- export ----

// handleExport writes a range (or all=1) as GeoJSON (default), GPX or CSV.
// Points inside privacy zones are yours, so they stay in unless zones=1
// asks to leave them out (for sharing a file).
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	_, loc := s.userLoc(ctx, u.ID)
	from, to := s.rangeParams(r, loc)
	if queryBool(r, "all") {
		from, to = 0, time.Now().Unix()+86400
	}
	format := firstNonEmpty(r.URL.Query().Get("format"), "geojson")
	var zones []store.PrivacyZone
	if queryBool(r, "zones") {
		zones, _ = s.store.PrivacyZones(ctx, u.ID)
	}
	inZone := func(p store.Point) bool {
		for _, z := range zones {
			if geo.Haversine(z.Lat, z.Lng, p.Lat, p.Lng) <= z.Radius {
				return true
			}
		}
		return false
	}
	name := fmt.Sprintf("movement-%s-%s", time.Unix(from, 0).In(loc).Format("20060102"), time.Unix(to, 0).In(loc).Format("20060102"))
	if from == 0 {
		name = "movement-all-" + time.Now().In(loc).Format("20060102")
	}
	q := store.PointQuery{From: from, To: to, IncludeOutliers: queryBool(r, "outliers")}
	bw := bufio.NewWriterSize(w, 1<<16)
	defer bw.Flush()
	switch format {
	case "gpx":
		w.Header().Set("Content-Type", "application/gpx+xml")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.gpx"`)
		fmt.Fprint(bw, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<gpx version="1.1" creator="Pocket Movement" xmlns="http://www.topografix.com/GPX/1/1"><trk><name>`+name+`</name><trkseg>`+"\n")
		var prev int64
		_ = s.store.EachPoint(ctx, u.ID, q, func(p store.Point) error {
			if inZone(p) {
				return nil
			}
			if prev > 0 && p.TS-prev > 30*60 {
				fmt.Fprint(bw, "</trkseg><trkseg>\n")
			}
			prev = p.TS
			fmt.Fprintf(bw, `<trkpt lat="%.7f" lon="%.7f">`, p.Lat, p.Lng)
			if p.Altitude != nil {
				fmt.Fprintf(bw, "<ele>%.1f</ele>", *p.Altitude)
			}
			fmt.Fprintf(bw, "<time>%s</time>", time.Unix(p.TS, 0).UTC().Format(time.RFC3339))
			if p.Speed != nil {
				fmt.Fprintf(bw, "<extensions><speed>%.2f</speed></extensions>", *p.Speed)
			}
			fmt.Fprint(bw, "</trkpt>\n")
			return nil
		})
		fmt.Fprint(bw, "</trkseg></trk></gpx>\n")
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		fmt.Fprint(bw, "timestamp,time,latitude,longitude,altitude,accuracy,speed,course,battery,country,source,tracker_id\n")
		_ = s.store.EachPoint(ctx, u.ID, q, func(p store.Point) error {
			if inZone(p) {
				return nil
			}
			fmt.Fprintf(bw, "%d,%s,%.7f,%.7f,%s,%s,%s,%s,%s,%s,%s,%s\n", p.TS, time.Unix(p.TS, 0).UTC().Format(time.RFC3339), p.Lat, p.Lng, fmtF(p.Altitude), fmtF(p.Accuracy),
				fmtF(p.Speed), fmtF(p.Course), is(p.Battery), p.Country, p.Source, csvSafe(p.TrackerID))
			return nil
		})
	default:
		w.Header().Set("Content-Type", "application/geo+json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.geojson"`)
		fmt.Fprint(bw, `{"type":"FeatureCollection","features":[`+"\n")
		firstRow := true
		enc := json.NewEncoder(bw)
		_ = s.store.EachPoint(ctx, u.ID, q, func(p store.Point) error {
			if inZone(p) {
				return nil
			}
			if !firstRow {
				fmt.Fprint(bw, ",")
			}
			firstRow = false
			props := map[string]any{"timestamp": p.TS, "time": time.Unix(p.TS, 0).UTC().Format(time.RFC3339), "altitude": p.Altitude, "accuracy": p.Accuracy,
				"vertical_accuracy": p.VerticalAccuracy, "speed": p.Speed, "velocity": p.Speed, "course": p.Course, "battery": p.Battery, "battery_state": nullable(p.BatteryState),
				"tracker_id": nullable(p.TrackerID), "country": nullable(p.Country), "source": p.Source}
			return enc.Encode(map[string]any{"type": "Feature", "geometry": map[string]any{"type": "Point", "coordinates": []float64{p.Lng, p.Lat}}, "properties": props})
		})
		fmt.Fprint(bw, "]}\n")
	}
}

func fmtF(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

func is(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

func csvSafe(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
