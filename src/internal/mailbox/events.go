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
const mailboxInlinePreviewBytes = 4 * 1024

var (
	ErrEventProjectionConfiguration = errors.New("mailbox event projector configuration is invalid")
	ErrEventProjection              = errors.New("mailbox event projection failed")
	ErrEventProjectionChunk         = errors.New("mailbox event chunk exceeds projection limit")
)

type inlineOutputPreview struct {
	bytes   []byte
	invalid bool
}

func (p *inlineOutputPreview) append(chunk []byte) {
	if !utf8.Valid(chunk) {
		p.invalid = true
		return
	}
	remaining := mailboxInlinePreviewBytes - len(p.bytes)
	if remaining <= 0 {
		return
	}
	length := len(chunk)
	if length > remaining {
		length = remaining
		for length > 0 && !utf8.RuneStart(chunk[length]) {
			length--
		}
	}
	p.bytes = append(p.bytes, chunk[:length]...)
}

func (p inlineOutputPreview) string() string {
	if p.invalid || len(p.bytes) == 0 {
		return ""
	}
	return string(p.bytes)
}

// inlineOutputPreviews returns bounded UTF-8 previews for the two output
// streams. If a stream contains binary data, its preview is omitted; the
// ordered event projection remains the lossless source in every case.
func inlineOutputPreviews(events []store.CommandEventRecord) (stdout, stderr string) {
	var out, errOut inlineOutputPreview
	for _, event := range events {
		switch event.Type {
		case "stdout":
			out.append(event.Payload)
		case "stderr":
			errOut.append(event.Payload)
		}
	}
	return out.string(), errOut.string()
}

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
	return p.ProjectThrough(ctx, commandID, 0)
}

// ProjectThrough renders only the durable prefix through sequence. A zero
// limit renders the complete currently available stream; a positive limit
// preserves an older frozen response cursor even when later events exist.
func (p EventProjector) ProjectThrough(ctx context.Context, commandID domain.CommandID, through int64) ([]byte, int64, error) {
	if p.Authority == nil {
		return nil, 0, ErrEventProjectionConfiguration
	}
	if through < 0 {
		return nil, 0, fmt.Errorf("%w: negative sequence limit", ErrEventProjection)
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
	if through > 0 {
		if int64(len(events)) < through {
			return nil, 0, fmt.Errorf("%w: retained cursor %d is below requested %d", ErrEventProjection, len(events), through)
		}
		events = events[:through]
	}
	data, err := RenderCommandEvents(command, events)
	if err != nil {
		return nil, 0, err
	}
	return data, events[len(events)-1].Sequence, nil
}

// ProjectMailboxResponseThrough rebuilds only the event prefix frozen into a
// live terminal mailbox response. The store authorizes this exceptional read
// through the durable response-to-command reference and cleanup deadline.
func (p EventProjector) ProjectMailboxResponseThrough(ctx context.Context, requestID string, commandID domain.CommandID) ([]byte, int64, error) {
	if p.Authority == nil {
		return nil, 0, ErrEventProjectionConfiguration
	}
	command, events, err := p.Authority.MailboxResponseCommandEvents(ctx, requestID, commandID)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, 0, fmt.Errorf("%w: pinned response has no events", ErrEventProjection)
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
