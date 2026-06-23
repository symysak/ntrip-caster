package sourcetable

import (
	"strings"
	"testing"

	"github.com/symysak/ntrip-caster/internal/config"
)

func testCfg() *config.Config {
	return &config.Config{
		Caster: config.CasterInfo{
			Identifier: "Test", Operator: "Op", Country: "JPN",
			Host: "caster.example.jp", Port: 2101, Lat: 35.6812, Lon: 139.7671, NMEA: true,
		},
		Mountpoints: []config.Mountpoint{
			{Name: "TOKYO", Format: "RTCM 3.2", Carrier: 2, NavSystem: "GPS+GLO",
				Country: "JPN", Lat: 35.6586, Lon: 139.7454, Bitrate: 9600,
				Authentication: "B", Fee: "N"},
		},
		Handover: []config.HandoverGroup{
			{Name: "AUTO", Format: "RTCM 3.2", Country: "JPN", Members: []string{"TOKYO"}},
		},
	}
}

func TestBuildAllOnline(t *testing.T) {
	cfg := testCfg()
	out := Build(cfg, func(string) bool { return true }, false)
	lines := splitRecords(out)

	want := []string{
		"CAS;caster.example.jp;2101;Test;Op;1;JPN;35.6812;139.7671;0.0.0.0;0;none",
		"STR;TOKYO;;RTCM 3.2;;2;GPS+GLO;;JPN;35.6586;139.7454;0;0;;;B;N;9600;",
		"STR;AUTO;;RTCM 3.2;;2;;;JPN;35.6812;139.7671;1;0;;none;B;N;0;handover",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d records, want %d:\n%q", len(lines), len(want), lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("record %d:\n got  %q\n want %q", i, lines[i], w)
		}
	}
}

func TestBuildOnlineOnly(t *testing.T) {
	cfg := testCfg()
	// TOKYO offline -> neither the TOKYO STR nor the AUTO handover (its only
	// member) should appear; the CAS record always does.
	out := Build(cfg, func(string) bool { return false }, true)
	if strings.Contains(out, "STR;TOKYO;") {
		t.Errorf("offline TOKYO should be omitted:\n%s", out)
	}
	if strings.Contains(out, "STR;AUTO;") {
		t.Errorf("handover with no online member should be omitted:\n%s", out)
	}
	if !strings.HasPrefix(out, "CAS;") {
		t.Errorf("CAS record should always be present:\n%s", out)
	}
}

// splitRecords returns the CRLF-terminated records as trimmed lines.
func splitRecords(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\r\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
