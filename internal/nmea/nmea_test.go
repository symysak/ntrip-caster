package nmea

import (
	"math"
	"testing"
)

func TestParseGGA(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantLat  float64
		wantLon  float64
		wantQual int
		wantErr  bool
	}{
		{
			name:     "tokyo with checksum",
			line:     "$GPGGA,123519,3539.516,N,13944.724,E,1,08,0.9,545.4,M,46.9,M,,*47",
			wantLat:  35.658600,
			wantLon:  139.745400,
			wantQual: 1,
		},
		{
			name:     "GNGGA talker, RTK fixed",
			line:     "$GNGGA,000000.00,3441.622,S,05826.412,W,4,12,0.6,10.0,M,0.0,M,,",
			wantLat:  -34.693700,
			wantLon:  -58.440200,
			wantQual: 4,
		},
		{name: "not gga", line: "$GPRMC,123519,A,4807.038,N", wantErr: true},
		{name: "empty", line: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fix, err := ParseGGA(tt.line)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got fix %+v", fix)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if math.Abs(fix.Lat-tt.wantLat) > 1e-4 {
				t.Errorf("lat = %f, want %f", fix.Lat, tt.wantLat)
			}
			if math.Abs(fix.Lon-tt.wantLon) > 1e-4 {
				t.Errorf("lon = %f, want %f", fix.Lon, tt.wantLon)
			}
			if fix.Quality != tt.wantQual {
				t.Errorf("quality = %d, want %d", fix.Quality, tt.wantQual)
			}
		})
	}
}
