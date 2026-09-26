package runnerlocald

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
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, Owner: owner, SocketPath: settings.LocalDSocket})
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald: construct private API: %v\n", err)
		return 1
	}
	if err := server.Listen(); err != nil {
		fmt.Fprintf(stderr, "runner-locald: listen: %v\n", err)
		return 1
	}
	defer server.Close(context.Background())
	fmt.Fprintf(stdout, "runner-locald listening on %s\n", settings.LocalDSocket)
	if err := server.Serve(); err != nil {
		fmt.Fprintf(stderr, "runner-locald: serve: %v\n", err)
		return 1
	}
	return 0
}
