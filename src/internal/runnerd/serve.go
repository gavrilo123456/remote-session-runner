package runnerd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/opshealth"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

const (
	linuxShutdownDrainTimeout   = 8 * time.Second
	linuxShutdownCleanupTimeout = 4 * time.Second
)

// NewLinuxExecutionService wires the selected Linux host-process adapter to
// the shared authority and environment registry. It is the only P046 runtime
// construction path; API handlers do not call the adapter directly.
func NewLinuxExecutionService(authority *store.AuthorityStore, options hostruntime.LinuxRuntimeOptions, environments ...domain.Environment) (*execution.Service, *LinuxSessionRuntime, error) {
	adapter, err := hostruntime.NewLinuxProcessAdapter(options)
	if err != nil {
		return nil, nil, err
	}
	runtimeAdapter, err := NewLinuxSessionRuntime(adapter)
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

// Run starts the configured Linux runnerd private API and the mandatory-mTLS
// direct HTTPS listener. Direct resource routes are added in later phases.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("runnerd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Linux runnerd YAML configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runnerd: --config is required")
		return 2
	}
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: load config: %v\n", err)
		return 1
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || loaded.Kind() != config.HostKindLinux {
		fmt.Fprintln(stderr, "runnerd: Linux host configuration is required")
		return 1
	}
	serverKey, ok := loaded.SecretReference(config.SecretLinuxServerTLSKey)
	if !ok {
		fmt.Fprintln(stderr, "runnerd: Linux server key reference is missing")
		return 1
	}
	ctx := context.Background()
	authorityDB, err := store.Open(ctx, settings.Database)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: open authority database: %v\n", err)
		return 1
	}
	defer authorityDB.Close()
	authority, err := store.NewAuthorityStore(authorityDB)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: construct authority store: %v\n", err)
		return 1
	}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			fmt.Fprintf(stderr, "runnerd: configured environment %q disappeared\n", name)
			return 1
		}
		environments = append(environments, registered.Policy())
	}
	profile, err := hostruntime.NewLinuxProcessProfile(hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		fmt.Fprintln(stderr, "runnerd: host process profile is not ready")
		return 1
	}
	doctorContext, cancelDoctor := context.WithTimeout(context.Background(), 30*time.Second)
	profileReport, profileErr := profile.Doctor(doctorContext)
	cancelDoctor()
	if profileErr != nil || !profileReport.Ready {
		fmt.Fprintln(stderr, "runnerd: host process profile is not ready")
		return 1
	}
	thresholds := opshealth.NewThresholdMonitor()
	healthReport := func(ctx context.Context) opshealth.Report {
		return linuxRunnerHealthReportWithMetrics(ctx, authority, true, true, thresholds)
	}
	service, _, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account:       settings.Account,
		WorkspaceRoot: settings.Workspaces,
		ShellPath:     "/usr/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: construct execution service: %v\n", err)
		return 1
	}
	reconciliation, err := service.ReconcileStartup(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: startup runtime reconciliation: %v\n", err)
		return 1
	}
	slog.Info("runnerd startup reconciliation complete",
		"sessions_inspected", reconciliation.SessionsInspected,
		"sessions_lost", reconciliation.SessionsLost,
		"commands_lost", reconciliation.CommandsLost,
		"commands_rejected", reconciliation.CommandsRejected,
		"cleanup_unconfirmed", reconciliation.CleanupUnconfirmed,
	)
	requestGate := lifecycle.NewGate()
	dispatchGate := lifecycle.NewGate()
	directHandler, err := newDirectHTTPSAPIHandler(service, requestGate, dispatchGate)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: construct direct HTTPS API: %v\n", err)
		return 1
	}
	directHandler = opshealth.Middleware("linux_runnerd", healthReport, directHandler)
	httpsServer, err := NewDirectHTTPSServer(DirectHTTPSServerOptions{
		BindAddress:        settings.DirectHTTPSBind,
		ServerCertificate:  settings.ServerCertificate,
		ServerPrivateKey:   serverKey.File,
		ClientCA:           settings.ClientCA,
		ClientPrincipalMap: settings.ClientPrincipalMap,
		Handler:            directHandler,
	})
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: configure direct HTTPS: %v\n", err)
		return 1
	}
	server, err := NewPrivateServer(PrivateServerOptions{
		Service: service, SocketPath: settings.PrivateSocket, HealthReport: healthReport,
		RequestGate: requestGate, DispatchGate: dispatchGate,
	})
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: construct private API: %v\n", err)
		return 1
	}
	if err := server.Listen(); err != nil {
		fmt.Fprintf(stderr, "runnerd: listen: %v\n", err)
		return 1
	}
	if err := httpsServer.Listen(); err != nil {
		_ = server.Close(context.Background())
		fmt.Fprintf(stderr, "runnerd: listen direct HTTPS: %v\n", err)
		return 1
	}
	hooks := &linuxShutdownHooks{
		private: server, https: httpsServer, service: service, authority: authority,
		requestGate: requestGate, dispatchGate: dispatchGate,
	}
	coordinator, err := lifecycle.NewCoordinator(hooks, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: linuxShutdownDrainTimeout, CleanupTimeout: linuxShutdownCleanupTimeout,
	})
	if err != nil {
		_ = httpsServer.Close(context.Background())
		_ = server.Close(context.Background())
		fmt.Fprintf(stderr, "runnerd: configure shutdown: %v\n", err)
		return 1
	}
	metricsObserverDone := make(chan struct{})
	go func() {
		defer close(metricsObserverDone)
		observeLinuxOperationalMetrics(signalContext, authority, thresholds)
	}()
	fmt.Fprintf(stdout, "runnerd private API listening on %s\n", settings.PrivateSocket)
	fmt.Fprintf(stdout, "runnerd direct HTTPS listening on %s (TLS 1.3, client certificate required)\n", httpsServer.Addr())
	serveErr := serveUntilCoordinator(signalContext, server, httpsServer, coordinator)
	stopSignals()
	<-metricsObserverDone
	if serveErr != nil {
		fmt.Fprintf(stderr, "runnerd: serve: %v\n", serveErr)
		return 1
	}
	return 0
}

func observeLinuxOperationalMetrics(ctx context.Context, authority *store.AuthorityStore, thresholds *opshealth.ThresholdMonitor) {
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
		thresholds.LogThresholds(slog.Default(), "linux_runnerd", metrics)
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

// serveUntilSignal is retained for listener-only tests. Production runnerd
// uses serveUntilCoordinator so session and command cleanup precedes stream
// closure and socket removal.
func serveUntilSignal(ctx context.Context, privateServer *PrivateServer, httpsServer *DirectHTTPSServer) error {
	return serveUntil(ctx, privateServer, httpsServer, func() error {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return errors.Join(httpsServer.Close(closeContext), privateServer.Close(closeContext))
	})
}

func serveUntilCoordinator(ctx context.Context, privateServer *PrivateServer, httpsServer *DirectHTTPSServer, coordinator *lifecycle.Coordinator) error {
	if coordinator == nil {
		return errors.New("runnerd shutdown coordinator is not configured")
	}
	return serveUntil(ctx, privateServer, httpsServer, func() error {
		return coordinator.Shutdown(context.Background())
	})
}

func serveUntil(ctx context.Context, privateServer *PrivateServer, httpsServer *DirectHTTPSServer, shutdown func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if privateServer == nil || httpsServer == nil || shutdown == nil {
		return errors.New("runnerd listeners or shutdown action are not configured")
	}
	serveErrors := make(chan error, 2)
	go func() { serveErrors <- privateServer.Serve() }()
	go func() { serveErrors <- httpsServer.Serve() }()

	remaining := 2
	var serveErr error
	select {
	case serveErr = <-serveErrors:
		remaining--
		if ctx.Err() == nil && serveErr == nil {
			serveErr = errors.New("runnerd listener stopped unexpectedly")
		}
	case <-ctx.Done():
	}

	shutdownErr := shutdown()

	for i := 0; i < remaining; i++ {
		select {
		case err := <-serveErrors:
			serveErr = errors.Join(serveErr, err)
		case <-time.After(2 * time.Second):
			serveErr = errors.Join(serveErr, errors.New("runnerd listener did not stop after close"))
		}
	}
	return errors.Join(serveErr, shutdownErr)
}
