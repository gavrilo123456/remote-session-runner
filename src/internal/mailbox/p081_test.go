package mailbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestP081MarkerLastImportsOnlyAfterReadyAndPassesExactBytes(t *testing.T) {
	root := p081MailboxRoot(t)
	var received []Request
	importer, err := NewImporter(root, func(_ context.Context, request Request) error {
		received = append(received, request)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"request_id":"req-marker","idempotency_key":"key-marker","operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"printf 'marker\\n'"}`)
	requestPath := filepath.Join(importer.InboxPath(), "req-marker.json")
	markerPath := filepath.Join(importer.InboxPath(), "req-marker.ready")
	writeMailboxFile(t, requestPath, raw, 0o600)
	first, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 || len(received) != 0 {
		t.Fatalf("unmarked draft imported: results=%+v received=%d", first, len(received))
	}
	writeMailboxFile(t, markerPath, nil, 0o600)
	results, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != ResultAccepted || results[0].RequestID != "req-marker" || len(received) != 1 {
		t.Fatalf("marker import results=%+v received=%d", results, len(received))
	}
	if received[0].RequestID != "req-marker" || received[0].Operation != "run" || received[0].Script != "printf 'marker\\n'" || string(received[0].RawJSON) != string(raw) {
		t.Fatalf("received request = %+v", received[0])
	}
}

func TestP081ImportsWorkspaceModeRequestPair(t *testing.T) {
	root := p081MailboxRoot(t)
	var received []Request
	importer, err := NewImporter(root, func(_ context.Context, request Request) error {
		received = append(received, request)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-workspace-mode"
	raw := p081RunJSON(t, requestID, "printf 'workspace\\n'")
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+RequestSuffix), raw, MailboxWorkspaceIngressFileMode)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), requestID+ReadySuffix), nil, MailboxWorkspaceIngressFileMode)

	results, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != ResultAccepted || len(received) != 1 {
		t.Fatalf("workspace-mode import results=%+v received=%d", results, len(received))
	}
	if received[0].RequestID != requestID || string(received[0].RawJSON) != string(raw) {
		t.Fatalf("workspace-mode request = %+v", received[0])
	}
}

func TestP081RejectsUnsafeNamesSymlinksNonRegularAndWrongModes(t *testing.T) {
	root := p081MailboxRoot(t)
	var received int
	importer, err := NewImporter(root, func(context.Context, Request) error {
		received++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := p081RunJSON(t, "req-safe", "echo safe")
	// A symlink request must be rejected before its target is opened.
	outside := filepath.Join(root, "outside.json")
	writeMailboxFile(t, outside, valid, 0o600)
	if err := os.Symlink(outside, filepath.Join(importer.InboxPath(), "req-safe.json")); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-safe.ready"), nil, 0o600)
	// A symlink marker is also inert even when the paired request is valid.
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-marker.json"), p081RunJSON(t, "req-marker", "echo marker"), 0o600)
	markerTarget := filepath.Join(root, "marker.target")
	writeMailboxFile(t, markerTarget, nil, 0o600)
	if err := os.Symlink(markerTarget, filepath.Join(importer.InboxPath(), "req-marker.ready")); err != nil {
		t.Fatal(err)
	}
	// A directory in the request position is not a request file.
	if err := os.Mkdir(filepath.Join(importer.InboxPath(), "req-dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-dir.ready"), nil, 0o600)
	// Only the exact ingress modes are accepted; group-readable variants are
	// rejected even when the JSON and marker otherwise look valid.
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-mode-0640.json"), p081RunJSON(t, "req-mode-0640", "echo mode"), 0o640)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-mode-0640.ready"), nil, 0o600)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-mode-0664.json"), p081RunJSON(t, "req-mode-0664", "echo mode"), 0o664)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-mode-0664.ready"), nil, 0o600)
	// The marker itself must also have an exact ingress mode.
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-marker-mode.json"), p081RunJSON(t, "req-marker-mode", "echo marker mode"), 0o600)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-marker-mode.ready"), nil, 0o640)
	// Unsafe marker basename never maps to a path outside inbox.
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "bad name.ready"), nil, 0o600)
	results, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 7 || received != 0 {
		t.Fatalf("unsafe input results=%+v handler_calls=%d", results, received)
	}
	for _, result := range results {
		if result.Status != ResultRejected {
			t.Fatalf("unsafe input accepted: %+v", result)
		}
	}
}

func TestP081RejectsMismatchedIngressFileOwner(t *testing.T) {
	currentOwner := uint32(os.Geteuid())
	current := p081FileInfo{mode: MailboxFileMode, stat: &syscall.Stat_t{Uid: currentOwner}}
	if !mailboxFileOwnedByCurrentUser(current) || !safeIngressFileInfo(current) {
		t.Fatal("current-user ingress file was rejected")
	}
	mismatched := p081FileInfo{mode: MailboxWorkspaceIngressFileMode, stat: &syscall.Stat_t{Uid: currentOwner + 1}}
	if mailboxFileOwnedByCurrentUser(mismatched) || safeIngressFileInfo(mismatched) {
		t.Fatal("mismatched ingress file owner was accepted")
	}
}

func TestP081RejectsPartialSchemaAndRequestIDMismatch(t *testing.T) {
	root := p081MailboxRoot(t)
	var received int
	importer, err := NewImporter(root, func(context.Context, Request) error {
		received++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body []byte
	}{
		{name: "partial", body: []byte(`{"request_id":"req-partial","operation":"run"`)},
		{name: "mismatch", body: p081RunJSON(t, "different-id", "echo mismatch")},
		{name: "unknown-field", body: []byte(`{"request_id":"req-unknown","idempotency_key":"key","operation":"get_command","command_id":"cmd-1","unexpected":true}`)},
	}
	for _, fixture := range cases {
		writeMailboxFile(t, filepath.Join(importer.InboxPath(), fixture.name+".json"), fixture.body, 0o600)
		writeMailboxFile(t, filepath.Join(importer.InboxPath(), fixture.name+".ready"), nil, 0o600)
	}
	results, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(cases) || received != 0 {
		t.Fatalf("invalid input results=%+v handler_calls=%d", results, received)
	}
	for _, result := range results {
		if result.Status != ResultRejected || result.Reason == "" {
			t.Fatalf("invalid input result = %+v", result)
		}
	}
}

func TestP081SharedRequestAndScriptByteLimits(t *testing.T) {
	root := p081MailboxRoot(t)
	var received int
	importer, err := NewImporter(root, func(context.Context, Request) error {
		received++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	oversizeRequest := p081RunJSON(t, "req-oversize", strings.Repeat("x", domain.MaxSerializedRequestBytes))
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-oversize.json"), oversizeRequest, 0o600)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-oversize.ready"), nil, 0o600)
	oversizeScript := p081RunJSON(t, "req-script", strings.Repeat("x", domain.MaxScriptUTF8Bytes+1))
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-script.json"), oversizeScript, 0o600)
	writeMailboxFile(t, filepath.Join(importer.InboxPath(), "req-script.ready"), nil, 0o600)
	results, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || received != 0 {
		t.Fatalf("oversize input results=%+v handler_calls=%d", results, received)
	}
	for _, result := range results {
		if result.Status != ResultRejected || !strings.Contains(result.Reason, "byte") {
			t.Fatalf("oversize input result = %+v", result)
		}
	}
}

func p081RunJSON(t *testing.T, requestID, script string) []byte {
	t.Helper()
	value := map[string]any{
		"request_id": requestID, "idempotency_key": "key-" + requestID, "operation": "run", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": script,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeMailboxFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func p081MailboxRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "rsr-p081-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

type p081FileInfo struct {
	mode os.FileMode
	stat *syscall.Stat_t
}

func (i p081FileInfo) Name() string       { return "p081" }
func (i p081FileInfo) Size() int64        { return 0 }
func (i p081FileInfo) Mode() os.FileMode  { return i.mode }
func (i p081FileInfo) ModTime() time.Time { return time.Time{} }
func (i p081FileInfo) IsDir() bool        { return false }
func (i p081FileInfo) Sys() any           { return i.stat }
