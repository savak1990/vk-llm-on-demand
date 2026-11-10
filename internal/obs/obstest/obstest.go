package obstest

import (
	"bytes"
	"testing"
)

// failer is the subset of *testing.T that AssertNoSecret needs, so a test
// double can verify its own failure behavior without failing for real.
type failer interface {
	Helper()
	Errorf(format string, args ...any)
}

// AssertNoSecret fails t if any secret appears as a literal substring of
// logOutput. Use it on captured log bytes, never on response bodies (those
// get their own assertions) or on partial identifiers like a key id.
func AssertNoSecret(t *testing.T, logOutput []byte, secrets ...string) {
	t.Helper()
	assertNoSecretOn(t, logOutput, secrets...)
}

func assertNoSecretOn(t failer, logOutput []byte, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if bytes.Contains(logOutput, []byte(s)) {
			t.Errorf("log output contains a secret value (len %d): leaked", len(s))
		}
	}
}
