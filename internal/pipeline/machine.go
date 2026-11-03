package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"

	"installer/internal/runner"
)

// Machine runs commands on a machine: this one (the zero value) or another
// one over ssh. Authentication is always via key or ssh-agent.
type Machine struct {
	ssh   []string // ssh argv up to and including the destination; nil = this machine
	quiet bool     // stream nothing to the runner and keep output out of errors
}

// SSH returns the root account on the machine at addr, authenticating via
// ssh-agent and ~/.ssh keys. BatchMode=yes prevents interactive prompts.
// TODO: replace StrictHostKeyChecking=no with TOFU once host key pinning is implemented.
func SSH(addr string) Machine {
	return Machine{ssh: []string{
		"ssh",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "BatchMode=yes",
		"-l", "root", addr,
	}}
}

// Loopback is a "remote" machine whose ssh is the local shell, so a command
// takes the same route as over ssh: quoted here, parsed by a shell there. For
// tests of code that runs on the target.
func Loopback() Machine { return Machine{ssh: []string{"sh", "-c"}} }

// Quiet returns m with its output kept away from the runner, the log file and
// error messages. Use it for every command that can print a secret.
func (m Machine) Quiet() Machine {
	m.quiet = true
	return m
}

// Run runs a command and returns its stdout. Stderr is streamed to the runner
// task in ctx unless m is quiet. A command that cannot be started or exits
// non-zero is an error that ends with the last line of stderr; it wraps
// *exec.ExitError for callers that need the exit code.
func (m Machine) Run(ctx context.Context, name string, args ...string) (string, error) {
	return m.RunStdin(ctx, nil, name, args...)
}

// RunStdin is Run with stdin connected. Secrets go here, never into args.
func (m Machine) RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	if m.ssh != nil {
		argv = append(slices.Clone(m.ssh), "umask 077 && "+shellJoin(argv))
	}
	process := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Stdin = stdin

	var stdout, stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	if !m.quiet {
		process.Stderr = io.MultiWriter(&stderr, runner.Output(ctx))
	}

	if err := process.Run(); err != nil {
		if last := lastLine(stderr.String()); last != "" && !m.quiet {
			return "", fmt.Errorf("%s: %w: %s", name, err, last)
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return stdout.String(), nil
}

// shellJoin quotes argv for the remote shell: ssh hands it a single string.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// lastLine is the last non-empty line of s.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}
