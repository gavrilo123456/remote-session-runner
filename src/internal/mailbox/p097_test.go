package mailbox

import (
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP097InlineOutputPreviewsAreBoundedUTF8AndOmitBinaryStreams(t *testing.T) {
	commandID := domain.CommandID("command-p097-preview")
	events := []store.CommandEventRecord{
		{CommandID: commandID, Sequence: 1, Type: "stdout", Payload: []byte(strings.Repeat("a", mailboxInlinePreviewBytes-1)), ByteCount: mailboxInlinePreviewBytes - 1, OccurredAt: time.Unix(1, 0).UTC()},
		{CommandID: commandID, Sequence: 2, Type: "stdout", Payload: []byte("€tail"), ByteCount: int64(len("€tail")), OccurredAt: time.Unix(2, 0).UTC()},
		{CommandID: commandID, Sequence: 3, Type: "stderr", Payload: []byte("diagnostic"), ByteCount: int64(len("diagnostic")), OccurredAt: time.Unix(3, 0).UTC()},
		{CommandID: commandID, Sequence: 4, Type: "stderr", Payload: []byte{0xff, 0x00}, ByteCount: 2, OccurredAt: time.Unix(4, 0).UTC()},
	}
	stdout, stderr := inlineOutputPreviews(events)
	if len(stdout) != mailboxInlinePreviewBytes-1 || strings.Repeat("a", mailboxInlinePreviewBytes-1) != stdout {
		t.Fatalf("stdout preview length=%d, want %d", len(stdout), mailboxInlinePreviewBytes-1)
	}
	if stderr != "" {
		t.Fatalf("binary stderr produced inline preview %q", stderr)
	}
}

func TestP097InlineOutputPreviewsIncludeTinyUTF8Output(t *testing.T) {
	commandID := domain.CommandID("command-p097-tiny")
	events := []store.CommandEventRecord{{
		CommandID: commandID, Sequence: 1, Type: "stdout", Payload: []byte("tiny 🍎\n"), ByteCount: int64(len("tiny 🍎\n")), OccurredAt: time.Unix(1, 0).UTC(),
	}}
	stdout, stderr := inlineOutputPreviews(events)
	if stdout != "tiny 🍎\n" || stderr != "" {
		t.Fatalf("inline previews stdout=%q stderr=%q", stdout, stderr)
	}
}
