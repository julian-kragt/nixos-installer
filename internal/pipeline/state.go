package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"installer/internal/machine"
)

// State is what must survive a restart of the installer, per host. Choices
// the operator makes again on every run belong in Inputs.
type State struct {
	Host      string
	Completed []string // IDs of the stages that are done
	// HostRecipient is the age recipient of the target's own age key.
	HostRecipient string
	// UserRecipients maps each user on the host to the recipient of their own
	// age key on the target.
	UserRecipients map[string]string `json:",omitempty"`
	// SecretsCommit is the nix-secrets commit with the host's secrets.
	SecretsCommit string `json:",omitempty"`

	path string
}

// LoadState reads {dir}/{host}.json into a State. If the file does not exist,
// a fresh State for host is returned.
func LoadState(dir, host string) (*State, error) {
	s := &State{Host: host, path: filepath.Join(dir, host+".json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("state: unmarshal %s: %w", s.path, err)
	}
	return s, nil
}

// RemoveState deletes what is saved for host: its state file and its host key
// pin. The state goes first: a pin left behind only means a changed key to accept.
func RemoveState(dir, host string) error {
	for _, path := range []string{filepath.Join(dir, host+".json"), machine.NewHostPin(dir, host).File} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("state: remove: %w", err)
		}
	}
	return nil
}

// IsCompleted reports whether the stage with this ID is done.
func (s *State) IsCompleted(id string) bool { return slices.Contains(s.Completed, id) }

// Reopen drops ids from the completed stages and saves s, so they run again
// even when this run stops before them.
func (s *State) Reopen(ids ...string) error {
	s.Completed = slices.DeleteFunc(s.Completed, func(id string) bool { return slices.Contains(ids, id) })
	return s.Save()
}

// Path is the file s is saved to.
func (s *State) Path() string { return s.path }

// Save writes s as JSON to its file atomically.
func (s *State) Save() error {
	if s.path == "" {
		return errors.New("state: no file to save to (not from LoadState)")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("state: create dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("state: write: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	return nil
}
