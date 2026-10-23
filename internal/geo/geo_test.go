package geo

import (
	"math"
	"testing"
)

func TestCountryAt(t *testing.T) {
	cases := []struct {
		lat, lng float64
		want     string
	}{
		{43.8563, 18.4131, "BA"},   // Sarajevo
		{45.8150, 15.9819, "HR"},   // Zagreb
		{44.7866, 20.4489, "RS"},   // Belgrade
		{42.4304, 19.2594, "ME"},   // Podgorica
		{41.9028, 12.4964, "IT"},   // Rome
		{51.5074, -0.1278, "GB"},   // London
		{40.7128, -74.0060, "US"},  // New York
		{35.6762, 139.6503, "JP"},  // Tokyo
		{-33.8688, 151.2093, "AU"}, // Sydney
		{36.0, -40.0, ""},          // mid-Atlantic
	}
	for _, c := range cases {
		if got := CountryAt(c.lat, c.lng); got != c.want {
			t.Errorf("CountryAt(%v,%v) = %q, want %q", c.lat, c.lng, got, c.want)
		}
	}
	if CountryName("BA") == "" {
		t.Error("no name for BA")
	}
}

func TestPolylineRoundTrip(t *testing.T) {
	pts := []Pt{{Lat: 38.5, Lng: -120.2}, {Lat: 40.7, Lng: -120.95}, {Lat: 43.252, Lng: -126.453}}
	enc := EncodePolyline(pts)
	if enc != "_p~iF~ps|U_ulLnnqC_mqNvxq`@" {
		t.Fatalf("encode = %q", enc)
	}
	dec := DecodePolyline(enc)
	for i := range pts {
		if math.Abs(dec[i].Lat-pts[i].Lat) > 1e-5 || math.Abs(dec[i].Lng-pts[i].Lng) > 1e-5 {
			t.Fatalf("decode %d = %+v", i, dec[i])
		}
	}
}

func BenchmarkCountryAt(b *testing.B) {
	for i := 0; i < b.N; i++ {
		lookupCache.Clear()
		CountryAt(43.8563+float64(i%100)*0.01, 18.4131)
	}
}
