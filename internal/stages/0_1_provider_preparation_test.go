package stages

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"installer/internal/machine"
)

func TestDiskRows(t *testing.T) {
	const lsblk = `{"blockdevices": [
		{"path":"/dev/loop0", "name":"loop0", "size":"1.5G", "model":null, "type":"loop"},
		{"path":"/dev/sda", "name":"sda", "size":"59.8G", "model":"Type-C", "type":"disk"},
		{"path":"/dev/nvme0n1", "name":"nvme0n1", "size":"476.9G", "model":"PC SN730 NVMe WDC 512GB", "type":"disk"},
		{"path":"/dev/vda", "name":"vda", "size":"20G", "model":null, "type":"disk"}
	]}`
	for name, tc := range map[string]struct {
		erased []string
		want   []string
	}{
		"one erased": {
			erased: []string{"/dev/nvme0n1"},
			want: []string{
				"sda      59.8G  Type-C",
				"nvme0n1 476.9G  PC SN730 NVMe WDC 512GB  (will be erased)",
				"vda        20G",
			},
		},
		"erased without a model": {
			erased: []string{"/dev/vda"},
			want: []string{
				"sda      59.8G  Type-C",
				"nvme0n1 476.9G  PC SN730 NVMe WDC 512GB",
				"vda        20G  (will be erased)",
			},
		},
		"erased is not a listed disk": {
			erased: []string{"/dev/loop0"},
			want: []string{
				"sda      59.8G  Type-C",
				"nvme0n1 476.9G  PC SN730 NVMe WDC 512GB",
				"vda        20G",
			},
		},
	} {
		got, err := diskRows(lsblk, tc.erased)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, tc.want)
		}
	}

	if _, err := diskRows("not json", nil); err == nil {
		t.Error("invalid JSON: no error")
	}
}

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
