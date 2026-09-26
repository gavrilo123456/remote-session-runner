package runnerd

import (
	"context"
	"flag"
	"fmt"
	"io"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
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

// Run starts the configured Linux runnerd private API. Public HTTPS and the
// SSH bridge remain separate later ingress phases.
func Run(args []string, stdout, stderr io.Writer) int {
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
	service, _, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account:       settings.Account,
		WorkspaceRoot: settings.Workspaces,
		ShellPath:     "/usr/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: construct execution service: %v\n", err)
		return 1
	}
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: settings.PrivateSocket})
	if err != nil {
		fmt.Fprintf(stderr, "runnerd: construct private API: %v\n", err)
		return 1
	}
	if err := server.Listen(); err != nil {
		fmt.Fprintf(stderr, "runnerd: listen: %v\n", err)
		return 1
	}
	defer server.Close(context.Background())
	fmt.Fprintf(stdout, "runnerd listening on %s\n", settings.PrivateSocket)
	if err := server.Serve(); err != nil {
		fmt.Fprintf(stderr, "runnerd: serve: %v\n", err)
		return 1
	}
	return 0
}
