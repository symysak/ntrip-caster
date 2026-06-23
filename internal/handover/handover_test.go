package handover

import (
	"math"
	"testing"

	"github.com/symysak/ntrip-caster/internal/config"
)

func TestNearest(t *testing.T) {
	cfg := &config.Config{
		Mountpoints: []config.Mountpoint{
			{Name: "TOKYO", Lat: 35.6586, Lon: 139.7454},
			{Name: "OSAKA", Lat: 34.6937, Lon: 135.5023},
		},
	}
	group := config.HandoverGroup{Name: "AUTO", Members: []string{"TOKYO", "OSAKA"}}

	allOnline := func(string) bool { return true }
	sel := NewSelector(cfg, allOnline)

	// A point in Yokohama is nearest Tokyo.
	if got := sel.Nearest(group, 35.4437, 139.6380); got != "TOKYO" {
		t.Errorf("near Yokohama: got %q, want TOKYO", got)
	}
	// A point in Kobe is nearest Osaka.
	if got := sel.Nearest(group, 34.6901, 135.1955); got != "OSAKA" {
		t.Errorf("near Kobe: got %q, want OSAKA", got)
	}

	// When Tokyo is offline, Yokohama falls back to Osaka.
	onlyOsaka := func(name string) bool { return name == "OSAKA" }
	sel2 := NewSelector(cfg, onlyOsaka)
	if got := sel2.Nearest(group, 35.4437, 139.6380); got != "OSAKA" {
		t.Errorf("Tokyo offline: got %q, want OSAKA", got)
	}

	// No online member yields "".
	none := func(string) bool { return false }
	if got := NewSelector(cfg, none).Nearest(group, 35, 139); got != "" {
		t.Errorf("none online: got %q, want empty", got)
	}
}

func TestSelectHysteresis(t *testing.T) {
	cfg := &config.Config{
		Mountpoints: []config.Mountpoint{
			{Name: "TOKYO", Lat: 35.6586, Lon: 139.7454},
			{Name: "OSAKA", Lat: 34.6937, Lon: 135.5023},
		},
	}
	group := config.HandoverGroup{Name: "AUTO", Members: []string{"TOKYO", "OSAKA"}}
	allOnline := func(string) bool { return true }
	sel := NewSelector(cfg, allOnline)

	// A point clearly nearer OSAKA than TOKYO.
	lat, lon := 34.6901, 135.1955 // Kobe
	dTokyo := HaversineKm(lat, lon, 35.6586, 139.7454)
	dOsaka := HaversineKm(lat, lon, 34.6937, 135.5023)
	diff := dTokyo - dOsaka // how much closer OSAKA is (positive)

	// Currently on TOKYO. With a margin larger than the gap, stay on TOKYO.
	if got := sel.Select(group, lat, lon, "TOKYO", diff+10); got != "TOKYO" {
		t.Errorf("within margin: got %q, want TOKYO (stay)", got)
	}
	// With a margin smaller than the gap, switch to the closer OSAKA.
	if got := sel.Select(group, lat, lon, "TOKYO", diff-10); got != "OSAKA" {
		t.Errorf("beyond margin: got %q, want OSAKA (switch)", got)
	}
	// No current member: pick the nearest regardless of margin.
	if got := sel.Select(group, lat, lon, "", 1e9); got != "OSAKA" {
		t.Errorf("no current: got %q, want OSAKA", got)
	}

	// Failover: current member offline -> nearest online, margin ignored.
	onlyOsaka := func(name string) bool { return name == "OSAKA" }
	if got := NewSelector(cfg, onlyOsaka).Select(group, lat, lon, "TOKYO", 1e9); got != "OSAKA" {
		t.Errorf("current offline: got %q, want OSAKA (failover)", got)
	}
}

func TestHaversineKm(t *testing.T) {
	// Tokyo to Osaka is roughly 400 km.
	d := HaversineKm(35.6586, 139.7454, 34.6937, 135.5023)
	if math.Abs(d-400) > 30 {
		t.Errorf("Tokyo-Osaka = %.1f km, want ~400", d)
	}
}
