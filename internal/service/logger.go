package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type infoEntry struct {
	at   time.Time
	line string
}

type infoWindowWriter struct {
	mu      sync.Mutex
	path    string
	window  time.Duration
	entries []infoEntry
}

func (w *infoWindowWriter) append(at time.Time, line string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, infoEntry{at: at, line: line})
	cutoff := at.Add(-w.window)
	kept := w.entries[:0]
	for _, e := range w.entries {
		if e.at.After(cutoff) {
			kept = append(kept, e)
		}
	}
	w.entries = kept
	f, err := os.Create(w.path) // truncate on each rewrite
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range w.entries {
		if _, err := f.WriteString(e.line); err != nil {
			return err
		}
	}
	return nil
}

type splitFileHandler struct {
	level  slog.Level
	stdout slog.Handler
	debug  slog.Handler
	errors slog.Handler
	infoW  *infoWindowWriter
	attrs  []slog.Attr
	groups []string
}

func (h *splitFileHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func renderRecord(r slog.Record, attrs []slog.Attr, groups []string, level slog.Level) (string, error) {
	var b bytes.Buffer
	base := slog.NewTextHandler(&b, &slog.HandlerOptions{Level: level})
	h := slog.Handler(base)
	if len(attrs) > 0 {
		h = h.WithAttrs(attrs)
	}
	for _, g := range groups {
		h = h.WithGroup(g)
	}
	if err := h.Handle(context.Background(), r); err != nil {
		return "", err
	}
	return b.String(), nil
}

func (h *splitFileHandler) Handle(ctx context.Context, r slog.Record) error {
	rc := r.Clone()
	if h.stdout != nil {
		oh := h.stdout
		if len(h.attrs) > 0 {
			oh = oh.WithAttrs(h.attrs)
		}
		for _, g := range h.groups {
			oh = oh.WithGroup(g)
		}
		if err := oh.Handle(ctx, rc); err != nil {
			return err
		}
	}

	switch {
	case rc.Level >= slog.LevelError:
		if h.errors != nil {
			eh := h.errors
			if len(h.attrs) > 0 {
				eh = eh.WithAttrs(h.attrs)
			}
			for _, g := range h.groups {
				eh = eh.WithGroup(g)
			}
			if err := eh.Handle(ctx, rc); err != nil {
				return err
			}
		}
	case rc.Level == slog.LevelDebug:
		if h.debug != nil {
			dh := h.debug
			if len(h.attrs) > 0 {
				dh = dh.WithAttrs(h.attrs)
			}
			for _, g := range h.groups {
				dh = dh.WithGroup(g)
			}
			if err := dh.Handle(ctx, rc); err != nil {
				return err
			}
		}
	case rc.Level == slog.LevelInfo || rc.Level == slog.LevelWarn:
		line, err := renderRecord(rc, h.attrs, h.groups, h.level)
		if err != nil {
			return err
		}
		if h.infoW != nil {
			return h.infoW.append(time.Now(), line)
		}
	}
	return nil
}

func (h *splitFileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	next = append(next, h.attrs...)
	next = append(next, attrs...)
	return &splitFileHandler{
		level:  h.level,
		stdout: h.stdout,
		debug:  h.debug,
		errors: h.errors,
		infoW:  h.infoW,
		attrs:  next,
		groups: h.groups,
	}
}

func (h *splitFileHandler) WithGroup(name string) slog.Handler {
	nextGroups := make([]string, 0, len(h.groups)+1)
	nextGroups = append(nextGroups, h.groups...)
	nextGroups = append(nextGroups, name)
	return &splitFileHandler{
		level:  h.level,
		stdout: h.stdout,
		debug:  h.debug,
		errors: h.errors,
		infoW:  h.infoW,
		attrs:  h.attrs,
		groups: nextGroups,
	}
}

func NewLogger(level, logDir string) (*slog.Logger, func() error, error) {
	lvl := parseLevel(level)
	if strings.TrimSpace(logDir) == "" {
		return nil, nil, fmt.Errorf("empty log dir path")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, err
	}
	debugPath := filepath.Join(logDir, "debug.log")
	errorPath := filepath.Join(logDir, "errors.log")
	infoPath := filepath.Join(logDir, "info.log")

	// Truncate each run.
	df, err := os.Create(debugPath)
	if err != nil {
		return nil, nil, err
	}
	ef, err := os.Create(errorPath)
	if err != nil {
		_ = df.Close()
		return nil, nil, err
	}
	// create/truncate info file
	if f, err := os.Create(infoPath); err == nil {
		_ = f.Close()
	} else {
		_ = df.Close()
		_ = ef.Close()
		return nil, nil, err
	}

	debugH := slog.NewTextHandler(df, &slog.HandlerOptions{Level: lvl})
	errorH := slog.NewTextHandler(ef, &slog.HandlerOptions{Level: lvl})
	stdoutH := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	split := &splitFileHandler{
		level:  lvl,
		stdout: stdoutH,
		debug:  debugH,
		errors: errorH,
		infoW: &infoWindowWriter{
			path:   infoPath,
			window: 10 * time.Minute,
		},
	}
	logger := slog.New(split)
	closer := func() error {
		err1 := df.Close()
		err2 := ef.Close()
		if err1 != nil {
			return err1
		}
		return err2
	}
	return logger, closer, nil
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "info", "":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
