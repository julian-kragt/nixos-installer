package stages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/julian-kragt/nixos-installer/internal/commands/hostidentity"
	"github.com/julian-kragt/nixos-installer/internal/commands/sops"
	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// rekeyShared encrypts each shared user file of the host's users, and each the
// host's users left (State.LeftShared), to the recipients its rule now gives
// it, where they differ from those it is encrypted to. Only a key that already
// reads a file can do that, so it runs on another host the file is encrypted
// to, over ssh: the plaintext stays there. A file that no other host reads, as
// after the only host was installed again, is left to be written from its
// template. sopsConfig is the clone's .sops.yaml, which cfg holds.
func rekeyShared(ctx context.Context, env *pipeline.Env, layout secrets.Layout, cfg *secrets.Config, sopsConfig []byte, users []string) error {
	if !layout.HasShared() && len(env.State.LeftShared) == 0 {
		return nil
	}
	var files []string
	for _, f := range layout.HostFiles(env.Host, users) {
		if f.Shared {
			files = append(files, f.Path)
		}
	}
	for _, f := range env.State.LeftShared {
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	for _, f := range files {
		err := runner.Run(ctx, "Check the recipients of "+f, func(ctx context.Context) error {
			return rekeyFile(ctx, env, cfg, sopsConfig, f)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// rekeyFile is rekeyShared for one file.
func rekeyFile(ctx context.Context, env *pipeline.Env, cfg *secrets.Config, sopsConfig []byte, file string) error {
	ciphertext, err := os.ReadFile(filepath.Join(env.SecretsDir, file))
	if errors.Is(err, os.ErrNotExist) {
		return nil // written from its template, or deleted by 2.2
	}
	if err != nil {
		return err
	}
	if sops.CheckCiphertext(ciphertext) != nil {
		return nil // written from its template
	}
	want, err := cfg.Recipients(file)
	if err != nil {
		return err
	}
	have, err := sops.Recipients(ciphertext)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if sameSet(have, want) {
		return nil
	}

	hosts, err := cfg.RuleHosts(file)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(hosts, func(h secrets.HostKey) bool {
		return h.Host != env.Host && slices.Contains(have, h.Recipient)
	})
	if i < 0 {
		runner.Warn(ctx, file+": no other host in its rule can read it, so it is written again from its template")
		return nil
	}
	holder := hosts[i].Host
	// A removed recipient may have kept the data key: give the file a new one.
	rotate := slices.ContainsFunc(have, func(r string) bool { return !slices.Contains(want, r) })

	m, err := holderMachine(ctx, env, holder)
	if err != nil {
		return err
	}
	out, err := sops.UpdateKeys(ctx, m, hostidentity.KeyFile, sopsConfig, file, ciphertext, rotate)
	if err != nil {
		return fmt.Errorf("%s on %s: %w", file, holder, err)
	}
	if problem, err := sops.StaticProblem(out, want); problem != "" || err != nil {
		return fmt.Errorf("%s as %s encrypted it: %s", file, holder, joinProblems(problem, err))
	}
	if err := os.WriteFile(filepath.Join(env.SecretsDir, file), out, 0o644); err != nil {
		return err
	}
	runner.Info(ctx, fmt.Sprintf("Encrypted %s to its new recipients on %s", file, holder))
	return nil
}

// holderMachine is root on host, an installed host the installer reaches over
// ssh at the address --rekey-via gives it, or at its name. Its host key is the
// one pinned when the installer installed it; without one, the operator
// confirms the key it offers, which is then pinned.
func holderMachine(ctx context.Context, env *pipeline.Env, host string) (machine.Machine, error) {
	addr := host
	if a, ok := env.RekeyVia[host]; ok {
		addr = a
	}
	pin := machine.NewHostPin(filepath.Dir(env.State.Path()), host)
	key, err := pin.Load()
	if err != nil {
		return machine.Machine{}, err
	}
	if key == nil {
		offered, err := machine.ScanHostKey(ctx, addr)
		if err != nil {
			return machine.Machine{}, fmt.Errorf("%s at %s: %w (give its address with --rekey-via %s=<address>)", host, addr, err, host)
		}
		runner.Info(ctx, fmt.Sprintf("%s at %s offers the host key %s, and none is pinned for it", host, addr, machine.Fingerprint(offered)))
		err = runner.RequireTyped(ctx, "Type 'trust' to trust this key for "+host+": ", "trust",
			errors.New("confirmation required: run the installer in a terminal"),
			errors.New("aborted: the host key of "+host+" was not trusted"))
		if err != nil {
			return machine.Machine{}, err
		}
		if err := pin.Save(offered); err != nil {
			return machine.Machine{}, err
		}
	}
	return machine.SSH(addr, pin), nil
}

// sameSet reports whether a and b hold the same strings, in any order.
func sameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}
