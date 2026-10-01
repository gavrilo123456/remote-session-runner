package runnerd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// runRecoverRetainedCapacity performs the narrow online repair for a fully
// occupied set of terminal-lost runtime pairs while runnerd.service remains
// active. It deliberately does not take the runnerd lifecycle lock: the
// installed daemon must keep serving and its dispatcher may claim preserved
// queued work only after the P1 atomic release commits.
//
// This is an owner-only local operator command. It is not exposed through the
// direct HTTPS API, the SSH bridge, or the mailbox schema. The recovery service
// never executes stored script bytes or accepts a new command identity.
func runRecoverRetainedCapacity(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runnerd recover-retained-capacity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Linux runner configuration")
	online := flags.Bool("online", false, "confirm that runnerd.service remains active during recovery")
	apply := flags.Bool("apply", false, "perform the explicit online retained-capacity recovery")
	var pairValues repeatedRecoveryValue
	flags.Var(&pairValues, "lost-pair", "terminal lost SESSION_ID:COMMAND_ID pair; repeat for every retained lost runtime")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*online || !*apply || flags.NArg() != 0 || len(pairValues) == 0 {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: --config, --online, --apply, and at least one --lost-pair are required")
		return 2
	}
	lostRequests, err := parseLostRecoveryPairs(pairValues)
	if err != nil {
		// Do not echo arbitrary command-line text. A valid pair is reported on
		// success; malformed input receives only a stable usage reason.
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: invalid --lost-pair")
		return 2
	}

	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: configuration is not ready")
		return 1
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || loaded.Kind() != config.HostKindLinux {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: Linux host configuration is required")
		return 1
	}
	// Do not acquire runnerd's lifecycle lock here. That lock belongs to the
	// installed daemon and would either block this online repair or make the
	// service's active state ambiguous. The active-service predicate is an
	// explicit guard before opening the authority store.
	if err := requireRunnerdServiceActive(); err != nil {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: runnerd.service is not active")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profile, err := hostruntime.NewLinuxProcessProfile(hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	})
	if err != nil {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: host process profile is not ready")
		return 1
	}
	profileReport, err := profile.Doctor(ctx)
	if err != nil || !profileReport.Ready {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: host process profile is not ready")
		return 1
	}
	// Online maintenance must never migrate, create, or repair the authority
	// database while runnerd.service has it open. It may proceed only against
	// the already-current, owner-only database selected by the active service.
	database, err := store.OpenExistingCurrent(ctx, settings.Database)
	if err != nil {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: authority database is unavailable")
		return 1
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: authority store is unavailable")
		return 1
	}
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			fmt.Fprintln(stderr, "runnerd recover-retained-capacity: configured environment is unavailable")
			return 1
		}
		environments = append(environments, registered.Policy())
	}
	service, _, err := NewLinuxExecutionService(authority, hostruntime.LinuxRuntimeOptions{
		Account: settings.Account, ServiceRoot: settings.ServiceRoot, WorkspaceRoot: settings.Workspaces, ShellPath: "/usr/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: runtime recovery adapter is unavailable")
		return 1
	}
	results, err := service.RecoverLostRuntimeBatchPreservingQueuedOneOffs(ctx, lostRequests)
	if err != nil {
		fmt.Fprintf(stderr, "runnerd recover-retained-capacity: recovery failed: reason=%s\n", retainedCapacityRecoveryFailureReason(err))
		return 1
	}
	if len(results) != len(lostRequests) {
		fmt.Fprintln(stderr, "runnerd recover-retained-capacity: recovery failed: reason=operation_failed")
		return 1
	}
	recovered, alreadyRecovered := recoveryResultCounts(results)
	for index, request := range lostRequests {
		status := "recovered"
		if results[index].AlreadyRecovered {
			status = "already_recovered"
		}
		fmt.Fprintf(stdout, "runnerd recover-retained-capacity: %s session_id=%s command_id=%s\n", status, request.SessionID, request.CommandID)
	}
	fmt.Fprintf(stdout, "runnerd recover-retained-capacity: recovered_lost_pairs=%d already_recovered_lost_pairs=%d\n", recovered, alreadyRecovered)
	return 0
}

// requireRunnerdServiceActive confirms that the separate helper is used only
// against a live systemd runnerd service. This is a preflight observation, not
// an interprocess liveness lock: runnerd may change state after the check. The
// P1 immediate authority transaction is the safety boundary that prevents a
// dispatcher claim until all selected retained capacity is released together.
// This helper intentionally neither stops the service nor checks/acquires the
// lifecycle lock, because both actions would defeat online recovery.
func requireRunnerdServiceActive() error {
	return requireRunnerdServiceActiveWith(runnerdSystemdValue)
}

func requireRunnerdServiceActiveWith(systemdValue func(string) (string, error)) error {
	activeState, err := systemdValue("ActiveState")
	if err != nil {
		return fmt.Errorf("read runnerd.service active state: %w", err)
	}
	if activeState != "active" {
		return fmt.Errorf("runnerd.service state=%q; online retained-capacity recovery requires it to remain active", activeState)
	}
	mainPID, err := systemdValue("MainPID")
	if err != nil {
		return fmt.Errorf("read runnerd.service main PID: %w", err)
	}
	pid, err := strconv.ParseInt(mainPID, 10, 32)
	if err != nil || pid <= 0 {
		return fmt.Errorf("runnerd.service main PID=%q; online retained-capacity recovery requires a live service process", mainPID)
	}
	return nil
}

// retainedCapacityRecoveryFailureReason makes the operator-facing result
// actionable without serializing runtime details, SQL state, paths, scripts,
// credentials, headers, or process information.
func retainedCapacityRecoveryFailureReason(err error) string {
	switch {
	case errors.Is(err, execution.ErrLostRuntimeRecoveryUnconfirmed):
		return "cleanup_unconfirmed"
	case errors.Is(err, execution.ErrLostRuntimeRecoveryFinalization):
		return "finalization_unconfirmed"
	case errors.Is(err, execution.ErrLostRuntimeRecoveryIneligible), errors.Is(err, store.ErrLostRuntimeRecoveryNotReleasable):
		return "inventory_not_eligible"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "operation_failed"
	}
}
