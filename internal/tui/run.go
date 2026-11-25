package tui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// PromptSuffix ends every question the CLI asks on stderr; the TUI answers it in a dialog.
const PromptSuffix = "? (y/N) "

type lineKind int

const (
	linePlain lineKind = iota
	lineStream
	lineStepBegin
	lineStepOK
	lineStepFail
	lineDebug
	lineInfo
	lineWarn
	lineError
)

type outLine struct {
	kind lineKind
	text string
}

// markers are what internal/log prints when stderr is not a terminal.
var markers = []struct {
	prefix string
	kind   lineKind
}{
	{"▶ ", lineStepBegin},
	{"✓ ", lineStepOK},
	{"✗ ", lineStepFail},
	{"│ ", lineStream},
	{"ERROR ", lineError},
	{" WARN ", lineWarn},
	{" INFO ", lineInfo},
	{"DEBUG ", lineDebug},
}

func parseLine(s string) outLine {
	s = strings.ReplaceAll(ansi.Strip(s), "\r", "")
	for _, m := range markers {
		if strings.HasPrefix(s, m.prefix) {
			return outLine{kind: m.kind, text: s[len(m.prefix):]}
		}
	}
	return outLine{kind: linePlain, text: s}
}

func (l outLine) render() string {
	switch l.kind {
	case lineStepBegin:
		return stTitle.Render("▶ " + l.text)
	case lineStepOK:
		return stOK.Render("✓ ") + l.text
	case lineStepFail:
		return stBad.Bold(true).Render("✗ " + l.text)
	case lineStream:
		return stDim.Render("│ " + l.text)
	case lineError:
		return stBad.Bold(true).Render("ERROR") + " " + l.text
	case lineWarn:
		return stWarn.Bold(true).Render(" WARN") + " " + l.text
	case lineInfo:
		return stOK.Bold(true).Render(" INFO") + " " + l.text
	case lineDebug:
		return stDim.Render("DEBUG " + l.text)
	}
	return l.text
}

type runState int

const (
	runRunning runState = iota
	runSucceeded
	runFailed
)

func (s runState) icon(frame int) string {
	switch s {
	case runSucceeded:
		return stOK.Render("✓")
	case runFailed:
		return stBad.Render("✗")
	}
	return stAccent.Render(spinner[frame%len(spinner)])
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type runStep struct {
	name  string
	state runState
	dur   string
}

type runEvent struct {
	line   *outLine
	prompt string
	done   bool
	err    error
}

// runMsg tells the model that run id has events to take.
type runMsg struct{ id int }

const maxRunLines = 20000

// run is one `rondo …` child: the CLI does the work, the TUI shows its steps and output.
type run struct {
	id      int
	op      *operation
	args    []string
	start   time.Time
	end     time.Time
	state   runState
	err     error
	steps   []runStep
	lines   []outLine
	prompt  string
	stdin   io.WriteCloser
	proc    *os.Process
	stopped bool
	done    chan struct{}

	mu      sync.Mutex
	pending []runEvent
}

// startRun launches rondo; notify is called when events wait, once until the model takes them.
func startRun(id int, op *operation, exe string, args, env []string, notify func(tea.Msg)) (*run, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	detach(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close()
	r := &run{
		id: id, op: op, args: args, start: time.Now(),
		stdin: stdin, proc: cmd.Process, done: make(chan struct{}),
	}
	emit := func(ev runEvent) {
		r.mu.Lock()
		first := len(r.pending) == 0
		r.pending = append(r.pending, ev)
		r.mu.Unlock()
		if first {
			notify(runMsg{id: id})
		}
	}
	read := make(chan struct{})
	go func() {
		readOutput(pr, emit)
		close(read)
	}()
	go func() {
		err := cmd.Wait()
		// A grandchild that outlives rondo (an ssh master) keeps the pipe open: stop reading anyway.
		_ = pr.SetReadDeadline(time.Now().Add(2 * time.Second))
		<-read
		pr.Close()
		emit(runEvent{done: true, err: err})
		close(r.done)
	}()
	return r, nil
}

func (r *run) take() []runEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := r.pending
	r.pending = nil
	return ev
}

// readOutput emits each line, and a prompt for a question left without its newline.
func readOutput(r io.Reader, emit func(runEvent)) {
	buf := make([]byte, 32*1024)
	var pending []byte
	for {
		n, err := r.Read(buf)
		pending = append(pending, buf[:n]...)
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break
			}
			l := parseLine(string(pending[:i]))
			emit(runEvent{line: &l})
			pending = pending[i+1:]
		}
		if bytes.HasSuffix(pending, []byte(PromptSuffix)) {
			emit(runEvent{prompt: strings.TrimSpace(string(pending[:len(pending)-len(PromptSuffix)]))})
			pending = nil
		}
		if err != nil {
			if len(pending) > 0 {
				l := parseLine(string(pending))
				emit(runEvent{line: &l})
			}
			return
		}
	}
}

func (r *run) apply(ev runEvent) {
	switch {
	case ev.done:
		r.end = time.Now()
		r.err = ev.err
		r.prompt = ""
		r.state = runSucceeded
		if ev.err != nil {
			r.state = runFailed
		}
		for i := range r.steps {
			if r.steps[i].state == runRunning {
				r.steps[i].state = r.state
			}
		}
	case ev.prompt != "":
		r.prompt = ev.prompt
	case ev.line != nil:
		r.addLine(*ev.line)
	}
}

func (r *run) addLine(l outLine) {
	r.lines = append(r.lines, l)
	if len(r.lines) > maxRunLines {
		r.lines = r.lines[len(r.lines)-maxRunLines:]
	}
	switch l.kind {
	case lineStepBegin:
		r.steps = append(r.steps, runStep{name: l.text})
	case lineStepOK, lineStepFail:
		name, dur := splitDuration(l.text)
		state := runSucceeded
		if l.kind == lineStepFail {
			state = runFailed
		}
		for i := len(r.steps) - 1; i >= 0; i-- {
			if r.steps[i].state == runRunning && r.steps[i].name == name {
				r.steps[i].state, r.steps[i].dur = state, dur
				return
			}
		}
		r.steps = append(r.steps, runStep{name: name, state: state, dur: dur})
	}
}

// splitDuration cuts "name (1.2s)" as internal/log closes a step.
func splitDuration(s string) (string, string) {
	i := strings.LastIndex(s, " (")
	if i < 0 || !strings.HasSuffix(s, ")") {
		return s, ""
	}
	return s[:i], s[i+2 : len(s)-1]
}

func (r *run) answer(yes bool) {
	ans := "n\n"
	if yes {
		ans = "y\n"
	}
	r.prompt = ""
	_, _ = io.WriteString(r.stdin, ans)
}

// interrupt sends SIGINT: rondo cancels its context and stops its tools as on a Ctrl+C.
func (r *run) interrupt() error {
	if r.state != runRunning || r.proc == nil {
		return nil
	}
	r.stopped = true
	err := r.proc.Signal(os.Interrupt)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (r *run) elapsed(now time.Time) time.Duration {
	if r.state == runRunning {
		return now.Sub(r.start)
	}
	return r.end.Sub(r.start)
}

func (r *run) commandLine() string {
	return "rondo " + strings.Join(r.args, " ")
}
