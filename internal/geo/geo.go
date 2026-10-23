// Package geo is the maths of movement (distances, bearings, route
// simplification, encoded polylines), an offline country lookup from
// embedded Natural Earth borders, and a polite reverse geocoder.
package geo

import (
	"math"
	"strings"
)

// EarthRadius in metres.
const EarthRadius = 6371008.8

// Haversine returns metres between two coordinates.
func Haversine(lat1, lng1, lat2, lng2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := (lat2 - lat1) * math.Pi / 180
	dl := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * EarthRadius * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// Pt is a coordinate with a time, for simplification.
type Pt struct {
	Lat, Lng float64
	TS       int64
}

// Simplify runs Douglas–Peucker with a tolerance in metres (planar
// approximation, fine at route scale). The first and last points stay.
func Simplify(pts []Pt, toleranceM float64) []Pt {
	if len(pts) <= 2 || toleranceM <= 0 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	type seg struct{ a, b int }
	stack := []seg{{0, len(pts) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		maxD, idx := 0.0, -1
		for i := s.a + 1; i < s.b; i++ {
			if d := perpDist(pts[i], pts[s.a], pts[s.b]); d > maxD {
				maxD, idx = d, i
			}
		}
		if idx >= 0 && maxD > toleranceM {
			keep[idx] = true
			stack = append(stack, seg{s.a, idx}, seg{idx, s.b})
		}
	}
	out := make([]Pt, 0, len(pts)/4+2)
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

func perpDist(p, a, b Pt) float64 {
	k := math.Cos((a.Lat + b.Lat) / 2 * math.Pi / 180)
	ax, ay := a.Lng*k*111320, a.Lat*110540
	bx, by := b.Lng*k*111320, b.Lat*110540
	px, py := p.Lng*k*111320, p.Lat*110540
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

// PathLength sums the distance along a path in metres.
func PathLength(pts []Pt) float64 {
	var d float64
	for i := 1; i < len(pts); i++ {
		d += Haversine(pts[i-1].Lat, pts[i-1].Lng, pts[i].Lat, pts[i].Lng)
	}
	return d
}

// EncodePolyline encodes points in Google's polyline format (precision 5).
// The browser decodes it in a few lines and it is ~10× smaller than JSON.
func EncodePolyline(pts []Pt) string {
	var b strings.Builder
	var plat, plng int64
	for _, p := range pts {
		lat := int64(math.Round(p.Lat * 1e5))
		lng := int64(math.Round(p.Lng * 1e5))
		encodeValue(&b, lat-plat)
		encodeValue(&b, lng-plng)
		plat, plng = lat, lng
	}
	return b.String()
}

func encodeValue(b *strings.Builder, v int64) {
	v <<= 1
	if v < 0 {
		v = ^v
	}
	for v >= 0x20 {
		b.WriteByte(byte((0x20 | (v & 0x1f)) + 63))
		v >>= 5
	}
	b.WriteByte(byte(v + 63))
}

// DecodePolyline is the inverse of EncodePolyline (times are lost).
func DecodePolyline(s string) []Pt {
	var out []Pt
	var lat, lng int64
	for i := 0; i < len(s); {
		var d [2]int64
		for k := 0; k < 2; k++ {
			var res int64
			var shift uint
			for i < len(s) {
				c := int64(s[i]) - 63
				i++
				res |= (c & 0x1f) << shift
				shift += 5
				if c < 0x20 {
					break
				}
			}
			if res&1 != 0 {
				d[k] = ^(res >> 1)
			} else {
				d[k] = res >> 1
			}
		}
		lat += d[0]
		lng += d[1]
		out = append(out, Pt{Lat: float64(lat) / 1e5, Lng: float64(lng) / 1e5})
	}
	return out
}

// Centroid is the mean position (fine for clusters a few hundred metres wide).
func Centroid(pts []Pt) (float64, float64) {
	if len(pts) == 0 {
		return 0, 0
	}
	var la, ln float64
	for _, p := range pts {
		la += p.Lat
		ln += p.Lng
	}
	return la / float64(len(pts)), ln / float64(len(pts))
}

// ValidCoord reports whether lat/lng are on Earth and not the 0,0 "null island".
func ValidCoord(lat, lng float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lng) || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return false
	}
	return !(math.Abs(lat) < 1e-7 && math.Abs(lng) < 1e-7)
}

// CellKey buckets a coordinate into a grid of roughly `meters` squares, for
// caches and the fog of war.
func CellKey(lat, lng, meters float64) (int64, int64) {
	dLat := meters / 110540
	dLng := meters / (111320 * math.Max(0.05, math.Cos(lat*math.Pi/180)))
	return int64(math.Floor(lat / dLat)), int64(math.Floor(lng / dLng))
}
