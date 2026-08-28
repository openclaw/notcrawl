package main

import (
	"errors"
	"io"
	"log/slog"
	"time"
)

type syncTrace struct {
	logger  *slog.Logger
	started time.Time
	phase   string
}

func newSyncTrace(w io.Writer, enabled bool) *syncTrace {
	if !enabled {
		return nil
	}
	return &syncTrace{logger: progressLogger(w), started: time.Now()}
}

func (t *syncTrace) start(phase string) {
	if t == nil {
		return
	}
	t.phase = phase
	t.emit(phase, "started")
}

func (t *syncTrace) done(counts ...any) {
	if t == nil {
		return
	}
	t.emit(t.phase, "finished", counts...)
}

func (t *syncTrace) finish(err error) error {
	if t == nil {
		return err
	}
	if err != nil {
		t.emit(t.phase, "failed")
		// Upstream errors and warnings can contain URLs, identifiers, or content.
		// The normal CLI retains them; verbose diagnostics never format them.
		return errors.New("sync failed; verbose diagnostics omit private error details")
	}
	t.emit("sync", "finished")
	return nil
}

func (t *syncTrace) emit(phase, state string, counts ...any) {
	attrs := []any{"phase", phase, "state", state, "elapsed", time.Since(t.started).Round(time.Millisecond)}
	t.logger.Info("sync trace", append(attrs, counts...)...)
}

func (t *syncTrace) apiLogger() *slog.Logger {
	if t == nil {
		return nil
	}
	return t.logger
}
