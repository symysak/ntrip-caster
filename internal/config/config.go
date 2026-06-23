// Package config loads and validates the YAML configuration for the caster.
//
// The configuration is split into two conceptual halves:
//   - Bootstrap settings (listen address, server identity) that require a
//     restart to change.
//   - Hot-reloadable settings (client users, mountpoint definitions, base
//     station metadata, handover groups) that are applied on SIGHUP.
package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Config is the top-level configuration document.
type Config struct {
	// Listen is the TCP address the caster binds to, e.g. ":2101".
	// Changing it requires a restart.
	Listen string `yaml:"listen"`

	// Caster holds the operator/identity metadata advertised in the
	// sourcetable CAS entry.
	Caster CasterInfo `yaml:"caster"`

	// ClientUsers are credentials for NTRIP clients (rovers) pulling data.
	ClientUsers []ClientUser `yaml:"client_users"`

	// Mountpoints are the streams that NTRIP servers push into.
	Mountpoints []Mountpoint `yaml:"mountpoints"`

	// Handover defines virtual endpoints that switch a client to the
	// nearest member mountpoint based on the client's NMEA GGA position.
	Handover []HandoverGroup `yaml:"handover"`
}

// CasterInfo is advertised in the sourcetable "CAS" record.
type CasterInfo struct {
	Identifier string  `yaml:"identifier"`
	Operator   string  `yaml:"operator"`
	Country    string  `yaml:"country"`
	Lat        float64 `yaml:"lat"`
	Lon        float64 `yaml:"lon"`
	// NMEA reports whether the caster accepts client NMEA (0/1 in CAS).
	NMEA bool `yaml:"nmea"`
	// Fallback host/port advertised in the CAS record (optional).
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// ClientUser is a rover credential.
type ClientUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Mountpoints lists the mountpoint/handover names this user may read.
	// "*" grants access to all.
	Mountpoints []string `yaml:"mountpoints"`
}

// Allowed reports whether the user may access the named mountpoint.
func (u ClientUser) Allowed(name string) bool {
	for _, m := range u.Mountpoints {
		if m == "*" || m == name {
			return true
		}
	}
	return false
}

// Mountpoint describes a single stream and its sourcetable "STR" metadata.
type Mountpoint struct {
	Name string `yaml:"name"`
	// Password authenticates the NTRIP server pushing to this mountpoint
	// (used for both v1 SOURCE and v2 POST). Stored in plaintext.
	Password string `yaml:"password"`
	// Open allows clients to read this mountpoint without authentication.
	// Server (push) authentication is unaffected. Defaults to false.
	Open bool `yaml:"open"`

	// Sourcetable STR fields.
	Identifier    string  `yaml:"identifier"`
	Format        string  `yaml:"format"`
	FormatDetails string  `yaml:"format_details"`
	Carrier       int     `yaml:"carrier"`
	NavSystem     string  `yaml:"nav_system"`
	Network       string  `yaml:"network"`
	Country       string  `yaml:"country"`
	Lat           float64 `yaml:"lat"`
	Lon           float64 `yaml:"lon"`
	NMEA          bool    `yaml:"nmea"`
	Solution      int     `yaml:"solution"`
	Generator     string  `yaml:"generator"`
	Compression   string  `yaml:"compression"`
	// Authentication: "N" none, "B" basic, "D" digest. Defaults to "B".
	Authentication string `yaml:"authentication"`
	Fee            string `yaml:"fee"`
	Bitrate        int    `yaml:"bitrate"`
	Misc           string `yaml:"misc"`
}

// HandoverGroup is a virtual mountpoint that routes a client to the nearest
// member mountpoint based on the client's reported GGA position.
type HandoverGroup struct {
	Name string `yaml:"name"`
	// Members are mountpoint names eligible for selection.
	Members []string `yaml:"members"`
	// Open allows clients to use this handover endpoint without authentication.
	// Defaults to false.
	Open bool `yaml:"open"`
	// SwitchMarginKm is the hysteresis margin (km) that suppresses flapping
	// near a boundary: once attached, a client switches to another member only
	// if that member is closer than the current one by more than this margin.
	// 0 (default) means always use the strictly nearest member. A source
	// disconnect bypasses the margin (immediate failover).
	SwitchMarginKm float64 `yaml:"switch_margin_km"`

	// Sourcetable STR metadata advertised for the handover endpoint itself.
	Identifier    string `yaml:"identifier"`
	Format        string `yaml:"format"`
	FormatDetails string `yaml:"format_details"`
	NavSystem     string `yaml:"nav_system"`
	Network       string `yaml:"network"`
	Country       string `yaml:"country"`
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data), yaml.DisallowUnknownField())
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":2101"
	}
	for i := range c.Mountpoints {
		if c.Mountpoints[i].Authentication == "" {
			c.Mountpoints[i].Authentication = "B"
		}
		if c.Mountpoints[i].Fee == "" {
			c.Mountpoints[i].Fee = "N"
		}
	}
}

// Validate checks for structural errors and name collisions.
func (c *Config) Validate() error {
	names := map[string]string{} // name -> kind
	for _, mp := range c.Mountpoints {
		if mp.Name == "" {
			return fmt.Errorf("mountpoint with empty name")
		}
		if prev, ok := names[mp.Name]; ok {
			return fmt.Errorf("duplicate name %q (already used by %s)", mp.Name, prev)
		}
		names[mp.Name] = "mountpoint"
	}
	for _, h := range c.Handover {
		if h.Name == "" {
			return fmt.Errorf("handover group with empty name")
		}
		if prev, ok := names[h.Name]; ok {
			return fmt.Errorf("handover name %q collides with %s", h.Name, prev)
		}
		names[h.Name] = "handover"
		if len(h.Members) == 0 {
			return fmt.Errorf("handover %q has no members", h.Name)
		}
		for _, m := range h.Members {
			found := false
			for _, mp := range c.Mountpoints {
				if mp.Name == m {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("handover %q references unknown mountpoint %q", h.Name, m)
			}
		}
	}
	seenUser := map[string]bool{}
	for _, u := range c.ClientUsers {
		if u.Username == "" {
			return fmt.Errorf("client user with empty username")
		}
		if seenUser[u.Username] {
			return fmt.Errorf("duplicate client user %q", u.Username)
		}
		seenUser[u.Username] = true
	}
	return nil
}

// LookupMountpoint returns the mountpoint with the given name, or false.
func (c *Config) LookupMountpoint(name string) (Mountpoint, bool) {
	for _, mp := range c.Mountpoints {
		if mp.Name == name {
			return mp, true
		}
	}
	return Mountpoint{}, false
}

// LookupHandover returns the handover group with the given name, or false.
func (c *Config) LookupHandover(name string) (HandoverGroup, bool) {
	for _, h := range c.Handover {
		if h.Name == name {
			return h, true
		}
	}
	return HandoverGroup{}, false
}

// LookupClientUser returns the client user with the given username, or false.
func (c *Config) LookupClientUser(username string) (ClientUser, bool) {
	for _, u := range c.ClientUsers {
		if u.Username == username {
			return u, true
		}
	}
	return ClientUser{}, false
}
