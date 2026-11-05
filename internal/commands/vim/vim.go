// Package vim lets the operator write a file in vim on the provider. Vim keeps
// no copy of its own, and the file is removed as soon as vim exits.
package vim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"installer/internal/runner"
)

// flags make vim keep nothing besides the file it edits: no vimrc or plugins,
// which could turn the rest back on; no swap file; no viminfo with registers
// and history; no backup copy, also not while saving; no undo file.
var flags = []string{"--clean", "-n", "-i", "NONE", "--cmd", "set noswapfile nobackup nowritebackup noundofile"}

// ErrAborted means the operator gave up on the edit.
var ErrAborted = errors.New("aborted in the editor")

// command is vim editing path, attached to the terminal in and out.
func command(ctx context.Context, path string, in, out *os.File) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "vim", slices.Concat(flags, []string{"--", path})...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	return cmd
}

// edit opens path in vim on the operator's terminal. A variable so tests can
// stand in for the operator.
var edit = func(ctx context.Context, path string) error {
	return runner.Hand(ctx, func(in, out *os.File) error {
		return command(ctx, path, in, out).Run()
	})
}

// EditUntil opens initial in vim as a file called name and returns what the
// operator saved once check accepts it. The file lives in a new directory only
// the operator can read and is removed as soon as vim exits. When vim exits
// non-zero (:cq), or the operator chooses to stop after a rejected edit, it is
// ErrAborted. check's error is shown, so it must not hold the text itself.
func EditUntil(ctx context.Context, name string, initial []byte, check func([]byte) error) ([]byte, error) {
	dir, err := os.MkdirTemp("", "nixos-installer-*") // mode 700
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, name)

	text := initial
	for {
		got, err := editOnce(ctx, path, text)
		if err != nil {
			return nil, err
		}
		problem := "nothing was changed"
		if !bytes.Equal(got, initial) {
			err := check(got)
			if err == nil {
				return got, nil
			}
			problem = err.Error()
		}
		runner.Warn(ctx, name+": "+problem)
		i, err := runner.Choose(ctx, "Edit "+name+" again?", []string{"Edit again", "Abort"})
		if err != nil {
			return nil, err
		}
		if i == 1 {
			return nil, ErrAborted
		}
		text = got
	}
}

// editOnce writes text to path, opens it in vim and returns what was saved.
// path is gone again when it returns.
func editOnce(ctx context.Context, path string, text []byte) ([]byte, error) {
	defer os.Remove(path)
	if err := os.WriteFile(path, text, 0o600); err != nil {
		return nil, err
	}
	err := edit(ctx, path)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil, ErrAborted
	}
	if err != nil {
		return nil, fmt.Errorf("vim: %w", err)
	}
	return os.ReadFile(path)
}
