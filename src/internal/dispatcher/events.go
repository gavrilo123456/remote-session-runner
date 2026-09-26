package dispatcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

var (
	ErrRemoteEventCallerUnavailable  = errors.New("remote event stream is unavailable")
	ErrRemoteEventStream             = errors.New("remote event stream is invalid")
	ErrRemoteEventHistoryUnavailable = errors.New("remote event history is unavailable")
	ErrRemoteEventCursor             = errors.New("remote event stream cursor is invalid")
	ErrRemoteTerminalUnconfirmed     = errors.New("remote terminal outcome is not independently confirmed")
)

type remoteEventGapError struct {
	available int64
	message   string
}

func (e *remoteEventGapError) Error() string {
	return fmt.Sprintf("%s: available sequence %d: %s", ErrRemoteEventHistoryUnavailable, e.available, e.message)
}

func (e *remoteEventGapError) Unwrap() error { return ErrRemoteEventHistoryUnavailable }

// RemoteEventStreamer is implemented by the SSH client and by bounded stream
// fakes. It delivers bridge event frames in wire order without mutation IDs.
type RemoteEventStreamer interface {
	Stream(context.Context, sshbridge.RequestFrame, func(sshbridge.ReplyFrame) error) error
}

// MirrorCommandEvents replays remote command events after the durable Mac
// cursor and commits each event plus its contiguous cursor atomically.
func (d *RemoteDriver) MirrorCommandEvents(ctx context.Context, commandID domain.CommandID, controller domain.ControllerIdentity) (store.RemoteEventMirrorResult, error) {
	if d == nil || d.authority == nil {
		return store.RemoteEventMirrorResult{}, ErrRemoteDriverConfiguration
	}
	streamer, ok := d.caller.(RemoteEventStreamer)
	if !ok {
		return store.RemoteEventMirrorResult{}, ErrRemoteEventCallerUnavailable
	}
	validatedCommand, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return store.RemoteEventMirrorResult{}, err
	}
	if controller.Type() == "" || controller.ID() == "" {
		return store.RemoteEventMirrorResult{}, fmt.Errorf("%w: controller is empty", ErrRemoteEventStream)
	}
	after, err := d.authority.GetRemoteEventCursor(ctx, validatedCommand)
	if err != nil {
		return store.RemoteEventMirrorResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{"command_id": string(validatedCommand), "after_sequence": after})
	request := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       fmt.Sprintf("events/%s/%d", validatedCommand, after),
		Operation:       sshbridge.OperationStreamCommandEvents,
		ResourceID:      string(validatedCommand),
		Payload:         payload,
	}
	result := store.RemoteEventMirrorResult{CommandID: validatedCommand, LastSequence: after}
	streamErr := streamer.Stream(ctx, request, func(reply sshbridge.ReplyFrame) error {
		if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != request.RequestID {
			return fmt.Errorf("%w: protocol or request identity mismatch", ErrRemoteEventStream)
		}
		switch reply.ResponseType {
		case "event":
			event, err := decodeRemoteEventReply(validatedCommand, reply.Payload)
			if err != nil {
				return err
			}
			mirrored, err := d.authority.MirrorRemoteEvents(ctx, []store.RemoteEventRecord{event})
			if err != nil {
				return err
			}
			result.LastSequence = mirrored.LastSequence
			result.Mirrored += mirrored.Mirrored
			result.Duplicates += mirrored.Duplicates
			return nil
		case "stream_end":
			var end struct {
				LastSequence int64 `json:"last_sequence"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(reply.Payload)))
			if err := decoder.Decode(&end); err != nil || end.LastSequence != result.LastSequence {
				return fmt.Errorf("%w: stream end last_sequence=%d cursor=%d", ErrRemoteEventCursor, end.LastSequence, result.LastSequence)
			}
			return nil
		case "error":
			var payload sshbridge.ErrorPayload
			decoder := json.NewDecoder(strings.NewReader(string(reply.Payload)))
			if err := decoder.Decode(&payload); err != nil {
				return fmt.Errorf("%w: error payload: %v", ErrRemoteEventStream, err)
			}
			if payload.Code == "event_history_unavailable" {
				if available, ok := remoteGapAvailableSequence(payload.Details); ok {
					return &remoteEventGapError{available: available, message: payload.Message}
				}
				return fmt.Errorf("%w: %s", ErrRemoteEventHistoryUnavailable, payload.Message)
			}
			return fmt.Errorf("%w: %s", ErrRemoteEventStream, payload.Message)
		default:
			return fmt.Errorf("%w: response type %q", ErrRemoteEventStream, reply.ResponseType)
		}
	})
	if streamErr != nil {
		var gapErr *remoteEventGapError
		if !errors.As(streamErr, &gapErr) {
			return result, streamErr
		}
		if gapErr.available != result.LastSequence {
			return result, fmt.Errorf("%w: stream prefix %d differs from durable cursor %d", ErrRemoteEventCursor, gapErr.available, result.LastSequence)
		}
		gap, confirmErr := d.confirmRemoteTerminal(ctx, validatedCommand, controller, result.LastSequence)
		if confirmErr != nil {
			return result, fmt.Errorf("%w: %w", ErrRemoteEventHistoryUnavailable, confirmErr)
		}
		confirmed, recordErr := d.authority.RecordRemoteEventGap(ctx, gap)
		if recordErr != nil {
			return result, recordErr
		}
		if err := d.reconcileIntentAfterRemoteGap(ctx, validatedCommand, controller); err != nil {
			return result, err
		}
		result.Gap = &confirmed
		return result, nil
	}
	return result, nil
}

func remoteGapAvailableSequence(details map[string]any) (int64, bool) {
	value, ok := details["last_sequence"]
	if !ok {
		return 0, false
	}
	switch number := value.(type) {
	case float64:
		if number >= 0 && number == float64(int64(number)) {
			return int64(number), true
		}
	case json.Number:
		parsed, err := number.Int64()
		if err == nil && parsed >= 0 {
			return parsed, true
		}
	case int64:
		if number >= 0 {
			return number, true
		}
	}
	return 0, false
}

// confirmRemoteTerminal performs a separate authoritative read after an
// event-history gap. It never changes the event cursor and only accepts a
// terminal command whose final sequence extends past the available prefix.
func (d *RemoteDriver) confirmRemoteTerminal(ctx context.Context, commandID domain.CommandID, controller domain.ControllerIdentity, available int64) (store.RemoteEventGapRecord, error) {
	payload, _ := json.Marshal(map[string]string{"command_id": string(commandID)})
	request := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       fmt.Sprintf("terminal/%s/%d", commandID, available),
		Operation:       sshbridge.OperationGetCommand,
		ResourceID:      string(commandID),
		Payload:         payload,
	}
	reply, err := d.caller.Call(ctx, request)
	if err != nil {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: terminal read transport: %v", ErrRemoteTerminalUnconfirmed, err)
	}
	if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != request.RequestID {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: terminal read protocol or request identity mismatch", ErrRemoteTerminalUnconfirmed)
	}
	if reply.ResponseType == "error" {
		var payload sshbridge.ErrorPayload
		decoder := json.NewDecoder(strings.NewReader(string(reply.Payload)))
		if err := decoder.Decode(&payload); err != nil {
			return store.RemoteEventGapRecord{}, fmt.Errorf("%w: terminal error payload: %v", ErrRemoteTerminalUnconfirmed, err)
		}
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: %s", ErrRemoteTerminalUnconfirmed, payload.Message)
	}
	if reply.ResponseType != "result" {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: terminal response type %q", ErrRemoteTerminalUnconfirmed, reply.ResponseType)
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(reply.Payload)))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: terminal result object", ErrRemoteTerminalUnconfirmed)
	}
	readString := func(name string) (string, error) {
		raw, ok := object[name]
		if !ok {
			return "", fmt.Errorf("%w: terminal result missing %s", ErrRemoteTerminalUnconfirmed, name)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%w: terminal result invalid %s", ErrRemoteTerminalUnconfirmed, name)
		}
		return value, nil
	}
	if value, err := readString("command_id"); err != nil || value != string(commandID) {
		if err != nil {
			return store.RemoteEventGapRecord{}, err
		}
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: command_id mismatch", ErrRemoteTerminalUnconfirmed)
	}
	stateText, err := readString("command_state")
	if err != nil {
		return store.RemoteEventGapRecord{}, err
	}
	state := domain.CommandState(stateText)
	if !state.Valid() || !state.IsTerminal() {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: command state %q is not terminal", ErrRemoteTerminalUnconfirmed, stateText)
	}
	var finalSequence int64
	if raw, ok := object["final_event_sequence"]; !ok || json.Unmarshal(raw, &finalSequence) != nil || finalSequence <= available {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: final event sequence does not extend available prefix", ErrRemoteTerminalUnconfirmed)
	}
	return store.RemoteEventGapRecord{
		CommandID: commandID, MissingFrom: available + 1, MissingTo: finalSequence,
		AvailableSequence: available, FinalSequence: finalSequence, TerminalState: state,
		OutputComplete: false, OutputUnavailableReason: "remote_event_gap", ConfirmedAt: d.now().UTC(),
	}, nil
}

// reconcileIntentAfterRemoteGap settles only an already accepted/confirmed
// submit intent. Recorded or uncertain work remains blocked because the gap
// cannot itself prove that the mutation was delivered.
func (d *RemoteDriver) reconcileIntentAfterRemoteGap(ctx context.Context, commandID domain.CommandID, controller domain.ControllerIdentity) error {
	intent, err := d.authority.GetLocalIntentByResource(ctx, operationSubmitCommand, string(commandID), controller)
	if errors.Is(err, store.ErrLocalIntentNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch intent.DeliveryState {
	case store.LocalIntentAccepted:
		_, err = d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentReconciled, "remote_terminal_confirmed_event_gap")
		return err
	case store.LocalIntentUncertain:
		accepted, transitionErr := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "remote_terminal_confirmed")
		if transitionErr != nil {
			return transitionErr
		}
		_, err = d.authority.TransitionLocalIntent(ctx, accepted.IntentID, store.LocalIntentReconciled, "remote_terminal_confirmed_event_gap")
		return err
	case store.LocalIntentReconciled, store.LocalIntentNotDelivered:
		return nil
	default:
		return fmt.Errorf("%w: submit intent state %s", ErrRemoteTerminalUnconfirmed, intent.DeliveryState)
	}
}

type remoteEventReply struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Encoding   string    `json:"encoding,omitempty"`
	DataBase64 string    `json:"data_base64,omitempty"`
	ByteCount  int64     `json:"byte_count,omitempty"`
}

func decodeRemoteEventReply(commandID domain.CommandID, raw []byte) (store.RemoteEventRecord, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var input remoteEventReply
	if err := decoder.Decode(&input); err != nil {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: event payload: %v", ErrRemoteEventStream, err)
	}
	if input.CommandID != string(commandID) || input.Sequence <= 0 || strings.TrimSpace(input.Type) == "" || input.Timestamp.IsZero() {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: event envelope", ErrRemoteEventStream)
	}
	if input.ByteCount < 0 {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: negative byte count", ErrRemoteEventStream)
	}
	var payload []byte
	if input.DataBase64 != "" {
		var err error
		payload, err = base64.StdEncoding.DecodeString(input.DataBase64)
		if err != nil || len(payload) > 16<<10 {
			return store.RemoteEventRecord{}, fmt.Errorf("%w: output bytes", ErrRemoteEventStream)
		}
	}
	if input.Type == "stdout" || input.Type == "stderr" {
		if len(payload) == 0 || int64(len(payload)) != input.ByteCount {
			return store.RemoteEventRecord{}, fmt.Errorf("%w: output byte count", ErrRemoteEventStream)
		}
	} else if len(payload) != 0 || input.ByteCount != 0 {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: non-output payload", ErrRemoteEventStream)
	}
	return store.RemoteEventRecord{CommandID: commandID, Sequence: input.Sequence, Type: input.Type, Payload: payload, ByteCount: input.ByteCount, OccurredAt: input.Timestamp.UTC()}, nil
}
