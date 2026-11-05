package runner

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

type nodeState int

const (
	stateRunning nodeState = iota
	stateOK
	stateFailed
)

const tailKept = 20

var (
	errParentEnded = errors.New("parent ended")
	errClosed      = errors.New("runner closed")
)

// node is a task or a log line. Command output lives in a task's tail.
type node struct {
	title    string // of a task; the message of a log line
	isLog    bool   // a log line: never runs, has no children
	level    level
	parent   *node
	top      *node // the entry of Runner.roots that n is drawn under; n itself for an entry
	children []*node

	start, end time.Time
	state      nodeState
	err        error

	tail []string // last tailKept output lines
	out  *outputWriter

	hidden    bool // not shown (debug task without verbose, or inside one)
	committed bool // entries of Runner.roots only
}

func (n *node) depth() int {
	d := 0
	for p := n.parent; p != nil; p = p.parent {
		d++
	}
	return d
}

// path joins the titles from the root to n with " › ".
func (n *node) path() string {
	if n == nil {
		return ""
	}
	var titles []string
	for p := n; p != nil; p = p.parent {
		titles = append(titles, p.title)
	}
	slices.Reverse(titles)
	return strings.Join(titles, " › ")
}

// finished reports whether n has ended.
func (n *node) finished() bool { return n.state != stateRunning }

// Task is a handle on a running task. It only handles lifecycle; logging
// goes through the context returned by Start.
type Task struct {
	runner *Runner
	node   *node
}

// Start begins a task as a child of ctx's task, or as a root. The returned
// context carries the new task.
func Start(ctx context.Context, title string) (context.Context, *Task) {
	return start(ctx, title, levelInfo)
}

// StartDebug is like Start but the task and everything under it is hidden
// unless the runner is verbose.
func StartDebug(ctx context.Context, title string) (context.Context, *Task) {
	return start(ctx, title, levelDebug)
}

func start(ctx context.Context, title string, lv level) (context.Context, *Task) {
	v := from(ctx)
	if v == nil {
		return ctx, &Task{}
	}
	r := v.runner
	r.mu.Lock()
	defer r.mu.Unlock()

	n := &node{
		title:  oneLine(title),
		level:  lv,
		parent: v.task,
		start:  r.now(),
	}
	n.top = n
	n.hidden = (lv == levelDebug && !r.verbose) || (v.task != nil && v.task.hidden)

	switch {
	case n.hidden:
		// Drawn nowhere; only the log file sees it.
	case v.task == nil, v.task.state != stateRunning:
		// A task under a parent that already ended gets its own entry, drawn
		// at its depth below everything that started before it.
		r.roots = append(r.roots, n)
	default:
		n.top = v.task.top
		v.task.children = append(v.task.children, n)
	}

	r.logToFile("▸", n, "started")
	r.redrawIfNoTicker()
	return context.WithValue(ctx, ctxKey{}, &ctxVal{runner: r, task: n}), &Task{runner: r, node: n}
}

// End finishes the task: ✓ if err is nil, else ✗ with the error.
func (t *Task) End(err error) {
	if t == nil || t.runner == nil {
		return
	}
	t.runner.mu.Lock()
	defer t.runner.mu.Unlock()
	t.runner.endLocked(t.node, err)
	t.runner.redrawIfNoTicker()
}

// endLocked finishes n and closes tasks still running below it as failed.
func (r *Runner) endLocked(n *node, err error) {
	if n.state != stateRunning {
		return
	}
	for _, c := range n.children {
		r.endLocked(c, errParentEnded)
	}
	if n.out != nil {
		n.out.flushLocked()
	}

	n.end = r.now()
	n.err = err
	n.state = stateOK
	if err != nil {
		n.state = stateFailed
	}

	sym, msg := "✓", fmtDuration(n.end.Sub(n.start))
	if err != nil {
		sym, msg = "✗", oneLine(err.Error())
	}
	r.logToFile(sym, n, msg)
}

// failedTitle is the text of a failed task line. The error goes on the rows
// below it, see errorLines.
func failedTitle(n *node) string {
	switch {
	case errors.Is(n.err, errParentEnded):
		return n.title + " (parent ended)"
	case errors.Is(n.err, errClosed):
		return n.title + " (runner closed)"
	}
	return n.title
}

// errorLines is the error of a failed task as printable rows, keeping its line
// breaks and indentation. It is empty when the title already says it all, or
// when the error says no more than the failed children show themselves.
func errorLines(n *node) []string {
	if n.state != stateFailed || errors.Is(n.err, errParentEnded) || errors.Is(n.err, errClosed) {
		return nil
	}
	rest := n.err.Error()
	for _, c := range n.children {
		if c.state == stateFailed {
			rest = strings.Replace(rest, c.err.Error(), "", 1)
		}
	}
	if strings.TrimSpace(rest) == "" {
		return nil
	}
	var rows []string
	for _, s := range strings.Split(n.err.Error(), "\n") {
		if s = strings.TrimRight(stripControl(s), " "); s != "" {
			rows = append(rows, s)
		}
	}
	return rows
}

// Run starts a task, runs fn with its context and ends the task.
func Run(ctx context.Context, title string, fn func(context.Context) error) (err error) {
	ctx, t := Start(ctx, title)
	defer func() {
		if r := recover(); r != nil {
			t.End(panicError(r, false))
			panic(r)
		}
		t.End(err)
	}()
	return fn(ctx)
}

// panicError turns a recovered panic value into an error, optionally with the
// panicking goroutine's stack (for panics that are not re-raised).
func panicError(r any, withStack bool) error {
	if !withStack {
		return fmt.Errorf("panic: %v", r)
	}
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return fmt.Errorf("panic: %v\n%s", r, buf[:n])
}

// Step is one unit of work for Parallel and RunSteps.
type Step struct {
	Title string
	Fn    func(context.Context) error
}

// RunSteps runs the steps one after another with Run and stops at the first
// error, which it returns.
func RunSteps(ctx context.Context, steps ...Step) error {
	for _, s := range steps {
		if err := Run(ctx, s.Title, s.Fn); err != nil {
			return err
		}
	}
	return nil
}

// Parallel runs the steps concurrently, each as its own child task of ctx's
// task. The first error cancels the context of the others. The returned
// error joins all step errors, except cancellations caused by a sibling's
// failure.
//
// Steps must not write shared state; they return results and the caller
// assigns them after Parallel returns.
func Parallel(ctx context.Context, steps ...Step) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Create the tasks up front so they appear in step order.
	ctxs := make([]context.Context, len(steps))
	tasks := make([]*Task, len(steps))
	for i, s := range steps {
		ctxs[i], tasks[i] = Start(ctx, s.Title)
	}

	errs := make([]error, len(steps))
	var wg sync.WaitGroup
	for i, s := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			defer func() {
				if r := recover(); r != nil {
					err = panicError(r, true)
					tasks[i].End(err)
					errs[i] = err
					cancel()
				}
			}()
			err = s.Fn(ctxs[i])
			tasks[i].End(err)
			errs[i] = err
			if err != nil {
				cancel()
			}
		}()
	}
	wg.Wait()

	var failed []error
	for _, err := range errs {
		if err == nil || errors.Is(err, context.Canceled) {
			continue
		}
		failed = append(failed, err)
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	return parent.Err()
}
