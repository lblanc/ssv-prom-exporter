// Package svc wraps the Windows service runtime so the exporter can run
// either as a console process or under the Service Control Manager,
// from the same binary.
//
// The OS-specific implementation lives in svc_windows.go (real svc/mgr/
// eventlog calls). On non-Windows builds, svc_other.go provides stubs:
// IsService is always false and Run just calls the inner function. This
// lets cmd/ssv-prom-exporter import this package unconditionally and
// keeps the Linux build green.
package svc

// Config describes the service-mode integration: the SCM service name,
// the display name shown in services.msc, and the description.
type Config struct {
	Name        string
	DisplayName string
	Description string
}
