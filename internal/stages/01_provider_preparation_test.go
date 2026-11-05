package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/machine"
)

func TestCheckDisks(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sda")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDisks(context.Background(), machine.Loopback(), "h", nil); err != nil {
		t.Errorf("no disks: %v", err)
	}
	// Block devices cannot be created in a test, so only the refusals are covered.
	for name, devices := range map[string]map[string]string{
		"plain file":     {"main": file},
		"missing device": {"main": "/dev/disk/nope/REPLACE_ME"},
	} {
		err := checkDisks(context.Background(), machine.Loopback(), "h", devices)
		if err == nil || !strings.Contains(err.Error(), "not a block device") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
