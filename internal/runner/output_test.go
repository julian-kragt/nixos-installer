package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestOutputWriter(t *testing.T) {
	h := newTTY(t, 80, 24, false)
	ctx, task := Start(h.ctx, "Cmd")
	w := Output(ctx)

	io.WriteString(w, "part")
	if len(task.node.tail) != 0 {
		t.Fatal("partial line emitted")
	}
	io.WriteString(w, "ial\n\033[31mred\033[0m\n")
	io.WriteString(w, "10%\r50%\r100%\n")
	io.WriteString(w, "crlf\r\n")
	io.WriteString(w, "ctrl\x07\x00chars\ttab\n\n   \n")
	io.WriteString(w, "no newline at end")

	want := []string{"partial", "red", "100%", "crlf", "ctrlchars tab"}
	if strings.Join(task.node.tail, "|") != strings.Join(want, "|") {
		t.Fatalf("tail = %q, want %q", task.node.tail, want)
	}
	task.End(nil) // flushes the partial line
	if got := task.node.tail[len(task.node.tail)-1]; got != "no newline at end" {
		t.Fatalf("last = %q", got)
	}
}

func TestTailShownWhileRunningDroppedOnSuccess(t *testing.T) {
	h := newTTY(t, 80, 24, false)
	ctx, task := Start(h.ctx, "Cmd")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(Output(ctx), "out %d\n", i)
	}
	h.r.draw()
	eq(t, squash(h.screen()), []string{"⠋ Cmd", "│ out 4", "│ out 5", "│ out 6", "│ out 7", "│ out 8"})

	task.End(nil)
	h.r.draw()
	eq(t, squash(h.screen()), []string{"✓ Cmd 0ms"})
}

func TestFailureKeepsLastTwentyLines(t *testing.T) {
	h := newTTY(t, 80, 100, false)
	err := Run(h.ctx, "Cmd", func(ctx context.Context) error {
		for i := 1; i <= 30; i++ {
			fmt.Fprintf(Output(ctx), "out %d\n", i)
		}
		return errors.New("exit 1")
	})
	if err == nil {
		t.Fatal("no error")
	}
	h.r.draw()
	got := h.screen()
	if len(got) != 22 || !strings.HasPrefix(got[0], "✗ Cmd") ||
		!strings.Contains(got[1], "out 11") || !strings.Contains(got[20], "out 30") || got[21] != "  exit 1" {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestFileSink(t *testing.T) {
	var out bytes.Buffer
	f := &syncBuf{}
	r := newRunner(Options{Out: &out, File: f}, false, nil)
	clk := &clock{}
	clk.t = clk.t.Add(15*time.Hour + 12*time.Minute + 3*time.Second + 501*time.Millisecond)
	r.now = clk.now
	ctx := NewContext(context.Background(), r)

	ctx, base := Start(ctx, "Base install")
	_ = Parallel(ctx,
		Step{"Partitioning disk", func(ctx context.Context) error {
			fmt.Fprintln(Output(ctx), "\033[1mCreating new GPT entries\033[0m")
			return nil
		}},
		Step{"Copying closure", func(ctx context.Context) error {
			Debug(ctx, "hidden on screen but in file")
			return nil
		}},
	)
	_, dbg := StartDebug(ctx, "debug task")
	dbg.End(nil)
	base.End(nil)

	file := f.String()
	if strings.Contains(file, "\033") {
		t.Fatalf("ANSI in file:\n%s", file)
	}
	for _, want := range []string{
		"15:12:03.501 │ Base install › Partitioning disk | Creating new GPT entries",
		"Base install › Copying closure | hidden on screen but in file",
		"▸ Base install › debug task | started",
		"✓ Base install | 0ms",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("missing %q in:\n%s", want, file)
		}
	}
	if strings.Contains(out.String(), "hidden on screen") || strings.Contains(out.String(), "debug task") {
		t.Errorf("debug leaked to screen:\n%s", out.String())
	}
}
