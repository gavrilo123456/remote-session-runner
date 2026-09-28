// Package runnercli implements resource commands for the shared
// Runner client. Every resource operation uses one explicitly selected
// endpoint for its complete lifetime.
package runnercli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"remote-session-runner/src/internal/commandstub"
	"remote-session-runner/src/internal/runnerclient"
)

const (
	defaultOperationWait = time.Minute
	maximumOperationWait = 10 * time.Minute
	readinessPollEvery   = 250 * time.Millisecond
	apiRequestTimeout    = 15 * time.Second

	exitInvalidInvocation = 2
	exitWaitPending       = 3
)

type cliDependencies struct {
	resolver   endpointResolver
	openEvents func(context.Context, sessionClient, string, int64, bool) (commandEventStream, error)
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
}

type commandEventStream interface {
	Next() (runnerclient.Event, error)
	Cursor() int64
	Close() error
}

type commandOperations interface {
	SubmitCommand(context.Context, string, runnerclient.SubmitCommandRequest, string) (runnerclient.Acceptance, error)
	GetCommand(context.Context, string) (runnerclient.Snapshot[runnerclient.CommandResource], error)
}

type lifecycleOperations interface {
	CancelCommand(context.Context, string, string) (runnerclient.Acceptance, error)
	CloseSession(context.Context, string, string, string) (runnerclient.Acceptance, error)
}

type jobOperations interface {
	Run(context.Context, runnerclient.RunJobRequest, string) (runnerclient.Acceptance, error)
	GetJob(context.Context, string) (runnerclient.Snapshot[runnerclient.JobResource], error)
}

type commandEventOpener interface {
	StreamCommandEvents(context.Context, string, int64, bool) (*runnerclient.EventStream, error)
}

// Run handles runner help/version and the session, command, event, lifecycle,
// and one-off job commands.
func Run(args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return runWithDependencies(args, stdout, stderr, cliDependencies{
		resolver:   newDefaultEndpointResolver(),
		openEvents: openRunnerCommandEvents,
		now:        time.Now,
		sleep:      sleepContext,
	})
}

func runWithDependencies(args []string, stdout, stderr io.Writer, dependencies cliDependencies) int {
	if dependencies.now == nil {
		dependencies.now = time.Now
	}
	if dependencies.sleep == nil {
		dependencies.sleep = sleepContext
	}
	if dependencies.openEvents == nil {
		dependencies.openEvents = openRunnerCommandEvents
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		writeUsage(stdout)
		return 0
	}

	global := flag.NewFlagSet("runner", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	var endpointName string
	var configPath string
	var waitTimeout time.Duration
	var help, version bool
	global.StringVar(&endpointName, "endpoint", "", "required ingress: local or a configured endpoint profile")
	global.StringVar(&configPath, "config", "", "owner-only Mac endpoint configuration (remote profiles only)")
	global.DurationVar(&waitTimeout, "wait-timeout", defaultOperationWait, "maximum readiness, close, or run wait (1s through 10m)")
	global.BoolVar(&help, "help", false, "show help")
	global.BoolVar(&help, "h", false, "show help")
	global.BoolVar(&version, "version", false, "show version")
	if err := global.Parse(args); err != nil {
		fmt.Fprintln(stderr, "runner: invalid global options; use --help")
		return exitInvalidInvocation
	}
	if help {
		writeUsage(stdout)
		return 0
	}
	if version {
		fmt.Fprintf(stdout, "runner %s\n", commandstub.Version)
		return 0
	}
	if configPath == "" {
		configPath = defaultMacConfigPath()
	}

	remaining := global.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(stderr, "runner: expected a command; use --help")
		return exitInvalidInvocation
	}
	if dependencies.resolver == nil {
		fmt.Fprintln(stderr, "runner: endpoint resolver is unavailable")
		return 1
	}

	switch remaining[0] {
	case "session":
		if len(remaining) < 2 {
			fmt.Fprintln(stderr, "runner: expected session create, status, or close; use --help")
			return exitInvalidInvocation
		}
		switch remaining[1] {
		case "create":
			return runSessionCreate(remaining[2:], endpointName, configPath, waitTimeout, stdout, stderr, dependencies)
		case "status":
			return runSessionStatus(remaining[2:], endpointName, configPath, stdout, stderr, dependencies)
		case "close":
			return runSessionClose(remaining[2:], endpointName, configPath, waitTimeout, stdout, stderr, dependencies)
		default:
			fmt.Fprintln(stderr, "runner: expected session create, status, or close; use --help")
			return exitInvalidInvocation
		}
	case "cancel":
		return runCommandCancel(remaining[1:], endpointName, configPath, stdout, stderr, dependencies)
	case "run":
		return runOneOffJob(remaining[1:], endpointName, configPath, waitTimeout, stdout, stderr, dependencies)
	case "exec":
		return runCommandExec(remaining[1:], endpointName, configPath, stdout, stderr, dependencies)
	case "events":
		return runCommandEvents(remaining[1:], endpointName, configPath, stdout, stderr, dependencies)
	default:
		fmt.Fprintln(stderr, "runner: expected session, exec, events, cancel, or run; use --help")
		return exitInvalidInvocation
	}
}

func runSessionCreate(args []string, endpointName, configPath string, waitTimeout time.Duration, stdout, stderr io.Writer, dependencies cliDependencies) int {
	options := flag.NewFlagSet("runner session create", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	var environment, targetKind, targetProfile, idempotencyKey string
	var noWait, help bool
	options.StringVar(&environment, "environment", "", "configured environment name")
	options.StringVar(&targetKind, "target", "", "immutable execution target: local or remote")
	options.StringVar(&targetProfile, "profile", "", "execution target profile")
	options.StringVar(&idempotencyKey, "idempotency-key", "", "stable mutation key; generated when omitted")
	options.BoolVar(&noWait, "no-wait", false, "return after durable acceptance without waiting for readiness")
	options.BoolVar(&help, "help", false, "show session create help")
	options.BoolVar(&help, "h", false, "show session create help")
	if err := options.Parse(args); err != nil {
		fmt.Fprintln(stderr, "runner: invalid session create options; use --help")
		return exitInvalidInvocation
	}
	if help {
		writeCreateUsage(stdout)
		return 0
	}
	if len(options.Args()) != 0 || strings.TrimSpace(environment) == "" ||
		(targetKind != "local" && targetKind != "remote") || strings.TrimSpace(targetProfile) == "" {
		fmt.Fprintln(stderr, "runner: session create requires --environment, --target, and --profile; use --help")
		return exitInvalidInvocation
	}
	if endpointName == "" {
		fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		return exitInvalidInvocation
	}
	if !noWait && (waitTimeout < time.Second || waitTimeout > maximumOperationWait) {
		fmt.Fprintln(stderr, "runner: --wait-timeout must be between 1s and 10m")
		return exitInvalidInvocation
	}

	client, err := dependencies.resolver.Resolve(endpointName, configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select endpoint profile %q: %v\n", endpointName, err)
		return 1
	}
	if client.EndpointKind() == runnerclient.EndpointHTTPS && targetKind != "remote" {
		fmt.Fprintln(stderr, "runner: direct HTTPS endpoint profiles accept remote targets only")
		return exitInvalidInvocation
	}
	if idempotencyKey == "" {
		idempotencyKey, err = newIdempotencyKey()
		if err != nil {
			fmt.Fprintln(stderr, "runner: could not create an idempotency key")
			return 1
		}
	}

	requestContext, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	accepted, err := client.CreateSession(requestContext, runnerclient.CreateSessionRequest{
		Environment: environment,
		ExecutionTarget: runnerclient.Target{
			Kind:    targetKind,
			Profile: targetProfile,
		},
	}, idempotencyKey)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "runner: session create failed; retry only with the same --idempotency-key %q if delivery may be uncertain: %v\n", idempotencyKey, err)
		return 1
	}

	fmt.Fprintf(stdout, "endpoint: %s\n", endpointName)
	fmt.Fprintf(stdout, "session_id: %s\n", accepted.SessionID)
	fmt.Fprintf(stdout, "acceptance_scope: %s\n", accepted.AcceptanceScope)
	fmt.Fprintf(stdout, "execution_target: %s/%s\n", accepted.ExecutionTarget.Kind, accepted.ExecutionTarget.Profile)
	fmt.Fprintf(stdout, "idempotency_key: %s\n", idempotencyKey)
	if accepted.KnownState.SessionState != "" {
		fmt.Fprintf(stdout, "session_state: %s\n", accepted.KnownState.SessionState)
	}
	if accepted.KnownState.DeliveryState != "" {
		fmt.Fprintf(stdout, "delivery_state: %s\n", accepted.KnownState.DeliveryState)
	}
	if noWait {
		fmt.Fprintln(stdout, "readiness: pending (not waited; session was not cancelled)")
		return 0
	}

	snapshot, outcome, err := waitForSessionReady(context.Background(), client, accepted.SessionID, waitTimeout, dependencies)
	if snapshot != nil {
		writeSessionStatus(stdout, endpointName, *snapshot, false)
	}
	switch outcome {
	case waitReady:
		return 0
	case waitTerminal:
		state := "unknown"
		if snapshot != nil {
			state = displaySessionState(snapshot.Resource)
		}
		fmt.Fprintf(stderr, "runner: session %s reached terminal creation outcome %s\n", accepted.SessionID, state)
		return 1
	case waitPending:
		if snapshot == nil {
			fmt.Fprintln(stdout, "session_state: pending")
		}
		fmt.Fprintln(stdout, "readiness: pending (wait timed out; session was not cancelled)")
		fmt.Fprintf(stderr, "runner: session %s remains accepted; check it with --endpoint %s session status %s\n", accepted.SessionID, endpointName, accepted.SessionID)
		return exitWaitPending
	default:
		fmt.Fprintf(stderr, "runner: readiness check failed after acceptance; session %s was not cancelled; check it with --endpoint %s session status %s: %v\n", accepted.SessionID, endpointName, accepted.SessionID, err)
		return 1
	}
}

func runSessionStatus(args []string, endpointName, configPath string, stdout, stderr io.Writer, dependencies cliDependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writeStatusUsage(stdout)
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "runner: session status requires exactly one SESSION_ID; use --help")
		return exitInvalidInvocation
	}
	if endpointName == "" {
		fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		return exitInvalidInvocation
	}
	client, err := dependencies.resolver.Resolve(endpointName, configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select endpoint profile %q: %v\n", endpointName, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	snapshot, err := client.GetSession(ctx, args[0])
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "runner: session status request failed: %v\n", err)
		return 1
	}
	writeSessionStatus(stdout, endpointName, snapshot, true)
	return 0
}

func runCommandCancel(args []string, endpointName, configPath string, stdout, stderr io.Writer, dependencies cliDependencies) int {
	options := flag.NewFlagSet("runner cancel", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	var idempotencyKey string
	var help bool
	options.StringVar(&idempotencyKey, "idempotency-key", "", "stable mutation key; generated when omitted")
	options.BoolVar(&help, "help", false, "show cancel help")
	options.BoolVar(&help, "h", false, "show cancel help")
	if err := options.Parse(args); err != nil {
		fmt.Fprintln(stderr, "runner: invalid cancel options; use --help")
		return exitInvalidInvocation
	}
	if help {
		writeCancelUsage(stdout)
		return 0
	}
	if len(options.Args()) != 1 || strings.TrimSpace(options.Args()[0]) == "" {
		fmt.Fprintln(stderr, "runner: cancel requires exactly one COMMAND_ID; use --help")
		return exitInvalidInvocation
	}
	if endpointName == "" {
		fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		return exitInvalidInvocation
	}
	_, operations, err := resolveLifecycleOperations(endpointName, configPath, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select cancel endpoint %q: %v\n", endpointName, err)
		return 1
	}
	if idempotencyKey == "" {
		idempotencyKey, err = newIdempotencyKey()
		if err != nil {
			fmt.Fprintln(stderr, "runner: could not create an idempotency key")
			return 1
		}
	}
	commandID := options.Args()[0]
	fmt.Fprintf(stderr, "idempotency_key: %s\n", idempotencyKey)
	requestContext, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	accepted, err := operations.CancelCommand(requestContext, commandID, idempotencyKey)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "runner: cancel request failed; retry only with the same --idempotency-key %q if delivery may be uncertain; command_id: %s: %v\n", idempotencyKey, commandID, err)
		return 1
	}
	if accepted.CommandID != commandID || accepted.ResourceID != commandID {
		fmt.Fprintf(stderr, "runner: cancel acceptance did not identify command %s; preserve idempotency key %q\n", commandID, idempotencyKey)
		return 1
	}
	fmt.Fprintf(stdout, "endpoint: %s\ncommand_id: %s\ncancel_requested: accepted\nacceptance_scope: %s\n", endpointName, commandID, valueOrUnknown(accepted.AcceptanceScope))
	fmt.Fprintf(stdout, "command_state: %s\n", valueOrUnknown(accepted.KnownState.CommandState))
	if accepted.KnownState.DeliveryState != "" {
		fmt.Fprintf(stdout, "delivery_state: %s\n", accepted.KnownState.DeliveryState)
	}
	fmt.Fprintln(stdout, "cancellation is a request; this response does not claim a terminal cancelled state")
	return 0
}

func runSessionClose(args []string, endpointName, configPath string, waitTimeout time.Duration, stdout, stderr io.Writer, dependencies cliDependencies) int {
	options := flag.NewFlagSet("runner session close", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	var policy, idempotencyKey string
	var help bool
	options.StringVar(&policy, "policy", "graceful", "session close policy (defaults to graceful)")
	options.StringVar(&idempotencyKey, "idempotency-key", "", "stable mutation key; generated when omitted")
	options.BoolVar(&help, "help", false, "show session close help")
	options.BoolVar(&help, "h", false, "show session close help")
	if err := options.Parse(args); err != nil {
		fmt.Fprintln(stderr, "runner: invalid session close options; use --help")
		return exitInvalidInvocation
	}
	if help {
		writeCloseUsage(stdout)
		return 0
	}
	if len(options.Args()) != 1 || strings.TrimSpace(options.Args()[0]) == "" || strings.TrimSpace(policy) == "" {
		fmt.Fprintln(stderr, "runner: session close requires one SESSION_ID and a nonempty policy; use --help")
		return exitInvalidInvocation
	}
	if endpointName == "" {
		fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		return exitInvalidInvocation
	}
	if waitTimeout < time.Second || waitTimeout > maximumOperationWait {
		fmt.Fprintln(stderr, "runner: --wait-timeout must be between 1s and 10m")
		return exitInvalidInvocation
	}
	client, operations, err := resolveLifecycleOperations(endpointName, configPath, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select close endpoint %q: %v\n", endpointName, err)
		return 1
	}
	if idempotencyKey == "" {
		idempotencyKey, err = newIdempotencyKey()
		if err != nil {
			fmt.Fprintln(stderr, "runner: could not create an idempotency key")
			return 1
		}
	}
	sessionID := options.Args()[0]
	fmt.Fprintf(stderr, "idempotency_key: %s\n", idempotencyKey)
	requestContext, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	accepted, err := operations.CloseSession(requestContext, sessionID, policy, idempotencyKey)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "runner: close request failed; session_id: %s; retry only with the same --idempotency-key %q if delivery may be uncertain: %v\n", sessionID, idempotencyKey, err)
		return 1
	}
	if accepted.SessionID != sessionID || accepted.ResourceID != sessionID {
		fmt.Fprintf(stderr, "runner: close acceptance did not identify session %s; preserve idempotency key %q\n", sessionID, idempotencyKey)
		return 1
	}
	fmt.Fprintf(stdout, "endpoint: %s\nsession_id: %s\nclose_requested: accepted\nclose_policy: %s\nacceptance_scope: %s\n", endpointName, sessionID, policy, valueOrUnknown(accepted.AcceptanceScope))
	if accepted.KnownState.DeliveryState != "" {
		fmt.Fprintf(stdout, "delivery_state: %s\n", accepted.KnownState.DeliveryState)
	}
	if accepted.KnownState.DeliveryState == "not_delivered" {
		fmt.Fprintln(stdout, "session_state: not_delivered")
		fmt.Fprintf(stderr, "runner: close was accepted locally but not delivered; session %s has no confirmed target teardown\n", sessionID)
		return 1
	}

	snapshot, outcome, err := waitForSessionClosed(context.Background(), client, sessionID, waitTimeout, dependencies)
	if snapshot != nil {
		writeSessionStatus(stdout, endpointName, *snapshot, false)
		fmt.Fprintf(stdout, "teardown_outcome: %s\n", sessionTeardownOutcome(snapshot.Resource))
	} else if outcome == waitPending {
		fmt.Fprintln(stdout, "session_state: pending")
		fmt.Fprintln(stdout, "teardown_outcome: pending")
	}
	switch outcome {
	case waitReady:
		return 0
	case waitTerminal:
		state := "unknown"
		if snapshot != nil {
			state = displaySessionState(snapshot.Resource)
		}
		fmt.Fprintf(stderr, "runner: close was accepted but teardown reached %s; session_id: %s\n", state, sessionID)
		return 1
	case waitPending:
		fmt.Fprintln(stdout, "close: pending (wait timed out; no second close was sent)")
		fmt.Fprintf(stderr, "runner: session %s remains accepted; check it with --endpoint %s session status %s; retry close only with the same --idempotency-key %q\n", sessionID, endpointName, sessionID, idempotencyKey)
		return exitWaitPending
	default:
		fmt.Fprintf(stderr, "runner: close outcome could not be confirmed; session_id: %s; check with --endpoint %s session status %s: %v\n", sessionID, endpointName, sessionID, err)
		return 1
	}
}

func runOneOffJob(args []string, endpointName, configPath string, waitTimeout time.Duration, stdout, stderr io.Writer, dependencies cliDependencies) int {
	options := flag.NewFlagSet("runner run", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	var environment, targetKind, targetProfile, idempotencyKey string
	var help bool
	options.StringVar(&environment, "environment", "", "configured environment name")
	options.StringVar(&targetKind, "target", "", "immutable execution target: local or remote")
	options.StringVar(&targetProfile, "profile", "", "execution target profile")
	options.StringVar(&idempotencyKey, "idempotency-key", "", "stable mutation key; generated when omitted")
	options.BoolVar(&help, "help", false, "show run help")
	options.BoolVar(&help, "h", false, "show run help")
	if err := options.Parse(args); err != nil {
		fmt.Fprintln(stderr, "runner: invalid run options; use --help")
		return exitInvalidInvocation
	}
	if help {
		writeRunUsage(stdout)
		return 0
	}
	if len(options.Args()) != 1 || strings.TrimSpace(environment) == "" ||
		(targetKind != "local" && targetKind != "remote") || strings.TrimSpace(targetProfile) == "" {
		fmt.Fprintln(stderr, "runner: run requires --environment, --target, --profile, and exactly one SCRIPT after --; use --help")
		return exitInvalidInvocation
	}
	if endpointName == "" {
		fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		return exitInvalidInvocation
	}
	if waitTimeout < time.Second || waitTimeout > maximumOperationWait {
		fmt.Fprintln(stderr, "runner: --wait-timeout must be between 1s and 10m")
		return exitInvalidInvocation
	}
	client, err := dependencies.resolver.Resolve(endpointName, configPath)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select endpoint profile %q: %v\n", endpointName, err)
		return 1
	}
	if client.EndpointKind() == runnerclient.EndpointHTTPS && targetKind != "remote" {
		fmt.Fprintln(stderr, "runner: direct HTTPS endpoint profiles accept remote targets only")
		return exitInvalidInvocation
	}
	jobs, ok := client.(jobOperations)
	if !ok {
		fmt.Fprintln(stderr, "runner: selected endpoint does not support one-off jobs")
		return 1
	}
	if _, ok := client.(commandOperations); !ok {
		fmt.Fprintln(stderr, "runner: selected endpoint does not support command reads")
		return 1
	}
	if idempotencyKey == "" {
		idempotencyKey, err = newIdempotencyKey()
		if err != nil {
			fmt.Fprintln(stderr, "runner: could not create an idempotency key")
			return 1
		}
	}
	// Print the retry key before the one-off mutation. Replaying that exact
	// request and key resumes the same durable job rather than allocating work.
	fmt.Fprintf(stderr, "idempotency_key: %s\n", idempotencyKey)
	requestContext, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	accepted, err := jobs.Run(requestContext, runnerclient.RunJobRequest{
		Environment:     environment,
		ExecutionTarget: runnerclient.Target{Kind: targetKind, Profile: targetProfile},
		Script:          options.Args()[0],
	}, idempotencyKey)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "runner: run acceptance failed; outcome may be uncertain; retry only with the same --idempotency-key %q: %v\n", idempotencyKey, err)
		return 1
	}
	if accepted.JobID == "" || accepted.SessionID == "" || accepted.CommandID == "" || accepted.ResourceID != accepted.JobID {
		fmt.Fprintf(stderr, "runner: job acceptance was malformed; preserve idempotency key %q\n", idempotencyKey)
		return 1
	}
	fmt.Fprintf(stderr, "endpoint: %s\njob_id: %s\nsession_id: %s\ncommand_id: %s\nacceptance_scope: %s\nexecution_target: %s/%s\n", endpointName, accepted.JobID, accepted.SessionID, accepted.CommandID, valueOrUnknown(accepted.AcceptanceScope), accepted.ExecutionTarget.Kind, accepted.ExecutionTarget.Profile)

	jobDeadline := dependencies.now().Add(waitTimeout)
	jobContext, cancelJob := context.WithTimeout(context.Background(), waitTimeout)
	defer cancelJob()
	jobSnapshot, outcome, err := waitForJobUntil(jobContext, jobs, accepted.JobID, jobDeadline, dependencies, jobCanStream)
	if jobSnapshot != nil {
		if err := validateAcceptedJob(*jobSnapshot, accepted); err != nil {
			fmt.Fprintf(stderr, "runner: accepted job identity changed; preserve job_id %s, session_id %s, and command_id %s: %v\n", accepted.JobID, accepted.SessionID, accepted.CommandID, err)
			return 1
		}
	}
	if outcome == waitPending {
		if jobSnapshot != nil {
			fmt.Fprintln(stderr, "job_snapshot: last observed before wait timeout")
			writeJobStatus(stderr, endpointName, *jobSnapshot, 0, 0)
		}
		writeRunPending(stderr, endpointName, accepted, idempotencyKey, "job authority or command acceptance")
		return exitWaitPending
	}
	if outcome == waitFailed {
		if jobSnapshot != nil {
			fmt.Fprintln(stderr, "job_snapshot: last observed before status read failure")
			writeJobStatus(stderr, endpointName, *jobSnapshot, 0, 0)
		}
		fmt.Fprintf(stderr, "runner: could not read accepted job %s; retry the same request with idempotency key %q: %v\n", accepted.JobID, idempotencyKey, err)
		return 1
	}
	if jobSnapshot == nil {
		fmt.Fprintf(stderr, "runner: job %s has no readable state; preserve session_id %s and command_id %s\n", accepted.JobID, accepted.SessionID, accepted.CommandID)
		return 1
	}
	if !jobHasCommandState(jobSnapshot.Resource) {
		writeJobStatus(stderr, endpointName, *jobSnapshot, 0, 0)
		if jobSnapshot.Resource.DeliveryState == "not_delivered" {
			fmt.Fprintf(stderr, "runner: job %s was proven not delivered; the command was not executed\n", accepted.JobID)
			return 1
		}
		if jobIsTerminal(jobSnapshot.Resource) {
			return jobExitStatus(jobSnapshot.Resource)
		}
		fmt.Fprintf(stderr, "runner: job %s reached a terminal pre-command outcome; no command output was followed\n", accepted.JobID)
		return 1
	}

	lastObservedJob := *jobSnapshot
	cursor, _, streamErr := consumeCommandEvents(jobContext, client, accepted.CommandID, 0, true, stdout, stderr, dependencies)
	if streamErr != nil {
		fmt.Fprintln(stderr, "job_snapshot: last observed before stream stopped")
		writeJobStatus(stderr, endpointName, *jobSnapshot, cursor, 0)
		if errors.Is(jobContext.Err(), context.DeadlineExceeded) || jobDeadline.Sub(dependencies.now()) <= 0 {
			writeRunPending(stderr, endpointName, accepted, idempotencyKey, "command output or completion")
			return exitWaitPending
		}
		if !writeEventHistoryError(stderr, endpointName, accepted.CommandID, cursor, streamErr) {
			writeCommandResumeError(stderr, endpointName, accepted.CommandID, cursor, streamErr)
		}
		fmt.Fprintf(stderr, "runner: job %s remains durable; command was not restarted; retry run only with idempotency key %q\n", accepted.JobID, idempotencyKey)
		return 1
	}

	jobSnapshot, outcome, err = waitForJobUntil(jobContext, jobs, accepted.JobID, jobDeadline, dependencies, jobIsTerminal)
	if jobSnapshot != nil {
		if err := validateAcceptedJob(*jobSnapshot, accepted); err != nil {
			fmt.Fprintf(stderr, "runner: final job identity changed; preserve job_id %s, session_id %s, and command_id %s: %v\n", accepted.JobID, accepted.SessionID, accepted.CommandID, err)
			return 1
		}
		writeJobStatus(stderr, endpointName, *jobSnapshot, cursor, 0)
	} else {
		fmt.Fprintln(stderr, "job_snapshot: last observed before teardown status read")
		writeJobStatus(stderr, endpointName, lastObservedJob, cursor, 0)
	}
	if outcome == waitPending {
		writeRunPending(stderr, endpointName, accepted, idempotencyKey, "session teardown")
		return exitWaitPending
	}
	if outcome == waitFailed {
		fmt.Fprintf(stderr, "runner: job %s command finished but teardown status could not be read; preserve all IDs and retry only with idempotency key %q: %v\n", accepted.JobID, idempotencyKey, err)
		return 1
	}
	if jobSnapshot == nil {
		fmt.Fprintf(stderr, "runner: job %s completed without a final readable status; session_id %s command_id %s\n", accepted.JobID, accepted.SessionID, accepted.CommandID)
		return 1
	}
	return jobExitStatus(jobSnapshot.Resource)
}

func sessionTeardownOutcome(resource runnerclient.SessionResource) string {
	switch resource.SessionState {
	case "closed", "expired":
		return "closed"
	case "lost":
		return "lost"
	case "failed":
		return "failed"
	}
	if resource.SessionState == "" && resource.DeliveryState == "not_delivered" {
		return "not_created"
	}
	return "pending"
}

func resolveLifecycleOperations(endpointName, configPath string, dependencies cliDependencies) (sessionClient, lifecycleOperations, error) {
	client, err := dependencies.resolver.Resolve(endpointName, configPath)
	if err != nil {
		return nil, nil, err
	}
	operations, ok := client.(lifecycleOperations)
	if !ok {
		return nil, nil, errors.New("selected endpoint does not support lifecycle operations")
	}
	return client, operations, nil
}

func waitForSessionClosed(ctx context.Context, client sessionClient, sessionID string, timeout time.Duration, dependencies cliDependencies) (*runnerclient.Snapshot[runnerclient.SessionResource], waitOutcome, error) {
	deadline := dependencies.now().Add(timeout)
	var last *runnerclient.Snapshot[runnerclient.SessionResource]
	for {
		remaining := deadline.Sub(dependencies.now())
		if remaining <= 0 {
			return last, waitPending, nil
		}
		requestTimeout := remaining
		if requestTimeout > apiRequestTimeout {
			requestTimeout = apiRequestTimeout
		}
		requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
		snapshot, err := client.GetSession(requestContext, sessionID)
		cancel()
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || deadline.Sub(dependencies.now()) <= 0 {
				return last, waitPending, nil
			}
			return last, waitFailed, err
		}
		if snapshot.Resource.SessionID != sessionID {
			return last, waitFailed, fmt.Errorf("%w: session response ID differs from requested close", runnerclient.ErrProtocol)
		}
		last = &snapshot
		switch snapshot.Resource.SessionState {
		case "closed", "expired":
			return last, waitReady, nil
		case "failed", "lost":
			return last, waitTerminal, nil
		}
		if snapshot.Resource.SessionState == "" && snapshot.Resource.DeliveryState == "not_delivered" {
			// The create intent was proven never delivered, so there is no
			// target session to tear down; the local close is a known no-op.
			return last, waitReady, nil
		}
		remaining = deadline.Sub(dependencies.now())
		if remaining <= 0 {
			return last, waitPending, nil
		}
		pause := readinessPollEvery
		if pause > remaining {
			pause = remaining
		}
		if err := dependencies.sleep(ctx, pause); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || deadline.Sub(dependencies.now()) <= 0 {
				return last, waitPending, nil
			}
			return last, waitFailed, err
		}
	}
}

func waitForJobUntil(ctx context.Context, operations jobOperations, jobID string, deadline time.Time, dependencies cliDependencies, done func(runnerclient.JobResource) bool) (*runnerclient.Snapshot[runnerclient.JobResource], waitOutcome, error) {
	var last *runnerclient.Snapshot[runnerclient.JobResource]
	for {
		remaining := deadline.Sub(dependencies.now())
		if remaining <= 0 {
			return last, waitPending, nil
		}
		requestTimeout := remaining
		if requestTimeout > apiRequestTimeout {
			requestTimeout = apiRequestTimeout
		}
		requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
		snapshot, err := operations.GetJob(requestContext, jobID)
		cancel()
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || deadline.Sub(dependencies.now()) <= 0 {
				return last, waitPending, nil
			}
			return last, waitFailed, err
		}
		if snapshot.Resource.JobID != jobID {
			return last, waitFailed, fmt.Errorf("%w: job response ID differs from requested job", runnerclient.ErrProtocol)
		}
		last = &snapshot
		if done(snapshot.Resource) {
			return last, waitReady, nil
		}
		remaining = deadline.Sub(dependencies.now())
		if remaining <= 0 {
			return last, waitPending, nil
		}
		pause := readinessPollEvery
		if pause > remaining {
			pause = remaining
		}
		if err := dependencies.sleep(ctx, pause); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || deadline.Sub(dependencies.now()) <= 0 {
				return last, waitPending, nil
			}
			return last, waitFailed, err
		}
	}
}

func jobHasCommandState(resource runnerclient.JobResource) bool {
	return resource.CommandState != nil && *resource.CommandState != ""
}

func validateAcceptedJob(snapshot runnerclient.Snapshot[runnerclient.JobResource], accepted runnerclient.Acceptance) error {
	resource := snapshot.Resource
	if resource.JobID != accepted.JobID || resource.SessionID != accepted.SessionID || resource.CommandID != accepted.CommandID {
		return fmt.Errorf("job snapshot IDs (%s, %s, %s) do not match accepted IDs (%s, %s, %s)", resource.JobID, resource.SessionID, resource.CommandID, accepted.JobID, accepted.SessionID, accepted.CommandID)
	}
	return nil
}

func jobCanStream(resource runnerclient.JobResource) bool {
	return jobHasCommandState(resource) || jobIsTerminal(resource)
}

func jobIsTerminal(resource runnerclient.JobResource) bool {
	switch resource.EffectivePhase() {
	case "failed", "lost":
		return true
	case "complete":
		return resource.TeardownState != "pending"
	}
	return resource.TeardownState == "failed" || resource.TeardownState == "lost" || resource.DeliveryState == "not_delivered"
}

func writeJobStatus(output io.Writer, endpointName string, snapshot runnerclient.Snapshot[runnerclient.JobResource], cursor, requestedAfter int64) {
	resource := snapshot.Resource
	state := ""
	if resource.CommandState != nil {
		state = *resource.CommandState
	}
	fmt.Fprintf(output, "endpoint: %s\njob_id: %s\nsession_id: %s\ncommand_id: %s\nview: %s\njob_phase: %s\ncommand_state: %s\n", endpointName, resource.JobID, resource.SessionID, resource.CommandID, valueOrUnknown(snapshot.View), valueOrUnknown(resource.EffectivePhase()), valueOrUnknown(state))
	if resource.ExitCode == nil {
		fmt.Fprintln(output, "command_exit_code: unknown")
	} else {
		fmt.Fprintf(output, "command_exit_code: %d\n", *resource.ExitCode)
	}
	fmt.Fprintf(output, "event_cursor: %d\n", cursor)
	finalSequence := int64(-1)
	if resource.FinalEventSequence == nil {
		fmt.Fprintln(output, "final_event_sequence: unknown")
	} else {
		finalSequence = *resource.FinalEventSequence
		fmt.Fprintf(output, "final_event_sequence: %d\n", finalSequence)
	}
	fmt.Fprintf(output, "output_complete: %t\noutput_truncated: %t\noutput_unavailable_reason: %s\n", resource.OutputComplete, resource.OutputTruncated, valueOrUnknown(resource.OutputUnavailableReason))
	completeRead := requestedAfter == 0 && finalSequence >= 0 && cursor == finalSequence && resource.OutputComplete && !resource.OutputTruncated
	fmt.Fprintf(output, "event_history_complete_this_read: %t\nteardown_state: %s\n", completeRead, valueOrUnknown(resource.TeardownState))
	if resource.TeardownReason != "" {
		fmt.Fprintf(output, "teardown_reason: %s\n", resource.TeardownReason)
	}
	if resource.DeliveryState != "" {
		fmt.Fprintf(output, "delivery_state: %s\n", resource.DeliveryState)
	}
	if resource.Reason != "" {
		fmt.Fprintf(output, "reason: %s\n", resource.Reason)
	}
	fmt.Fprintf(output, "is_stale: %t\n", snapshot.IsStale || resource.IsStale)
}

func jobExitStatus(resource runnerclient.JobResource) int {
	if resource.EffectivePhase() != "complete" || resource.TeardownState != "closed" || !jobHasCommandState(resource) {
		return 1
	}
	return commandExitStatus(runnerclient.CommandResource{CommandState: *resource.CommandState, ExitCode: resource.ExitCode})
}

func writeRunPending(stderr io.Writer, endpointName string, accepted runnerclient.Acceptance, idempotencyKey, stage string) {
	fmt.Fprintf(stderr, "runner: run remains pending during %s; job_id: %s; session_id: %s; command_id: %s; no replacement job was created\n", stage, accepted.JobID, accepted.SessionID, accepted.CommandID)
	fmt.Fprintf(stderr, "runner: resume with the same request and --idempotency-key %q at --endpoint %s; the accepted command was not cancelled\n", idempotencyKey, endpointName)
}

func runCommandExec(args []string, endpointName, configPath string, stdout, stderr io.Writer, dependencies cliDependencies) int {
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			writeExecUsage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "runner: exec requires SESSION_ID -- SCRIPT; use --help")
		return exitInvalidInvocation
	}
	if separator+2 != len(args) {
		fmt.Fprintln(stderr, "runner: exec requires exactly one SCRIPT argument after --; use --help")
		return exitInvalidInvocation
	}

	var sessionID, idempotencyKey string
	for i := 0; i < separator; i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			writeExecUsage(stdout)
			return 0
		case arg == "--idempotency-key":
			if i+1 >= separator || idempotencyKey != "" {
				fmt.Fprintln(stderr, "runner: exec --idempotency-key requires one value; use --help")
				return exitInvalidInvocation
			}
			i++
			idempotencyKey = args[i]
		case strings.HasPrefix(arg, "--idempotency-key="):
			if idempotencyKey != "" {
				fmt.Fprintln(stderr, "runner: exec accepts --idempotency-key only once")
				return exitInvalidInvocation
			}
			idempotencyKey = strings.TrimPrefix(arg, "--idempotency-key=")
		default:
			if strings.HasPrefix(arg, "-") || sessionID != "" {
				fmt.Fprintln(stderr, "runner: exec expects one SESSION_ID before --; use --help")
				return exitInvalidInvocation
			}
			sessionID = arg
		}
	}
	if sessionID == "" || endpointName == "" {
		if endpointName == "" {
			fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		} else {
			fmt.Fprintln(stderr, "runner: exec requires one SESSION_ID before --; use --help")
		}
		return exitInvalidInvocation
	}
	client, operations, err := resolveCommandOperations(endpointName, configPath, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select command endpoint %q: %v\n", endpointName, err)
		return 1
	}
	if idempotencyKey == "" {
		idempotencyKey, err = newIdempotencyKey()
		if err != nil {
			fmt.Fprintln(stderr, "runner: could not create an idempotency key")
			return 1
		}
	}
	// Print the retry key before mutation so it remains available if the
	// response is lost and delivery is uncertain.
	fmt.Fprintf(stderr, "idempotency_key: %s\n", idempotencyKey)
	requestContext, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	accepted, err := operations.SubmitCommand(requestContext, sessionID, runnerclient.SubmitCommandRequest{Script: args[separator+1]}, idempotencyKey)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "runner: command submission failed; its outcome may be uncertain; preserve SESSION_ID %s and retry only with the same idempotency key: %v\n", sessionID, err)
		return 1
	}
	if accepted.CommandID == "" || accepted.SessionID != sessionID {
		fmt.Fprintf(stderr, "runner: command acceptance was malformed; session_id: %s; idempotency_key: %s\n", sessionID, idempotencyKey)
		return 1
	}
	fmt.Fprintf(stderr, "endpoint: %s\nsession_id: %s\ncommand_id: %s\nacceptance_scope: %s\n", endpointName, sessionID, accepted.CommandID, valueOrUnknown(accepted.AcceptanceScope))

	cursor, _, err := consumeCommandEvents(context.Background(), client, accepted.CommandID, 0, true, stdout, stderr, dependencies)
	if err != nil {
		if !writeEventHistoryError(stderr, endpointName, accepted.CommandID, cursor, err) {
			writeCommandResumeError(stderr, endpointName, accepted.CommandID, cursor, err)
		}
		return 1
	}
	snapshot, err := getCommandSnapshot(operations, accepted.CommandID)
	if err != nil {
		writeCommandResumeError(stderr, endpointName, accepted.CommandID, cursor, err)
		return 1
	}
	writeCommandStatus(stderr, endpointName, snapshot, cursor, 0)
	return commandExitStatus(snapshot.Resource)
}

func runCommandEvents(args []string, endpointName, configPath string, stdout, stderr io.Writer, dependencies cliDependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writeEventsUsage(stdout)
		return 0
	}
	var commandID string
	var after int64
	follow := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			writeEventsUsage(stdout)
			return 0
		case arg == "--follow":
			follow = true
		case arg == "--after":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "runner: events --after requires a nonnegative sequence")
				return exitInvalidInvocation
			}
			i++
			parsed, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil || parsed < 0 {
				fmt.Fprintln(stderr, "runner: events --after requires a nonnegative sequence")
				return exitInvalidInvocation
			}
			after = parsed
		case strings.HasPrefix(arg, "--after="):
			parsed, err := strconv.ParseInt(strings.TrimPrefix(arg, "--after="), 10, 64)
			if err != nil || parsed < 0 {
				fmt.Fprintln(stderr, "runner: events --after requires a nonnegative sequence")
				return exitInvalidInvocation
			}
			after = parsed
		default:
			if strings.HasPrefix(arg, "-") || commandID != "" {
				fmt.Fprintln(stderr, "runner: events requires one COMMAND_ID and accepts --after and --follow; use --help")
				return exitInvalidInvocation
			}
			commandID = arg
		}
	}
	if commandID == "" || endpointName == "" {
		if endpointName == "" {
			fmt.Fprintln(stderr, "runner: resource commands require an explicit --endpoint")
		} else {
			fmt.Fprintln(stderr, "runner: events requires one COMMAND_ID; use --help")
		}
		return exitInvalidInvocation
	}
	client, operations, err := resolveCommandOperations(endpointName, configPath, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "runner: could not select command endpoint %q: %v\n", endpointName, err)
		return 1
	}
	writeCursor := after
	if follow {
		writeCursor, _, err = consumeCommandEvents(context.Background(), client, commandID, after, true, stdout, stderr, dependencies)
	} else {
		writeCursor, _, err = consumeCommandEvents(context.Background(), client, commandID, after, false, stdout, stderr, dependencies)
	}
	if err != nil {
		if !writeEventHistoryError(stderr, endpointName, commandID, writeCursor, err) {
			writeCommandResumeError(stderr, endpointName, commandID, writeCursor, err)
		}
		return 1
	}
	snapshot, err := getCommandSnapshot(operations, commandID)
	if err != nil {
		writeCommandResumeError(stderr, endpointName, commandID, writeCursor, err)
		return 1
	}
	writeCommandStatus(stderr, endpointName, snapshot, writeCursor, after)
	if follow {
		return commandExitStatus(snapshot.Resource)
	}
	return 0
}

func resolveCommandOperations(endpointName, configPath string, dependencies cliDependencies) (sessionClient, commandOperations, error) {
	client, err := dependencies.resolver.Resolve(endpointName, configPath)
	if err != nil {
		return nil, nil, err
	}
	operations, ok := client.(commandOperations)
	if !ok {
		return nil, nil, errors.New("selected endpoint does not support command operations")
	}
	return client, operations, nil
}

func openRunnerCommandEvents(ctx context.Context, client sessionClient, commandID string, after int64, follow bool) (commandEventStream, error) {
	opener, ok := client.(commandEventOpener)
	if !ok {
		return nil, errors.New("selected endpoint does not support command events")
	}
	return opener.StreamCommandEvents(ctx, commandID, after, follow)
}

func consumeCommandEvents(ctx context.Context, client sessionClient, commandID string, after int64, follow bool, stdout, stderr io.Writer, dependencies cliDependencies) (int64, bool, error) {
	operations, ok := client.(commandOperations)
	if !ok {
		return after, false, errors.New("selected endpoint does not support command reads")
	}
	cursor := after
	terminal := false
	for {
		openContext := ctx
		cancel := func() {}
		if !follow {
			openContext, cancel = context.WithTimeout(ctx, apiRequestTimeout)
		}
		stream, err := dependencies.openEvents(openContext, client, commandID, cursor, follow)
		if err != nil {
			cancel()
			return cursor, terminal, err
		}
		for {
			event, nextErr := stream.Next()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				_ = stream.Close()
				cancel()
				return cursor, terminal, nextErr
			}
			if event.CommandID != commandID || event.Sequence != cursor+1 {
				_ = stream.Close()
				cancel()
				return cursor, terminal, fmt.Errorf("%w: command event identity or sequence does not continue after %d", runnerclient.ErrProtocol, cursor)
			}
			if event.Type == "stdout" || event.Type == "stderr" {
				writer := stdout
				if event.Type == "stderr" {
					writer = stderr
				}
				if err := writeAll(writer, event.Data); err != nil {
					_ = stream.Close()
					cancel()
					return cursor, terminal, fmt.Errorf("write command %s output: %w", event.Type, err)
				}
			} else if err := writeLifecycleEvent(stderr, event); err != nil {
				_ = stream.Close()
				cancel()
				return cursor, terminal, fmt.Errorf("write command event: %w", err)
			}
			cursor = event.Sequence
			terminal = terminal || isTerminalCommandEvent(event.Type)
		}
		_ = stream.Close()
		cancel()
		if !follow || terminal {
			return cursor, terminal, nil
		}
		snapshot, err := getCommandSnapshot(operations, commandID)
		if err != nil {
			return cursor, terminal, err
		}
		if isTerminalCommandState(snapshot.Resource.CommandState) {
			return cursor, true, nil
		}
		if err := dependencies.sleep(ctx, readinessPollEvery); err != nil {
			return cursor, terminal, err
		}
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writeLifecycleEvent(writer io.Writer, event runnerclient.Event) error {
	if event.ExitCode != nil {
		return writeAll(writer, []byte(fmt.Sprintf("event: sequence=%d type=%s exit_code=%d\n", event.Sequence, event.Type, *event.ExitCode)))
	}
	return writeAll(writer, []byte(fmt.Sprintf("event: sequence=%d type=%s\n", event.Sequence, event.Type)))
}

func isTerminalCommandEvent(eventType string) bool {
	switch eventType {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func isTerminalCommandState(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "timed_out", "rejected", "lost":
		return true
	default:
		return false
	}
}

func getCommandSnapshot(client commandOperations, commandID string) (runnerclient.Snapshot[runnerclient.CommandResource], error) {
	ctx, cancel := context.WithTimeout(context.Background(), apiRequestTimeout)
	defer cancel()
	return client.GetCommand(ctx, commandID)
}

func writeCommandStatus(output io.Writer, endpointName string, snapshot runnerclient.Snapshot[runnerclient.CommandResource], cursor, requestedAfter int64) {
	resource := snapshot.Resource
	fmt.Fprintf(output, "endpoint: %s\ncommand_id: %s\nview: %s\ncommand_state: %s\n", endpointName, resource.CommandID, valueOrUnknown(snapshot.View), valueOrUnknown(resource.CommandState))
	if resource.ExitCode == nil {
		fmt.Fprintln(output, "command_exit_code: unknown")
	} else {
		fmt.Fprintf(output, "command_exit_code: %d\n", *resource.ExitCode)
	}
	fmt.Fprintf(output, "event_cursor: %d\n", cursor)
	finalSequence := int64(-1)
	if resource.FinalEventSequence == nil {
		fmt.Fprintln(output, "final_event_sequence: unknown")
	} else {
		finalSequence = *resource.FinalEventSequence
		fmt.Fprintf(output, "final_event_sequence: %d\n", finalSequence)
	}
	fmt.Fprintf(output, "output_complete: %t\noutput_truncated: %t\n", resource.OutputComplete, resource.OutputTruncated)
	fmt.Fprintf(output, "output_unavailable_reason: %s\n", valueOrUnknown(resource.OutputUnavailableReason))
	eventsComplete := requestedAfter == 0 && finalSequence >= 0 && cursor == finalSequence && resource.OutputComplete && !resource.OutputTruncated
	fmt.Fprintf(output, "event_history_complete_this_read: %t\n", eventsComplete)
	if resource.DeliveryState != "" {
		fmt.Fprintf(output, "delivery_state: %s\n", resource.DeliveryState)
	}
	if resource.Reason != "" {
		fmt.Fprintf(output, "reason: %s\n", resource.Reason)
	}
	fmt.Fprintf(output, "is_stale: %t\n", snapshot.IsStale || resource.IsStale)
}

func commandExitStatus(resource runnerclient.CommandResource) int {
	if resource.ExitCode != nil && *resource.ExitCode != 0 {
		if *resource.ExitCode > 0 && *resource.ExitCode <= 255 {
			return *resource.ExitCode
		}
		return 1
	}
	if resource.CommandState == "succeeded" {
		return 0
	}
	if isTerminalCommandState(resource.CommandState) {
		return 1
	}
	return 0
}

func writeCommandResumeError(stderr io.Writer, endpointName, commandID string, cursor int64, err error) {
	fmt.Fprintf(stderr, "runner: command %s stream/status failed after validated cursor %d; command was not cancelled; resume with --endpoint %s events %s --after %d --follow: %v\n", commandID, cursor, endpointName, commandID, cursor, err)
}

func writeEventHistoryError(stderr io.Writer, endpointName, commandID string, cursor int64, err error) bool {
	var apiError *runnerclient.APIError
	if !errors.As(err, &apiError) || apiError.Code != "event_history_unavailable" {
		return false
	}
	details := apiError.EventHistoryDetails()
	complete := false
	if details.OutputComplete != nil {
		complete = *details.OutputComplete
	}
	fmt.Fprintf(stderr, "runner: event history unavailable; command_id: %s\noutput_complete: %t\noutput_unavailable_reason: %s\nevent_cursor: %d\n", commandID, complete, valueOrUnknown(details.OutputUnavailableReason), cursor)
	if details.EarliestAvailable != nil {
		fmt.Fprintf(stderr, "earliest_available_sequence: %d\n", *details.EarliestAvailable)
		if *details.EarliestAvailable > 0 {
			fmt.Fprintf(stderr, "retained tail can be inspected with --endpoint %s events %s --after %d --follow; the complete output remains unavailable\n", endpointName, commandID, *details.EarliestAvailable-1)
		}
	}
	fmt.Fprintf(stderr, "event stream stopped at cursor %d: %v\n", cursor, err)
	return true
}

type waitOutcome uint8

const (
	waitPending waitOutcome = iota
	waitReady
	waitTerminal
	waitFailed
)

func waitForSessionReady(ctx context.Context, client sessionClient, sessionID string, timeout time.Duration, dependencies cliDependencies) (*runnerclient.Snapshot[runnerclient.SessionResource], waitOutcome, error) {
	deadline := dependencies.now().Add(timeout)
	var last *runnerclient.Snapshot[runnerclient.SessionResource]
	for {
		remaining := deadline.Sub(dependencies.now())
		if remaining <= 0 {
			return last, waitPending, nil
		}
		requestTimeout := remaining
		if requestTimeout > apiRequestTimeout {
			requestTimeout = apiRequestTimeout
		}
		requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
		snapshot, err := client.GetSession(requestContext, sessionID)
		cancel()
		if err != nil {
			if deadline.Sub(dependencies.now()) <= 0 {
				return last, waitPending, nil
			}
			return last, waitFailed, err
		}
		last = &snapshot
		if snapshot.Resource.SessionState == "ready" {
			return last, waitReady, nil
		}
		if isTerminalSession(snapshot.Resource) {
			return last, waitTerminal, nil
		}
		remaining = deadline.Sub(dependencies.now())
		if remaining <= 0 {
			return last, waitPending, nil
		}
		pause := readinessPollEvery
		if pause > remaining {
			pause = remaining
		}
		if err := dependencies.sleep(ctx, pause); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || deadline.Sub(dependencies.now()) <= 0 {
				return last, waitPending, nil
			}
			return last, waitFailed, err
		}
	}
}

func isTerminalSession(resource runnerclient.SessionResource) bool {
	switch resource.SessionState {
	case "failed", "lost", "closed", "expired":
		return true
	default:
		return resource.DeliveryState == "not_delivered"
	}
}

func displaySessionState(resource runnerclient.SessionResource) string {
	if resource.SessionState != "" {
		return resource.SessionState
	}
	if resource.DeliveryState == "not_delivered" {
		return "not_delivered"
	}
	return "unknown"
}

func writeSessionStatus(output io.Writer, endpointName string, snapshot runnerclient.Snapshot[runnerclient.SessionResource], includeID bool) {
	resource := snapshot.Resource
	if includeID {
		fmt.Fprintf(output, "session_id: %s\n", resource.SessionID)
	}
	fmt.Fprintf(output, "endpoint: %s\n", endpointName)
	fmt.Fprintf(output, "view: %s\n", valueOrUnknown(snapshot.View))
	fmt.Fprintf(output, "session_state: %s\n", displaySessionState(resource))
	fmt.Fprintf(output, "execution_target: %s/%s\n", valueOrUnknown(resource.ExecutionTarget.Kind), valueOrUnknown(resource.ExecutionTarget.Profile))
	if resource.DeliveryState != "" {
		fmt.Fprintf(output, "delivery_state: %s\n", resource.DeliveryState)
	}
	if resource.Authority != "" {
		fmt.Fprintf(output, "authority: %s\n", resource.Authority)
	}
	if resource.Environment != "" {
		fmt.Fprintf(output, "environment: %s\n", resource.Environment)
	}
	sourceMode := resource.Source.Mode
	if sourceMode == "" {
		sourceMode = "empty"
	}
	fmt.Fprintf(output, "source_mode: %s\n", sourceMode)
	if resource.Source.ResolvedCommit != "" {
		fmt.Fprintf(output, "resolved_commit: %s\n", resource.Source.ResolvedCommit)
	}
	if resource.Source.Path != "" {
		fmt.Fprintf(output, "source_path: %s\n", resource.Source.Path)
	}
	if resource.Capabilities.HostClass != "" {
		fmt.Fprintf(output, "host_class: %s\n", resource.Capabilities.HostClass)
	}
	if resource.Capabilities.EffectiveAccount != "" {
		fmt.Fprintf(output, "effective_account: %s\n", resource.Capabilities.EffectiveAccount)
	}
	if resource.Capabilities.Isolation != "" {
		fmt.Fprintf(output, "isolation: %s\n", resource.Capabilities.Isolation)
	}
	stale := snapshot.IsStale || resource.IsStale
	fmt.Fprintf(output, "is_stale: %t\n", stale)
	keys := make([]string, 0, len(resource.Capabilities.ServiceLimits))
	for key := range resource.Capabilities.ServiceLimits {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, err := json.Marshal(resource.Capabilities.ServiceLimits[key])
		if err != nil {
			fmt.Fprintf(output, "service_limit.%s: unavailable\n", key)
			continue
		}
		fmt.Fprintf(output, "service_limit.%s: %s\n", key, value)
	}
	if resource.Reason != "" {
		fmt.Fprintf(output, "reason: %s\n", resource.Reason)
	}
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func newIdempotencyKey() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "runner-cli-" + hex.EncodeToString(random[:]), nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintf(output, "Usage: runner --endpoint <local|profile> [--config PATH] [--wait-timeout DURATION] COMMAND\n")
	fmt.Fprintln(output, "Use one explicitly selected ingress for the complete resource operation.")
	fmt.Fprintln(output, "\nCommands:")
	fmt.Fprintln(output, "  session create --environment NAME --target local|remote --profile NAME [--no-wait] [--idempotency-key KEY]")
	fmt.Fprintln(output, "  session status SESSION_ID")
	fmt.Fprintln(output, "  session close [--policy graceful] [--idempotency-key KEY] SESSION_ID")
	fmt.Fprintln(output, "  exec [--idempotency-key KEY] SESSION_ID -- SCRIPT")
	fmt.Fprintln(output, "  events COMMAND_ID [--after SEQUENCE] [--follow]")
	fmt.Fprintln(output, "  cancel [--idempotency-key KEY] COMMAND_ID")
	fmt.Fprintln(output, "  run --environment NAME --target local|remote --profile NAME [--idempotency-key KEY] -- SCRIPT")
	fmt.Fprintln(output, "\n--wait-timeout bounds session readiness, session close, and one-off run waits (1s through 10m).")
	fmt.Fprintln(output, "\nEndpoint selection:")
	fmt.Fprintln(output, "  local       Mac Unix-socket API")
	fmt.Fprintln(output, "  linux-poc   configured direct HTTPS profile (mandatory mTLS)")
	fmt.Fprintf(output, "  --version   print runner %s\n", commandstub.Version)
}

func writeCreateUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> [--config PATH] [--wait-timeout DURATION] session create --environment NAME --target local|remote --profile NAME [--no-wait] [--idempotency-key KEY]")
	fmt.Fprintln(output, "By default, waits up to --wait-timeout (1s through 10m) for ready or a terminal creation outcome.")
	fmt.Fprintln(output, "--no-wait returns the accepted session ID immediately. A readiness timeout never cancels the session.")
}

func writeStatusUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> [--config PATH] session status SESSION_ID")
	fmt.Fprintln(output, "The selected endpoint is used exactly as supplied; the session ID never selects an ingress.")
}

func writeCloseUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> [--config PATH] [--wait-timeout DURATION] session close [--policy POLICY] [--idempotency-key KEY] SESSION_ID")
	fmt.Fprintln(output, "The default close policy is graceful. Waits for confirmed close/expiry; lost or failed teardown is an error.")
}

func writeCancelUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> [--config PATH] cancel [--idempotency-key KEY] COMMAND_ID")
	fmt.Fprintln(output, "Cancellation is an idempotent request; acceptance does not guarantee a cancelled terminal state.")
}

func writeExecUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> exec [--idempotency-key KEY] SESSION_ID -- SCRIPT")
	fmt.Fprintln(output, "SCRIPT is one argument passed unchanged as UTF-8. Accepted command IDs are printed before output is followed.")
	fmt.Fprintln(output, "A transport or local output error does not cancel the accepted command; resume with events and its last validated cursor.")
}

func writeEventsUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> events COMMAND_ID [--after SEQUENCE] [--follow]")
	fmt.Fprintln(output, "By default reads retained events once. --follow resumes from the last validated cursor until the command is terminal.")
	fmt.Fprintln(output, "Command output bytes go to stdout/stderr; lifecycle and command metadata go to stderr.")
}

func writeRunUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: runner --endpoint <local|profile> [--config PATH] [--wait-timeout DURATION] run --environment NAME --target local|remote --profile NAME [--idempotency-key KEY] -- SCRIPT")
	fmt.Fprintln(output, "Runs one command in an ephemeral session, follows output, and waits for teardown.")
	fmt.Fprintln(output, "A wait timeout leaves the accepted job running; resume with the same request and idempotency key.")
}
