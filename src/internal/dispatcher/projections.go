package dispatcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

// upsertRemoteProjectionFromReply records a complete remote snapshot only for
// session lifecycle mutations. Command and one-off run mutation replies prove
// acceptance identity, not terminal state: their projections are written only
// by strict target reads plus the matching event boundary.
func (d *RemoteDriver) upsertRemoteProjectionFromReply(ctx context.Context, intent store.LocalIntentRecord, reply sshbridge.ReplyFrame) error {
	// Status data in a command or run mutation reply is intentionally
	// sparse-compatible, so it cannot become a locally trusted projection.
	// Reconciliation persists command/job projections only from strict reads and
	// the matching durable event boundary.
	if intent.Operation == operationSubmitCommand || intent.Operation == "run" {
		return nil
	}
	if reply.ResponseType != "result" {
		return nil
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(reply.Payload))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("%w: projection result object", ErrRemoteResponse)
	}
	if intent.Operation == operationCreateSession || intent.Operation == operationCloseSession {
		projection, present, err := remoteSessionProjectionFromReply(intent, object, d.now())
		if err != nil || !present {
			return err
		}
		_, err = d.authority.UpsertRemoteSessionProjection(ctx, projection)
		return err
	}
	return nil
}

func remoteSessionProjectionFromReply(intent store.LocalIntentRecord, object map[string]json.RawMessage, fallback time.Time) (store.RemoteSessionProjection, bool, error) {
	state, ok, err := readProjectionString(object, "session_state")
	if err != nil || !ok {
		return store.RemoteSessionProjection{}, false, err
	}
	if _, err := domain.NewSessionID(string(intent.SessionID)); err != nil {
		return store.RemoteSessionProjection{}, false, err
	}
	target, err := projectionTarget(object, intent.Target)
	if err != nil {
		return store.RemoteSessionProjection{}, false, err
	}
	expectedController, err := queuedRemoteController(intent.Controller)
	if err != nil {
		return store.RemoteSessionProjection{}, false, err
	}
	if _, err := projectionController(object, expectedController); err != nil {
		return store.RemoteSessionProjection{}, false, err
	}
	controller := intent.Controller
	environment := intent.Environment
	if value, present, readErr := readProjectionString(object, "environment"); readErr != nil {
		return store.RemoteSessionProjection{}, false, readErr
	} else if present {
		environment = value
	}
	source, resolvedRevision, present, err := projectionSource(object, intent.Source)
	if err != nil {
		return store.RemoteSessionProjection{}, false, err
	}
	if !present {
		source = intent.Source
	}
	capabilities, present, err := projectionCapabilities(object)
	if err != nil || !present {
		return store.RemoteSessionProjection{}, false, err
	}
	observedAt, present, err := projectionObservedAt(object, fallback)
	if err != nil || !present {
		return store.RemoteSessionProjection{}, false, err
	}
	runtimeGeneration, _, err := readProjectionString(object, "runtime_generation")
	if err != nil {
		return store.RemoteSessionProjection{}, false, err
	}
	return store.RemoteSessionProjection{SessionID: intent.SessionID, Target: target, Controller: controller, State: domain.SessionState(state), Environment: environment, Source: source, ResolvedRevision: resolvedRevision, RuntimeGeneration: runtimeGeneration, Capabilities: capabilities, ObservedAt: observedAt, IsStale: false}, true, nil
}

func remoteCommandProjectionFromReply(intent store.LocalIntentRecord, object map[string]json.RawMessage, fallback time.Time) (store.RemoteCommandProjection, bool, error) {
	stateText, ok, err := readProjectionString(object, "command_state")
	if err != nil || !ok {
		return store.RemoteCommandProjection{}, false, err
	}
	state := domain.CommandState(stateText)
	if !state.Valid() {
		return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: invalid command_state", ErrRemoteResponse)
	}
	target, err := projectionTarget(object, intent.Target)
	if err != nil {
		return store.RemoteCommandProjection{}, false, err
	}
	expectedController, err := queuedRemoteController(intent.Controller)
	if err != nil {
		return store.RemoteCommandProjection{}, false, err
	}
	if _, err := projectionController(object, expectedController); err != nil {
		return store.RemoteCommandProjection{}, false, err
	}
	controller := intent.Controller
	sessionID := intent.SessionID
	if value, present, readErr := readProjectionString(object, "session_id"); readErr != nil {
		return store.RemoteCommandProjection{}, false, readErr
	} else if present {
		parsed, parseErr := domain.NewSessionID(value)
		if parseErr != nil || parsed != intent.SessionID {
			return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: command session identity", ErrRemoteResponse)
		}
		sessionID = parsed
	}
	if value, present, readErr := readProjectionString(object, "command_id"); readErr != nil || (present && value != string(intent.CommandID)) {
		if readErr != nil {
			return store.RemoteCommandProjection{}, false, readErr
		}
		return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: command identity", ErrRemoteResponse)
	}
	ordinal, present := positiveRemoteAuthorityOrdinal(object)
	if !present {
		return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: missing or invalid remote command ordinal", ErrRemoteResponse)
	}
	environment := intent.Environment
	if value, present, readErr := readProjectionString(object, "environment"); readErr != nil {
		return store.RemoteCommandProjection{}, false, readErr
	} else if present {
		environment = value
	}
	source, _, present, err := projectionSource(object, intent.Source)
	if err != nil {
		return store.RemoteCommandProjection{}, false, err
	}
	if !present {
		source = intent.Source
	}
	capabilities, present, err := projectionCapabilities(object)
	if err != nil || !present {
		return store.RemoteCommandProjection{}, false, err
	}
	observedAt, present, err := projectionObservedAt(object, fallback)
	if err != nil || !present {
		return store.RemoteCommandProjection{}, false, err
	}
	result := store.RemoteCommandProjection{CommandID: intent.CommandID, SessionID: sessionID, Ordinal: ordinal, State: state, OutputComplete: false, OutputTruncated: false, Target: target, Controller: controller, Environment: environment, Source: source, Capabilities: capabilities, ObservedAt: observedAt, IsStale: false}
	if raw, ok := object["exit_code"]; ok && string(raw) != "null" {
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: exit_code", ErrRemoteResponse)
		}
		result.ExitCode = &value
	}
	if raw, ok := object["final_event_sequence"]; ok && string(raw) != "null" {
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil {
			return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: final_event_sequence", ErrRemoteResponse)
		}
		result.FinalEventSequence = &value
	}
	if raw, ok := object["output_complete"]; ok {
		if err := json.Unmarshal(raw, &result.OutputComplete); err != nil {
			return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: output_complete", ErrRemoteResponse)
		}
	}
	if raw, ok := object["output_truncated"]; ok {
		if err := json.Unmarshal(raw, &result.OutputTruncated); err != nil {
			return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: output_truncated", ErrRemoteResponse)
		}
	}
	if value, present, readErr := readProjectionString(object, "output_unavailable_reason"); readErr != nil {
		return store.RemoteCommandProjection{}, false, readErr
	} else if present {
		result.OutputUnavailableReason = value
	}
	return result, true, nil
}

// strictRemoteCommandProjectionFromReadReply accepts only the complete
// authoritative read contract. Mutation replies remain deliberately
// sparse-compatible, but a recovery read must never fill missing identity or
// authority context from the local intent and then treat it as target proof.
func strictRemoteCommandProjectionFromReadReply(intent store.LocalIntentRecord, object map[string]json.RawMessage, fallback time.Time) (store.RemoteCommandProjection, error) {
	if err := requireRemoteProjectionReadContext(intent, object); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if err := requireProjectionIdentity(object, "command_id", string(intent.CommandID), "command"); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if err := requireProjectionIdentity(object, "session_id", string(intent.SessionID), "command session"); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if err := requireProjectionScriptHash(object, intent.ScriptBytes); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if err := requireProjectionScriptByteCount(object, len(intent.ScriptBytes)); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if err := requireProjectionBools(object); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	projection, present, err := remoteCommandProjectionFromReply(intent, object, fallback)
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if !present {
		return store.RemoteCommandProjection{}, fmt.Errorf("%w: command projection is incomplete", ErrRemoteResponse)
	}
	if err := validateStrictProjectionOutput(projection.State, projection.ExitCode, projection.FinalEventSequence, projection.OutputComplete, projection.OutputUnavailableReason); err != nil {
		return store.RemoteCommandProjection{}, err
	}
	return projection, nil
}

// strictRemoteJobProjectionFromReadReply is the one-off-job counterpart to
// strictRemoteCommandProjectionFromReadReply. It is used only by read-only
// recovery, where a partial reply is not evidence that a completed job belongs
// to the accepted local intent.
func strictRemoteJobProjectionFromReadReply(intent store.LocalIntentRecord, object map[string]json.RawMessage, fallback time.Time) (store.RemoteJobProjection, error) {
	if err := requireRemoteProjectionReadContext(intent, object); err != nil {
		return store.RemoteJobProjection{}, err
	}
	if err := requireProjectionIdentity(object, "job_id", string(intent.JobID), "job"); err != nil {
		return store.RemoteJobProjection{}, err
	}
	if err := requireProjectionIdentity(object, "session_id", string(intent.SessionID), "job session"); err != nil {
		return store.RemoteJobProjection{}, err
	}
	if err := requireProjectionIdentity(object, "command_id", string(intent.CommandID), "job command"); err != nil {
		return store.RemoteJobProjection{}, err
	}
	if err := requireProjectionBools(object); err != nil {
		return store.RemoteJobProjection{}, err
	}
	projection, present, err := remoteJobProjectionFromReply(intent, object, fallback)
	if err != nil {
		return store.RemoteJobProjection{}, err
	}
	if !present {
		return store.RemoteJobProjection{}, fmt.Errorf("%w: job projection is incomplete", ErrRemoteResponse)
	}
	if projection.CommandState != nil {
		if err := validateStrictProjectionOutput(*projection.CommandState, projection.ExitCode, projection.FinalEventSequence, projection.OutputComplete, projection.OutputUnavailableReason); err != nil {
			return store.RemoteJobProjection{}, err
		}
	} else if projection.ExitCode != nil || projection.FinalEventSequence != nil || projection.OutputComplete || projection.OutputTruncated || projection.OutputUnavailableReason != "" {
		return store.RemoteJobProjection{}, fmt.Errorf("%w: job without command carries command output", ErrRemoteResponse)
	}
	return projection, nil
}

// requireRemoteProjectionReadContext checks context which would otherwise be
// silently inherited from a local intent by the sparse-compatible parser.
func requireRemoteProjectionReadContext(intent store.LocalIntentRecord, object map[string]json.RawMessage) error {
	if _, present := object["execution_target"]; !present {
		return fmt.Errorf("%w: missing projection execution_target", ErrRemoteResponse)
	}
	if _, err := projectionTarget(object, intent.Target); err != nil {
		return err
	}
	expectedController, err := queuedRemoteController(intent.Controller)
	if err != nil {
		return err
	}
	if _, present := object["controller"]; !present {
		return fmt.Errorf("%w: missing projection controller", ErrRemoteResponse)
	}
	if _, err := projectionController(object, expectedController); err != nil {
		return err
	}
	authority, present, err := readProjectionString(object, "authority")
	if err != nil || !present || authority != "remote" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: projection authority", ErrRemoteResponse)
	}
	environment, present, err := readProjectionString(object, "environment")
	if err != nil || !present || environment != intent.Environment {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: projection environment", ErrRemoteResponse)
	}
	if err := requireProjectionSource(object, intent.Source); err != nil {
		return err
	}
	capabilities, present, err := projectionCapabilities(object)
	if err != nil || !present {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: missing projection capabilities", ErrRemoteResponse)
	}
	if capabilities.HostClass == "" || capabilities.Isolation != string(domain.IsolationOSUser) || capabilities.EffectiveAccount != "ubuntu" || capabilities.ServiceLimits == nil {
		return fmt.Errorf("%w: projection capabilities", ErrRemoteResponse)
	}
	if _, present, err := projectionObservedAt(object, time.Time{}); err != nil || !present {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: missing projection observed_at", ErrRemoteResponse)
	}
	return nil
}

func requireProjectionSource(object map[string]json.RawMessage, expected domain.Source) error {
	raw, present := object["source"]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: missing projection source", ErrRemoteResponse)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("%w: projection source", ErrRemoteResponse)
	}
	modeRaw, present := fields["mode"]
	if !present || bytes.Equal(bytes.TrimSpace(modeRaw), []byte("null")) {
		return fmt.Errorf("%w: projection source mode", ErrRemoteResponse)
	}
	var mode string
	if err := json.Unmarshal(modeRaw, &mode); err != nil || mode == "" || mode != string(expected.Mode()) {
		return fmt.Errorf("%w: projection source mode", ErrRemoteResponse)
	}
	_, _, sourcePresent, err := projectionSource(object, expected)
	if err != nil || !sourcePresent {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: missing projection source", ErrRemoteResponse)
	}
	return nil
}

func requireProjectionScriptHash(object map[string]json.RawMessage, script []byte) error {
	actual, present, err := readProjectionString(object, "script_sha256")
	if err != nil || !present {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: missing projection script_sha256", ErrRemoteResponse)
	}
	sum := sha256.Sum256(script)
	if actual != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%w: projection script_sha256", ErrRemoteResponse)
	}
	return nil
}

func requireProjectionScriptByteCount(object map[string]json.RawMessage, expected int) error {
	raw, present := object["script_byte_count"]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: missing projection script_byte_count", ErrRemoteResponse)
	}
	var actual int
	if err := json.Unmarshal(raw, &actual); err != nil || actual != expected {
		return fmt.Errorf("%w: projection script_byte_count", ErrRemoteResponse)
	}
	return nil
}

func requireProjectionBools(object map[string]json.RawMessage) error {
	for _, name := range []string{"output_complete", "output_truncated"} {
		raw, present := object[name]
		if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%w: missing projection %s", ErrRemoteResponse, name)
		}
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("%w: projection %s", ErrRemoteResponse, name)
		}
	}
	return nil
}

func validateStrictProjectionOutput(state domain.CommandState, exitCode *int, final *int64, complete bool, reason string) error {
	if !state.IsTerminal() {
		if reason != "" || complete || final != nil {
			return fmt.Errorf("%w: active command output contract", ErrRemoteResponse)
		}
		return nil
	}
	if final == nil || *final <= 0 {
		return fmt.Errorf("%w: terminal command final event sequence", ErrRemoteResponse)
	}
	if minimum := minimumTerminalEventSequence(state); *final < minimum {
		return fmt.Errorf("%w: terminal command final event sequence %d precedes required boundary %d", ErrRemoteResponse, *final, minimum)
	}
	if state == domain.CommandStateLost && complete {
		return fmt.Errorf("%w: lost command claims complete output", ErrRemoteResponse)
	}
	if complete {
		if reason != "" {
			return fmt.Errorf("%w: complete command unavailable reason", ErrRemoteResponse)
		}
	} else {
		switch reason {
		case "capture_boundary_unconfirmed":
			if state != domain.CommandStateLost {
				return fmt.Errorf("%w: capture boundary requires lost command", ErrRemoteResponse)
			}
		case "remote_event_gap", "retention_expired":
			// A remote suffix or an expired retained prefix makes output
			// incomplete independently of the terminal command state.
		default:
			return fmt.Errorf("%w: terminal command unavailable reason", ErrRemoteResponse)
		}
	}
	switch state {
	case domain.CommandStateSucceeded:
		if exitCode == nil || *exitCode != 0 {
			return fmt.Errorf("%w: succeeded command exit code", ErrRemoteResponse)
		}
	case domain.CommandStateFailed:
		if exitCode == nil || *exitCode == 0 {
			return fmt.Errorf("%w: failed command exit code", ErrRemoteResponse)
		}
	}
	return nil
}

// minimumTerminalEventSequence is implied by the frozen command-event
// lifecycle. Every accepted command first emits command_queued. Only
// cancellation and rejection can terminate directly from queued; every other
// terminal state requires command_started before its terminal event.
func minimumTerminalEventSequence(state domain.CommandState) int64 {
	switch state {
	case domain.CommandStateCancelled, domain.CommandStateRejected:
		return 2
	default:
		return 3
	}
}

func requireProjectionIdentity(object map[string]json.RawMessage, field, expected, label string) error {
	actual, present, err := readProjectionString(object, field)
	if err != nil {
		return err
	}
	if !present || actual != expected {
		return fmt.Errorf("%w: %s identity", ErrRemoteResponse, label)
	}
	return nil
}

func readProjectionString(object map[string]json.RawMessage, name string) (string, bool, error) {
	raw, ok := object[name]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", true, fmt.Errorf("%w: projection %s", ErrRemoteResponse, name)
	}
	return value, true, nil
}

func projectionTarget(object map[string]json.RawMessage, fallback domain.ExecutionTarget) (domain.ExecutionTarget, error) {
	target := fallback
	if raw, ok := object["execution_target"]; ok {
		var input struct {
			Kind    string `json:"kind"`
			Profile string `json:"profile"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return domain.ExecutionTarget{}, fmt.Errorf("%w: execution_target", ErrRemoteResponse)
		}
		parsed, err := domain.NewExecutionTarget(domain.TargetKind(input.Kind), input.Profile)
		if err != nil || parsed != fallback {
			return domain.ExecutionTarget{}, fmt.Errorf("%w: projection target mismatch", ErrRemoteResponse)
		}
		target = parsed
	}
	return target, nil
}

func projectionController(object map[string]json.RawMessage, fallback domain.ControllerIdentity) (domain.ControllerIdentity, error) {
	if raw, ok := object["controller"]; ok {
		var input struct {
			Type string `json:"controller_type"`
			ID   string `json:"controller_id"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return domain.ControllerIdentity{}, fmt.Errorf("%w: controller", ErrRemoteResponse)
		}
		id, err := domain.NewControllerID(input.ID)
		if err != nil {
			return domain.ControllerIdentity{}, err
		}
		parsed, err := domain.NewControllerIdentity(domain.ControllerType(input.Type), id)
		if err != nil || parsed != fallback {
			return domain.ControllerIdentity{}, fmt.Errorf("%w: projection controller mismatch", ErrRemoteResponse)
		}
	}
	return fallback, nil
}

// queuedRemoteController converts the Mac ingress identity into the distinct
// principal authenticated by the server-side SSH key map. The POC deliberately
// uses the same selected account ID in the local_user and queued_mac namespaces.
func queuedRemoteController(controller domain.ControllerIdentity) (domain.ControllerIdentity, error) {
	switch controller.Type() {
	case domain.ControllerTypeQueuedMac:
		return controller, nil
	case domain.ControllerTypeLocalUser:
		mapped, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, controller.ID())
		if err != nil {
			return domain.ControllerIdentity{}, fmt.Errorf("%w: queued controller mapping", ErrRemoteResponse)
		}
		return mapped, nil
	default:
		return domain.ControllerIdentity{}, fmt.Errorf("%w: local intent controller cannot use the queued SSH bridge", ErrRemoteResponse)
	}
}

func projectionSource(object map[string]json.RawMessage, fallback domain.Source) (domain.Source, string, bool, error) {
	raw, ok := object["source"]
	if !ok {
		return fallback, "", false, nil
	}
	var input struct {
		Mode              string `json:"mode"`
		RepositoryAlias   string `json:"repository_alias"`
		RequestedRevision string `json:"requested_revision"`
		ResolvedCommit    string `json:"resolved_commit"`
		Path              string `json:"path"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return domain.Source{}, "", true, fmt.Errorf("%w: source", ErrRemoteResponse)
	}
	var source domain.Source
	switch domain.SourceMode(input.Mode) {
	case "", domain.SourceModeEmpty:
		source = domain.NewEmptySource()
	case domain.SourceModeGitRevision:
		var err error
		source, err = domain.NewGitRevisionSource(input.RepositoryAlias, input.RequestedRevision)
		if err != nil {
			return domain.Source{}, "", true, err
		}
	default:
		return domain.Source{}, "", true, fmt.Errorf("%w: source mode", ErrRemoteResponse)
	}
	if source.Mode() != fallback.Mode() || source.RepositoryAlias() != fallback.RepositoryAlias() || source.RequestedRevision() != fallback.RequestedRevision() {
		return domain.Source{}, "", true, fmt.Errorf("%w: projection source mismatch", ErrRemoteResponse)
	}
	return source, input.ResolvedCommit, true, nil
}

func projectionCapabilities(object map[string]json.RawMessage) (store.RemoteCapabilities, bool, error) {
	raw, ok := object["capabilities"]
	if !ok {
		return store.RemoteCapabilities{}, false, nil
	}
	var capabilities store.RemoteCapabilities
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		return store.RemoteCapabilities{}, true, fmt.Errorf("%w: capabilities", ErrRemoteResponse)
	}
	return capabilities, true, nil
}

func projectionObservedAt(object map[string]json.RawMessage, fallback time.Time) (time.Time, bool, error) {
	raw, ok := object["observed_at"]
	if !ok {
		return time.Time{}, false, nil
	}
	var observed time.Time
	if err := json.Unmarshal(raw, &observed); err != nil || observed.IsZero() {
		return time.Time{}, true, fmt.Errorf("%w: observed_at", ErrRemoteResponse)
	}
	return observed.UTC(), true, nil
}

func remoteJobProjectionFromReply(intent store.LocalIntentRecord, object map[string]json.RawMessage, fallback time.Time) (store.RemoteJobProjection, bool, error) {
	jobID, present, err := readProjectionString(object, "job_id")
	if err != nil || !present {
		return store.RemoteJobProjection{}, false, err
	}
	if jobID != string(intent.JobID) {
		return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job identity", ErrRemoteResponse)
	}
	phaseText, present, err := readProjectionString(object, "job_phase")
	if err != nil || !present {
		return store.RemoteJobProjection{}, false, err
	}
	phase := store.JobPhase(phaseText)
	if !phase.Valid() {
		return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job_phase", ErrRemoteResponse)
	}
	target, err := projectionTarget(object, intent.Target)
	if err != nil {
		return store.RemoteJobProjection{}, false, err
	}
	expectedController, err := queuedRemoteController(intent.Controller)
	if err != nil {
		return store.RemoteJobProjection{}, false, err
	}
	if _, err := projectionController(object, expectedController); err != nil {
		return store.RemoteJobProjection{}, false, err
	}
	controller := intent.Controller
	if value, ok := object["session_id"]; ok {
		var sessionID string
		if json.Unmarshal(value, &sessionID) != nil || sessionID != string(intent.SessionID) {
			return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job session identity", ErrRemoteResponse)
		}
	}
	if value, ok := object["command_id"]; ok {
		var commandID string
		if json.Unmarshal(value, &commandID) != nil || commandID != string(intent.CommandID) {
			return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job command identity", ErrRemoteResponse)
		}
	}
	environment := intent.Environment
	if value, present, readErr := readProjectionString(object, "environment"); readErr != nil {
		return store.RemoteJobProjection{}, false, readErr
	} else if present {
		environment = value
	}
	source, _, sourcePresent, err := projectionSource(object, intent.Source)
	if err != nil {
		return store.RemoteJobProjection{}, false, err
	}
	if !sourcePresent {
		source = intent.Source
	}
	capabilities, capabilitiesPresent, err := projectionCapabilities(object)
	if err != nil || !capabilitiesPresent {
		return store.RemoteJobProjection{}, false, err
	}
	observedAt, observedPresent, err := projectionObservedAt(object, fallback)
	if err != nil || !observedPresent {
		return store.RemoteJobProjection{}, false, err
	}
	teardownText, present, err := readProjectionString(object, "teardown_state")
	if err != nil || !present {
		return store.RemoteJobProjection{}, false, err
	}
	teardownState := store.JobTeardownState(teardownText)
	if !teardownState.Valid() {
		return store.RemoteJobProjection{}, false, fmt.Errorf("%w: teardown_state", ErrRemoteResponse)
	}
	result := store.RemoteJobProjection{JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID, Phase: phase, OutputComplete: false, OutputTruncated: false, TeardownState: teardownState, Target: target, Controller: controller, Environment: environment, Source: source, Capabilities: capabilities, ObservedAt: observedAt, IsStale: false}
	if raw, ok := object["command_state"]; ok && string(raw) != "null" {
		var stateText string
		if json.Unmarshal(raw, &stateText) != nil {
			return store.RemoteJobProjection{}, false, fmt.Errorf("%w: command_state", ErrRemoteResponse)
		}
		state := domain.CommandState(stateText)
		if !state.Valid() {
			return store.RemoteJobProjection{}, false, fmt.Errorf("%w: command_state", ErrRemoteResponse)
		}
		result.CommandState = &state
	}
	if raw, ok := object["exit_code"]; ok && string(raw) != "null" {
		var value int
		if json.Unmarshal(raw, &value) != nil {
			return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job exit_code", ErrRemoteResponse)
		}
		result.ExitCode = &value
	}
	if raw, ok := object["final_event_sequence"]; ok && string(raw) != "null" {
		var value int64
		if json.Unmarshal(raw, &value) != nil {
			return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job final_event_sequence", ErrRemoteResponse)
		}
		result.FinalEventSequence = &value
	}
	for name, destination := range map[string]*bool{"output_complete": &result.OutputComplete, "output_truncated": &result.OutputTruncated} {
		if raw, ok := object[name]; ok {
			if json.Unmarshal(raw, destination) != nil {
				return store.RemoteJobProjection{}, false, fmt.Errorf("%w: job %s", ErrRemoteResponse, name)
			}
		}
	}
	if value, present, readErr := readProjectionString(object, "output_unavailable_reason"); readErr != nil {
		return store.RemoteJobProjection{}, false, readErr
	} else if present {
		result.OutputUnavailableReason = value
	}
	if value, present, readErr := readProjectionString(object, "teardown_reason"); readErr != nil {
		return store.RemoteJobProjection{}, false, readErr
	} else if present {
		result.TeardownReason = value
	}
	return result, true, nil
}
