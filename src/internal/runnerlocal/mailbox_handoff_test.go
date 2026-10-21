package runnerlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/mailboxhandoff"
	"remote-session-runner/src/internal/store"
)

type runtimeHandoffFixture struct {
	receipt      mailboxhandoff.Receipt
	path, digest string
	definitions  []config.MailboxDefinition
}

func newRuntimeHandoffFixture(t *testing.T) runtimeHandoffFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	prd, qas := filepath.Join(base, "prd"), filepath.Join(base, "qas")
	root, native := filepath.Join(base, "project", "mailbox-"), filepath.Join(qas, "mailbox")
	for _, path := range []string{prd, qas, root, native, filepath.Join(qas, "staging", "mailbox-handoffs")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var stat unix.Stat_t
	if err := unix.Stat(root, &stat); err != nil {
		t.Fatal(err)
	}
	receipt := mailboxhandoff.Receipt{Version: mailboxhandoff.Version, OperationID: strings.Repeat("a", 32), SourceLane: "prd", TargetLane: "qas", SourceServiceRoot: prd, TargetServiceRoot: qas,
		SourceConfigSHA256: strings.Repeat("b", 64), TargetConfigSHA256: strings.Repeat("c", 64), Phase: "active", EnvironmentAliases: map[string]string{"mac-dev": "mac-workstation-qas"},
		Mailboxes: []mailboxhandoff.Mailbox{{SourceID: "analytics", TargetID: "analytics", Root: root, Device: uint64(stat.Dev), Inode: uint64(stat.Ino), ArchiveRelative: ".runner-mailbox-handoffs/" + strings.Repeat("a", 32) + "/prd", BlockedRequestIDs: []string{"req-prd-history"}, BlockedIdempotencySHA256: []string{mailboxhandoff.SHA256([]byte("key-prd-history"))}}}}
	raw, err := mailboxhandoff.MarshalReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	path := mailboxhandoff.ReceiptPath(receipt)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f := runtimeHandoffFixture{receipt: receipt, path: path, digest: mailboxhandoff.SHA256(raw), definitions: []config.MailboxDefinition{{ID: "default", Root: native}, {ID: "analytics", Root: root}}}
	f.writeMarker(t, f.digest)
	return f
}

func (f runtimeHandoffFixture) writeMarker(t *testing.T, digest string) {
	t.Helper()
	m := f.receipt.Mailboxes[0]
	if err := mailboxhandoff.WriteMarker(m.Root, mailboxhandoff.Marker{OperationID: f.receipt.OperationID, OwnerLane: "qas", ReceiptSHA256: digest, Device: m.Device, Inode: m.Inode}); err != nil {
		t.Fatal(err)
	}
}

func (f runtimeHandoffFixture) acquire(t *testing.T) *mailboxhandoff.Lease {
	t.Helper()
	definitions := make([]mailboxhandoff.Definition, 0, len(f.definitions))
	for _, d := range f.definitions {
		definitions = append(definitions, mailboxhandoff.Definition{ID: d.ID, Root: d.Root})
	}
	lease, err := mailboxhandoff.Acquire(f.path, "qas", f.receipt.TargetServiceRoot, f.receipt.TargetConfigSHA256, definitions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease
}

func TestHandoffStartupRejectsMissingReceiptBeforeDatabaseOrPathMutation(t *testing.T) {
	f := newRuntimeHandoffFixture(t)
	dbPath := filepath.Join(f.receipt.TargetServiceRoot, "state", "local.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	before := []byte("not a SQLite database: must not be opened or migrated")
	if err := os.WriteFile(dbPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	settings := config.MacSettings{ServiceRoot: f.receipt.TargetServiceRoot, Database: dbPath, MailboxIngressEnabled: true}
	service, err := newServiceFromConfiguration(config.Config{}, settings, f.definitions, false, nil)
	if service != nil || !errors.Is(err, errMailboxHandoffAuthorization) {
		t.Fatalf("service=%v err=%v", service, err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("database changed: %v", err)
	}
	for _, path := range []string{dbPath + "-wal", dbPath + "-shm", filepath.Join(settings.ServiceRoot, "logs"), filepath.Join(settings.ServiceRoot, "run")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("startup created %s: %v", path, err)
		}
	}
}

func TestHandoffDisabledIngressPreservesRegistryAndAllMailboxInput(t *testing.T) {
	h := newP154MailboxHarness(t)
	definitions := []store.MailboxConfiguration{}
	for _, runtime := range h.service.mailboxes {
		definitions = append(definitions, store.MailboxConfiguration{ID: runtime.id, Root: filepath.Dir(runtime.importer.InboxPath())})
	}
	if err := h.authority.RegisterConfiguredMailboxSet(context.Background(), definitions, nil); err != nil {
		t.Fatal(err)
	}
	runtime := h.service.mailboxes[0]
	p154WriteMailboxRequest(t, runtime.importer, "req-paused", map[string]any{"request_id": "req-paused", "idempotency_key": "key-paused", "operation": "run", "script": "printf paused"})
	h.service.mailboxDisabled = true
	h.service.mailboxConfig = nil
	h.service.mailboxDefinitions = nil
	if err := h.service.activateAndPrepareMailboxRuntimes(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.service.runMailboxCycles(context.Background(), io.Discard)
	h.service.reconcileMailboxRuntimes(context.Background(), io.Discard, "paused")
	if err := h.authority.ValidateConfiguredMailboxSet(context.Background(), definitions, nil); err != nil {
		t.Fatalf("disabled ingress modified retained registry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime.importer.InboxPath(), "req-paused.ready")); err != nil {
		t.Fatalf("paused input removed: %v", err)
	}
	ref, _ := store.NewMailboxExchangeRef(runtime.id, "req-paused")
	if _, err := h.authority.GetMailboxExchangeInMailbox(context.Background(), ref); !errors.Is(err, store.ErrMailboxExchangeNotFound) {
		t.Fatalf("paused input persisted: %v", err)
	}
}

func TestHandoffCompositionAliasesOnlyTransferredInputsAndKeepsRegistryReversible(t *testing.T) {
	f := newRuntimeHandoffFixture(t)
	lease := f.acquire(t)
	h := newP154MailboxHarness(t)
	h.now = time.Now().UTC()
	native := configuredMailboxDefinitions(f.definitions[:1])
	if err := h.authority.RegisterConfiguredMailboxSet(context.Background(), native, native); err != nil {
		t.Fatal(err)
	}
	runtimes, err := composeMailboxRuntimes(f.definitions, h.authority, h.owner, h.api, handoffExecutionResolver{original: handoffTestResolver{}, lease: lease}, time.Hour, lease)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{database: h.authority, mailboxes: runtimes, mailboxConfig: f.definitions, mailboxDefinitions: configuredMailboxDefinitions(f.definitions), legacyMailboxSet: native, mailboxHandoff: lease}
	if err := service.activateConfiguredMailboxSet(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Temporary ownership cannot enter the monotonic native root registry.
	if err := h.authority.ValidateConfiguredMailboxSet(context.Background(), native, native); err != nil {
		t.Fatalf("handoff made restore irreversible: %v", err)
	}
	var first store.MailboxExchangeRecord
	var firstJobID string
	for _, id := range []string{"req-fresh", "req-retry"} {
		p154WriteMailboxRequest(t, runtimes[1].importer, id, map[string]any{"request_id": id, "idempotency_key": "key-fresh", "operation": "run", "environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": "printf alias"})
		if _, err := runtimes[1].processor.Import(context.Background()); err != nil {
			t.Fatal(err)
		}
		ref, _ := store.NewMailboxExchangeRef("analytics", id)
		record, err := h.authority.GetMailboxExchangeInMailbox(context.Background(), ref)
		if err != nil || record.Selection == nil || record.Selection.Environment != "mac-workstation-qas" || !bytes.Contains(record.CanonicalPayload, []byte(`"environment":"mac-dev"`)) {
			t.Fatalf("alias exchange=%+v err=%v", record, err)
		}
		var response struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(record.ResponseBytes, &response); err != nil || response.JobID == "" {
			t.Fatalf("alias response=%s err=%v", record.ResponseBytes, err)
		}
		if id == "req-fresh" {
			first = record
			firstJobID = response.JobID
		} else if response.JobID != firstJobID || record.ExecutionIdempotencyKey != first.ExecutionIdempotencyKey {
			t.Fatal("retry created different work")
		}
	}
	p154WriteMailboxRequest(t, runtimes[0].importer, "req-native-old-env", map[string]any{"request_id": "req-native-old-env", "idempotency_key": "key-native", "operation": "run", "environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": "printf inert"})
	if _, err := runtimes[0].processor.Import(context.Background()); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.NewMailboxExchangeRef("default", "req-native-old-env")
	record, err := h.authority.GetMailboxExchangeInMailbox(context.Background(), ref)
	if err != nil || record.State != store.MailboxExchangeRejected || record.Selection != nil {
		t.Fatalf("native mailbox accepted transferred alias: %+v err=%v", record, err)
	}
	// Simulate an unexpected external replacement, which the supported marker
	// writer itself refuses while the runtime lease is held.
	markerPath := filepath.Join(f.receipt.Mailboxes[0].Root, mailboxhandoff.MarkerFileName)
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, bytes.ReplaceAll(raw, []byte(f.digest), []byte(strings.Repeat("d", 64))), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	service.runMailboxCycles(context.Background(), &log)
	if !strings.Contains(log.String(), "mailbox ownership authorization failed") {
		t.Fatalf("stale marker not fenced: %s", log.String())
	}
}

type handoffTestResolver struct{}

func (handoffTestResolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, alias string) (config.MailboxExecutionSelection, error) {
	if !environmentPresent || !targetPresent || environment != "mac-workstation-qas" || target.Kind() != domain.TargetKindLocal || target.Profile() != "mac-workstation" {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotFound
	}
	return config.MailboxExecutionSelection{MailboxID: mailboxID, ContextName: "mac-local", Environment: environment, Target: target, Source: config.MailboxExecutionSelectionSourceRequestOverride}, nil
}

var _ mailbox.MailboxExecutionResolver = handoffTestResolver{}
