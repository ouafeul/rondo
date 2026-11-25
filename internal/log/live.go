package log

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

func stripANSI(s string) string {
	return strings.ReplaceAll(ansiRe.ReplaceAllString(s, ""), "\r", "")
}

type liveRegion struct {
	mu      sync.Mutex
	out     *os.File
	live    bool
	active  bool
	name    string
	start   time.Time
	buf     []string
	frame   int
	drawn   int
	ticking bool
	stop    chan struct{}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const maxTail = 8

var region = newRegion(os.Stderr)

func newRegion(out *os.File) *liveRegion {
	fi, _ := out.Stat()
	tty := fi != nil && fi.Mode()&os.ModeCharDevice != 0
	return &liveRegion{out: out, live: tty}
}

func (r *liveRegion) width() int {
	if w, _, err := term.GetSize(int(r.out.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + ""
}

func (r *liveRegion) eraseLocked() {
	if r.drawn > 0 {
		fmt.Fprintf(r.out, "\033[%dA\033[0J", r.drawn)
		r.drawn = 0
	}
}

func (r *liveRegion) renderLocked() {
	if !r.live || !r.active {
		return
	}
	p := globalPalette
	w := r.width()
	r.eraseLocked()
	// Autowrap off: a line wider than the terminal must not take a second row, or the erase misses it.
	fmt.Fprint(r.out, "\033[?7l")
	defer fmt.Fprint(r.out, "\033[?7h")

	frame := spinnerFrames[r.frame%len(spinnerFrames)]
	fmt.Fprintf(r.out, "%s%s%s %s\n", p.cyan, frame, p.reset, truncate(r.name, w-3))
	lines := 1

	start := 0
	if len(r.buf) > maxTail {
		start = len(r.buf) - maxTail
	}
	for _, l := range r.buf[start:] {
		fmt.Fprintf(r.out, "%s│ %s%s\n", p.gray, truncate(l, w-3), p.reset)
		lines++
	}
	r.drawn = lines
}

func (r *liveRegion) Write(p []byte) (int, error) {
	r.printAbove(string(p))
	return len(p), nil
}

func (r *liveRegion) printAbove(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live && r.active {
		r.eraseLocked()
	}
	fmt.Fprint(r.out, s)
	r.renderLocked()
}

func (r *liveRegion) begin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		r.finishLocked(true)
	}
	r.name = name
	r.start = time.Now()
	r.buf = r.buf[:0]
	r.drawn = 0
	r.active = true
	if r.live {
		r.frame = 0
		r.renderLocked()
		r.startTickerLocked()
		return
	}
	p := globalPalette
	fmt.Fprintf(r.out, "%s%s▶ %s%s\n", p.cyan, p.bold, name, p.reset)
}

func (r *liveRegion) startTickerLocked() {
	if r.ticking {
		return
	}
	r.ticking = true
	r.stop = make(chan struct{})
	go func(stop chan struct{}) {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.mu.Lock()
				if r.active && r.live {
					r.frame++
					r.renderLocked()
				}
				r.mu.Unlock()
			}
		}
	}(r.stop)
}

func (r *liveRegion) stopTickerLocked() {
	if r.ticking {
		close(r.stop)
		r.ticking = false
	}
}

func (r *liveRegion) streamLine(s string) {
	s = stripANSI(s)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		r.buf = append(r.buf, s)
	}
	if r.live && r.active {
		r.renderLocked()
		return
	}
	p := globalPalette
	fmt.Fprintf(r.out, "%s│ %s%s\n", p.gray, s, p.reset)
}

func (r *liveRegion) finishLocked(success bool) {
	if !r.active {
		return
	}
	r.stopTickerLocked()
	p := globalPalette
	dur := time.Since(r.start).Round(100 * time.Millisecond)
	if r.live {
		r.eraseLocked()
	}
	if success {
		fmt.Fprintf(r.out, "%s✓%s %s %s(%s)%s\n", p.green, p.reset, r.name, p.gray, dur, p.reset)
	} else {
		fmt.Fprintf(r.out, "%s✗ %s%s %s(%s)%s\n", p.red, r.name, p.reset, p.gray, dur, p.reset)
		if r.live {
			for _, l := range r.buf {
				fmt.Fprintf(r.out, "%s│ %s%s\n", p.gray, l, p.reset)
			}
		}
	}
	r.active = false
}

func (r *liveRegion) finish(success bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishLocked(success)
}
