package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
)

func levelFor(name string) slog.Level {
	switch name {
	case "trace", "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type palette struct {
	reset, dim, bold, red, green, yellow, cyan, gray string
}

func makePalette(color bool) palette {
	if !color {
		return palette{}
	}
	return palette{
		reset:  "\033[0m",
		dim:    "\033[2m",
		bold:   "\033[1m",
		red:    "\033[31m",
		green:  "\033[32m",
		yellow: "\033[33m",
		cyan:   "\033[36m",
		gray:   "\033[90m",
	}
}

func colorEnabled(f *os.File) bool {
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type handler struct {
	out   io.Writer
	level slog.Level
	pal   palette
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	p := h.pal
	tag, col := levelTag(r.Level)

	var b strings.Builder
	b.WriteString(col)
	b.WriteString(p.bold)
	b.WriteString(tag)
	b.WriteString(p.reset)
	b.WriteByte(' ')
	b.WriteString(r.Message)

	type kv struct{ k, v string }
	var attrs []kv
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, kv{a.Key, a.Value.String()})
		return true
	})
	if len(attrs) > 0 {
		sort.Slice(attrs, func(i, j int) bool { return attrs[i].k < attrs[j].k })
		b.WriteString("  ")
		b.WriteString(p.gray)
		for i, a := range attrs {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%s=%s", a.k, a.v)
		}
		b.WriteString(p.reset)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *handler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *handler) WithGroup(string) slog.Handler      { return h }

func levelTag(l slog.Level) (tag, color string) {
	pal := globalPalette
	switch {
	case l >= slog.LevelError:
		return "ERROR", pal.red
	case l >= slog.LevelWarn:
		return " WARN", pal.yellow
	case l >= slog.LevelInfo:
		return " INFO", pal.green
	default:
		return "DEBUG", pal.gray
	}
}

var (
	globalPalette = makePalette(colorEnabled(os.Stderr))
	logger        = newLogger(slog.LevelInfo)
)

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(&handler{out: region, level: level, pal: globalPalette})
}

func Setup(level string) {
	globalPalette = makePalette(colorEnabled(os.Stderr))
	logger = newLogger(levelFor(level))
}

func Handler() slog.Handler {
	return logger.Handler()
}

func Step(msg string, args ...any) { region.begin(stepName(msg, args...)) }

func stepName(msg string, args ...any) string {
	if len(args) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i+1 < len(args); i += 2 {
		fmt.Fprintf(&b, " %v=%v", args[i], args[i+1])
	}
	return b.String()
}

func StreamLine(s string) { region.streamLine(s) }

func Finish(err error) { region.finish(err == nil) }

func Info(msg string, args ...any)  { logger.Info(msg, args...) }
func Debug(msg string, args ...any) { logger.Debug(msg, args...) }
func Warn(msg string, args ...any)  { logger.Warn(msg, args...) }
func Error(msg string, args ...any) { logger.Error(msg, args...) }
