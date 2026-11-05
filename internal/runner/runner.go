// Package runner keeps a tree of tasks in memory and renders it as a live area
// on the terminal. The current task travels in a context.Context, so nested
// and parallel code runs under the correct parent without passing a *Runner
// to every function.
package runner

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// level determines the visibility/severity of a message.
type level int

const (
	levelDebug level = iota
	levelInfo
	levelDetail
	levelWarn
	levelError
)

// Options configures a Runner.
type Options struct {
	// Out receives the rendered tree. A terminal gets a live area with
	// colors; anything else only gets finished blocks, without escapes.
	Out io.Writer
	// In is where Ask reads the operator's answers from.
	In io.Reader
	// File, if set, receives every event at debug level, including all
	// command output, without ANSI codes.
	File io.Writer
	// Verbose shows debug messages and debug tasks on Out.
	Verbose bool
}

const tickInterval = 80 * time.Millisecond

// Runner owns the task tree of one run.
type Runner struct {
	mu      sync.Mutex
	out     io.Writer
	in      *bufio.Reader // nil: nobody to ask
	inFile  *os.File      // non-nil when in is a terminal, for ReadPassword
	file    io.Writer     // nil: no log file
	verbose bool
	now     func() time.Time

	tty  bool
	size func() (width, height int)

	roots        []*node
	numCommitted int // roots[:numCommitted] are flushed above the live area
	pending      strings.Builder
	liveLines    int
	frame        int
	cursorHidden bool
	closed       bool
	suspended    bool // an Ask is waiting for input; the live area is frozen
	rootOut      *outputWriter

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// New creates a Runner. Close must be called before the process exits.
func New(opts Options) *Runner {
	var tty bool
	var size func() (int, int)
	if f, ok := opts.Out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		tty = true
		size = func() (int, int) {
			w, h, err := term.GetSize(int(f.Fd()))
			if err != nil {
				return 80, 24
			}
			return w, h
		}
	}

	r := newRunner(opts, tty, size)
	if tty {
		r.wg.Add(1)
		go r.tick()
	}
	return r
}

func newRunner(opts Options, tty bool, size func() (int, int)) *Runner {
	r := &Runner{
		out:     opts.Out,
		file:    opts.File,
		verbose: opts.Verbose,
		now:     time.Now,
		tty:     tty,
		size:    size,
		stop:    make(chan struct{}),
	}
	if r.out == nil {
		r.out = io.Discard
	}
	if r.size == nil {
		r.size = func() (int, int) { return 0, 0 }
	}
	if opts.In != nil {
		r.in = bufio.NewReader(opts.In)
		if f, ok := opts.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			r.inFile = f
		}
	}
	return r
}

func (r *Runner) tick() {
	defer r.wg.Done()
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.draw()
		case <-r.stop:
			return
		}
	}
}

// Close stops the ticker, ends tasks that are still running, draws a final
// time and restores the cursor. It must run before os.Exit.
func (r *Runner) Close() {
	r.once.Do(func() {
		close(r.stop)
		r.wg.Wait()

		r.mu.Lock()
		defer r.mu.Unlock()
		for _, root := range r.roots[r.numCommitted:] {
			r.endLocked(root, errClosed)
		}
		r.closed = true
		r.drawLocked()
		if r.cursorHidden {
			r.write(showCursor)
			r.cursorHidden = false
		}
	})
}

// write sends s to Out. Caller must hold r.mu.
func (r *Runner) write(s string) {
	io.WriteString(r.out, s)
}

// logToFile records an event for n in the log file, one line without ANSI
// codes:
//
//	15:12:03.501 │ Base install › Partitioning disk | Creating new GPT entries
//
// msg is a single line that went through oneLine or stripControl. Caller must
// hold r.mu.
func (r *Runner) logToFile(sym string, n *node, msg string) {
	if r.file == nil {
		return
	}
	line := r.now().Format("15:04:05.000") + " " + sym + " "
	if path := n.path(); path != "" {
		line += path + " "
	}
	io.WriteString(r.file, line+"| "+msg+"\n")
}

// redrawIfNoTicker redraws immediately when no ticker is going to do it.
// Caller must hold r.mu.
func (r *Runner) redrawIfNoTicker() {
	if !r.tty || r.closed {
		r.drawLocked()
	}
}

type ctxKey struct{}

type ctxVal struct {
	runner *Runner
	task   *node // nil: no task yet, the root level
}

// NewContext returns a context that carries r. Without a runner in the
// context, all calls in this package are no-ops.
func NewContext(ctx context.Context, r *Runner) context.Context {
	return context.WithValue(ctx, ctxKey{}, &ctxVal{runner: r})
}

func from(ctx context.Context) *ctxVal {
	v, _ := ctx.Value(ctxKey{}).(*ctxVal)
	if v == nil || v.runner == nil {
		return nil
	}
	return v
}

// Debug logs a debug message into ctx's task. Shown only when verbose.
func Debug(ctx context.Context, msg string) { logAt(ctx, levelDebug, msg) }

// Info logs an informational message into ctx's task.
func Info(ctx context.Context, msg string) { logAt(ctx, levelInfo, msg) }

// Detail logs a row that continues the previous log line: no symbol, indented
// below it. Spacing in msg is kept, so rows can line up as a table.
func Detail(ctx context.Context, msg string) { logAt(ctx, levelDetail, msg) }

// Warn logs a warning into ctx's task.
func Warn(ctx context.Context, msg string) { logAt(ctx, levelWarn, msg) }

// Error logs an error into ctx's task.
func Error(ctx context.Context, msg string) { logAt(ctx, levelError, msg) }

func logAt(ctx context.Context, lv level, msg string) {
	v := from(ctx)
	if v == nil {
		return
	}
	v.runner.mu.Lock()
	defer v.runner.mu.Unlock()
	v.runner.logLocked(v.task, lv, oneLine(msg))
	v.runner.redrawIfNoTicker()
}

// logLocked records a log line under n (or at the root if n is nil).
func (r *Runner) logLocked(n *node, lv level, msg string) {
	sym := "•"
	switch lv {
	case levelInfo:
		sym = "→"
	case levelDetail:
		sym = "│"
	case levelWarn:
		sym = "⚠"
	case levelError:
		sym = "✗"
	}
	r.logToFile(sym, n, msg)

	if (lv == levelDebug && !r.verbose) || (n != nil && n.hidden) {
		return
	}

	ln := &node{isLog: true, state: stateOK, level: lv, title: msg, parent: n, start: r.now()}
	ln.top = ln
	switch {
	case n == nil, n.top.committed:
		// What n is drawn under is already printed: the line gets its own
		// entry, drawn at its depth.
		r.roots = append(r.roots, ln)
	default:
		ln.top = n.top
		n.children = append(n.children, ln)
	}
}
