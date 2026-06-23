# CLAUDE.md

Guidance for working in this repository.

## What this is

An NTRIP (v1/v2) caster in Go. NTRIP servers (base stations) **push** RTCM into
mountpoints; NTRIP clients (rovers) **pull** streams. Handover endpoints route a
client to the nearest base station based on the client's NMEA GGA position.

## Commands

```sh
go build ./...                                  # build
go build -o ntrip-caster ./cmd/ntrip-caster     # build binary
go test ./...                                   # unit + integration tests
go test -race ./...                             # run with race detector (keep it clean)
go vet ./... && gofmt -l .                      # must be clean before committing
./ntrip-caster -config config.yaml -check       # validate a config and exit
```

## Layout

| Path                     | Responsibility                                              |
| ------------------------ | ----------------------------------------------------------- |
| `cmd/ntrip-caster`       | main: flags, logger, listener, SIGHUP reload, shutdown.     |
| `internal/config`        | YAML load/validate; the hot-reload snapshot type.           |
| `internal/caster`        | Runtime hub: `Manager`, `Mountpoint`, `Subscriber`, fan-out.|
| `internal/server`        | NTRIP v1/v2 wire protocol, auth, dispatch, handover.        |
| `internal/handover`      | Nearest-base selection (haversine) from a GGA fix.          |
| `internal/nmea`          | NMEA `GGA` parsing.                                         |
| `internal/sourcetable`   | Sourcetable (CAS/STR) rendering.                            |

## How a connection is handled (`internal/server`)

`server.go:handleConn` reads the first request bytes off a raw TCP conn and
branches — there is no `net/http` server, because NTRIP v1 uses non-HTTP framing
(`SOURCE …` request, `ICY 200 OK` response).

- `SOURCE …`  → `source.go:handleSourceV1` (v1 push)
- HTTP `POST`  → `source.go:handleSourceV2` (v2 push, Basic auth)
- HTTP `GET`   → `client.go:handleClient` (rover pull / sourcetable / handover)

**Version detection** (`versionOf`): a `GET` is v2 only when the request carries
`Ntrip-Version: Ntrip/2.0` (case-insensitive); otherwise v1. Push direction is
decided purely by verb: `SOURCE`=v1, `POST`=v2 — `versionOf` is not consulted there.

Response framing lives in `response.go` (v1 `ICY`/`SOURCETABLE`, v2 HTTP + chunked).

## Key design decisions

- **One source per mountpoint.** A second `SOURCE`/`POST` to a live mountpoint is
  rejected (409 / `ERROR`). `Mountpoint.AttachSource` enforces this.
- **Fan-out.** The source read loop copies each chunk and calls
  `Mountpoint.Broadcast`, which non-blocking-sends to every subscriber's buffered
  channel. A subscriber whose buffer (`subBuffer`, 512 chunks) overflows is
  **disconnected**, not served corrupted bytes.
- **Handover.** `client.go:handleHandover` keeps one `Subscriber` and moves it
  between member mountpoints (`Unsubscribe` old → `Subscribe` new) as new GGA
  fixes arrive; the client connection and channel stay constant. Selection re-reads
  the live config each fix so reloads take effect. If the active base's source
  drops, the subscriber is dropped (client must reconnect) — known limitation.
- **Hot reload.** Config is an `atomic.Pointer[config.Config]` in `caster.Manager`.
  SIGHUP reloads and swaps it; live connections are untouched. `listen` changes
  are ignored on reload (restart required) — logged as a warning. Reloadable:
  `client_users`, `mountpoints`, `handover`.
- **Auth.** Both clients and pushing servers authenticate. Passwords are plaintext
  in YAML (by chosen design) and compared with `crypto/subtle`. Client users have
  per-stream access control (`Mountpoints`, `"*"` = all). Source push auth is the
  per-mountpoint `password` (v1 SOURCE password / v2 POST Basic password).

## Conventions

- stdlib `log/slog` to **stderr**; systemd captures it into journald. Connection
  logs carry `remote` (IP:port) and `agent` (User-Agent).
- YAML via `github.com/goccy/go-yaml` (not gopkg.in). Config decoding uses
  `DisallowUnknownField()` — unknown keys are errors.
- Keep `go test -race ./...` green; the server is concurrency-heavy.
- Commit messages end with the `Co-Authored-By` trailer used in this repo's history.

## Not yet implemented

NTRIP v2 over TLS (terminate at a reverse proxy), automatic re-route of a handover
client when its base drops, metrics/admin endpoint.
