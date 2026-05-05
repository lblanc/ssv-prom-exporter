//go:build !windows

package svc

import (
	"context"
	"errors"
	"log/slog"
)

// IsService reports whether the current process was launched by the
// Service Control Manager. Always false on non-Windows.
func IsService() bool { return false }

// Install registers the service in the SCM. Not supported off Windows.
func Install(_ Config, _ string, _ []string) error {
	return errors.New("svc: install is only supported on Windows")
}

// Uninstall removes the service from the SCM. Not supported off Windows.
func Uninstall(_ string) error {
	return errors.New("svc: uninstall is only supported on Windows")
}

// Run executes runFn under the service runtime on Windows; on other
// platforms it just calls runFn directly.
func Run(_ Config, _ *slog.Logger, runFn func(ctx context.Context) error) error {
	return runFn(context.Background())
}

// EventLogHandler returns a slog.Handler that writes to the Windows
// Event Log. On non-Windows it returns nil so callers can fall back to
// their default handler.
func EventLogHandler(_ string) (slog.Handler, error) {
	return nil, errors.New("svc: event log is only supported on Windows")
}
