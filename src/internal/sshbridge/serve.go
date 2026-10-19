package sshbridge

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

// Run serves one forced-command SSH bridge session using a private runnerd
// Unix socket and an owner-only key/controller map.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-ssh-bridge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	stdio := flags.Bool("stdio", false, "serve the versioned bridge protocol on standard input/output")
	authenticatedKey := flags.String("authenticated-key", "", "server-supplied identity bound to the forced authorized-key entry")
	controllerMapPath := flags.String("controller-map", "", "owner-only key/controller map")
	runnerdSocketPath := flags.String("runnerd-socket", "", "owner-only runnerd Unix socket")
	catalogHost := flags.String("catalog-host", "", "fixed canonical host identity supplied by the installed wrapper")
	catalogLane := flags.String("catalog-lane", "", "fixed release lane supplied by the installed wrapper")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if !*stdio || flags.NArg() != 0 || !validKeyFingerprint(*authenticatedKey) || *controllerMapPath == "" || *runnerdSocketPath == "" {
		fmt.Fprintln(stderr, "runner-ssh-bridge: --stdio, a server-supplied authenticated key, controller map, and runnerd socket are required")
		return 2
	}
	if (*catalogHost == "") != (*catalogLane == "") {
		fmt.Fprintln(stderr, "runner-ssh-bridge: catalog host and lane identity must be supplied together")
		return 2
	}
	if *catalogHost != "" {
		plan, err := laneinstall.BuildCatalogPlan(*catalogHost, config.ReleaseLane(*catalogLane))
		if err != nil {
			fmt.Fprintln(stderr, "runner-ssh-bridge: installed catalog identity is invalid")
			return 78
		}
		executable, err := os.Executable()
		if err != nil || laneinstall.ValidateSelectedCatalogExecutableForPlan(plan, "runner-ssh-bridge", executable) != nil {
			fmt.Fprintln(stderr, "runner-ssh-bridge: running binary does not match the validated lane catalog selector")
			return 78
		}
	}
	controllers, err := LoadKeyControllerMap(*controllerMapPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner-ssh-bridge: load controller map: %v\n", err)
		return 1
	}
	if _, err := controllers.ControllerForKey(*authenticatedKey); err != nil {
		fmt.Fprintf(stderr, "runner-ssh-bridge: authenticated key is not mapped: %v\n", err)
		return 1
	}
	forwarder, err := NewUnixSocketForwarder(*runnerdSocketPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner-ssh-bridge: configure runnerd forwarder: %v\n", err)
		return 1
	}
	server, err := NewServer(ServerOptions{Controllers: controllers, Handler: forwarder})
	if err != nil {
		fmt.Fprintf(stderr, "runner-ssh-bridge: configure bridge server: %v\n", err)
		return 1
	}
	if err := server.Serve(context.Background(), *authenticatedKey, stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "runner-ssh-bridge: serve: %v\n", err)
		return 1
	}
	return 0
}
