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
	host := cfg.Caster.Host
	port := cfg.Caster.Port
	writeRecord(&b, []string{
		"CAS",
		host,
		itoa(port),
		nz(cfg.Caster.Identifier),
		nz(cfg.Caster.Operator),
		boolField(cfg.Caster.NMEA),
		nz(cfg.Caster.Country),
		ftoa(cfg.Caster.Lat),
		ftoa(cfg.Caster.Lon),
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
		writeRecord(&b, strFields(h.Name, nz(h.Identifier), nz(h.Format), nz(h.FormatDetails),
			2, nz(h.NavSystem), nz(h.Network), nz(h.Country), cfg.Caster.Lat, cfg.Caster.Lon,
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
		nz(identifier),
		nz(format),
		nz(formatDetails),
		itoa(carrier),
		nz(navSystem),
		nz(network),
		nz(country),
		ftoa(lat),
		ftoa(lon),
		boolField(nmea),
		itoa(solution),
		nz(generator),
		nz(compression),
		def(auth, "B"),
		def(fee, "N"),
		itoa(bitrate),
		nz(misc),
	}
}

func writeRecord(b *strings.Builder, fields []string) {
	b.WriteString(strings.Join(fields, ";"))
	b.WriteString("\r\n")
}

func itoa(i int) string { return strconv.Itoa(i) }
func ftoa(f float64) string {
	return fmt.Sprintf("%.4f", f)
}
func boolField(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func nz(s string) string { return s }
func def(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
