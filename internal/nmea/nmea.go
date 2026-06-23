// Package nmea parses the subset of NMEA 0183 needed for handover: the GGA
// sentence, from which the caster extracts the client's position.
package nmea

import (
	"errors"
	"strconv"
	"strings"
)

// ErrNotGGA indicates the line was not a parseable GGA sentence.
var ErrNotGGA = errors.New("nmea: not a GGA sentence")

// Fix holds the position decoded from a GGA sentence (decimal degrees).
type Fix struct {
	Lat     float64
	Lon     float64
	Quality int // GGA fix quality: 0=invalid, 1=GPS, 2=DGPS, 4=RTK fixed, 5=RTK float...
}

// ParseGGA decodes a $G?GGA sentence into a Fix. It accepts any talker ID
// (GPGGA, GNGGA, ...) and tolerates a trailing checksum.
func ParseGGA(line string) (Fix, error) {
	line = strings.TrimSpace(line)
	if len(line) < 6 || line[0] != '$' {
		return Fix{}, ErrNotGGA
	}
	// Strip checksum if present.
	if i := strings.IndexByte(line, '*'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Split(line, ",")
	if len(fields) < 7 {
		return Fix{}, ErrNotGGA
	}
	// fields[0] = $xxGGA
	if !strings.HasSuffix(fields[0], "GGA") {
		return Fix{}, ErrNotGGA
	}

	lat, err := parseCoord(fields[2], fields[3], true)
	if err != nil {
		return Fix{}, err
	}
	lon, err := parseCoord(fields[4], fields[5], false)
	if err != nil {
		return Fix{}, err
	}
	quality, _ := strconv.Atoi(strings.TrimSpace(fields[6]))

	return Fix{Lat: lat, Lon: lon, Quality: quality}, nil
}

// parseCoord converts NMEA ddmm.mmmm / dddmm.mmmm plus hemisphere to decimal
// degrees. isLat selects the 2-digit (lat) vs 3-digit (lon) degree field.
func parseCoord(value, hemi string, isLat bool) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, ErrNotGGA
	}
	degDigits := 3
	if isLat {
		degDigits = 2
	}
	if len(value) < degDigits+1 {
		return 0, ErrNotGGA
	}
	deg, err := strconv.ParseFloat(value[:degDigits], 64)
	if err != nil {
		return 0, ErrNotGGA
	}
	min, err := strconv.ParseFloat(value[degDigits:], 64)
	if err != nil {
		return 0, ErrNotGGA
	}
	d := deg + min/60.0
	switch strings.ToUpper(strings.TrimSpace(hemi)) {
	case "S", "W":
		d = -d
	}
	return d, nil
}
