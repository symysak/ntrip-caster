// Package handover implements nearest-base-station selection for handover
// endpoints. Given a client position (from NMEA GGA) and a group's member
// mountpoints, it picks the closest online member.
package handover

import (
	"math"

	"github.com/symysak/ntrip-caster/internal/config"
)

// OnlineFunc reports whether a mountpoint currently has an active source.
type OnlineFunc func(name string) bool

// Selector resolves the nearest member mountpoint for a handover group.
type Selector struct {
	cfg    *config.Config
	online OnlineFunc
}

// NewSelector builds a selector over the given config snapshot.
func NewSelector(cfg *config.Config, online OnlineFunc) *Selector {
	return &Selector{cfg: cfg, online: online}
}

// Nearest returns the name of the closest online member of group to (lat, lon).
// It returns "" if the group has no online member with a known position.
func (s *Selector) Nearest(group config.HandoverGroup, lat, lon float64) string {
	best := ""
	bestDist := math.MaxFloat64
	for _, name := range group.Members {
		mp, ok := s.cfg.LookupMountpoint(name)
		if !ok {
			continue
		}
		if !s.online(name) {
			continue
		}
		if mp.Lat == 0 && mp.Lon == 0 {
			// No position metadata: cannot rank, skip.
			continue
		}
		d := HaversineKm(lat, lon, mp.Lat, mp.Lon)
		if d < bestDist {
			bestDist = d
			best = name
		}
	}
	return best
}

// HaversineKm returns the great-circle distance between two WGS84 points in
// kilometers.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0088
	rlat1 := lat1 * math.Pi / 180
	rlat2 := lat2 * math.Pi / 180
	dlat := (lat2 - lat1) * math.Pi / 180
	dlon := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(rlat1)*math.Cos(rlat2)*math.Sin(dlon/2)*math.Sin(dlon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}
