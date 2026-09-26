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
)

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
				return fmt.Errorf("%w: %s", ErrRemoteEventHistoryUnavailable, payload.Message)
			}
			return fmt.Errorf("%w: %s", ErrRemoteEventStream, payload.Message)
		default:
			return fmt.Errorf("%w: response type %q", ErrRemoteEventStream, reply.ResponseType)
		}
	})
	if streamErr != nil {
		return result, streamErr
	}
	return result, nil
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
