package server

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/symysak/ntrip-caster/internal/config"
)

// basicCreds extracts username/password from an Authorization: Basic header.
func basicCreds(r *http.Request) (user, pass string, ok bool) {
	return parseBasic(r.Header.Get("Authorization"))
}

func parseBasic(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(raw), ":")
	if !ok {
		return "", "", false
	}
	return user, pass, true
}

// authClient validates a rover credential and its access to the named stream.
func authClient(cfg *config.Config, r *http.Request, stream string) bool {
	user, pass, ok := basicCreds(r)
	if !ok {
		return false
	}
	u, found := cfg.LookupClientUser(user)
	if !found {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(pass), []byte(u.Password)) != 1 {
		return false
	}
	return u.Allowed(stream)
}

// authSourcePassword validates an NTRIP-server push password against a
// mountpoint's configured password (used by both v1 SOURCE and v2 POST).
func authSourcePassword(mp config.Mountpoint, pass string) bool {
	if mp.Password == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pass), []byte(mp.Password)) == 1
}
