package tunnel

import "time"

// SetKeepaliveForTest shrinks the keepalive interval and miss count for
// tests; call the returned restore (e.g. via t.Cleanup) to put them back.
func SetKeepaliveForTest(interval time.Duration, maxMisses int) (restore func()) {
	oldInterval, oldMisses := keepaliveInterval, keepaliveMaxMisses
	keepaliveInterval, keepaliveMaxMisses = interval, maxMisses
	return func() { keepaliveInterval, keepaliveMaxMisses = oldInterval, oldMisses }
}
