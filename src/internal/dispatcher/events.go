package dispatcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	validatedCommand, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return store.RemoteEventMirrorResult{}, err
	}
	if controller.Type() == "" || controller.ID() == "" {
		return store.RemoteEventMirrorResult{}, fmt.Errorf("%w: controller is empty", ErrRemoteEventStream)
	}
	var intent *store.LocalIntentRecord
	if candidate, lookupErr := d.authority.GetLocalIntentByResource(ctx, operationSubmitCommand, string(validatedCommand), controller); lookupErr == nil {
		intent = &candidate
	} else if !errors.Is(lookupErr, store.ErrLocalIntentNotFound) {
		return store.RemoteEventMirrorResult{}, lookupErr
	}
	return d.mirrorCommandEvents(ctx, validatedCommand, controller, intent)
}

// mirrorCommandEventsForIntent mirrors events for a known remote intent. A
// one-off run owns its command under operation=run, so retaining that intent
// is required to validate an event-history-gap terminal read against the
// correct immutable script and target context.
func (d *RemoteDriver) mirrorCommandEventsForIntent(ctx context.Context, intent store.LocalIntentRecord) (store.RemoteEventMirrorResult, error) {
	if intent.Target.Kind() != domain.TargetKindRemote || (intent.Operation != operationSubmitCommand && intent.Operation != "run") || intent.CommandID == "" {
		return store.RemoteEventMirrorResult{}, fmt.Errorf("%w: remote command intent", ErrRemoteEventStream)
	}
	validatedCommand, err := domain.NewCommandID(string(intent.CommandID))
	if err != nil {
		return store.RemoteEventMirrorResult{}, err
	}
	return d.mirrorCommandEvents(ctx, validatedCommand, intent.Controller, &intent)
}

func (d *RemoteDriver) mirrorCommandEvents(ctx context.Context, validatedCommand domain.CommandID, controller domain.ControllerIdentity, intent *store.LocalIntentRecord) (store.RemoteEventMirrorResult, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.RemoteEventMirrorResult{}, ErrRemoteDriverConfiguration
	}
	streamer, ok := d.caller.(RemoteEventStreamer)
	if !ok {
		return store.RemoteEventMirrorResult{}, ErrRemoteEventCallerUnavailable
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
		Payload:         payload,
	}
	result := store.RemoteEventMirrorResult{CommandID: validatedCommand, LastSequence: after}
	sawStreamEnd := false
	sawTerminalEvent := false
	streamErr := streamer.Stream(ctx, request, func(reply sshbridge.ReplyFrame) error {
		if reply.ProtocolVersion != sshbridge.ProtocolVersion || reply.RequestID != request.RequestID {
			return fmt.Errorf("%w: protocol or request identity mismatch", ErrRemoteEventStream)
		}
		if sawStreamEnd {
			return fmt.Errorf("%w: frame after stream end", ErrRemoteEventStream)
		}
		switch reply.ResponseType {
		case "event":
			if sawTerminalEvent {
				return fmt.Errorf("%w: event follows a terminal command event", ErrRemoteEventStream)
			}
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
			if isRemoteTerminalEvent(event.Type) {
				sawTerminalEvent = true
			}
			return nil
		case "stream_end":
			end, err := decodeRemoteStreamEnd(reply.Payload)
			if err != nil || end != result.LastSequence {
				return fmt.Errorf("%w: stream end last_sequence=%d cursor=%d", ErrRemoteEventCursor, end, result.LastSequence)
			}
			sawStreamEnd = true
			return nil
		case "error":
			if sawTerminalEvent {
				return fmt.Errorf("%w: history error follows a terminal command event", ErrRemoteEventStream)
			}
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
	if streamErr == nil && !sawStreamEnd {
		streamErr = fmt.Errorf("%w: stream ended without stream_end", ErrRemoteEventStream)
	}
	if streamErr != nil {
		// A failed mirror leaves the last remote snapshot usable but explicitly
		// stale. Missing views are harmless because acceptance may have returned
		// only an identity envelope in older bridge responses.
		if markErr := d.authority.MarkRemoteCommandProjectionStale(ctx, validatedCommand); markErr != nil && !errors.Is(markErr, store.ErrRemoteProjectionNotFound) {
			return result, markErr
		}
		var gapErr *remoteEventGapError
		if !errors.As(streamErr, &gapErr) {
			return result, streamErr
		}
		if gapErr.available != result.LastSequence {
			return result, fmt.Errorf("%w: stream prefix %d differs from durable cursor %d", ErrRemoteEventCursor, gapErr.available, result.LastSequence)
		}
		gap, confirmErr := d.confirmRemoteTerminal(ctx, validatedCommand, controller, result.LastSequence, intent)
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

// RefreshCommandProjection reads and persists the target authority's current
// command state for mailbox/API readers. It does not settle the local intent:
// that requires the event-boundary proof performed by accepted-command
// reconciliation. Event mirroring alone advances output cursors but does not
// infer a terminal command state.
func (d *RemoteDriver) RefreshCommandProjection(ctx context.Context, commandID domain.CommandID, controller domain.ControllerIdentity) (store.RemoteCommandProjection, error) {
	if d == nil || d.authority == nil || d.caller == nil {
		return store.RemoteCommandProjection{}, ErrRemoteDriverConfiguration
	}
	validatedCommand, err := domain.NewCommandID(string(commandID))
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	intent, err := d.authority.GetLocalIntentByResource(ctx, operationSubmitCommand, string(validatedCommand), controller)
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	return d.refreshCommandProjectionForIntent(ctx, intent)
}

// refreshCommandProjectionForIntent reads an authoritative command state for
// either a session command or the command owned by a one-off run. The latter
// has no standalone submit_command intent, so callers that already hold its
// run intent must use this helper rather than inventing an extra intent.
func (d *RemoteDriver) refreshCommandProjectionForIntent(ctx context.Context, intent store.LocalIntentRecord) (store.RemoteCommandProjection, error) {
	if intent.Target.Kind() != domain.TargetKindRemote || (intent.Operation != operationSubmitCommand && intent.Operation != "run") ||
		(intent.DeliveryState != store.LocalIntentAccepted && intent.DeliveryState != store.LocalIntentReconciled) || intent.CommandID == "" || intent.SessionID == "" {
		return store.RemoteCommandProjection{}, fmt.Errorf("%w: command is not an accepted remote intent", ErrRemoteResponse)
	}
	validatedCommand, err := domain.NewCommandID(string(intent.CommandID))
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	payload, err := json.Marshal(map[string]string{"command_id": string(validatedCommand)})
	if err != nil {
		return store.RemoteCommandProjection{}, fmt.Errorf("%w: build command read payload", ErrRemotePayload)
	}
	request := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       fmt.Sprintf("command-state/%s/%d", validatedCommand, remoteProjectionReadSequence.Add(1)),
		Operation:       sshbridge.OperationGetCommand,
		Payload:         payload,
	}
	object, err := d.readRemoteProjectionObject(ctx, request, "command")
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	projection, err := strictRemoteCommandProjectionFromReadReply(intent, object, d.now())
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	stored, err := d.authority.UpsertRemoteCommandProjection(ctx, projection)
	if err != nil {
		return store.RemoteCommandProjection{}, err
	}
	if stored.IsStale || stored.ObservedAt.After(projection.ObservedAt) {
		return store.RemoteCommandProjection{}, fmt.Errorf("%w: command read did not replace a stale or newer projection", ErrRemoteResponse)
	}
	return stored, nil
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
func (d *RemoteDriver) confirmRemoteTerminal(ctx context.Context, commandID domain.CommandID, controller domain.ControllerIdentity, available int64, ownedIntent *store.LocalIntentRecord) (store.RemoteEventGapRecord, error) {
	var intent store.LocalIntentRecord
	if ownedIntent != nil {
		intent = *ownedIntent
	} else {
		var err error
		intent, err = d.authority.GetLocalIntentByResource(ctx, operationSubmitCommand, string(commandID), controller)
		if err != nil {
			return store.RemoteEventGapRecord{}, fmt.Errorf("%w: read matching local command intent: %v", ErrRemoteTerminalUnconfirmed, err)
		}
	}
	if intent.Target.Kind() != domain.TargetKindRemote || (intent.Operation != operationSubmitCommand && intent.Operation != "run") || intent.CommandID != commandID || intent.SessionID == "" || intent.Controller != controller {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: matching remote command intent", ErrRemoteTerminalUnconfirmed)
	}
	payload, _ := json.Marshal(map[string]string{"command_id": string(commandID)})
	request := sshbridge.RequestFrame{
		ProtocolVersion: sshbridge.ProtocolVersion,
		RequestID:       fmt.Sprintf("terminal/%s/%d", commandID, available),
		Operation:       sshbridge.OperationGetCommand,
		Payload:         payload,
	}
	object, err := d.readRemoteProjectionObject(ctx, request, "terminal command")
	if err != nil {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: %v", ErrRemoteTerminalUnconfirmed, err)
	}
	projection, err := strictRemoteCommandProjectionFromReadReply(intent, object, d.now())
	if err != nil {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: %v", ErrRemoteTerminalUnconfirmed, err)
	}
	if !projection.State.IsTerminal() || projection.FinalEventSequence == nil || *projection.FinalEventSequence <= available {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: final event sequence does not extend available prefix", ErrRemoteTerminalUnconfirmed)
	}
	if err := d.validateRemoteEventGapPrefix(ctx, commandID, available, projection); err != nil {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: retained event prefix: %v", ErrRemoteTerminalUnconfirmed, err)
	}
	stored, err := d.authority.UpsertRemoteCommandProjection(ctx, projection)
	if err != nil {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: persist terminal command read: %v", ErrRemoteTerminalUnconfirmed, err)
	}
	if stored.IsStale || stored.ObservedAt.After(projection.ObservedAt) {
		return store.RemoteEventGapRecord{}, fmt.Errorf("%w: terminal command read is stale", ErrRemoteTerminalUnconfirmed)
	}
	projection = stored
	return store.RemoteEventGapRecord{
		CommandID: commandID, MissingFrom: available + 1, MissingTo: *projection.FinalEventSequence,
		AvailableSequence: available, FinalSequence: *projection.FinalEventSequence, TerminalState: projection.State,
		OutputComplete: false, OutputUnavailableReason: "remote_event_gap", ConfirmedAt: d.now().UTC(),
	}, nil
}

// validateRemoteEventGapPrefix makes the retained event evidence part of the
// terminal-gap proof. A missing suffix can add a truncation marker, but it can
// never erase one already retained locally. Keeping this check in the shared
// confirmation path protects both normal remote commands and one-off runs.
func (d *RemoteDriver) validateRemoteEventGapPrefix(ctx context.Context, commandID domain.CommandID, available int64, projection store.RemoteCommandProjection) error {
	cursor, err := d.authority.GetRemoteEventCursor(ctx, commandID)
	if err != nil {
		return err
	}
	if cursor != available {
		return fmt.Errorf("%w: durable cursor %d differs from reported available sequence %d", ErrRemoteEventCursor, cursor, available)
	}
	events, err := d.authority.ListRemoteEvents(ctx, commandID, 0)
	if err != nil {
		return err
	}
	if int64(len(events)) != cursor || (cursor > 0 && (len(events) == 0 || events[len(events)-1].Sequence != cursor)) {
		return fmt.Errorf("%w: retained prefix does not match cursor %d", ErrRemoteEventCursor, cursor)
	}
	lifecycle, err := store.InspectRemoteEventLifecycle(events)
	if err != nil {
		return err
	}
	if lifecycle.TerminalState != nil {
		return fmt.Errorf("%w: retained prefix already has terminal state %q", ErrRemoteEventCursor, *lifecycle.TerminalState)
	}
	if lifecycle.OutputTruncated && !projection.OutputTruncated {
		return fmt.Errorf("%w: target terminal projection loses retained output truncation", ErrRemoteTerminalUnconfirmed)
	}
	return nil
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
		_, err = d.authority.MarkRemoteIntentTerminalProof(ctx, intent.IntentID, "remote_terminal_confirmed_event_gap")
		return err
	case store.LocalIntentUncertain:
		accepted, transitionErr := d.authority.TransitionLocalIntent(ctx, intent.IntentID, store.LocalIntentAccepted, "remote_terminal_confirmed")
		if transitionErr != nil {
			return transitionErr
		}
		_, err = d.authority.MarkRemoteIntentTerminalProof(ctx, accepted.IntentID, "remote_terminal_confirmed_event_gap")
		return err
	case store.LocalIntentReconciled:
		if store.HasRemoteTerminalProof(intent) {
			return nil
		}
		_, err = d.authority.MarkRemoteIntentTerminalProof(ctx, intent.IntentID, "remote_terminal_confirmed_event_gap")
		return err
	case store.LocalIntentNotDelivered:
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
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: event payload has trailing data", ErrRemoteEventStream)
	}
	if input.CommandID != string(commandID) || input.Sequence <= 0 || !validRemoteEventReplyType(input.Type) || input.Timestamp.IsZero() {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: event envelope", ErrRemoteEventStream)
	}
	if input.Sequence == 1 && input.Type != "command_queued" {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: sequence one must be command_queued", ErrRemoteEventStream)
	}
	if input.Sequence > 1 && input.Type == "command_queued" {
		return store.RemoteEventRecord{}, fmt.Errorf("%w: command_queued must be sequence one", ErrRemoteEventStream)
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

func decodeRemoteStreamEnd(raw []byte) (int64, error) {
	var end struct {
		LastSequence int64 `json:"last_sequence"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&end); err != nil {
		return 0, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return 0, fmt.Errorf("trailing stream end data")
	}
	if end.LastSequence < 0 {
		return 0, fmt.Errorf("negative stream end cursor")
	}
	return end.LastSequence, nil
}

func validRemoteEventReplyType(value string) bool {
	switch value {
	case "command_queued", "command_started", "stdout", "stderr", "output_truncated", "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}

func isRemoteTerminalEvent(value string) bool {
	switch value {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}
