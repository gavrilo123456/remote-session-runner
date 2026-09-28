package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

// upsertRemoteProjectionFromReply records a complete remote snapshot when the
// authority returned the projection fields. Older bridge fixtures intentionally
// return only acceptance identity; those replies remain valid but cannot
// manufacture a status projection.
func (d *RemoteDriver) upsertRemoteProjectionFromReply(ctx context.Context, intent store.LocalIntentRecord, reply sshbridge.ReplyFrame) error {
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
	if intent.Operation == operationSubmitCommand {
		projection, present, err := remoteCommandProjectionFromReply(intent, object, d.now())
		if err != nil || !present {
			return err
		}
		_, err = d.authority.UpsertRemoteCommandProjection(ctx, projection)
		return err
	}
	if intent.Operation == "run" {
		projection, present, err := remoteJobProjectionFromReply(intent, object, d.now())
		if err != nil || !present {
			return err
		}
		_, err = d.authority.UpsertRemoteJobProjection(ctx, projection)
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
	ordinal := int64(0)
	if intent.IntentOrdinal != nil {
		ordinal = *intent.IntentOrdinal
	}
	if raw, present := object["ordinal"]; present {
		if err := json.Unmarshal(raw, &ordinal); err != nil || ordinal <= 0 || intent.IntentOrdinal != nil && ordinal != *intent.IntentOrdinal {
			return store.RemoteCommandProjection{}, false, fmt.Errorf("%w: command ordinal", ErrRemoteResponse)
		}
	}
	if ordinal <= 0 {
		return store.RemoteCommandProjection{}, false, nil
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
