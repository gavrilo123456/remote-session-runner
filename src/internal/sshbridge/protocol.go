package sshbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"remote-session-runner/src/internal/domain"
)

// ProtocolVersion is the current version of the private bridge wire contract.
// Version negotiation is deliberately strict: an unknown major is rejected
// before a request is dispatched.
const ProtocolVersion = 1

const defaultFrameReaderBuffer = 4 << 10

var (
	ErrFrameTooLarge           = domain.ErrSerializedInputTooLarge
	ErrUnsupportedProtocol     = errors.New("unsupported SSH bridge protocol version")
	ErrInvalidFrame            = errors.New("invalid SSH bridge frame")
	ErrAuthenticatedKeyEmpty   = errors.New("authenticated SSH key is empty")
	ErrUnknownAuthenticatedKey = errors.New("authenticated SSH key is not mapped")
	ErrControllerField         = errors.New("controller identity must not be supplied in a bridge frame")
	ErrOperationUnsupported    = errors.New("SSH bridge operation is not available")
)

// Operation is one of the frozen v1 bridge operation names.
type Operation string

const (
	OperationHello                 Operation = "hello"
	OperationCreateOrResumeSession Operation = "create_or_resume_session"
	OperationGetSession            Operation = "get_session"
	OperationSubmitOrResumeCommand Operation = "submit_or_resume_command"
	OperationGetCommand            Operation = "get_command"
	OperationStreamCommandEvents   Operation = "stream_command_events"
	OperationCancelCommand         Operation = "cancel_command"
	OperationCloseSession          Operation = "close_session"
	OperationRunOrResumeJob        Operation = "run_or_resume_job"
	OperationGetJob                Operation = "get_job"
	OperationPing                  Operation = "ping"
)

var validOperations = map[Operation]struct{}{
	OperationHello: {}, OperationCreateOrResumeSession: {}, OperationGetSession: {},
	OperationSubmitOrResumeCommand: {}, OperationGetCommand: {}, OperationStreamCommandEvents: {},
	OperationCancelCommand: {}, OperationCloseSession: {}, OperationRunOrResumeJob: {},
	OperationGetJob: {}, OperationPing: {},
}

// RequestFrame is a versioned NDJSON request. Payload remains raw so the
// operation adapter can apply its own strict DTO and byte/script limits.
type RequestFrame struct {
	ProtocolVersion int             `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	Operation       Operation       `json:"operation"`
	ResourceID      string          `json:"resource_id,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

// ReplyFrame is the corresponding versioned NDJSON response envelope.
type ReplyFrame struct {
	ProtocolVersion int             `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	ResponseType    string          `json:"response_type"`
	Payload         json.RawMessage `json:"payload"`
}

// ErrorPayload is the protocol-level error payload. It intentionally contains
// no script, output, key material, or other secret-bearing data.
type ErrorPayload struct {
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Retryable  bool           `json:"retryable"`
	ResourceID string         `json:"resource_id,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

// KeyControllerMap maps the authenticated server-side SSH key identity to the
// controller principal used by the authority. Request JSON cannot alter it.
type KeyControllerMap map[string]domain.ControllerIdentity

// NewKeyControllerMap validates and copies an authenticated-key map.
func NewKeyControllerMap(entries map[string]domain.ControllerIdentity) (KeyControllerMap, error) {
	result := make(KeyControllerMap, len(entries))
	for key, controller := range entries {
		if strings.TrimSpace(key) == "" {
			return nil, ErrAuthenticatedKeyEmpty
		}
		if controller.Type() == "" || controller.ID() == "" {
			return nil, fmt.Errorf("%w: key %q has an empty controller", ErrInvalidFrame, key)
		}
		result[key] = controller
	}
	return result, nil
}

// ControllerForKey resolves only a server-supplied authenticated key.
func (m KeyControllerMap) ControllerForKey(authenticatedKey string) (domain.ControllerIdentity, error) {
	if strings.TrimSpace(authenticatedKey) == "" {
		return domain.ControllerIdentity{}, ErrAuthenticatedKeyEmpty
	}
	controller, ok := m[authenticatedKey]
	if !ok {
		return domain.ControllerIdentity{}, fmt.Errorf("%w: %q", ErrUnknownAuthenticatedKey, authenticatedKey)
	}
	return controller, nil
}

// Decoder reads one bounded JSON record at a time. It never accumulates more
// than max+1 bytes and validates the raw record before JSON decoding.
type Decoder struct {
	reader *bufio.Reader
	max    int
}

// NewDecoder creates a v1 bridge decoder with the shared one-MiB ceiling.
func NewDecoder(reader io.Reader) *Decoder {
	return NewDecoderWithLimit(reader, domain.MaxSerializedFrameBytes)
}

// NewDecoderWithLimit is useful for bounded tests and future profile tuning.
func NewDecoderWithLimit(reader io.Reader, maxBytes int) *Decoder {
	if maxBytes <= 0 {
		maxBytes = domain.MaxSerializedFrameBytes
	}
	return &Decoder{reader: bufio.NewReaderSize(reader, defaultFrameReaderBuffer), max: maxBytes}
}

// Decode reads and validates one request frame. A clean EOF before any bytes
// returns io.EOF; a final record does not need a trailing newline.
func (d *Decoder) Decode() (RequestFrame, error) {
	// Preallocate exactly the bounded maximum so append cannot grow a
	// geometrically oversized backing array while reading a hostile line.
	frame := make([]byte, 0, d.max+1)
	for {
		part, err := d.reader.ReadSlice('\n')
		if len(part) != 0 {
			if len(frame)+len(part) > d.max+1 {
				return RequestFrame{}, fmt.Errorf("%w: frame exceeds %d bytes", ErrFrameTooLarge, d.max)
			}
			frame = append(frame, part...)
			if len(frame) > d.max+1 {
				return RequestFrame{}, fmt.Errorf("%w: frame exceeds %d bytes", ErrFrameTooLarge, d.max)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(frame) == 0 {
				return RequestFrame{}, io.EOF
			}
			break
		}
		if err != nil {
			return RequestFrame{}, err
		}
		break
	}
	if len(frame) > 0 && frame[len(frame)-1] == '\n' {
		frame = frame[:len(frame)-1]
		if len(frame) > 0 && frame[len(frame)-1] == '\r' {
			frame = frame[:len(frame)-1]
		}
	}
	if err := domain.ValidateSerializedFrame(frame); err != nil {
		return RequestFrame{}, err
	}
	return DecodeRequest(frame)
}

// DecodeRequest validates one unframed JSON request before decoding it.
func DecodeRequest(raw []byte) (RequestFrame, error) {
	if err := domain.ValidateSerializedFrame(raw); err != nil {
		return RequestFrame{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request RequestFrame
	if err := decoder.Decode(&request); err != nil {
		return RequestFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return RequestFrame{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidFrame)
		}
		return RequestFrame{}, fmt.Errorf("%w: trailing JSON: %v", ErrInvalidFrame, err)
	}
	if err := validateRequest(request); err != nil {
		return RequestFrame{}, err
	}
	return request, nil
}

func validateRequest(request RequestFrame) error {
	if request.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedProtocol, request.ProtocolVersion, ProtocolVersion)
	}
	if strings.TrimSpace(request.RequestID) == "" {
		return fmt.Errorf("%w: request_id is empty", ErrInvalidFrame)
	}
	if _, ok := validOperations[request.Operation]; !ok {
		return fmt.Errorf("%w: operation %q", ErrInvalidFrame, request.Operation)
	}
	if len(request.Payload) == 0 || bytes.Equal(bytes.TrimSpace(request.Payload), []byte("null")) {
		return fmt.Errorf("%w: payload must be an object", ErrInvalidFrame)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(request.Payload, &payload); err != nil || payload == nil {
		return fmt.Errorf("%w: payload must be an object", ErrInvalidFrame)
	}
	if err := rejectControllerFields(request.Payload); err != nil {
		return err
	}
	switch request.Operation {
	case OperationHello, OperationPing:
		if len(payload) != 0 || request.ResourceID != "" || request.IdempotencyKey != "" {
			return fmt.Errorf("%w: %s has no resource, idempotency key, or payload fields", ErrInvalidFrame, request.Operation)
		}
	case OperationCreateOrResumeSession, OperationSubmitOrResumeCommand, OperationCancelCommand, OperationCloseSession, OperationRunOrResumeJob:
		if request.ResourceID == "" || request.IdempotencyKey == "" {
			return fmt.Errorf("%w: %s requires resource_id and idempotency_key", ErrInvalidFrame, request.Operation)
		}
	default:
		if request.ResourceID != "" || request.IdempotencyKey != "" {
			return fmt.Errorf("%w: %s cannot carry resource_id or idempotency_key", ErrInvalidFrame, request.Operation)
		}
	}
	return nil
}

func rejectControllerFields(raw []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: payload decode: %v", ErrInvalidFrame, err)
	}
	var walk func(any) error
	walk = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				switch key {
				case "controller", "controller_id", "controller_type", "principal":
					return fmt.Errorf("%w: field %q", ErrControllerField, key)
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

// Encode writes one bounded newline-terminated reply frame.
func Encode(writer io.Writer, reply ReplyFrame) error {
	if reply.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: reply version %d", ErrUnsupportedProtocol, reply.ProtocolVersion)
	}
	if strings.TrimSpace(reply.RequestID) == "" || strings.TrimSpace(reply.ResponseType) == "" || len(reply.Payload) == 0 {
		return fmt.Errorf("%w: reply envelope is incomplete", ErrInvalidFrame)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(reply.Payload, &payload); err != nil || payload == nil {
		return fmt.Errorf("%w: reply payload must be an object", ErrInvalidFrame)
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return fmt.Errorf("%w: marshal reply: %v", ErrInvalidFrame, err)
	}
	if err := domain.ValidateSerializedFrame(data); err != nil {
		return err
	}
	data = append(data, '\n')
	for len(data) != 0 {
		n, writeErr := writer.Write(data)
		if writeErr != nil {
			return writeErr
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func helloReply(requestID string) ReplyFrame {
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "hello", Payload: json.RawMessage(`{"supported_protocol_version":1}`)}
}

func pingReply(requestID string) ReplyFrame {
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "result", Payload: json.RawMessage(`{}`)}
}

func errorReply(requestID string, err error) ReplyFrame {
	code := "invalid_request"
	if errors.Is(err, ErrUnsupportedProtocol) {
		code = "unsupported_protocol"
	} else if errors.Is(err, ErrOperationUnsupported) {
		code = "operation_unsupported"
	} else if errors.Is(err, ErrUnknownAuthenticatedKey) {
		code = "unauthorized"
	}
	payload, _ := json.Marshal(ErrorPayload{Code: code, Message: err.Error(), Retryable: false})
	return ReplyFrame{ProtocolVersion: ProtocolVersion, RequestID: requestID, ResponseType: "error", Payload: payload}
}

// RequestHandler receives non-handshake operations after framing and
// authenticated controller mapping. Streaming operations use the optional
// StreamRequestHandler interface below.
type RequestHandler interface {
	Handle(context.Context, domain.ControllerIdentity, RequestFrame) (ReplyFrame, error)
}

// StreamRequestHandler handles an operation that can produce multiple reply
// frames. The callback is called in wire order.
type StreamRequestHandler interface {
	Stream(context.Context, domain.ControllerIdentity, RequestFrame, func(ReplyFrame) error) error
}
