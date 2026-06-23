// Package caster holds the runtime state of the NTRIP caster: the live
// mountpoints, their connected source (NTRIP server), and the subscribed
// clients. It owns the fan-out of RTCM bytes from each source to its readers.
package caster

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/symysak/ntrip-caster/internal/config"
)

// subBuffer is the per-subscriber queue depth (in chunks). A client that falls
// this far behind is disconnected rather than served a corrupted stream.
const subBuffer = 512

// Manager owns all runtime state and the current configuration snapshot.
type Manager struct {
	log *slog.Logger

	cfg atomic.Pointer[config.Config]

	mu          sync.RWMutex
	mountpoints map[string]*Mountpoint
}

// New creates a Manager seeded with the given configuration.
func New(cfg *config.Config, log *slog.Logger) *Manager {
	m := &Manager{
		log:         log,
		mountpoints: make(map[string]*Mountpoint),
	}
	m.cfg.Store(cfg)
	return m
}

// Config returns the current configuration snapshot. The returned value must
// be treated as read-only.
func (m *Manager) Config() *config.Config { return m.cfg.Load() }

// Reload swaps in a new configuration snapshot. Live source and subscriber
// connections are unaffected; the new metadata, users, and handover groups
// take effect for subsequent operations. Mountpoints removed from the config
// keep serving until their source disconnects.
func (m *Manager) Reload(cfg *config.Config) {
	m.cfg.Store(cfg)
	m.log.Info("configuration reloaded",
		"mountpoints", len(cfg.Mountpoints),
		"client_users", len(cfg.ClientUsers),
		"handover", len(cfg.Handover))
}

// GetOrCreate returns the runtime Mountpoint for name, creating it if absent.
func (m *Manager) GetOrCreate(name string) *Mountpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	mp, ok := m.mountpoints[name]
	if !ok {
		mp = &Mountpoint{
			Name: name,
			subs: make(map[*Subscriber]struct{}),
		}
		m.mountpoints[name] = mp
	}
	return mp
}

// Mountpoint returns the runtime mountpoint for name, or nil if it has never
// been instantiated (i.e. no source has ever connected).
func (m *Manager) Mountpoint(name string) *Mountpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mountpoints[name]
}

// Online reports whether the named mountpoint currently has an active source.
func (m *Manager) Online(name string) bool {
	mp := m.Mountpoint(name)
	return mp != nil && mp.HasSource()
}

// Mountpoint is the runtime hub for a single stream.
type Mountpoint struct {
	Name string

	mu     sync.RWMutex
	source *Source
	subs   map[*Subscriber]struct{}
}

// Source is a connected NTRIP server pushing data into a mountpoint.
type Source struct {
	RemoteAddr string
	Agent      string
	ConnectAt  time.Time
}

// AttachSource registers src as the active source. It returns false if the
// mountpoint already has a connected source (mountpoint in use).
func (mp *Mountpoint) AttachSource(src *Source) bool {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.source != nil {
		return false
	}
	mp.source = src
	return true
}

// DetachSource removes the active source and disconnects all subscribers,
// since the stream has ended.
func (mp *Mountpoint) DetachSource(src *Source) {
	mp.mu.Lock()
	if mp.source != src {
		mp.mu.Unlock()
		return
	}
	mp.source = nil
	subs := make([]*Subscriber, 0, len(mp.subs))
	for s := range mp.subs {
		subs = append(subs, s)
	}
	mp.subs = make(map[*Subscriber]struct{})
	mp.mu.Unlock()

	for _, s := range subs {
		s.drop()
	}
}

// HasSource reports whether a source is currently connected.
func (mp *Mountpoint) HasSource() bool {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	return mp.source != nil
}

// SubscriberCount returns the number of currently subscribed clients.
func (mp *Mountpoint) SubscriberCount() int {
	mp.mu.RLock()
	defer mp.mu.RUnlock()
	return len(mp.subs)
}

// Broadcast fans a chunk of stream bytes out to every subscriber. The chunk is
// shared read-only with all subscribers, so the caller must neither mutate nor
// reuse data after the call. Subscribers whose queue is full are dropped.
func (mp *Mountpoint) Broadcast(data []byte) {
	mp.mu.Lock()
	var slow []*Subscriber
	for s := range mp.subs {
		select {
		case s.ch <- data:
		default:
			slow = append(slow, s)
			delete(mp.subs, s)
		}
	}
	mp.mu.Unlock()

	for _, s := range slow {
		s.drop()
	}
}

// Subscriber is a connected client reading a stream. Its channel may be moved
// between mountpoints during handover; the channel itself never changes.
type Subscriber struct {
	ch      chan []byte
	done    chan struct{}
	dropped atomic.Bool
	Addr    string
}

// NewSubscriber creates an unattached subscriber.
func NewSubscriber(addr string) *Subscriber {
	return &Subscriber{
		ch:   make(chan []byte, subBuffer),
		done: make(chan struct{}),
		Addr: addr,
	}
}

// Chunks returns the channel of stream bytes to write to the client.
func (s *Subscriber) Chunks() <-chan []byte { return s.ch }

// Done is closed when the subscriber is dropped (slow client or source gone).
func (s *Subscriber) Done() <-chan struct{} { return s.done }

func (s *Subscriber) drop() {
	if s.dropped.CompareAndSwap(false, true) {
		close(s.done)
	}
}

// Close drops the subscriber (e.g. when the client connection is gone). It is
// safe to call multiple times.
func (s *Subscriber) Close() { s.drop() }

// Subscribe attaches s to the mountpoint. Returns false if no source is active.
func (mp *Mountpoint) Subscribe(s *Subscriber) bool {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.source == nil {
		return false
	}
	mp.subs[s] = struct{}{}
	return true
}

// Unsubscribe detaches s from the mountpoint (used during handover switching
// and on client disconnect).
func (mp *Mountpoint) Unsubscribe(s *Subscriber) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	delete(mp.subs, s)
}
