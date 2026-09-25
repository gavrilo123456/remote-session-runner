package domain

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	// MaxSerializedRequestBytes bounds the serialized JSON request body before
	// a caller decodes it or records any intent or authority state.
	MaxSerializedRequestBytes = 1 << 20

	// MaxSerializedFrameBytes bounds one serialized SSH bridge record before
	// decoding. The NDJSON line terminator is framing, not part of the record.
	MaxSerializedFrameBytes = MaxSerializedRequestBytes

	// MaxScriptUTF8Bytes bounds the decoded shell script by its UTF-8 byte size.
	MaxScriptUTF8Bytes = 128 << 10
)

var (
	// ErrSerializedInputTooLarge means a serialized request or frame exceeds
	// the shared one-MiB ceiling.
	ErrSerializedInputTooLarge = errors.New("serialized request or frame exceeds byte limit")

	// ErrScriptTooLarge means a UTF-8 script exceeds the shared 128-KiB ceiling.
	ErrScriptTooLarge = errors.New("script exceeds UTF-8 byte limit")

	// ErrScriptInvalidUTF8 means a script contains bytes that are not valid UTF-8.
	ErrScriptInvalidUTF8 = errors.New("script is not valid UTF-8")
)

// ValidateSerializedRequest checks raw HTTP/request bytes. Call it before JSON
// decoding and before recording local intent or authoritative acceptance.
func ValidateSerializedRequest(raw []byte) error {
	return validateSerializedSize("request", raw, MaxSerializedRequestBytes)
}

// ValidateSerializedFrame checks one raw SSH bridge JSON record before JSON
// decoding. The NDJSON line terminator is not part of frame.
func ValidateSerializedFrame(frame []byte) error {
	return validateSerializedSize("frame", frame, MaxSerializedFrameBytes)
}

// ValidateScriptUTF8 checks the script's encoding and UTF-8 byte size. Go
// strings may contain invalid UTF-8, so callers must not rely on len alone.
func ValidateScriptUTF8(script string) error {
	if !utf8.ValidString(script) {
		return ErrScriptInvalidUTF8
	}
	if len(script) > MaxScriptUTF8Bytes {
		return fmt.Errorf("%w: got %d bytes, maximum %d", ErrScriptTooLarge, len(script), MaxScriptUTF8Bytes)
	}
	return nil
}

func validateSerializedSize(kind string, raw []byte, maximum int) error {
	if len(raw) > maximum {
		return fmt.Errorf("%w: %s is %d bytes, maximum %d", ErrSerializedInputTooLarge, kind, len(raw), maximum)
	}
	return nil
}
