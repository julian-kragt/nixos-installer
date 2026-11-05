// Package mkpasswd turns a login password into the crypt hash NixOS stores.
package mkpasswd

import (
	"context"
	"fmt"
	"strings"

	"installer/internal/machine"
)

// Hash returns the yescrypt hash of password, computed on m. The password goes
// over stdin and the command is quiet, so neither reaches args or the log.
func Hash(ctx context.Context, m machine.Machine, password string) (string, error) {
	out, err := m.Quiet().RunStdin(ctx, strings.NewReader(password), "mkpasswd", "--method=yescrypt", "--stdin")
	if err != nil {
		return "", fmt.Errorf("hash the password: %w", err)
	}
	hash := strings.TrimSpace(out)
	if !strings.HasPrefix(hash, "$y$") {
		return "", fmt.Errorf("hash the password: mkpasswd printed something that is not a yescrypt hash")
	}
	return hash, nil
}
