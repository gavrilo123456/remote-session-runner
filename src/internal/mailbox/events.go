package mailbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

const mailboxEventChunkBytes = 16 * 1024

var (
	ErrEventProjectionConfiguration = errors.New("mailbox event projector configuration is invalid")
	ErrEventProjection              = errors.New("mailbox event projection failed")
	ErrEventProjectionChunk         = errors.New("mailbox event chunk exceeds projection limit")
)

// EventProjector renders the durable command event stream in the mailbox's
// lossless NDJSON representation. It does not publish or sync a file; those
// durability boundaries are owned by later mailbox phases.
type EventProjector struct {
	Authority *store.AuthorityStore
	Clock     func() time.Time
}

// Project reads one contiguous durable command stream and returns newline-
// terminated mailbox event records plus its highest available sequence.
func (p EventProjector) Project(ctx context.Context, commandID domain.CommandID) ([]byte, int64, error) {
	if p.Authority == nil {
		return nil, 0, ErrEventProjectionConfiguration
	}
	command, err := p.Authority.GetCommand(ctx, commandID)
	if err != nil {
		return nil, 0, err
	}
	events, err := p.Authority.ListCommandEvents(ctx, commandID)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, 0, fmt.Errorf("%w: no retained events", ErrEventProjection)
	}
	data, err := RenderCommandEvents(command, events)
	if err != nil {
		return nil, 0, err
	}
	return data, events[len(events)-1].Sequence, nil
}

// RenderCommandEvents converts store events to the versioned mailbox event
// shape. Every output payload is represented as either a whole valid UTF-8
// chunk or exact base64 bytes; no replacement characters or dropped bytes are
// possible.
func RenderCommandEvents(command store.CommandRecord, events []store.CommandEventRecord) ([]byte, error) {
	if command.CommandID == "" || command.Ordinal < 1 || len(events) == 0 {
		return nil, fmt.Errorf("%w: command or event stream is empty", ErrEventProjection)
	}
	var output bytes.Buffer
	for index, event := range events {
		if event.CommandID != command.CommandID || event.Sequence != int64(index+1) || event.Type == "" || event.OccurredAt.IsZero() {
			return nil, fmt.Errorf("%w: non-contiguous event at index %d", ErrEventProjection, index)
		}
		if (event.Type == "stdout" || event.Type == "stderr") && len(event.Payload) > mailboxEventChunkBytes {
			return nil, fmt.Errorf("%w: sequence %d has %d bytes", ErrEventProjectionChunk, event.Sequence, len(event.Payload))
		}
		if (event.Type == "stdout" || event.Type == "stderr") && int64(len(event.Payload)) != event.ByteCount {
			return nil, fmt.Errorf("%w: sequence %d byte count mismatch", ErrEventProjection, event.Sequence)
		}
		if event.Type != "stdout" && event.Type != "stderr" && (len(event.Payload) != 0 || event.ByteCount != 0) {
			return nil, fmt.Errorf("%w: non-output sequence %d carries bytes", ErrEventProjection, event.Sequence)
		}
		projected := mailboxEvent{
			CommandID: command.CommandID,
			Sequence:  event.Sequence,
			Type:      event.Type,
			Timestamp: event.OccurredAt.UTC().Format(time.RFC3339Nano),
			Ordinal:   command.Ordinal,
		}
		if command.ExitCode != nil && isTerminalMailboxEvent(event.Type) {
			value := *command.ExitCode
			projected.ExitCode = &value
		}
		if event.Type == "stdout" || event.Type == "stderr" {
			projected.ByteCount = event.ByteCount
			if utf8.Valid(event.Payload) {
				projected.Encoding = "utf8"
				projected.Text = string(event.Payload)
			} else {
				projected.Encoding = "base64"
				projected.DataBase64 = base64.StdEncoding.EncodeToString(event.Payload)
			}
		}
		line, err := json.Marshal(projected)
		if err != nil {
			return nil, fmt.Errorf("%w: marshal sequence %d: %v", ErrEventProjection, event.Sequence, err)
		}
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

type mailboxEvent struct {
	CommandID  domain.CommandID `json:"command_id"`
	Sequence   int64            `json:"sequence"`
	Type       string           `json:"type"`
	Timestamp  string           `json:"timestamp"`
	Ordinal    int64            `json:"ordinal,omitempty"`
	Encoding   string           `json:"encoding,omitempty"`
	Text       string           `json:"text,omitempty"`
	DataBase64 string           `json:"data_base64,omitempty"`
	ByteCount  int64            `json:"byte_count,omitempty"`
	ExitCode   *int             `json:"exit_code,omitempty"`
}

func isTerminalMailboxEvent(eventType string) bool {
	switch eventType {
	case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
		return true
	default:
		return false
	}
}
