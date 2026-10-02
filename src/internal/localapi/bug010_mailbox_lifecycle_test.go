package localapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const (
	b010StatusInboxID             = "slidestud-io"
	b010StatusBodySentinel        = "B010_SECRET_SCRIPT__never_render"
	b010StatusTokenSentinel       = "B010_TOKEN__never_render"
	b010StatusOutputSentinel      = "B010_TERMINAL_OUTPUT__never_render"
	b010StatusIdempotencySentinel = "B010_IDEMPOTENCY_KEY__never_render"
	b010StatusKeySentinel         = "B010_PRIVATE_KEY__never_render"
)

func TestBUG010MailboxLifecycleStatusIsMetadataOnlyAndReadOnly(t *testing.T) {
	h := newB010MailboxLifecycleStatusHarness(t, true)

	b010WriteIngressFile(t, filepath.Join(h.root, "inbox", "req-b010-external.json"), []byte(`{"script":"`+b010StatusBodySentinel+`","authorization":"`+b010StatusTokenSentinel+`"}`), mailbox.MailboxWorkspaceIngressFileMode)
	b010WriteIngressFile(t, filepath.Join(h.root, "inbox", "req-b010-external.ready"), nil, mailbox.MailboxWorkspaceIngressFileMode)
	b010WriteIngressFile(t, filepath.Join(h.root, "inbox", "req-b010-unreadable.json"), []byte(`{"script":"`+b010StatusBodySentinel+`"}`), 0o000)
	b010WriteIngressFile(t, filepath.Join(h.root, "inbox", "req-b010-unreadable.ready"), nil, mailbox.MailboxWorkspaceIngressFileMode)
	b010CreateTerminalExchange(t, h.authority, b010StatusInboxID, "req-b010-terminal", false)
	b010WriteIngressFile(t, filepath.Join(h.root, "inbox", "req-b010-terminal.ready"), nil, mailbox.MailboxWorkspaceIngressFileMode)
	b010CreateTerminalExchange(t, h.authority, b010StatusInboxID, "req-b010-acknowledged", true)
	b010WriteIngressFile(t, filepath.Join(h.root, "acks", "req-b010-acknowledged.ready"), nil, mailbox.MailboxWorkspaceIngressFileMode)

	beforeFiles := b010MailboxTreeSnapshot(t, h.root)
	beforeAudit := b010AuditCount(t, h.db)

	response, body := b010LifecycleRequest(t, h.client, http.MethodGet, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle?request_id=req-b010-unreadable")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unreadable status=%d body=%s", response.StatusCode, body)
	}
	b010AssertLifecycleJSONKeys(t, body, "inbox_id", "available", "counts", "request")
	b010AssertLifecycleRedacted(t, body, h.root)
	var unreadable struct {
		InboxID   string `json:"inbox_id"`
		Available bool   `json:"available"`
		Request   struct {
			RequestID string `json:"request_id"`
			Request   struct {
				InputShape   string `json:"input_shape"`
				DurableState string `json:"durable_state"`
				Action       string `json:"action"`
			} `json:"request"`
			Terminal     bool `json:"terminal"`
			Acknowledged bool `json:"acknowledged"`
		} `json:"request"`
	}
	if err := json.Unmarshal(body, &unreadable); err != nil {
		t.Fatal(err)
	}
	if unreadable.InboxID != b010StatusInboxID || !unreadable.Available || unreadable.Request.RequestID != "req-b010-unreadable" ||
		unreadable.Request.Request.InputShape != "unsafe_inert" || unreadable.Request.Request.DurableState != "none" ||
		unreadable.Request.Request.Action != "retain_unproven_inert" || unreadable.Request.Terminal || unreadable.Request.Acknowledged {
		t.Fatalf("unreadable lifecycle=%+v", unreadable)
	}

	response, body = b010LifecycleRequest(t, h.client, http.MethodGet, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle?request_id=req-b010-terminal")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"terminal":true`) || strings.Contains(string(body), `"acknowledged":true`) {
		t.Fatalf("terminal lifecycle status=%d body=%s", response.StatusCode, body)
	}
	b010AssertLifecycleJSONKeys(t, body, "inbox_id", "available", "counts", "request")
	b010AssertLifecycleRedacted(t, body, h.root)
	response, body = b010LifecycleRequest(t, h.client, http.MethodGet, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle?request_id=req-b010-acknowledged")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"terminal":true`) || !strings.Contains(string(body), `"acknowledged":true`) || !strings.Contains(string(body), `"terminal_acknowledged"`) {
		t.Fatalf("acknowledged lifecycle status=%d body=%s", response.StatusCode, body)
	}
	b010AssertLifecycleJSONKeys(t, body, "inbox_id", "available", "counts", "request")
	b010AssertLifecycleRedacted(t, body, h.root)

	response, body = b010LifecycleRequest(t, h.client, http.MethodGet, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("aggregate status=%d body=%s", response.StatusCode, body)
	}
	b010AssertLifecycleJSONKeys(t, body, "inbox_id", "available", "counts")
	b010AssertLifecycleRedacted(t, body, h.root)
	var aggregate struct {
		InboxID   string `json:"inbox_id"`
		Available bool   `json:"available"`
		Counts    struct {
			RequestInputShapes         map[string]int `json:"request_input_shapes"`
			AcknowledgementInputShapes map[string]int `json:"acknowledgement_input_shapes"`
			DurableStates              map[string]int `json:"durable_states"`
			RequestActions             map[string]int `json:"request_actions"`
			AcknowledgementActions     map[string]int `json:"acknowledgement_actions"`
			ActionableUnacceptedPairs  int            `json:"actionable_unaccepted_pairs"`
		} `json:"counts"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(body, &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate.InboxID != b010StatusInboxID || !aggregate.Available || len(aggregate.Request) != 0 ||
		aggregate.Counts.RequestInputShapes["publishable_pair"] != 1 || aggregate.Counts.RequestInputShapes["unsafe_inert"] != 1 ||
		aggregate.Counts.RequestInputShapes["request_marker_only"] != 1 || aggregate.Counts.AcknowledgementInputShapes["ack_marker_only"] != 1 ||
		aggregate.Counts.DurableStates["terminal_unacknowledged"] != 1 || aggregate.Counts.DurableStates["terminal_acknowledged"] != 1 ||
		aggregate.Counts.RequestActions["eligible_durable_orphan_cleanup"] != 1 || aggregate.Counts.AcknowledgementActions["eligible_durable_orphan_cleanup"] != 1 ||
		aggregate.Counts.ActionableUnacceptedPairs != 1 {
		t.Fatalf("aggregate lifecycle=%+v", aggregate)
	}

	afterFiles := b010MailboxTreeSnapshot(t, h.root)
	if !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatalf("read-only status changed mailbox tree: before=%v after=%v", beforeFiles, afterFiles)
	}
	if afterAudit := b010AuditCount(t, h.db); afterAudit != beforeAudit {
		t.Fatalf("read-only status changed audit rows: before=%d after=%d", beforeAudit, afterAudit)
	}

	info, err := os.Stat(h.server.SocketPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("owner-only local API socket=%v err=%v", info, err)
	}
}

func TestBUG010MailboxLifecycleStatusRejectsUnknownOrUnsafeSelection(t *testing.T) {
	h := newB010MailboxLifecycleStatusHarness(t, true)
	beforeFiles := b010MailboxTreeSnapshot(t, h.root)
	for _, test := range []struct {
		name string
		path string
		want int
		code string
	}{
		{name: "unknown inbox", path: "/v1/mailboxes/not-configured/lifecycle", want: http.StatusNotFound, code: "mailbox_not_found"},
		{name: "invalid request ID", path: "/v1/mailboxes/" + b010StatusInboxID + "/lifecycle?request_id=../not-safe", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "duplicate request ID", path: "/v1/mailboxes/" + b010StatusInboxID + "/lifecycle?request_id=req-one&request_id=req-two", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "empty request ID", path: "/v1/mailboxes/" + b010StatusInboxID + "/lifecycle?request_id=", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "arbitrary root parameter", path: "/v1/mailboxes/" + b010StatusInboxID + "/lifecycle?root=/tmp/other", want: http.StatusBadRequest, code: "invalid_request"},
		{name: "path traversal", path: "/v1/mailboxes/%2E%2E/lifecycle", want: http.StatusBadRequest, code: "invalid_request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := b010LifecycleRequest(t, h.client, http.MethodGet, test.path)
			if response.StatusCode != test.want || !strings.Contains(string(body), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
		})
	}
	response, body := b010LifecycleRequest(t, h.client, http.MethodPost, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle")
	if response.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"code":"resource_not_found"`) {
		t.Fatalf("mutation status=%d body=%s", response.StatusCode, body)
	}
	if afterFiles := b010MailboxTreeSnapshot(t, h.root); !reflect.DeepEqual(beforeFiles, afterFiles) {
		t.Fatalf("rejected lifecycle requests changed mailbox tree: before=%v after=%v", beforeFiles, afterFiles)
	}
}

func TestBUG010MailboxLifecycleStatusExplicitlyReportsUnavailable(t *testing.T) {
	h := newB010MailboxLifecycleStatusHarness(t, false)
	response, body := b010LifecycleRequest(t, h.client, http.MethodGet, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle")
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"available":false`) || !strings.Contains(string(body), `"code":"mailbox_lifecycle_unavailable"`) {
		t.Fatalf("unavailable status=%d body=%s", response.StatusCode, body)
	}

	withProvider := newB010MailboxLifecycleStatusHarness(t, true)
	if err := os.Remove(filepath.Join(withProvider.root, "acks")); err != nil {
		t.Fatal(err)
	}
	response, body = b010LifecycleRequest(t, withProvider.client, http.MethodGet, "/v1/mailboxes/"+b010StatusInboxID+"/lifecycle")
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"available":false`) || !strings.Contains(string(body), `"code":"mailbox_lifecycle_unavailable"`) {
		t.Fatalf("missing-tree status=%d body=%s", response.StatusCode, body)
	}
}

type b010MailboxLifecycleStatusHarness struct {
	root      string
	db        *sql.DB
	authority *store.AuthorityStore
	server    *Server
	client    *http.Client
}

func newB010MailboxLifecycleStatusHarness(t *testing.T, withProvider bool) *b010MailboxLifecycleStatusHarness {
	t.Helper()
	root := testfixture.New(t).Path()
	mailboxRoot := filepath.Join(root, "external-project", "mailbox-")
	for _, path := range []string{mailboxRoot, filepath.Join(mailboxRoot, "inbox"), filepath.Join(mailboxRoot, "acks")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(state, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "rsr-b010-lifecycle-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	options := ServerOptions{Authority: authority, Owner: p063Owner(t), SocketPath: filepath.Join(runDir, "local-api.sock")}
	if withProvider {
		provider, err := mailbox.NewLifecycleStatusRegistry(mailbox.LifecycleStatusRegistryOptions{
			Authority: authority,
			Mailboxes: []mailbox.LifecycleStatusMailbox{{ID: b010StatusInboxID, Root: mailboxRoot}},
		})
		if err != nil {
			t.Fatal(err)
		}
		options.MailboxLifecycleStatus = provider
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	go func() { _ = server.Serve() }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", server.SocketPath())
	}}
	return &b010MailboxLifecycleStatusHarness{root: mailboxRoot, db: db, authority: authority, server: server, client: &http.Client{Transport: transport}}
}

func b010LifecycleRequest(t *testing.T, client *http.Client, method, path string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, "http://runner.local"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func b010AssertLifecycleRedacted(t *testing.T, body []byte, root string) {
	t.Helper()
	for _, forbidden := range []string{
		b010StatusBodySentinel,
		b010StatusTokenSentinel,
		b010StatusOutputSentinel,
		b010StatusIdempotencySentinel,
		b010StatusKeySentinel,
		"authorization",
		"private key",
		root,
	} {
		if strings.Contains(strings.ToLower(string(body)), strings.ToLower(forbidden)) {
			t.Fatalf("lifecycle response leaked %q: %s", forbidden, body)
		}
	}
}

func b010AssertLifecycleJSONKeys(t *testing.T, body []byte, expected ...string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range expected {
		if _, exists := fields[key]; !exists {
			t.Fatalf("lifecycle response lacks %q: %s", key, body)
		}
		delete(fields, key)
	}
	if len(fields) != 0 {
		t.Fatalf("lifecycle response has unexpected fields %v: %s", fields, body)
	}
}

func b010WriteIngressFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func b010CreateTerminalExchange(t *testing.T, authority *store.AuthorityStore, mailboxID, requestID string, acknowledged bool) {
	t.Helper()
	ctx := context.Background()
	owner := p063Owner(t)
	digest := sha256.Sum256([]byte("b010-status-" + requestID))
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.NewMailboxExchangeRef(mailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: mailboxID, RequestID: requestID, Operation: "run", Controller: owner,
		IdempotencyKey: b010StatusIdempotencySentinel + "-" + requestID, RequestHash: hash, CanonicalPayload: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	record, err := authority.PublishMailboxResponseInMailbox(ctx, ref, store.MailboxResponsePublication{
		State: store.MailboxExchangeComplete, Bytes: []byte(`{"request_state":"complete","stdout":"` + b010StatusOutputSentinel + `","private_key":"` + b010StatusKeySentinel + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged {
		if _, err := authority.AcknowledgeMailboxExchangeInMailbox(ctx, ref, store.MailboxAcknowledgement{
			MailboxID: mailboxID, RequestID: requestID, ResponseRevision: record.ResponseRevision, AvailableEventSequence: record.AvailableEventSequence,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

type b010MailboxTreeEntry struct {
	Mode    os.FileMode
	Size    int64
	ModTime time.Time
}

func b010MailboxTreeSnapshot(t *testing.T, root string) map[string]b010MailboxTreeEntry {
	t.Helper()
	result := make(map[string]b010MailboxTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = b010MailboxTreeEntry{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func b010AuditCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM runner_audit_records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
