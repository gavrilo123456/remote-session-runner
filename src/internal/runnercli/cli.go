// Package runnercli implements the first resource commands for the shared
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
	resolver endpointResolver
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

// Run handles runner help/version and the P119 session create/status commands.
func Run(args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return runWithDependencies(args, stdout, stderr, cliDependencies{
		resolver: newDefaultEndpointResolver(),
		now:      time.Now,
		sleep:    sleepContext,
	})
}

func runWithDependencies(args []string, stdout, stderr io.Writer, dependencies cliDependencies) int {
	if dependencies.now == nil {
		dependencies.now = time.Now
	}
	if dependencies.sleep == nil {
		dependencies.sleep = sleepContext
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
	if len(remaining) == 0 || remaining[0] != "session" {
		fmt.Fprintln(stderr, "runner: expected session create or session status; use --help")
		return exitInvalidInvocation
	}
	if len(remaining) < 2 {
		fmt.Fprintln(stderr, "runner: expected session create or session status; use --help")
		return exitInvalidInvocation
	}
	if dependencies.resolver == nil {
		fmt.Fprintln(stderr, "runner: endpoint resolver is unavailable")
		return 1
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
	fmt.Fprintln(output, "Create and inspect sessions through one explicitly selected ingress.")
	fmt.Fprintln(output, "\nCommands:")
	fmt.Fprintln(output, "  session create --environment NAME --target local|remote --profile NAME [--no-wait] [--idempotency-key KEY]")
	fmt.Fprintln(output, "  session status SESSION_ID")
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
