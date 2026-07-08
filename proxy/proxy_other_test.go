//go:build !darwin

package proxy_test

import "testing"

// SkipDarwin skips test on macOS systems.
func skipDarwin(tb testing.TB) {}
