//go:build darwin

package proxy_test

import "testing"

// SkipDarwin skips test on macOS systems.
func skipDarwin(tb testing.TB) {
	tb.Skipf("skipping; not supported for darwin")
}
