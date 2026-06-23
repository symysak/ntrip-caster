// Package sourcetable renders the NTRIP sourcetable (STR/CAS records) from the
// current configuration.
package sourcetable

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/symysak/ntrip-caster/internal/config"
)

// Build renders the sourcetable body (without HTTP headers and without the
// trailing "ENDSOURCETABLE" terminator, which the server adds). Each STR/CAS
// record is CRLF-terminated.
//
// onlineOnly, when true, emits only mountpoints that currently have a source.
func Build(cfg *config.Config, online func(string) bool, onlineOnly bool) string {
	var b strings.Builder

	// CAS record for this caster.
	writeRecord(&b, []string{
		"CAS",
		cfg.Caster.Host,
		formatInt(cfg.Caster.Port),
		cfg.Caster.Identifier,
		cfg.Caster.Operator,
		formatNMEAFlag(cfg.Caster.NMEA),
		cfg.Caster.Country,
		formatCoord(cfg.Caster.Lat),
		formatCoord(cfg.Caster.Lon),
		"0.0.0.0", "0", "none",
	})

	// STR records for static mountpoints.
	for _, mp := range cfg.Mountpoints {
		if onlineOnly && !online(mp.Name) {
			continue
		}
		writeRecord(&b, strFields(mp.Name, mp.Identifier, mp.Format, mp.FormatDetails,
			mp.Carrier, mp.NavSystem, mp.Network, mp.Country, mp.Lat, mp.Lon,
			mp.NMEA, mp.Solution, mp.Generator, mp.Compression,
			mp.Authentication, mp.Fee, mp.Bitrate, mp.Misc))
	}

	// STR records for handover endpoints (advertised as NMEA-required streams).
	for _, h := range cfg.Handover {
		// A handover endpoint is "online" if any member is online.
		if onlineOnly && !slices.ContainsFunc(h.Members, online) {
			continue
		}
		writeRecord(&b, strFields(h.Name, h.Identifier, h.Format, h.FormatDetails,
			2, h.NavSystem, h.Network, h.Country, cfg.Caster.Lat, cfg.Caster.Lon,
			true, 0, "", "none", "B", "N", 0, "handover"))
	}

	return b.String()
}

func strFields(name, identifier, format, formatDetails string, carrier int,
	navSystem, network, country string, lat, lon float64, nmea bool, solution int,
	generator, compression, auth, fee string, bitrate int, misc string) []string {
	return []string{
		"STR",
		name,
		identifier,
		format,
		formatDetails,
		formatInt(carrier),
		navSystem,
		network,
		country,
		formatCoord(lat),
		formatCoord(lon),
		formatNMEAFlag(nmea),
		formatInt(solution),
		generator,
		compression,
		orDefault(auth, "B"),
		orDefault(fee, "N"),
		formatInt(bitrate),
		misc,
	}
}

func writeRecord(b *strings.Builder, fields []string) {
	b.WriteString(strings.Join(fields, ";"))
	b.WriteString("\r\n")
}

// formatInt renders an integer sourcetable field.
func formatInt(i int) string { return strconv.Itoa(i) }

// formatCoord renders a latitude/longitude field to four decimal places, as
// expected in STR/CAS records.
func formatCoord(deg float64) string { return fmt.Sprintf("%.4f", deg) }

// formatNMEAFlag renders the NMEA capability flag ("1" if NMEA is accepted).
func formatNMEAFlag(accepted bool) string {
	if accepted {
		return "1"
	}
	return "0"
}

// orDefault returns value, or fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
