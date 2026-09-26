package mailbox

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP085ProjectsExactUTF8AndBinaryNDJSON(t *testing.T) {
	authority, command := p085CommandAuthority(t)
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	utf8Chunk := []byte("café\n")
	binaryChunk := []byte{0x00, 0xff, 0x01, 0xfe}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: utf8Chunk, ByteCount: int64(len(utf8Chunk))}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{CommandID: command.CommandID, Type: "stderr", Payload: binaryChunk, ByteCount: int64(len(binaryChunk))}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: intPointerP085(7), OutputComplete: true}); err != nil {
		t.Fatal(err)
	}

	projector := EventProjector{Authority: authority}
	data, available, err := projector.Project(context.Background(), command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if available != 5 || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("available=%d data=%q", available, data)
	}
	lines := splitNDJSONP085(t, data)
	if len(lines) != 5 {
		t.Fatalf("projected lines=%d, want 5", len(lines))
	}
	var stdout, stderr []byte
	for index, line := range lines {
		value := p085MailboxEventValue(t, line)
		if value["command_id"] != string(command.CommandID) || value["sequence"] != json.Number(strconv.Itoa(index+1)) || value["ordinal"] != json.Number(strconv.FormatInt(command.Ordinal, 10)) {
			t.Fatalf("line %d metadata=%v", index+1, value)
		}
		switch value["type"] {
		case "stdout":
			if value["encoding"] != "utf8" || value["text"] != string(utf8Chunk) || value["byte_count"] != json.Number(strconv.Itoa(len(utf8Chunk))) {
				t.Fatalf("UTF-8 projection=%v", value)
			}
			stdout = append(stdout, []byte(value["text"].(string))...)
		case "stderr":
			if value["encoding"] != "base64" || value["byte_count"] != json.Number(strconv.Itoa(len(binaryChunk))) {
				t.Fatalf("binary projection=%v", value)
			}
			decoded, err := base64.StdEncoding.DecodeString(value["data_base64"].(string))
			if err != nil {
				t.Fatal(err)
			}
			stderr = append(stderr, decoded...)
		case "command_succeeded":
			if value["exit_code"] != json.Number("7") {
				t.Fatalf("terminal exit code=%v", value["exit_code"])
			}
		}
	}
	if !reflect.DeepEqual(stdout, utf8Chunk) || !reflect.DeepEqual(stderr, binaryChunk) || !utf8.Valid(utf8Chunk) {
		t.Fatalf("round trip stdout=%v stderr=%v", stdout, stderr)
	}
}

func TestP085RejectsOversizedOrInconsistentProjectionEvents(t *testing.T) {
	command := store.CommandRecord{CommandID: "command-p085-invalid", Ordinal: 1}
	oversized := store.CommandEventRecord{CommandID: command.CommandID, Sequence: 1, Type: "command_queued", OccurredAt: time.Unix(1, 0).UTC()}
	if _, err := RenderCommandEvents(command, []store.CommandEventRecord{oversized}); err != nil {
		t.Fatal(err)
	}
	oversized = store.CommandEventRecord{CommandID: command.CommandID, Sequence: 1, Type: "stdout", Payload: make([]byte, mailboxEventChunkBytes+1), ByteCount: mailboxEventChunkBytes + 1, OccurredAt: time.Unix(1, 0).UTC()}
	if _, err := RenderCommandEvents(command, []store.CommandEventRecord{oversized}); !errors.Is(err, ErrEventProjectionChunk) {
		t.Fatalf("oversized error=%v, want ErrEventProjectionChunk", err)
	}
	wrong := oversized
	wrong.Payload = []byte("ok")
	wrong.ByteCount = 99
	if _, err := RenderCommandEvents(command, []store.CommandEventRecord{wrong}); !errors.Is(err, ErrEventProjection) {
		t.Fatalf("byte mismatch error=%v, want ErrEventProjection", err)
	}
	if _, _, err := (EventProjector{}).Project(context.Background(), command.CommandID); !errors.Is(err, ErrEventProjectionConfiguration) {
		t.Fatalf("nil projector error=%v", err)
	}
}

func p085CommandAuthority(t *testing.T) (*store.AuthorityStore, store.CommandRecord) {
	t.Helper()
	root := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(root.Path(), "state", "p085.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	controllerID, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, controllerID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	limits := domain.DefaultServiceLimits()
	session, _, err := authority.AcceptSessionCreate(context.Background(), store.SessionCreateAcceptance{
		SessionCreate:  store.SessionCreate{SessionID: "session-p085", Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), Limits: domain.EffectiveSessionLimits{CommandTimeout: limits.CommandTimeout, IdleTimeout: limits.IdleTimeout, SessionMaxLifetime: limits.SessionMaxLifetime, OutputBytesPerCommand: limits.OutputBytesPerCommand}},
		IdempotencyKey: "key-p085-session", RequestHash: p085Hash(t, "create_session", `{"operation":"create_session","session_id":"session-p085"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), session.SessionID, domain.SessionStateReady, "agent_ready"); err != nil {
		t.Fatal(err)
	}
	command, _, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{CommandID: "command-p085", SessionID: session.SessionID, IdempotencyKey: "key-p085-command", RequestHash: p085Hash(t, "submit_command", `{"operation":"submit_command","session_id":"session-p085","script":"echo p085"}`), Script: "echo p085", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return authority, command
}

func p085Hash(t *testing.T, operation, raw string) domain.CanonicalHash {
	t.Helper()
	hash, err := domain.HashMutationRequestJSON(operation, []byte(raw), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func splitNDJSONP085(t *testing.T, data []byte) [][]byte {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 1024), 1<<20)
	var lines [][]byte
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func p085MailboxEventValue(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := p004MailboxSchemas(t)["event"].Validate(value); err != nil {
		t.Fatalf("event schema rejected %s: %v", line, err)
	}
	if err := p004MailboxSemantic("event", value); err != nil {
		t.Fatalf("event semantic validation rejected %s: %v", line, err)
	}
	return value
}

func intPointerP085(value int) *int { return &value }
