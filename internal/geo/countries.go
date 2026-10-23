package geo

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

//go:embed data/countries.json.gz
var countriesGz []byte

// Country is one entry of the embedded Natural Earth 1:50m borders.
type Country struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Continent string `json:"continent"`
	bbox      [4]float64
	polys     [][][]float64 // polygon → rings (first outer) → flat lng,lat
}

var (
	countriesOnce sync.Once
	countries     []*Country
	countryByCode map[string]*Country
	lookupCache   sync.Map // cell → code
)

func loadCountries() {
	countriesOnce.Do(func() {
		countryByCode = map[string]*Country{}
		zr, err := gzip.NewReader(bytes.NewReader(countriesGz))
		if err != nil {
			return
		}
		var raw []struct {
			C string        `json:"c"`
			N string        `json:"n"`
			K string        `json:"k"`
			B [4]float64    `json:"b"`
			P [][][]float64 `json:"p"`
		}
		if err := json.NewDecoder(zr).Decode(&raw); err != nil {
			return
		}
		for _, r := range raw {
			c := &Country{Code: r.C, Name: r.N, Continent: r.K, bbox: r.B, polys: r.P}
			countries = append(countries, c)
			if prev, ok := countryByCode[r.C]; !ok || len(prev.polys) < len(c.polys) {
				countryByCode[r.C] = c
			}
		}
		// Small countries first: an enclave (San Marino, Vatican, Lesotho)
		// wins over the country around it when borders are coarse.
		sort.SliceStable(countries, func(i, j int) bool {
			ai := (countries[i].bbox[2] - countries[i].bbox[0]) * (countries[i].bbox[3] - countries[i].bbox[1])
			aj := (countries[j].bbox[2] - countries[j].bbox[0]) * (countries[j].bbox[3] - countries[j].bbox[1])
			return ai < aj
		})
	})
}

// CountryAt returns the ISO 3166-1 alpha-2 code of the country containing
// the point, or "" at sea. Results are cached on a ~1 km grid.
func CountryAt(lat, lng float64) string {
	loadCountries()
	ky, kx := CellKey(lat, lng, 1000)
	key := [2]int64{ky, kx}
	if v, ok := lookupCache.Load(key); ok {
		return v.(string)
	}
	code := ""
	for _, c := range countries {
		if lng < c.bbox[0] || lng > c.bbox[2] || lat < c.bbox[1] || lat > c.bbox[3] {
			continue
		}
		if c.contains(lat, lng) {
			code = c.Code
			break
		}
	}
	if code == "" {
		code = nearestCoast(lat, lng)
	}
	lookupCache.Store(key, code)
	return code
}

// nearestCoast catches points just off a coarse coastline (a ferry in the
// harbour, a beach): the country whose box is within ~3 km.
func nearestCoast(lat, lng float64) string {
	const pad = 0.03
	for _, c := range countries {
		if lng < c.bbox[0]-pad || lng > c.bbox[2]+pad || lat < c.bbox[1]-pad || lat > c.bbox[3]+pad {
			continue
		}
		for _, d := range [][2]float64{{pad, 0}, {-pad, 0}, {0, pad}, {0, -pad}} {
			if c.contains(lat+d[0], lng+d[1]) {
				return c.Code
			}
		}
	}
	return ""
}

func (c *Country) contains(lat, lng float64) bool {
	for _, poly := range c.polys {
		if len(poly) == 0 || !inRing(poly[0], lat, lng) {
			continue
		}
		hole := false
		for _, h := range poly[1:] {
			if inRing(h, lat, lng) {
				hole = true
				break
			}
		}
		if !hole {
			return true
		}
	}
	return false
}

// inRing is the even–odd ray cast over a flat lng,lat ring.
func inRing(r []float64, lat, lng float64) bool {
	in := false
	n := len(r) / 2
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := r[2*i], r[2*i+1]
		xj, yj := r[2*j], r[2*j+1]
		if (yi > lat) != (yj > lat) && lng < (xj-xi)*(lat-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// CountryName returns the English name for an ISO code ("" when unknown).
func CountryName(code string) string {
	loadCountries()
	if c, ok := countryByCode[code]; ok {
		return c.Name
	}
	return ""
}

// CountryInfo returns code, name and continent.
func CountryInfo(code string) (Country, bool) {
	loadCountries()
	c, ok := countryByCode[code]
	if !ok {
		return Country{}, false
	}
	return Country{Code: c.Code, Name: c.Name, Continent: c.Continent}, true
}

// CountryShape returns the simplified outline of a country as rings of
// [lat, lng] for the scratch map (nil when unknown).
func CountryShape(code string) [][][2]float64 {
	loadCountries()
	var out [][][2]float64
	for _, c := range countries {
		if c.Code != code {
			continue
		}
		for _, poly := range c.polys {
			if len(poly) == 0 {
				continue
			}
			r := poly[0]
			ring := make([][2]float64, 0, len(r)/2)
			for i := 0; i+1 < len(r); i += 2 {
				ring = append(ring, [2]float64{r[i+1], r[i]})
			}
			out = append(out, ring)
		}
	}
	return out
}
