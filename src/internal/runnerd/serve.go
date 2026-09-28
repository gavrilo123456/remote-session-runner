package runnerd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/opshealth"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
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
	healthReport := func(ctx context.Context) opshealth.Report {
		return linuxRunnerHealthReport(ctx, authority, true, true)
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
	directHandler, err := NewDirectHTTPSAPIHandler(service)
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
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: settings.PrivateSocket, HealthReport: healthReport})
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
	fmt.Fprintf(stdout, "runnerd private API listening on %s\n", settings.PrivateSocket)
	fmt.Fprintf(stdout, "runnerd direct HTTPS listening on %s (TLS 1.3, client certificate required)\n", httpsServer.Addr())
	if err := serveUntilSignal(signalContext, server, httpsServer); err != nil {
		fmt.Fprintf(stderr, "runnerd: serve: %v\n", err)
		return 1
	}
	return 0
}

// serveUntilSignal owns the two listener goroutines. Closing the private
// server removes only its own Unix socket, allowing systemd to restart the
// service without unlinking an unknown path. P132 adds coordinated execution
// draining; this boundary only stops ingress listeners cleanly.
func serveUntilSignal(ctx context.Context, privateServer *PrivateServer, httpsServer *DirectHTTPSServer) error {
	if ctx == nil {
		ctx = context.Background()
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

	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	closeErr := errors.Join(httpsServer.Close(closeContext), privateServer.Close(closeContext))
	cancel()

	for i := 0; i < remaining; i++ {
		select {
		case err := <-serveErrors:
			serveErr = errors.Join(serveErr, err)
		case <-time.After(6 * time.Second):
			serveErr = errors.Join(serveErr, errors.New("runnerd listener did not stop after close"))
		}
	}
	return errors.Join(serveErr, closeErr)
}
