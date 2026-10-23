package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Geocoder turns coordinates into a place name and address. It speaks to a
// Nominatim-compatible server (the public one by default, where the usage
// policy allows one request a second with a real User-Agent) or Photon.
type Geocoder struct {
	BaseURL string // https://nominatim.openstreetmap.org or a Photon URL
	Kind    string // nominatim | photon | off
	UA      string
	HTTP    *http.Client

	mu   sync.Mutex
	next time.Time
	gap  time.Duration
}

// NewGeocoder creates a geocoder; kind "" means nominatim.
func NewGeocoder(kind, baseURL, ua string) *Geocoder {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "nominatim"
	}
	if baseURL == "" {
		switch kind {
		case "photon":
			baseURL = "https://photon.komoot.io"
		default:
			baseURL = "https://nominatim.openstreetmap.org"
		}
	}
	gap := 1100 * time.Millisecond
	if !strings.Contains(baseURL, "openstreetmap.org") && !strings.Contains(baseURL, "komoot.io") {
		gap = 100 * time.Millisecond // your own server
	}
	return &Geocoder{BaseURL: strings.TrimRight(baseURL, "/"), Kind: kind, UA: ua, HTTP: &http.Client{Timeout: 20 * time.Second}, gap: gap}
}

// Enabled reports whether lookups are allowed.
func (g *Geocoder) Enabled() bool { return g != nil && g.Kind != "off" && g.BaseURL != "" }

// Place is a reverse-geocoding answer.
type Place struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	City        string `json:"city"`
	State       string `json:"state"`
	Country     string `json:"country"`
	CountryCode string `json:"country_code"`
	Category    string `json:"category"`
	Type        string `json:"type"`
}

func (g *Geocoder) wait(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	at := g.next
	if at.Before(now) {
		at = now
	}
	g.next = at.Add(g.gap)
	g.mu.Unlock()
	if d := time.Until(at); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return nil
}

func (g *Geocoder) getJSON(ctx context.Context, u string, v any) error {
	if err := g.wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", g.UA)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en")
	res, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("geocoder HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

// Reverse looks up the place at a coordinate.
func (g *Geocoder) Reverse(ctx context.Context, lat, lng float64) (*Place, error) {
	if !g.Enabled() {
		return nil, fmt.Errorf("reverse geocoding is off")
	}
	if g.Kind == "photon" {
		return g.reversePhoton(ctx, lat, lng)
	}
	q := url.Values{"format": {"jsonv2"}, "lat": {ff(lat)}, "lon": {ff(lng)}, "zoom": {"18"}, "addressdetails": {"1"}, "namedetails": {"0"}}
	var r struct {
		Name     string            `json:"name"`
		Display  string            `json:"display_name"`
		Category string            `json:"category"`
		Type     string            `json:"type"`
		Address  map[string]string `json:"address"`
		Error    string            `json:"error"`
	}
	if err := g.getJSON(ctx, g.BaseURL+"/reverse?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("geocoder: %s", r.Error)
	}
	a := r.Address
	p := &Place{
		Name:        r.Name,
		City:        first(a["city"], a["town"], a["village"], a["municipality"], a["hamlet"], a["suburb"]),
		State:       first(a["state"], a["region"], a["county"]),
		Country:     a["country"],
		CountryCode: strings.ToUpper(a["country_code"]),
		Category:    r.Category,
		Type:        r.Type,
	}
	street := strings.TrimSpace(first(a["road"], a["pedestrian"], a["footway"], a["path"]) + " " + a["house_number"])
	p.Address = strings.Join(nonEmpty(street, p.City, p.Country), ", ")
	if p.Name == "" {
		p.Name = first(street, a["neighbourhood"], a["suburb"], p.City, r.Display)
	}
	return p, nil
}

func (g *Geocoder) reversePhoton(ctx context.Context, lat, lng float64) (*Place, error) {
	q := url.Values{"lat": {ff(lat)}, "lon": {ff(lng)}, "limit": {"1"}, "lang": {"en"}}
	var r struct {
		Features []struct {
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if err := g.getJSON(ctx, g.BaseURL+"/reverse?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	if len(r.Features) == 0 {
		return nil, fmt.Errorf("geocoder: nothing here")
	}
	pr := r.Features[0].Properties
	s := func(k string) string { v, _ := pr[k].(string); return v }
	street := strings.TrimSpace(s("street") + " " + s("housenumber"))
	p := &Place{Name: s("name"), City: first(s("city"), s("town"), s("village"), s("district")), State: s("state"),
		Country: s("country"), CountryCode: strings.ToUpper(s("countrycode")), Category: s("osm_key"), Type: s("osm_value")}
	p.Address = strings.Join(nonEmpty(street, p.City, p.Country), ", ")
	if p.Name == "" {
		p.Name = first(street, p.City)
	}
	return p, nil
}

// Search finds places by name (for "add a place" and the map search box).
func (g *Geocoder) Search(ctx context.Context, q string, limit int) ([]SearchResult, error) {
	if !g.Enabled() {
		return nil, fmt.Errorf("geocoding is off")
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	if g.Kind == "photon" {
		var r struct {
			Features []struct {
				Geometry struct {
					Coordinates []float64 `json:"coordinates"`
				} `json:"geometry"`
				Properties map[string]any `json:"properties"`
			} `json:"features"`
		}
		if err := g.getJSON(ctx, g.BaseURL+"/api?"+url.Values{"q": {q}, "limit": {strconv.Itoa(limit)}, "lang": {"en"}}.Encode(), &r); err != nil {
			return nil, err
		}
		out := []SearchResult{}
		for _, f := range r.Features {
			if len(f.Geometry.Coordinates) < 2 {
				continue
			}
			s := func(k string) string { v, _ := f.Properties[k].(string); return v }
			out = append(out, SearchResult{Name: first(s("name"), s("street")), Display: strings.Join(nonEmpty(s("name"), s("city"), s("country")), ", "),
				Lat: f.Geometry.Coordinates[1], Lng: f.Geometry.Coordinates[0]})
		}
		return out, nil
	}
	var r []struct {
		Name    string `json:"name"`
		Display string `json:"display_name"`
		Lat     string `json:"lat"`
		Lon     string `json:"lon"`
	}
	if err := g.getJSON(ctx, g.BaseURL+"/search?"+url.Values{"format": {"jsonv2"}, "q": {q}, "limit": {strconv.Itoa(limit)}}.Encode(), &r); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(r))
	for _, x := range r {
		la, _ := strconv.ParseFloat(x.Lat, 64)
		lo, _ := strconv.ParseFloat(x.Lon, 64)
		out = append(out, SearchResult{Name: first(x.Name, x.Display), Display: x.Display, Lat: la, Lng: lo})
	}
	return out, nil
}

// SearchResult is a forward-geocoding hit.
type SearchResult struct {
	Name    string  `json:"name"`
	Display string  `json:"display"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

func ff(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }

func first(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func nonEmpty(v ...string) []string {
	out := v[:0:0]
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
