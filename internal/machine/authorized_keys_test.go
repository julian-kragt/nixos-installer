package machine

import (
	"errors"
	"testing"
)

func TestAuthError(t *testing.T) {
	rejected := errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain")
	if err := authError(rejected); !errors.Is(err, ErrAuth) || !errors.Is(err, rejected) {
		t.Errorf("rejected password: err = %v, want ErrAuth wrapping the cause", err)
	}
	other := errors.New("ssh: handshake failed: EOF")
	if err := authError(other); err != other {
		t.Errorf("other failure: err = %v, want it unchanged", err)
	}
}
