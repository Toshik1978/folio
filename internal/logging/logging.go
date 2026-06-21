package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/mattn/go-isatty"
)

const (
	colorReset   = "\x1b[0m"
	colorRed     = "\x1b[31m"
	colorGreen   = "\x1b[32m"
	colorYellow  = "\x1b[33m"
	colorMagenta = "\x1b[35m"
	colorCyan    = "\x1b[36m"
	colorGray    = "\x1b[90m"
)

type customHandler struct {
	mu        *sync.Mutex
	out       io.Writer
	attrs     []slog.Attr
	groupPfx  string // dotted path of open groups, e.g. "req.db." (empty at root)
	level     slog.Level
	useColor  bool
	envPrefix string
}

func newCustomHandler(out io.Writer, noColor bool, env string) *customHandler {
	useColor := !noColor && shouldColor(out)
	level := slog.LevelInfo

	var envPrefix string
	switch env {
	case "development", "dev":
		envPrefix = paint(useColor, colorMagenta, "[DEV]") + " "
		level = slog.LevelDebug
	case "production", "prod":
		envPrefix = paint(useColor, colorGray, "[PROD]") + " "
	default:
		if env != "" {
			envPrefix = paint(useColor, colorGray, "["+env+"]") + " "
		}
	}

	return &customHandler{
		mu:        &sync.Mutex{},
		out:       out,
		level:     level,
		useColor:  useColor,
		envPrefix: envPrefix,
	}
}

// shouldColor reports whether ANSI color should be emitted to out. This keeps escape
// codes out of files, pipes and non-TTY container logs (e.g. `docker run` without -t),
// while still coloring an interactive terminal — including inside containers started with a TTY.
func shouldColor(out io.Writer) bool {
	f, ok := out.(interface{ Fd() uintptr })
	if !ok {
		return false
	}

	fd := f.Fd()

	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// paint wraps s in the given color when useColor is set, mirroring chi's cW
// helper but returning a string for use with the slog handler's formatting.
func paint(useColor bool, color, s string) string {
	if !useColor {
		return s
	}
	return color + s + colorReset
}

func (h *customHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *customHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	timeStr := r.Time.Format("2006/01/02 15:04:05")

	// Level color
	var levelColor string
	switch r.Level {
	case slog.LevelDebug:
		levelColor = colorCyan
	case slog.LevelInfo:
		levelColor = colorGreen
	case slog.LevelWarn:
		levelColor = colorYellow
	case slog.LevelError:
		levelColor = colorRed
	default:
		levelColor = colorReset
	}

	// Message and attributes
	fmt.Fprintf(h.out, "%s %s %s%s",
		paint(h.useColor, colorGray, timeStr),
		paint(h.useColor, levelColor, r.Level.String()),
		h.envPrefix, r.Message)
	for _, attr := range h.attrs {
		fmt.Fprintf(h.out, " %s=%v", attr.Key, attr.Value.Any())
	}

	// Record attrs are namespaced under whatever groups are open on this handler;
	// attrs persisted via WithAttrs already carry their prefix baked into the key.
	r.Attrs(func(attr slog.Attr) bool {
		fmt.Fprintf(h.out, " %s%s=%v", h.groupPfx, attr.Key, attr.Value.Any())
		return true
	})

	// Print log
	fmt.Fprintln(h.out)

	return nil
}

func (h *customHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	// Qualify each new attr's key with the currently-open group path and bake it
	// in now, so interleaved With/WithGroup chains keep the correct namespace per
	// attr. A fresh slice avoids aliasing a shared backing array across handlers.
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	for _, a := range attrs {
		a.Key = h.groupPfx + a.Key
		merged = append(merged, a)
	}

	return &customHandler{
		mu:        h.mu,
		out:       h.out,
		attrs:     merged,
		groupPfx:  h.groupPfx,
		level:     h.level,
		useColor:  h.useColor,
		envPrefix: h.envPrefix,
	}
}

func (h *customHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return &customHandler{
		mu:        h.mu,
		out:       h.out,
		attrs:     h.attrs,
		groupPfx:  h.groupPfx + name + ".",
		level:     h.level,
		useColor:  h.useColor,
		envPrefix: h.envPrefix,
	}
}

// New builds the application logger.
func New(noColor bool, env string) *slog.Logger {
	return slog.New(newCustomHandler(os.Stdout, noColor, env))
}
