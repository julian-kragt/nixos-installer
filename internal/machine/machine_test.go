package machine

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/julian-kragt/nixos-installer/internal/runner"
)

var loopback = Loopback()

func TestRunReturnsStdoutAndStreamsStderr(t *testing.T) {
	var screen, file bytes.Buffer
	ctx := runner.NewContext(context.Background(), runner.New(runner.Options{Out: &screen, File: &file}))

	for name, m := range map[string]Machine{"local": {}, "remote": loopback} {
		file.Reset()
		out, err := m.Run(ctx, "sh", "-c", "echo out; echo err >&2")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != "out\n" {
			t.Errorf("%s: stdout = %q, want only stdout", name, out)
		}
		if !strings.Contains(file.String(), "err") {
			t.Errorf("%s: log %q misses stderr", name, file.String())
		}
	}
}

func TestQuietKeepsOutputAway(t *testing.T) {
	var screen, file bytes.Buffer
	ctx := runner.NewContext(context.Background(), runner.New(runner.Options{Out: &screen, File: &file}))

	for name, m := range map[string]Machine{"local": {}, "remote": loopback} {
		file.Reset()
		_, err := m.Quiet().Run(ctx, "sh", "-c", "echo hunter2 >&2; exit 1")
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: err = %v, want a failure without output", name, err)
		}
		if strings.Contains(file.String(), "hunter2") || strings.Contains(screen.String(), "hunter2") {
			t.Errorf("%s: quiet output reached the runner", name)
		}
	}
}

func TestRemoteCommandsRunWithUmask077(t *testing.T) {
	out, err := loopback.Run(context.Background(), "umask")
	if err != nil || strings.TrimSpace(out) != "0077" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestRunWorksWithoutRunner(t *testing.T) {
	out, err := Machine{}.Run(context.Background(), "echo", "hi")
	if err != nil || out != "hi\n" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestRunTurnsExitCodeIntoError(t *testing.T) {
	for name, m := range map[string]Machine{"local": {}, "remote": loopback} {
		_, err := m.Run(context.Background(), "sh", "-c", "echo first; echo why >&2; echo; exit 3")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Fatalf("%s: err = %v, want exit code 3", name, err)
		}
		if got, want := err.Error(), "sh: exit status 3: why"; got != want {
			t.Errorf("%s: err = %q, want %q", name, got, want)
		}
	}
}

func TestRunReportsLaunchFailure(t *testing.T) {
	_, err := Machine{}.Run(context.Background(), "definitely-not-a-command")
	if err == nil || !strings.HasPrefix(err.Error(), "definitely-not-a-command: ") {
		t.Errorf("err = %v, want a launch error naming the command", err)
	}
}

func TestRemoteArgumentsSurviveTheShell(t *testing.T) {
	args := []string{"a b", "it's", "$HOME", `"q"`, "*", "", `back\slash`, "semi;colon", "new\nline"}
	out, err := loopback.Run(context.Background(), "printf", append([]string{"<%s>"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<" + strings.Join(args, "><") + ">"; out != want {
		t.Fatalf("got  %q\nwant %q", out, want)
	}
}

func TestRunStdin(t *testing.T) {
	for name, m := range map[string]Machine{"local": {}, "remote": loopback} {
		out, err := m.RunStdin(context.Background(), strings.NewReader("secret\n"), "cat")
		if err != nil || out != "secret\n" {
			t.Errorf("%s: got %q, %v", name, out, err)
		}
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Machine{}).Run(ctx, "sleep", "10"); err == nil {
		t.Fatal("want an error for a cancelled context")
	}
}

func TestSSHArgv(t *testing.T) {
	for _, addr := range []string{"192.0.2.10", "2001:db8::5", "fe80::1%eth0"} {
		argv := SSH(addr, HostPin{Host: "h", File: "/state/h.known_hosts"}).ssh
		if argv[0] != "ssh" || !slices.Equal(argv[len(argv)-3:], []string{"-l", "root", addr}) {
			t.Errorf("%s: argv = %q", addr, argv)
		}
		if !slices.Contains(argv, "BatchMode=yes") {
			t.Errorf("%s: ssh may prompt and garble the live area: %q", addr, argv)
		}
		if !slices.Contains(argv, "StrictHostKeyChecking=yes") || !slices.Contains(argv, "HostKeyAlias=h") {
			t.Errorf("%s: ssh must check the pin: %q", addr, argv)
		}
	}
}

func TestAgentIsForwardedOnlyWithAgent(t *testing.T) {
	plain := SSH("192.0.2.10", HostPin{Host: "h", File: "/state/h.known_hosts"})
	forwarded := plain.WithAgent()
	if !slices.Contains(plain.ssh, "ForwardAgent=no") || slices.Contains(plain.ssh, "ForwardAgent=yes") {
		t.Errorf("plain argv = %q, want the agent off", plain.ssh)
	}
	if !slices.Contains(forwarded.ssh, "ForwardAgent=yes") || slices.Contains(forwarded.ssh, "ForwardAgent=no") {
		t.Errorf("WithAgent argv = %q, want the agent on", forwarded.ssh)
	}
	if out, err := loopback.WithAgent().Run(context.Background(), "echo", "hi"); err != nil || out != "hi\n" {
		t.Errorf("loopback with agent: got %q, %v", out, err)
	}
}

func TestLostConnection(t *testing.T) {
	_, lost := Machine{}.Run(context.Background(), "sh", "-c", "exit 255")
	_, failed := Machine{}.Run(context.Background(), "sh", "-c", "exit 1")
	if !LostConnection(lost) || LostConnection(failed) || LostConnection(nil) {
		t.Errorf("LostConnection: 255 = %v, 1 = %v, nil = %v", LostConnection(lost), LostConnection(failed), LostConnection(nil))
	}
}

func TestHasCommand(t *testing.T) {
	m := Loopback()
	if !m.HasCommand(context.Background(), "sh") {
		t.Error("sh: want found")
	}
	if m.HasCommand(context.Background(), "no-such-command-here") {
		t.Error("no-such-command-here: want not found")
	}
}
