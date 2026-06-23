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
		writeRecord(&b, mountpointRecord(mp).fields())
	}

	// STR records for handover endpoints (advertised as NMEA-required streams).
	for _, h := range cfg.Handover {
		// A handover endpoint is "online" if any member is online.
		if onlineOnly && !slices.ContainsFunc(h.Members, online) {
			continue
		}
		writeRecord(&b, handoverRecord(h, cfg.Caster.Lat, cfg.Caster.Lon).fields())
	}

	return b.String()
}

// strRecord is one sourcetable "STR" entry. Named fields make the field order
// explicit at every construction site, instead of a long positional argument
// list.
type strRecord struct {
	Mountpoint     string
	Identifier     string
	Format         string
	FormatDetails  string
	Carrier        int
	NavSystem      string
	Network        string
	Country        string
	Lat            float64
	Lon            float64
	NMEA           bool
	Solution       int
	Generator      string
	Compression    string
	Authentication string // defaults to "B"
	Fee            string // defaults to "N"
	Bitrate        int
	Misc           string
}

// fields renders the record into the ordered, semicolon-joined STR columns.
func (r strRecord) fields() []string {
	return []string{
		"STR",
		r.Mountpoint,
		r.Identifier,
		r.Format,
		r.FormatDetails,
		formatInt(r.Carrier),
		r.NavSystem,
		r.Network,
		r.Country,
		formatCoord(r.Lat),
		formatCoord(r.Lon),
		formatNMEAFlag(r.NMEA),
		formatInt(r.Solution),
		r.Generator,
		r.Compression,
		orDefault(r.Authentication, "B"),
		orDefault(r.Fee, "N"),
		formatInt(r.Bitrate),
		r.Misc,
	}
}

// mountpointRecord builds the STR entry for a configured mountpoint.
func mountpointRecord(mp config.Mountpoint) strRecord {
	return strRecord{
		Mountpoint:     mp.Name,
		Identifier:     mp.Identifier,
		Format:         mp.Format,
		FormatDetails:  mp.FormatDetails,
		Carrier:        mp.Carrier,
		NavSystem:      mp.NavSystem,
		Network:        mp.Network,
		Country:        mp.Country,
		Lat:            mp.Lat,
		Lon:            mp.Lon,
		NMEA:           mp.NMEA,
		Solution:       mp.Solution,
		Generator:      mp.Generator,
		Compression:    mp.Compression,
		Authentication: authField(mp.Open, mp.Authentication),
		Fee:            mp.Fee,
		Bitrate:        mp.Bitrate,
		Misc:           mp.Misc,
	}
}

// handoverRecord builds the STR entry for a handover endpoint. It advertises
// the endpoint at the caster's own position and requires NMEA from clients.
func handoverRecord(h config.HandoverGroup, lat, lon float64) strRecord {
	return strRecord{
		Mountpoint:     h.Name,
		Identifier:     h.Identifier,
		Format:         h.Format,
		FormatDetails:  h.FormatDetails,
		Carrier:        2,
		NavSystem:      h.NavSystem,
		Network:        h.Network,
		Country:        h.Country,
		Lat:            lat,
		Lon:            lon,
		NMEA:           true,
		Compression:    "none",
		Authentication: authField(h.Open, ""),
		Misc:           "handover",
	}
}

// authField reports the STR authentication column: "N" (none) for open streams,
// otherwise the configured value (which fields() defaults to "B").
func authField(open bool, configured string) string {
	if open {
		return "N"
	}
	return configured
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
