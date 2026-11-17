// SPDX-License-Identifier: GPL-3.0-or-later

// Package logring keeps the most recent log records for the status page.
package logring

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one kept record.
type Entry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   string // key=value pairs
}

// Ring stores the last records of at least MinLevel.
type Ring struct {
	MinLevel slog.Level

	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool
}

// New returns a ring of size records.
func New(size int, minLevel slog.Level) *Ring {
	return &Ring{MinLevel: minLevel, entries: make([]Entry, size)}
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
}

// Entries returns the kept records, newest first.
func (r *Ring) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = len(r.entries)
	}
	out := make([]Entry, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, r.entries[(r.next-i+len(r.entries))%len(r.entries)])
	}
	return out
}

// Handler passes every record to next and keeps a copy in the ring.
func (r *Ring) Handler(next slog.Handler) slog.Handler { return &handler{ring: r, next: next} }

type handler struct {
	ring   *Ring
	next   slog.Handler
	attrs  []slog.Attr
	groups string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.ring.MinLevel || h.next.Enabled(ctx, l)
}

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level >= h.ring.MinLevel {
		var b strings.Builder
		write := func(key string, v slog.Value) {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(key + "=" + v.String())
		}
		for _, a := range h.attrs { // already prefixed with their groups
			write(a.Key, a.Value)
		}
		rec.Attrs(func(a slog.Attr) bool { write(h.groups+a.Key, a.Value); return true })
		h.ring.add(Entry{Time: rec.Time, Level: rec.Level, Message: rec.Message, Attrs: b.String()})
	}
	if h.next.Enabled(ctx, rec.Level) {
		return h.next.Handle(ctx, rec)
	}
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(attrs)
	for _, a := range attrs {
		a.Key = h.groups + a.Key
		c.attrs = append(append([]slog.Attr(nil), c.attrs...), a)
	}
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	c := *h
	c.next = h.next.WithGroup(name)
	c.groups = h.groups + name + "."
	return &c
}
