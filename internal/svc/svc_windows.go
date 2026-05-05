//go:build windows

package svc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// IsService reports whether the current process was launched by the SCM.
func IsService() bool {
	is, _ := svc.IsWindowsService()
	return is
}

// Install creates the service in the SCM, pointed at exePath, and
// registers it as an Event Log source. args are baked into the
// service's command line and passed to the binary on every start.
//
// SECURITY: args land in the SCM's ImagePath, which is readable by any
// user with SeQueryServiceConfigPrivilege (and visible via
// `sc.exe qc <name>`). Don't pass long-lived secrets here for
// production deployments — prefer a config file with restricted ACLs
// once that's available.
func Install(cfg Config, exePath string, args []string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("svc: connect SCM: %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(cfg.Name); err == nil {
		s.Close()
		return fmt.Errorf("svc: service %q already installed", cfg.Name)
	}

	s, err := m.CreateService(cfg.Name, exePath, mgr.Config{
		DisplayName: cfg.DisplayName,
		Description: cfg.Description,
		StartType:   mgr.StartAutomatic,
	}, args...)
	if err != nil {
		return fmt.Errorf("svc: create service: %w", err)
	}
	defer s.Close()

	if err := eventlog.InstallAsEventCreate(cfg.Name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// Roll back the service if event log registration failed.
		_ = s.Delete()
		return fmt.Errorf("svc: register event log source: %w", err)
	}
	return nil
}

// Uninstall stops the service if it's running, removes it from the SCM,
// and unregisters the event log source.
func Uninstall(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("svc: connect SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("svc: open service: %w", err)
	}
	defer s.Close()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("svc: delete service: %w", err)
	}
	if err := eventlog.Remove(name); err != nil {
		return fmt.Errorf("svc: remove event log source: %w", err)
	}
	return nil
}

// EventLogHandler returns a slog.Handler that writes to the Windows
// Event Log under the given source name. The source must already be
// registered (Install does this).
func EventLogHandler(source string) (slog.Handler, error) {
	elog, err := eventlog.Open(source)
	if err != nil {
		return nil, fmt.Errorf("svc: open event log: %w", err)
	}
	return &eventLogHandler{elog: elog, level: slog.LevelInfo}, nil
}

// Run starts runFn under the SCM. The function should respect ctx
// cancellation: when the SCM asks the service to stop, ctx is cancelled
// and Run waits for runFn to return before reporting Stopped.
func Run(cfg Config, log *slog.Logger, runFn func(ctx context.Context) error) error {
	if log == nil {
		log = slog.Default()
	}
	return svc.Run(cfg.Name, &handler{cfg: cfg, log: log, runFn: runFn})
}

type handler struct {
	cfg   Config
	log   *slog.Logger
	runFn func(ctx context.Context) error
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- h.runFn(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: accepts}
	h.log.Info("ssv: service started")

loop:
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				h.log.Info("ssv: service stop requested")
				break loop
			default:
				h.log.Warn("ssv: unexpected control request", "cmd", c.Cmd)
			}
		case err := <-runErr:
			if err != nil {
				h.log.Error("ssv: exporter exited unexpectedly", "err", err)
				status <- svc.Status{State: svc.Stopped}
				cancel()
				return false, 1
			}
			break loop
		}
	}

	status <- svc.Status{State: svc.StopPending}
	cancel()
	<-runErr
	status <- svc.Status{State: svc.Stopped}
	return false, 0
}

// eventLogHandler implements slog.Handler over a Windows Event Log
// source. It maps slog levels to the three Event Log severities.
type eventLogHandler struct {
	elog  *eventlog.Log
	level slog.Level
	attrs []slog.Attr
	group string
}

func (h *eventLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *eventLogHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})

	msg := b.String()
	switch {
	case r.Level >= slog.LevelError:
		return h.elog.Error(1, msg)
	case r.Level >= slog.LevelWarn:
		return h.elog.Warning(2, msg)
	default:
		return h.elog.Info(3, msg)
	}
}

func (h *eventLogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), as...)
	return &clone
}

func (h *eventLogHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.group = name
	return &clone
}
