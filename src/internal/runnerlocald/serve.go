package runnerlocald

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/opshealth"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"syscall"
	"time"
)

const (
	macShutdownDrainTimeout   = 8 * time.Second
	macShutdownCleanupTimeout = 5 * time.Second
)

// NewMacExecutionService wires the shared execution service to the Mac
// process adapter and configured environment registry.
func NewMacExecutionService(authority *store.AuthorityStore, options hostruntime.MacRuntimeOptions, environments ...domain.Environment) (*execution.Service, *MacSessionRuntime, error) {
	adapter, err := hostruntime.NewMacProcessAdapter(options)
	if err != nil {
		return nil, nil, err
	}
	runtimeAdapter, err := NewMacSessionRuntime(adapter)
	if err != nil {
		return nil, nil, err
	}
	registry, err := execution.NewEnvironmentRegistry(environments...)
	if err != nil {
		return nil, nil, err
	}
	service, err := execution.NewExecutionService(authority, runtimeAdapter, registry, execution.RealClock{}, nil)
	if err != nil {
		return nil, nil, err
	}
	return service, runtimeAdapter, nil
}

// Run starts the owner-only Mac runner-locald private API.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("runner-locald", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald: --config is required")
		return 2
	}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: load config: %v\n", err)
		return 1
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac {
		fmt.Fprintln(stderr, "runner-locald: Mac host configuration is required")
		return 1
	}
	ctx := context.Background()
	authorityDB, err := store.Open(ctx, settings.Database)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: open authority database: %v\n", err)
		return 1
	}
	defer authorityDB.Close()
	authority, err := store.NewAuthorityStore(authorityDB)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct authority store: %v\n", err)
		return 1
	}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			fmt.Fprintf(stderr, "runner-locald: configured environment %q disappeared\n", name)
			return 1
		}
		environments = append(environments, registered.Policy())
	}
	service, _, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{Account: settings.Account, WorkspaceRoot: settings.Workspaces, ShellPath: "/bin/bash"}, environments...)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct execution service: %v\n", err)
		return 1
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID(settings.Account))
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: owner controller: %v\n", err)
		return 1
	}
	thresholds := opshealth.NewThresholdMonitor()
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, Owner: owner, SocketPath: settings.LocalDSocket, Thresholds: thresholds})
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct private API: %v\n", err)
		return 1
	}
	if err := server.Listen(); err != nil {
		fmt.Fprintf(stderr, "runner-locald: listen: %v\n", err)
		return 1
	}
	metricsObserverDone := make(chan struct{})
	go func() {
		defer close(metricsObserverDone)
		observeMacLocalOperationalMetrics(signalContext, authority, thresholds)
	}()
	fmt.Fprintf(stdout, "runner-locald listening on %s\n", settings.LocalDSocket)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve() }()
	coordinator, err := lifecycle.NewCoordinator(&privateServerShutdown{server: server}, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: macShutdownDrainTimeout, CleanupTimeout: macShutdownCleanupTimeout,
	})
	if err != nil {
		stop()
		_ = server.Close(context.Background())
		<-metricsObserverDone
		fmt.Fprintf(stderr, "runner-locald: configure shutdown: %v\n", err)
		return 1
	}
	select {
	case serveErr := <-serveErrors:
		shutdownErr := coordinator.Shutdown(context.Background())
		stop()
		<-metricsObserverDone
		if serveErr != nil || shutdownErr != nil {
			fmt.Fprintf(stderr, "runner-locald: serve: %v\n", errors.Join(serveErr, shutdownErr))
			return 1
		}
	case <-signalContext.Done():
		shutdownErr := coordinator.Shutdown(context.Background())
		serveErr := <-serveErrors
		<-metricsObserverDone
		if shutdownErr != nil || serveErr != nil {
			fmt.Fprintf(stderr, "runner-locald: shutdown: %v\n", errors.Join(shutdownErr, serveErr))
			return 1
		}
	}
	return 0
}

type privateServerShutdown struct {
	server  *PrivateServer
	stopErr error
}

func (h *privateServerShutdown) StopAccepting() {
	if h != nil && h.server != nil {
		h.stopErr = h.server.StopAccepting()
	}
}

func (h *privateServerShutdown) StopDispatch() {
	if h != nil && h.server != nil {
		h.server.StopDispatch()
	}
}

func (h *privateServerShutdown) Drain(ctx context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return errors.Join(h.stopErr, h.server.Drain(ctx))
}

func (h *privateServerShutdown) CancelRemaining(ctx context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return h.server.CancelRemaining(ctx)
}

func (h *privateServerShutdown) Flush(ctx context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return h.server.Flush(ctx)
}

func (h *privateServerShutdown) CloseStreams(context.Context) error {
	if h == nil || h.server == nil {
		return errors.New("runner-locald shutdown is not configured")
	}
	return h.server.CloseStreams()
}

func observeMacLocalOperationalMetrics(ctx context.Context, authority *store.AuthorityStore, thresholds *opshealth.ThresholdMonitor) {
	if ctx == nil {
		ctx = context.Background()
	}
	observe := func() {
		if authority == nil {
			return
		}
		durable, err := authority.ReadOperationalMetrics(ctx)
		if err != nil {
			return
		}
		metrics := opshealth.Metrics{
			ActiveSessionSlots: durable.ActiveSessionSlots, ActiveCommandSlots: durable.ActiveCommandSlots,
			QueuedCommands: durable.QueuedCommands, QueuedIntents: durable.QueuedIntents,
			DispatchAttemptsTotal: durable.DispatchAttemptsTotal, ReconciliationAgeSeconds: durable.ReconciliationAgeSeconds,
			EventLagEvents: durable.EventLagEvents, EventGapsTotal: durable.EventGapsTotal,
			OutputTruncationsTotal: durable.OutputTruncationsTotal,
			StorageErrorsTotal:     durable.StorageErrorsTotal, CleanupFailuresTotal: durable.CleanupFailuresTotal,
			MailboxBacklog: durable.MailboxBacklog,
		}
		thresholds.LogThresholds(slog.Default(), "mac_local_executor", metrics)
	}
	observe()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			observe()
		}
	}
}
