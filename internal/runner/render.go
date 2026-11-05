package runner

import (
	"fmt"
	"strings"
	"time"
)

const (
	minDurColumn = 40 // durations are aligned to at least this column
	tailShown    = 5  // output lines shown under a running task
)

// line is one rendered row before layout.
type line struct {
	depth int
	sym   string
	color string
	text  string
	dur   string
	faint bool // whole line is dim (command output)
}

// draw redraws the live area. The ticker calls it every tickInterval.
func (r *Runner) draw() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drawLocked()
}

// drawLocked commits finished roots and writes them, plus (on a terminal)
// the live area, in a single Write.
func (r *Runner) drawLocked() {
	if r.suspended {
		return
	}
	r.commitLocked()

	if !r.tty {
		if r.pending.Len() > 0 {
			r.write(r.pending.String())
			r.pending.Reset()
		}
		return
	}

	live := r.liveRender()
	if r.pending.Len() == 0 && len(live) == 0 && r.liveLines == 0 {
		return
	}

	var b strings.Builder
	if !r.cursorHidden {
		b.WriteString(hideCursor)
		r.cursorHidden = true
	}
	if r.liveLines > 0 {
		b.WriteString(cursorUp(r.liveLines))
	}
	b.WriteString(clearDown)
	b.WriteString(r.pending.String())
	r.pending.Reset()
	for _, s := range live {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	r.write(b.String())

	r.liveLines = len(live)
	r.frame++
}

// commitLocked moves finished roots into the pending output, in start order.
// A root is only committed once every root before it is.
func (r *Runner) commitLocked() {
	for r.numCommitted < len(r.roots) && r.roots[r.numCommitted].finished() {
		root := r.roots[r.numCommitted]
		var lines []line
		r.collect(root, root.depth(), r.now(), false, &lines)
		for _, s := range renderLines(lines, 0, r.tty) {
			r.pending.WriteString(s)
			r.pending.WriteByte('\n')
		}
		root.committed = true
		r.numCommitted++
	}
}

// liveRender renders the roots that are not committed yet, cut to the
// terminal size. Caller must hold r.mu.
func (r *Runner) liveRender() []string {
	var lines []line
	now := r.now()
	for _, root := range r.roots[r.numCommitted:] {
		r.collect(root, root.depth(), now, true, &lines)
	}
	width, height := r.size()
	out := renderLines(lines, width, true)

	room := height - 1 // the last row stays free for the cursor
	if height <= 0 || len(out) <= room {
		return out
	}
	// Too tall: keep the first row, a marker for what is left out and the
	// newest rows. That takes three rows; with fewer, only the newest show.
	if room < 3 {
		return out[len(out)-max(room, 1):]
	}
	newest := room - 2
	hidden := len(out) - 1 - newest
	marker := fmt.Sprintf("%s%s… %d lines%s", dim, indent(1), hidden, reset)
	return append([]string{out[0], marker}, out[len(out)-newest:]...)
}

// collect appends the rows for n and everything below it.
func (r *Runner) collect(n *node, depth int, now time.Time, live bool, out *[]line) {
	if n.isLog {
		if n.level == levelDetail {
			*out = append(*out, line{depth: depth + 1, sym: " ", text: n.title})
			return
		}
		sym, col := logStyle(n.level, depth == 0)
		*out = append(*out, line{depth: depth, sym: sym, color: col, text: n.title})
		return
	}

	ln := line{depth: depth, text: n.title}
	switch n.state {
	case stateRunning:
		ln.sym, ln.color = spinnerFrames[r.frame%len(spinnerFrames)], cyan
		if d := now.Sub(n.start); d >= time.Second {
			ln.dur = fmtDuration(d)
		}
	case stateOK:
		ln.sym, ln.color = "✓", green
		ln.dur = fmtDuration(n.end.Sub(n.start))
	case stateFailed:
		ln.sym, ln.color = "✗", red
		ln.text = failedTitle(n)
		ln.dur = fmtDuration(n.end.Sub(n.start))
	}
	*out = append(*out, ln)

	tail := n.tail
	switch {
	case n.state == stateFailed:
	case n.state == stateRunning && live:
		if len(tail) > tailShown {
			tail = tail[len(tail)-tailShown:]
		}
	default:
		tail = nil
	}
	for _, t := range tail {
		*out = append(*out, line{depth: depth, sym: "│", color: dim, text: t, faint: true})
	}

	for _, c := range n.children {
		r.collect(c, depth+1, now, live, out)
	}

	for _, s := range errorLines(n) {
		*out = append(*out, line{depth: depth, sym: " ", text: s})
	}
}

func logStyle(lv level, root bool) (sym, color string) {
	switch lv {
	case levelWarn:
		return "⚠", yellow
	case levelError:
		return "✗", red
	case levelInfo:
		if root {
			return "✓", green
		}
		return "→", green
	}
	if root {
		return "•", blue
	}
	return "→", blue
}

// renderLines lays out rows: indent, symbol, text and a dim duration aligned
// to a common column. With width > 0 every row is at most width-1 columns,
// measured on plain text, so it never wraps. color adds ANSI codes.
func renderLines(lines []line, width int, color bool) []string {
	// durCol is the column the durations start in: two past the longest text
	// that has one. maxDur is the widest duration.
	durCol, maxDur := minDurColumn, 0
	for _, ln := range lines {
		if ln.dur == "" {
			continue
		}
		durCol = max(durCol, prefixWidth(ln.depth)+runeLen(ln.text)+2)
		maxDur = max(maxDur, runeLen(ln.dur))
	}
	limit := width - 1 // the last column a row may use
	if width > 0 {
		durCol = min(durCol, limit-maxDur)
	}

	paint := func(c, s string) string {
		if !color {
			return s
		}
		return c + s + reset
	}

	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		prefix := prefixWidth(ln.depth)
		text := ln.text
		if width > 0 {
			// room is where prefix and text must end: the edge of the
			// terminal, or two before the duration.
			room := limit
			if ln.dur != "" {
				room = min(limit, durCol-2)
			}
			if prefix+runeLen(text) > room {
				text = truncate(text, room-prefix)
			}
		}
		used := prefix + runeLen(text) // columns taken so far

		var b strings.Builder
		b.WriteString(indent(ln.depth))
		b.WriteString(paint(ln.color, ln.sym))
		b.WriteByte(' ')
		if ln.faint {
			b.WriteString(paint(dim, text))
		} else {
			b.WriteString(text)
		}
		if ln.dur != "" && (width <= 0 || used+2+runeLen(ln.dur) <= limit) {
			b.WriteString(strings.Repeat(" ", max(durCol-used, 2)))
			b.WriteString(paint(dim, ln.dur))
		}
		out = append(out, b.String())
	}
	return out
}

// prefixWidth is the width of what precedes the text of a row: the indent,
// the symbol and a space.
func prefixWidth(depth int) int { return len(indent(depth)) + 2 }
