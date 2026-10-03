package runnerlocald

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/queueworker"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
)

// runRecoverStalled performs an explicit, offline repair of the complete
// retained Mac-local lost-runtime set. It owns no listener or dispatcher and
// cannot read, claim, or replay a stored script. The caller must first unload
// both Mac LaunchAgents; the lifecycle lock prevents a current runner-locald
// from racing the proof and paired-capacity release.
func runRecoverStalled(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runner-locald recover-stalled", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "owner-only Mac runner configuration")
	apply := flags.Bool("apply", false, "perform the explicit offline repair")
	var pairValues repeatedMacRecoveryValue
	flags.Var(&pairValues, "lost-pair", "terminal lost SESSION_ID:COMMAND_ID pair; repeat for every retained lost runtime")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" || !*apply || flags.NArg() != 0 || len(pairValues) == 0 {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: --config, --apply, and at least one --lost-pair are required")
		return 2
	}
	lostRequests, err := parseMacLostRecoveryPairs(pairValues)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: invalid --lost-pair")
		return 2
	}

	loaded, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: configuration is not ready")
		return 1
	}
	settings, ok := loaded.MacSettings()
	if !ok || loaded.Kind() != config.HostKindMac {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: Mac host configuration is required")
		return 1
	}
	releaseLifecycleLock, err := acquireRunnerLocaldLifecycleLock(settings.ServiceRoot)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: lifecycle repair is unavailable")
		return 1
	}
	defer releaseLifecycleLock()
	if err := requireRunnerLocaldServicesStopped(settings); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: both Mac LaunchAgents and private sockets must be stopped")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// This open rejects a missing, stale, or tampered authority instead of
	// creating or migrating one during an offline repair.
	database, err := store.OpenExistingCurrent(ctx, settings.Database)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: authority database is unavailable")
		return 1
	}
	defer database.Close()
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: authority store is unavailable")
		return 1
	}
	environments, err := macRecoveryEnvironments(loaded)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: configured environment is unavailable")
		return 1
	}
	service, runtimeAdapter, err := NewMacExecutionService(authority, hostruntime.MacRuntimeOptions{
		Account: settings.Account, WorkspaceRoot: settings.Workspaces, ShellPath: "/bin/bash",
	}, environments...)
	if err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: runtime recovery adapter is unavailable")
		return 1
	}
	if err := requireMacStalledRecoveryInventory(ctx, authority, service, runtimeAdapter, lostRequests); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: preflight refused")
		return 1
	}
	recovered, err := service.RecoverLostRuntimeBatch(ctx, lostRequests)
	if err != nil {
		fmt.Fprintf(stderr, "runner-locald recover-stalled: recovery failed: reason=%s\n", queueworker.RetainedCapacityRecoveryFailureReason(err))
		return 1
	}
	if err := requireMacStalledRecoveryPostflight(ctx, authority); err != nil {
		fmt.Fprintln(stderr, "runner-locald recover-stalled: postflight refused")
		return 1
	}
	recoveredCount, alreadyRecoveredCount := macRecoveryResultCounts(recovered)
	fmt.Fprintf(stdout, "runner-locald recover-stalled: recovered_lost_pairs=%d already_recovered_lost_pairs=%d\n", recoveredCount, alreadyRecoveredCount)
	return 0
}

func macRecoveryEnvironments(loaded config.Config) ([]domain.Environment, error) {
	environments := make([]domain.Environment, 0, len(loaded.EnvironmentNames()))
	for _, name := range loaded.EnvironmentNames() {
		registered, present := loaded.Environment(name)
		if !present {
			return nil, errors.New("configured environment is unavailable")
		}
		environments = append(environments, registered.Policy())
	}
	return environments, nil
}

type repeatedMacRecoveryValue []string

func (v *repeatedMacRecoveryValue) String() string { return strings.Join(*v, ",") }

func (v *repeatedMacRecoveryValue) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value is empty")
	}
	*v = append(*v, value)
	return nil
}

func parseMacLostRecoveryPairs(values []string) ([]execution.LostRuntimeRecoveryRequest, error) {
	requests := make([]execution.LostRuntimeRecoveryRequest, 0, len(values))
	seenSessions := make(map[domain.SessionID]struct{}, len(values))
	seenCommands := make(map[domain.CommandID]struct{}, len(values))
	for _, raw := range values {
		sessionValue, commandValue, found := strings.Cut(raw, ":")
		if !found || sessionValue == "" || commandValue == "" || strings.Contains(commandValue, ":") {
			return nil, errors.New("invalid lost pair")
		}
		sessionID, err := domain.NewSessionID(sessionValue)
		if err != nil {
			return nil, errors.New("invalid lost session")
		}
		commandID, err := domain.NewCommandID(commandValue)
		if err != nil {
			return nil, errors.New("invalid lost command")
		}
		if _, duplicate := seenSessions[sessionID]; duplicate {
			return nil, errors.New("duplicate lost session")
		}
		if _, duplicate := seenCommands[commandID]; duplicate {
			return nil, errors.New("duplicate lost command")
		}
		seenSessions[sessionID] = struct{}{}
		seenCommands[commandID] = struct{}{}
		requests = append(requests, execution.LostRuntimeRecoveryRequest{SessionID: sessionID, CommandID: commandID})
	}
	return requests, nil
}

func requireRunnerLocaldServicesStopped(settings config.MacSettings) error {
	return requireRunnerLocaldServicesStoppedWith(
		os.Getuid(),
		macLaunchdServiceLoaded,
		os.Lstat,
		settings.APISocket,
		settings.LocalDSocket,
	)
}

func requireRunnerLocaldServicesStoppedWith(uid int, serviceLoaded func(string) (bool, error), lstat func(string) (os.FileInfo, error), sockets ...string) error {
	if serviceLoaded == nil || lstat == nil || uid < 0 || len(sockets) != 2 {
		return errors.New("stopped-service inspection is unavailable")
	}
	for _, label := range []string{"com.remote-session-runner.local", "com.remote-session-runner.locald"} {
		loaded, err := serviceLoaded("gui/" + strconv.Itoa(uid) + "/" + label)
		if err != nil {
			return err
		}
		if loaded {
			return errors.New("Mac LaunchAgent remains loaded")
		}
	}
	for _, socket := range sockets {
		if socket == "" || !filepath.IsAbs(socket) {
			return errors.New("private socket path is invalid")
		}
		_, err := lstat(socket)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		return errors.New("private socket remains")
	}
	return nil
}

func macLaunchdServiceLoaded(target string) (bool, error) {
	err := exec.Command("launchctl", "print", target).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 113 {
		return false, nil
	}
	return false, err
}

func requireMacStalledRecoveryInventory(ctx context.Context, authority *store.AuthorityStore, service *execution.Service, runtimeAdapter execution.RuntimeOwnershipAuditor, lostRequests []execution.LostRuntimeRecoveryRequest) error {
	if authority == nil || service == nil || runtimeAdapter == nil || len(lostRequests) == 0 {
		return errors.New("recovery dependencies are incomplete")
	}
	activeSessions, err := authority.CountActiveSessions(ctx)
	if err != nil {
		return err
	}
	runningCommands, err := authority.CountRunningCommands(ctx)
	if err != nil {
		return err
	}
	if activeSessions != 0 || runningCommands != 0 {
		return errors.New("active work is present")
	}
	nonterminalJobs, err := authority.ListNonterminalJobIDs(ctx)
	if err != nil {
		return err
	}
	if len(nonterminalJobs) != 0 {
		return errors.New("nonterminal jobs are present")
	}
	pendingFinalizations, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	selectedPairs := make(map[store.LostRuntimeRecoveryPair]struct{}, len(lostRequests))
	for _, request := range lostRequests {
		selectedPairs[store.LostRuntimeRecoveryPair{SessionID: request.SessionID, CommandID: request.CommandID}] = struct{}{}
	}
	pendingPairs := make(map[store.LostRuntimeRecoveryPair]struct{}, len(pendingFinalizations))
	for _, pair := range pendingFinalizations {
		if _, selected := selectedPairs[pair]; !selected {
			return errors.New("pending lost recovery finalization is not selected")
		}
		pendingPairs[pair] = struct{}{}
	}
	checked, err := service.CheckLostRuntimeRecoveryBatch(ctx, lostRequests)
	if err != nil {
		return err
	}
	expectedRetained := 0
	for index, result := range checked {
		if !result.AlreadyRecovered {
			expectedRetained++
			continue
		}
		pair := store.LostRuntimeRecoveryPair{SessionID: lostRequests[index].SessionID, CommandID: lostRequests[index].CommandID}
		if _, pending := pendingPairs[pair]; !pending {
			return errors.New("selected released lost runtime has no pending finalization")
		}
	}
	liveSlots, err := authority.CountLiveCommandSlots(ctx)
	if err != nil {
		return err
	}
	liveReservations, err := authority.CountLiveSessionReservations(ctx)
	if err != nil {
		return err
	}
	if liveSlots != expectedRetained || liveReservations != expectedRetained {
		return errors.New("selected pairs do not match all retained capacity")
	}
	attributable := make(map[string]struct{}, len(lostRequests))
	for _, request := range lostRequests {
		attributable[string(request.SessionID)] = struct{}{}
	}
	return runtimeAdapter.AuditOwnership(ctx, attributable)
}

func requireMacStalledRecoveryPostflight(ctx context.Context, authority *store.AuthorityStore) error {
	if authority == nil {
		return errors.New("authority is unavailable")
	}
	activeSessions, err := authority.CountActiveSessions(ctx)
	if err != nil {
		return err
	}
	runningCommands, err := authority.CountRunningCommands(ctx)
	if err != nil {
		return err
	}
	liveSlots, err := authority.CountLiveCommandSlots(ctx)
	if err != nil {
		return err
	}
	liveReservations, err := authority.CountLiveSessionReservations(ctx)
	if err != nil {
		return err
	}
	nonterminalJobs, err := authority.ListNonterminalJobIDs(ctx)
	if err != nil {
		return err
	}
	pendingFinalizations, err := authority.ListPendingLostRuntimeRecoveryFinalizations(ctx)
	if err != nil {
		return err
	}
	if activeSessions != 0 || runningCommands != 0 || liveSlots != 0 || liveReservations != 0 || len(nonterminalJobs) != 0 || len(pendingFinalizations) != 0 {
		return errors.New("postflight is not quiescent")
	}
	return nil
}

func macRecoveryResultCounts(results []execution.LostRuntimeRecoveryResult) (recovered, alreadyRecovered int) {
	for _, result := range results {
		if result.AlreadyRecovered {
			alreadyRecovered++
		} else {
			recovered++
		}
	}
	return recovered, alreadyRecovered
}
