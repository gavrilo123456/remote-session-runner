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
	defaultReadinessWait = time.Minute
	maximumReadinessWait = 10 * time.Minute
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

type commandEventOpener interface {
	StreamCommandEvents(context.Context, string, int64, bool) (*runnerclient.EventStream, error)
}

// Run handles runner help/version and the session, command, and event commands.
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
	global.DurationVar(&waitTimeout, "wait-timeout", defaultReadinessWait, "maximum session readiness wait (1s through 10m)")
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
			fmt.Fprintln(stderr, "runner: expected session create or session status; use --help")
			return exitInvalidInvocation
		}
		switch remaining[1] {
		case "create":
			return runSessionCreate(remaining[2:], endpointName, configPath, waitTimeout, stdout, stderr, dependencies)
		case "status":
			return runSessionStatus(remaining[2:], endpointName, configPath, stdout, stderr, dependencies)
		default:
			fmt.Fprintln(stderr, "runner: expected session create or session status; use --help")
			return exitInvalidInvocation
		}
	case "exec":
		return runCommandExec(remaining[1:], endpointName, configPath, stdout, stderr, dependencies)
	case "events":
		return runCommandEvents(remaining[1:], endpointName, configPath, stdout, stderr, dependencies)
	default:
		fmt.Fprintln(stderr, "runner: expected session, exec, or events; use --help")
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
	if !noWait && (waitTimeout < time.Second || waitTimeout > maximumReadinessWait) {
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
	fmt.Fprintln(output, "  exec [--idempotency-key KEY] SESSION_ID -- SCRIPT")
	fmt.Fprintln(output, "  events COMMAND_ID [--after SEQUENCE] [--follow]")
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
