package runnerlocald

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// runRecoverFailedStartup repairs the narrow legacy window where a known
// runtime completed cleanup during startup failure but its reservation was not
// released. Like recover-stalled, it is installer-only, offline, selected by
// exact IDs, and cannot read, claim, or replay a stored script.
func runRecoverFailedStartup(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-locald recover-failed-startup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	apply := flags.Bool("apply", false, "perform the explicit offline repair")
	var sessionValues repeatedMacRecoveryValue
	flags.Var(&sessionValues, "failed-session", "terminal failed SESSION_ID with a known runtime generation; repeat for every retained failed startup")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*apply || flags.NArg() != 0 || len(sessionValues) == 0 {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: --config, --apply, and at least one --failed-session are required")
		return 2
	}
	sessions, err := parseMacFailedStartupRecoverySessions(sessionValues)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: invalid --failed-session")
		return 2
	}

	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: configuration is not ready")
		return 1
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: Mac host configuration is required")
		return 1
	}
	releaseLifecycleLock, err := acquireRunnerLocaldLifecycleLock(settings.ServiceRoot)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: lifecycle repair is unavailable")
		return 1
	}
	defer releaseLifecycleLock()
	if err := requireRunnerLocaldServicesStopped(settings); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: both Mac LaunchAgents and private sockets must be stopped")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := store.OpenExistingOfflineMaintenanceMigrating(ctx, settings.Database)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: authority database is unavailable")
		return 1
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: authority store is unavailable")
		return 1
	}
	environments, err := macRecoveryEnvironments(loaded)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: configured environment is unavailable")
		return 1
	}
	service, runtimeAdapter, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: settings.Account, WorkspaceRoot: settings.Workspaces, ShellPath: "/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: runtime recovery adapter is unavailable")
		return 1
	}
	if err := requireMacFailedStartupRecoveryInventory(ctx, authority, runtimeAdapter, sessions); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: preflight refused")
		return 1
	}
	report, err := service.ReconcileStartup(ctx)
	if err != nil || report.SessionReservationsReleased != len(sessions) {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: cleanup proof or release failed")
		return 1
	}
	if err := requireMacStalledRecoveryPostflight(ctx, authority); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-failed-startup: postflight refused")
		return 1
	}
	fmt.Fprintf(stdout, "runner-locald recover-failed-startup: recovered_failed_startup_sessions=%d\n", len(sessions))
	return 0
}

func parseMacFailedStartupRecoverySessions(values []string) ([]domain.SessionID, error) {
	requests, err := parseMacLostRecoverySessions(values)
	if err != nil {
		return nil, err
	}
	sessions := make([]domain.SessionID, 0, len(requests))
	for _, request := range requests {
		sessions = append(sessions, request.SessionID)
	}
	return sessions, nil
}

func requireMacFailedStartupRecoveryInventory(ctx context.Context, authority *store.AuthorityStore, runtimeAdapter execution.RuntimeOwnershipAuditor, selected []domain.SessionID) error {
	if authority == nil || runtimeAdapter == nil || len(selected) == 0 {
		return errors.New("recovery dependencies are incomplete")
	}
	if activeSessions, err := authority.CountActiveSessions(ctx); err != nil || activeSessions != 0 {
		return errors.New("active work is present")
	}
	if runningCommands, err := authority.CountRunningCommands(ctx); err != nil || runningCommands != 0 {
		return errors.New("running work is present")
	}
	if jobs, err := authority.ListNonterminalJobIDs(ctx); err != nil || len(jobs) != 0 {
		return errors.New("nonterminal jobs are present")
	}
	sessions, err := authority.ListSessions(ctx)
	if err != nil {
		return err
	}
	selectedSet := make(map[domain.SessionID]struct{}, len(selected))
	for _, sessionID := range selected {
		selectedSet[sessionID] = struct{}{}
	}
	found := make(map[domain.SessionID]struct{}, len(selected))
	for _, session := range sessions {
		reservation, err := authority.GetSessionReservation(ctx, session.SessionID)
		if err != nil {
			return err
		}
		if session.State != domain.SessionStateFailed || session.RuntimeGeneration == "" || reservation.CleanupConfirmedAt != nil {
			continue
		}
		if _, ok := selectedSet[session.SessionID]; !ok {
			return errors.New("retained failed startup is not selected")
		}
		commands, err := authority.ListSessionCommands(ctx, session.SessionID)
		if err != nil || len(commands) != 0 {
			return errors.New("failed startup has persisted commands")
		}
		found[session.SessionID] = struct{}{}
	}
	if len(found) != len(selected) {
		return errors.New("selected failed startup is unavailable or already released")
	}
	attributable := make(map[string]struct{}, len(selected))
	for _, sessionID := range selected {
		attributable[string(sessionID)] = struct{}{}
	}
	return runtimeAdapter.AuditOwnership(ctx, attributable)
}
