package runnerlocald

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

// runPrepareControlledRestart records the deliberately narrow durable restart
// plan before the installer hard-stops the old local executor. It performs no
// process action, does not execute a command, and prints only resource IDs.
func runPrepareControlledRestart(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-locald prepare-controlled-restart", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: --config is required")
		return 2
	}

	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: could not load the active Mac configuration")
		return 1
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac || settings.Account != config.MacAccount || settings.Database == "" {
		fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: active Mac configuration is invalid")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := store.Open(ctx, settings.Database)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: could not open the active authority database")
		return 1
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: could not construct the authority store")
		return 1
	}
	pairs, err := authority.ListRetainedLostRuntimeRecoveryPairs(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: could not read retained lost capacity")
		return 1
	}
	plan, err := authority.PrepareControlledRestartPlan(ctx, pairs)
	if err != nil {
		if errors.Is(err, store.ErrControlledRestartPlanConflict) {
			fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: a different controlled restart plan is already active")
		} else {
			fmt.Fprintln(stderr, "runner-locald prepare-controlled-restart: current local state is not eligible for controlled restart")
		}
		return 1
	}

	fmt.Fprintln(stdout, "runner-locald prepare-controlled-restart: durable plan prepared")
	fmt.Fprintf(stdout, "job_id: %s\n", plan.JobID)
	fmt.Fprintf(stdout, "session_id: %s\n", plan.SessionID)
	fmt.Fprintf(stdout, "command_id: %s\n", plan.CommandID)
	fmt.Fprintf(stdout, "lost_pair_count: %d\n", len(plan.LostPairs))
	return 0
}
