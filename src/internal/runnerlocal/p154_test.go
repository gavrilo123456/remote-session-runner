package runnerlocal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

func TestP154ConfiguredMailboxRuntimesCycleWithIsolatedArtifacts(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	commandID := h.createTerminalCommand(t)

	const sharedRunID = "req-p154-shared-run"
	const sharedReadID = "req-p154-shared-read"
	const sharedKey = "key-p154-shared-run"
	for _, runtime := range h.service.mailboxes {
		p154WriteMailboxRequest(t, runtime.importer, sharedRunID, map[string]any{
			"request_id": sharedRunID, "idempotency_key": sharedKey, "operation": "run",
			"script": "printf 'P154_ACCEPTED\n'",
		})
	}
	h.service.runMailboxCycles(ctx, io.Discard)

	// The same client-visible IDs are accepted independently because the
	// production composition passes each configured mailbox ID to its runtime.
	var exchangeIDs []string
	for _, mailboxID := range []string{store.DefaultMailboxID, "analytics"} {
		ref, err := store.NewMailboxExchangeRef(mailboxID, sharedRunID)
		if err != nil {
			t.Fatal(err)
		}
		record, err := h.authority.GetMailboxExchangeInMailbox(ctx, ref)
		if err != nil || record.State != store.MailboxExchangeAccepted || record.ExchangeID == "" {
			t.Fatalf("accepted %s exchange=%+v err=%v", mailboxID, record, err)
		}
		exchangeIDs = append(exchangeIDs, record.ExchangeID)
	}
	if exchangeIDs[0] == exchangeIDs[1] {
		t.Fatalf("same client request crossed inbox namespaces: %v", exchangeIDs)
	}

	report := macIngressHealthReportWithMetrics(ctx, h.authority, nil, mailboxRuntimeImporters(h.service.mailboxes), nil, nil, nil)
	if report.Metrics == nil || report.Metrics.MailboxBacklog != 2 ||
		report.Metrics.MailboxBacklogByInbox[store.DefaultMailboxID] != 1 ||
		report.Metrics.MailboxBacklogByInbox["analytics"] != 1 {
		t.Fatalf("multi-inbox metrics=%+v", report.Metrics)
	}

	for _, runtime := range h.service.mailboxes {
		p154WriteMailboxRequest(t, runtime.importer, sharedReadID, map[string]any{
			"request_id": sharedReadID, "operation": "get_command", "command_id": string(commandID),
		})
	}
	h.service.runMailboxCycles(ctx, io.Discard)

	for _, runtime := range h.service.mailboxes {
		response, responsePath := p154ReadResponse(t, runtime, sharedReadID)
		if response.RequestState != "complete" || response.CommandID != string(commandID) || response.AvailableEventSequence == nil || *response.AvailableEventSequence == 0 || response.InboxID != runtime.id {
			t.Fatalf("mailbox %s terminal response=%+v", runtime.id, response)
		}
		eventPath := filepath.Join(filepath.Dir(filepath.Dir(responsePath)), "events", string(commandID)+".ndjson")
		if event, err := os.ReadFile(eventPath); err != nil || !bytes.Contains(event, []byte("P154_TERMINAL_OUTPUT")) {
			t.Fatalf("mailbox %s event projection=%q err=%v", runtime.id, event, err)
		}
		p154WriteAck(t, runtime, sharedReadID, response.ResponseRevision, response.AvailableEventSequence)
	}
	h.service.runMailboxCycles(ctx, io.Discard)

	// ACK cleanup is deferred. Advance the authority clock and run the same
	// production cycle; each root removes only its own response/event files.
	h.now = h.now.Add(store.MailboxAckedResponseLifetime + time.Second)
	h.service.runMailboxCycles(ctx, io.Discard)
	for _, runtime := range h.service.mailboxes {
		_, responsePath := p154ReadResponsePath(runtime, sharedReadID)
		eventPath := filepath.Join(filepath.Dir(filepath.Dir(responsePath)), "events", string(commandID)+".ndjson")
		for _, path := range []string{responsePath, eventPath} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("mailbox %s cleanup path=%s err=%v", runtime.id, path, err)
			}
		}
	}
}

func TestBUG010MailboxBacklogExcludesMarkerOnlyResidue(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	runtime := h.service.mailboxes[0]
	markerOnly := filepath.Join(runtime.importer.InboxPath(), "req-b010-marker-only.ready")
	if err := os.WriteFile(markerOnly, nil, mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(markerOnly, mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	counts, readyTotal, err := mailboxBacklogByInbox(ctx, h.authority, mailboxRuntimeImporters(h.service.mailboxes))
	if err != nil {
		t.Fatal(err)
	}
	if readyTotal != 0 || counts[store.DefaultMailboxID] != 0 {
		t.Fatalf("marker-only residue inflated backlog: counts=%v ready=%d", counts, readyTotal)
	}

	p154WriteMailboxRequest(t, runtime.importer, "req-b010-complete-pair", map[string]any{
		"request_id": "req-b010-complete-pair", "operation": "run", "script": "printf B010",
	})
	counts, readyTotal, err = mailboxBacklogByInbox(ctx, h.authority, mailboxRuntimeImporters(h.service.mailboxes))
	if err != nil {
		t.Fatal(err)
	}
	if readyTotal != 1 || counts[store.DefaultMailboxID] != 1 {
		t.Fatalf("complete pair backlog=%v ready=%d, want one", counts, readyTotal)
	}

	retainedID := "req-b010-retained-accepted"
	ref, err := store.NewMailboxExchangeRef(store.DefaultMailboxID, retainedID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("p154-b010-retained"))
	hash, err := domain.NewCanonicalHash(domain.CanonicalizationVersionV1, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.authority.AcceptMailboxExchangeInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: store.DefaultMailboxID, RequestID: retainedID, Operation: "get_session", Controller: h.owner,
		RequestHash: hash, CanonicalPayload: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	p154WriteMailboxRequest(t, runtime.importer, retainedID, map[string]any{
		"request_id": retainedID, "operation": "get_session", "session_id": "sess-00000000000000000000000000000000",
	})
	counts, readyTotal, err = mailboxBacklogByInbox(ctx, h.authority, mailboxRuntimeImporters(h.service.mailboxes))
	if err != nil {
		t.Fatal(err)
	}
	if readyTotal != 1 || counts[store.DefaultMailboxID] != 2 {
		t.Fatalf("accepted retained pair was double-counted: counts=%v ready=%d", counts, readyTotal)
	}
}

func TestBUG010MailboxCyclesReconcileProvenOrphansAfterIntake(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	runtime := h.service.mailboxes[0]
	if runtime.orphanReconciler == nil {
		t.Fatal("configured durable orphan reconciler is missing")
	}
	commandID := h.createTerminalCommand(t)

	terminalID := "req-b010-cycle-terminal"
	p154WriteMailboxRequest(t, runtime.importer, terminalID, map[string]any{
		"request_id": terminalID, "operation": "get_command", "command_id": string(commandID),
	})
	h.service.runMailboxCycles(ctx, io.Discard)
	_, terminalResponsePath := p154ReadResponse(t, runtime, terminalID)
	terminalBefore, err := os.ReadFile(terminalResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime.importer.InboxPath(), terminalID+mailbox.ReadySuffix), nil, mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(runtime.importer.InboxPath(), terminalID+mailbox.ReadySuffix), mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}

	ackID := "req-b010-cycle-ack"
	p154WriteMailboxRequest(t, runtime.importer, ackID, map[string]any{
		"request_id": ackID, "operation": "get_command", "command_id": string(commandID),
	})
	h.service.runMailboxCycles(ctx, io.Discard)
	ackResponse, ackResponsePath := p154ReadResponse(t, runtime, ackID)
	ackBefore, err := os.ReadFile(ackResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	p154WriteAck(t, runtime, ackID, ackResponse.ResponseRevision, ackResponse.AvailableEventSequence)
	h.service.runMailboxCycles(ctx, io.Discard)
	if err := os.WriteFile(filepath.Join(filepath.Dir(runtime.importer.InboxPath()), "acks", ackID+mailbox.ReadySuffix), nil, mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(filepath.Dir(runtime.importer.InboxPath()), "acks", ackID+mailbox.ReadySuffix), mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}

	unknownID := "req-b010-cycle-unknown"
	if err := os.WriteFile(filepath.Join(runtime.importer.InboxPath(), unknownID+mailbox.ReadySuffix), nil, mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(runtime.importer.InboxPath(), unknownID+mailbox.ReadySuffix), mailbox.MailboxWorkspaceIngressFileMode); err != nil {
		t.Fatal(err)
	}

	h.service.runMailboxCycles(ctx, io.Discard)
	assertP154PathAbsent(t, filepath.Join(runtime.importer.InboxPath(), terminalID+mailbox.ReadySuffix))
	assertP154PathAbsent(t, filepath.Join(filepath.Dir(runtime.importer.InboxPath()), "acks", ackID+mailbox.ReadySuffix))
	assertP154PathPresent(t, filepath.Join(runtime.importer.InboxPath(), unknownID+mailbox.ReadySuffix))
	if terminalAfter, err := os.ReadFile(terminalResponsePath); err != nil || string(terminalAfter) != string(terminalBefore) {
		t.Fatalf("terminal response changed=%q err=%v", terminalAfter, err)
	}
	if ackAfter, err := os.ReadFile(ackResponsePath); err != nil || string(ackAfter) != string(ackBefore) {
		t.Fatalf("acknowledged response changed=%q err=%v", ackAfter, err)
	}
	evidence, err := h.authority.LookupMailboxLifecycleEvidenceForRequestIDsInMailbox(ctx, runtime.id, []string{terminalID, ackID, unknownID})
	if err != nil {
		t.Fatal(err)
	}
	if !evidence[terminalID].ExchangeExists || !evidence[ackID].ExchangeAcknowledged || evidence[unknownID].ExchangeExists {
		t.Fatalf("cycle durable evidence=%+v", evidence)
	}
}

func TestBUG010MailboxDurableOrphanCleanupIsExplicitPerInbox(t *testing.T) {
	h := newP154MailboxHarness(t)
	root := filepath.Dir(h.service.mailboxes[0].importer.InboxPath())
	withoutOptIn, err := composeMailboxRuntimes([]config.MailboxDefinition{{ID: store.DefaultMailboxID, Root: root}}, h.authority, h.owner, h.api, p154Resolver{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutOptIn) != 1 || withoutOptIn[0].orphanReconciler != nil {
		t.Fatalf("cleanup was active without opt-in: %+v", withoutOptIn)
	}
	withOptIn, err := composeMailboxRuntimes([]config.MailboxDefinition{{ID: store.DefaultMailboxID, Root: root, DurableOrphanCleanup: true}}, h.authority, h.owner, h.api, p154Resolver{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(withOptIn) != 1 || withOptIn[0].orphanReconciler == nil {
		t.Fatalf("cleanup was not active after opt-in: %+v", withOptIn)
	}
}

func TestP154RefusesInboxRemovalWhileItsAcceptedWorkIsPending(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	defaultOnly := []config.MailboxDefinition{{ID: store.DefaultMailboxID, Root: filepath.Join(config.MacServiceRoot, "mailbox")}}

	// This models a candidate's cheap pre-quiescence check. It is clean before
	// the old analytics runtime accepts work, so it cannot be the check that
	// authorizes an inbox removal.
	if err := validateRetainedMailboxSet(ctx, h.databasePath, defaultOnly); err != nil {
		t.Fatalf("early retained-mailbox validation error=%v, want nil", err)
	}

	var analytics mailboxRuntime
	for _, runtime := range h.service.mailboxes {
		if runtime.id == "analytics" {
			analytics = runtime
			break
		}
	}
	if analytics.importer == nil {
		t.Fatal("analytics runtime is missing")
	}
	p154WriteMailboxRequest(t, analytics.importer, "req-p154-removal-pending", map[string]any{
		"request_id": "req-p154-removal-pending", "idempotency_key": "key-p154-removal-pending",
		"operation": "run", "script": "printf P154_REMOVAL_PENDING",
	})
	h.service.runMailboxCycles(ctx, io.Discard)

	// A request accepted by the old runtime after that early check must make the
	// final, post-quiescence check fail. The installer performs this latter
	// check only after runner-local's API socket is gone.
	if err := validateConfiguredMailboxWork(ctx, h.authority, defaultOnly); !errors.Is(err, store.ErrMailboxConfigurationPending) {
		t.Fatalf("removed analytics inbox pending-work error=%v, want %v", err, store.ErrMailboxConfigurationPending)
	}
	if err := validateRetainedMailboxSet(ctx, h.databasePath, defaultOnly); !errors.Is(err, store.ErrMailboxConfigurationPending) {
		t.Fatalf("pre-restart retained-mailbox validation error=%v, want %v", err, store.ErrMailboxConfigurationPending)
	}
	if err := validateConfiguredMailboxWork(ctx, h.authority, []config.MailboxDefinition{
		{ID: store.DefaultMailboxID, Root: filepath.Join(config.MacServiceRoot, "mailbox")},
		{ID: "analytics", Root: filepath.Join(t.TempDir(), "mailboxes", "analytics")},
	}); err != nil {
		t.Fatalf("configured analytics inbox error=%v", err)
	}
}

func TestP154FailedCandidateConstructionLeavesV1StartupAvailable(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	defaultDefinition := config.MailboxDefinition{ID: store.DefaultMailboxID, Root: filepath.Dir(h.service.mailboxes[0].importer.InboxPath())}
	defaultSet := configuredMailboxDefinitions([]config.MailboxDefinition{defaultDefinition})

	unsafeRoot := filepath.Join(t.TempDir(), "analytics")
	if err := os.Mkdir(unsafeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	candidate := []config.MailboxDefinition{
		defaultDefinition,
		{ID: "analytics", Root: unsafeRoot},
	}
	if err := h.authority.ValidateConfiguredMailboxSet(ctx, configuredMailboxDefinitions(candidate), defaultSet); err != nil {
		t.Fatalf("candidate retained-work validation: %v", err)
	}
	if _, err := composeMailboxRuntimes(candidate, h.authority, h.owner, h.api, p154Resolver{}, time.Hour); err == nil {
		t.Fatal("unsafe candidate mailbox root unexpectedly composed")
	}

	// A pre-boundary candidate validation/construction failure must not poison
	// the append-only registry or block the prior V1/default configuration.
	movedDefault := []store.MailboxConfiguration{{ID: store.DefaultMailboxID, Root: filepath.Join(t.TempDir(), "moved-default")}}
	if err := h.authority.ValidateConfiguredMailboxSet(ctx, movedDefault, nil); err != nil {
		t.Fatalf("failed candidate registered a mailbox root: %v", err)
	}

	runDirectory, err := os.MkdirTemp("/tmp", "rsr-p154-v1-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDirectory) })
	api, err := localapi.NewServer(localapi.ServerOptions{
		Authority: h.authority, Owner: h.owner, SocketPath: filepath.Join(runDirectory, "local-api.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close(context.Background()) })
	defaultRuntimes, err := composeMailboxRuntimes([]config.MailboxDefinition{defaultDefinition}, h.authority, h.owner, api, p154Resolver{}, time.Hour)
	if err != nil {
		t.Fatalf("compose V1/default runtime: %v", err)
	}
	localDriver, err := dispatcher.NewLocalDriver(h.authority, p154NoopIntentAcceptor{}, "p154-v1-recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	remoteResolver, err := dispatcher.NewRemoteCallerResolver(map[string]dispatcher.RemoteCaller{})
	if err != nil {
		t.Fatal(err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriverWithResolver(h.authority, remoteResolver, "p154-v1-recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		database: h.authority, dbCloser: p154NoopCloser{}, api: api,
		localDriver: localDriver, remoteDriver: remoteDriver, mailboxes: defaultRuntimes,
		mailboxDefinitions: defaultSet, legacyMailboxSet: defaultSet,
		pollInterval: time.Hour,
	}
	serveContext, stop := context.WithCancel(context.Background())
	defer stop()
	serveDone := make(chan error, 1)
	go func() { serveDone <- service.Serve(serveContext, io.Discard, io.Discard) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := h.authority.ValidateConfiguredMailboxSet(ctx, movedDefault, nil)
		if errors.Is(err, store.ErrMailboxConfigurationPending) {
			break
		}
		if err != nil {
			t.Fatalf("V1 service activation validation: %v", err)
		}
		select {
		case serveErr := <-serveDone:
			t.Fatalf("V1/default service started then stopped before registration: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("V1/default service did not register its mailbox root")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("V1/default service shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("V1/default service did not stop")
	}
}

func TestP154CandidateActivationPreventsV1RemovalAfterMarkerPublication(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	defaultDefinition := config.MailboxDefinition{ID: store.DefaultMailboxID, Root: filepath.Dir(h.service.mailboxes[0].importer.InboxPath())}
	analyticsDefinition := config.MailboxDefinition{ID: "analytics", Root: filepath.Join(t.TempDir(), "mailboxes", "analytics")}
	defaultSet := configuredMailboxDefinitions([]config.MailboxDefinition{defaultDefinition})
	candidateSet := configuredMailboxDefinitions([]config.MailboxDefinition{defaultDefinition, analyticsDefinition})

	// This models the installer immediately after its irreversible boundary:
	// it records the candidate before handing off mac.yaml or opening its root.
	if err := h.authority.RegisterConfiguredMailboxSet(ctx, candidateSet, defaultSet); err != nil {
		t.Fatalf("activate candidate mailbox set: %v", err)
	}
	analytics, err := mailbox.New(mailbox.Options{MailboxID: analyticsDefinition.ID, Root: analyticsDefinition.Root})
	if err != nil {
		t.Fatalf("create candidate root: %v", err)
	}
	// Simulate a later candidate-startup failure: no processor imports this
	// marker, but a same-user producer can still publish it marker-last.
	p154WriteMailboxRequest(t, analytics, "req-p154-candidate-marker", map[string]any{
		"request_id": "req-p154-candidate-marker", "idempotency_key": "key-p154-candidate-marker",
		"operation": "run", "script": "printf P154_CANDIDATE_MARKER",
	})
	if err := h.authority.ValidateConfiguredMailboxSet(ctx, defaultSet, defaultSet); !errors.Is(err, store.ErrMailboxConfigurationPending) {
		t.Fatalf("V1 removal after candidate marker error=%v, want %v", err, store.ErrMailboxConfigurationPending)
	}
	if err := store.ValidateConfiguredMailboxSetAtPath(ctx, h.databasePath, defaultSet, defaultSet); !errors.Is(err, store.ErrMailboxConfigurationPending) {
		t.Fatalf("read-only V1 removal after candidate marker error=%v, want %v", err, store.ErrMailboxConfigurationPending)
	}
}

func TestP154LegacyDefaultAndV2ExamplesValidateWithoutStartingServices(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	for _, fixture := range []struct {
		name           string
		file           string
		wantVersion    int
		wantMailboxIDs []string
	}{
		{"legacy v1", "mac.yaml.example", config.VersionV1, []string{store.DefaultMailboxID}},
		{"named v2", "mac.v2.yaml.example", config.VersionV2, []string{store.DefaultMailboxID}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := p154CopyOwnerOnlyConfig(t, filepath.Join(repositoryRoot, "deploy", "macos", fixture.file))
			loaded, _, mailboxes, err := loadSelectedMacConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.SchemaVersion() != fixture.wantVersion || len(mailboxes) != len(fixture.wantMailboxIDs) {
				t.Fatalf("config version=%d mailboxes=%+v", loaded.SchemaVersion(), mailboxes)
			}
			for index, want := range fixture.wantMailboxIDs {
				if mailboxes[index].ID != want {
					t.Fatalf("mailboxes=%+v, want %v", mailboxes, fixture.wantMailboxIDs)
				}
			}
			if fixture.wantVersion == config.VersionV1 && mailboxes[0].Root != filepath.Join(config.MacServiceRoot, "mailbox") {
				t.Fatalf("legacy root=%q", mailboxes[0].Root)
			}
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"validate-config", "--config", path}, &stdout, &stderr); code != 0 ||
				!strings.Contains(stdout.String(), "schema_version=") || stderr.Len() != 0 {
				t.Fatalf("validate-config code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}

	valid := p154CopyOwnerOnlyConfig(t, filepath.Join(repositoryRoot, "deploy", "macos", "mac.yaml.example"))
	var activateErr bytes.Buffer
	if code := Run([]string{"validate-config", "--config", valid, "--activate-mailbox-set"}, io.Discard, &activateErr); code != 2 ||
		!strings.Contains(activateErr.String(), "requires --check-retained-mailboxes") {
		t.Fatalf("activation without retained check code=%d stderr=%q", code, activateErr.String())
	}
	if err := os.Chmod(valid, 0o640); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"validate-config", "--config", valid}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("permissive config validate-config code=%d, want 1", code)
	}

	link := filepath.Join(t.TempDir(), "mac-link.yaml")
	if err := os.Symlink(p154CopyOwnerOnlyConfig(t, filepath.Join(repositoryRoot, "deploy", "macos", "mac.yaml.example")), link); err != nil {
		t.Fatal(err)
	}
	if code := Run([]string{"validate-config", "--config", link}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("symlink config validate-config code=%d, want 1", code)
	}
}

func TestP154ServicePathsSecureEveryConfiguredMailboxDirectory(t *testing.T) {
	root := t.TempDir()
	settings := p154MacSettings(root)
	mailboxRoot := filepath.Join(root, "mailboxes", "analytics")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "mailboxes")); err != nil {
		t.Fatal(err)
	}
	if err := ensureMacServicePaths(settings, mailboxRoot); err == nil {
		t.Fatal("symlinked mailbox ancestor was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "analytics")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlinked ancestor created outside mailbox directory: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "mailboxes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "mailboxes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureMacServicePaths(settings, mailboxRoot); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "mailboxes"), mailboxRoot,
		filepath.Join(mailboxRoot, "inbox"), filepath.Join(mailboxRoot, "outbox"),
		filepath.Join(mailboxRoot, "events"), filepath.Join(mailboxRoot, "acks"),
		filepath.Join(mailboxRoot, "diagnostics"),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || int(stat.Uid) != os.Geteuid() {
			t.Fatalf("secured path %s info=%+v", path, info)
		}
	}
	unsafeRoot := filepath.Join(root, "unsafe-mailbox")
	if err := os.Mkdir(unsafeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := mailbox.New(mailbox.Options{MailboxID: "analytics", Root: unsafeRoot}); err == nil {
		t.Fatal("mailbox accepted a non-0700 root")
	}
}

func TestP154InstallerValidatesConfigBeforeLaunchAgentReplacement(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	script := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if err := exec.Command("sh", "-n", script).Run(); err != nil {
		t.Fatalf("installer shell syntax: %v", err)
	}
	staticValidate := `"$staging_directory/runner-local" validate-config --config "$selected_config"`
	checkMailboxDirectories := `"$staging_directory/runner-local" validate-config --check-mailbox-directories --config "$selected_config"`
	finalValidate := `"$staging_directory/runner-local" validate-config --check-retained-mailboxes --config "$selected_config"`
	activateMailboxSet := `"$staging_directory/runner-local" validate-config --check-retained-mailboxes --activate-mailbox-set --config "$selected_config"`
	quiesceLocal := `stop_agent_for_config_change com.remote-session-runner.local "$launch_agents/com.remote-session-runner.local.plist" "$service_root/run/local-api.sock"`
	quiesceLocalD := `stop_agent_for_config_change com.remote-session-runner.locald "$launch_agents/com.remote-session-runner.locald.plist" "$service_root/run/locald.sock"`
	preflightLocalD := `"$staging_directory/runner-locald" preflight-restart --config "$config_file"`
	preflightInvocation := `if "$staging_directory/runner-locald" preflight-restart --config "$config_file"; then`
	trueFirstInstall := "is_true_first_locald_install() {"
	missingAuthorityAllowance := `if [ "$preflight_status" -eq 3 ] && is_true_first_locald_install; then`
	activationBoundary := "candidate_activation_started=1"
	configHandoff := `mv -f "$config_stage" "$config_file"`
	handoffComplete := "candidate_config_handed_off=1"
	recoveryGuard := `if [ "$candidate_activation_started" -eq 1 ] && [ "$candidate_config_handed_off" -eq 0 ]; then`
	if !strings.Contains(text, "ensure_private_service_directory") || !strings.Contains(text, "ensure_launch_agents_directory") ||
		!strings.Contains(text, staticValidate) || !strings.Contains(text, finalValidate) ||
		!strings.Contains(text, checkMailboxDirectories) ||
		!strings.Contains(text, activateMailboxSet) ||
		!strings.Contains(text, quiesceLocal) || !strings.Contains(text, quiesceLocalD) ||
		!strings.Contains(text, preflightLocalD) || !strings.Contains(text, preflightInvocation) ||
		!strings.Contains(text, trueFirstInstall) || !strings.Contains(text, missingAuthorityAllowance) ||
		!strings.Contains(text, "$service_root/state/local.db-wal") ||
		!strings.Contains(text, "Refusing runner-locald refresh because local execution is not provably quiescent.") ||
		!strings.Contains(text, activationBoundary) || !strings.Contains(text, configHandoff) ||
		!strings.Contains(text, handoffComplete) || !strings.Contains(text, recoveryGuard) ||
		!strings.Contains(text, "restore_prior_agents_on_failure=1") ||
		!strings.Contains(text, "--config") || !strings.Contains(text, "mac.yaml.example") ||
		strings.Contains(text, "--register-mailboxes") {
		t.Fatalf("installer lacks P154 safe configuration flow")
	}
	staticIndex := strings.Index(text, staticValidate)
	checkPathsIndex := strings.Index(text, checkMailboxDirectories)
	localIndex := strings.Index(text, quiesceLocal)
	// The controlled-restart branch now has its own safe handoff. Keep this
	// check pinned to the ordinary branch, whose strict preflight and graceful
	// locald quiescence remain the default install contract.
	preflightIndex := strings.LastIndex(text, "\tpreflight_active_locald_restart\n")
	localDIndex := strings.LastIndex(text, quiesceLocalD)
	finalIndex := strings.Index(text, finalValidate)
	activationIndex := strings.LastIndex(text, activationBoundary)
	activateMailboxSetIndex := strings.Index(text, activateMailboxSet)
	configHandoffIndex := strings.Index(text, configHandoff)
	handoffCompleteIndex := strings.LastIndex(text, handoffComplete)
	recoveryGuardIndex := strings.Index(text, recoveryGuard)
	replacementLoop := strings.LastIndex(text, "for name in com.remote-session-runner.locald com.remote-session-runner.local; do")
	if replacementLoop < 0 {
		t.Fatal("installer lacks LaunchAgent replacement loop")
	}
	bootstrapIndex := replacementLoop + strings.Index(text[replacementLoop:], "launchctl bootstrap")
	if staticIndex < 0 || checkPathsIndex < 0 || localIndex < 0 || preflightIndex < 0 || localDIndex < 0 || finalIndex < 0 || activationIndex < 0 || activateMailboxSetIndex < 0 || configHandoffIndex < 0 || handoffCompleteIndex < 0 || recoveryGuardIndex < 0 || bootstrapIndex < replacementLoop {
		t.Fatalf("installer sequence indexes static=%d check_paths=%d local=%d preflight=%d locald=%d final=%d activation=%d activate_mailboxes=%d handoff=%d handoff_complete=%d recovery_guard=%d bootstrap=%d", staticIndex, checkPathsIndex, localIndex, preflightIndex, localDIndex, finalIndex, activationIndex, activateMailboxSetIndex, configHandoffIndex, handoffCompleteIndex, recoveryGuardIndex, bootstrapIndex)
	}
	if !(staticIndex < checkPathsIndex && checkPathsIndex < localIndex && localIndex < preflightIndex && preflightIndex < localDIndex && localDIndex < finalIndex && finalIndex < activationIndex && activationIndex < activateMailboxSetIndex && activateMailboxSetIndex < configHandoffIndex && configHandoffIndex < handoffCompleteIndex && handoffCompleteIndex < bootstrapIndex && recoveryGuardIndex < configHandoffIndex) {
		t.Fatalf("installer must statically validate, check mailbox paths without creating them, quiesce ingress, prove local executor quiescence without writing, quiesce locald, validate retained ingress, cross activation boundary, register candidate inboxes and create any missing tree, preserve pre-handoff recovery config, hand off config, then bootstrap: static=%d check_paths=%d local=%d preflight=%d locald=%d final=%d activation=%d activate_mailboxes=%d handoff=%d handoff_complete=%d recovery_guard=%d bootstrap=%d", staticIndex, checkPathsIndex, localIndex, preflightIndex, localDIndex, finalIndex, activationIndex, activateMailboxSetIndex, configHandoffIndex, handoffCompleteIndex, recoveryGuardIndex, bootstrapIndex)
	}
	if strings.Contains(text[preflightIndex:localDIndex], `if [ "$locald_was_loaded" -ne 1 ]; then`) {
		t.Fatal("ordinary installer branch unexpectedly requires a loaded local executor")
	}
}

func TestBUG011InstallerRestartPreflightPropagatesSafeOutcomes(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	script := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "is_true_first_locald_install() {")
	if start < 0 {
		t.Fatal("could not find installer restart preflight functions")
	}
	end := strings.Index(text[start:], "\nstop_candidate_agent_after_start_failure() {")
	if end < 0 {
		t.Fatal("could not isolate installer restart preflight functions")
	}
	functions := text[start : start+end]

	for _, fixture := range []struct {
		name               string
		preflightExit      int
		localdWasLoaded    int
		artifact           string
		wantSuccess        bool
		wantOutputFragment string
	}{
		{name: "quiescent authority passes", preflightExit: 0, wantSuccess: true},
		{name: "nonquiescent authority refuses", preflightExit: 1, wantOutputFragment: "Refusing runner-locald refresh"},
		{name: "unknown preflight failure refuses", preflightExit: 2, wantOutputFragment: "Refusing runner-locald refresh"},
		{name: "missing authority permits true first install", preflightExit: 3, wantSuccess: true, wantOutputFragment: "No existing local authority was found"},
		{name: "missing authority with old binary refuses", preflightExit: 3, artifact: "bin/runner-locald", wantOutputFragment: "Refusing runner-locald refresh"},
		{name: "missing authority with sidecar refuses", preflightExit: 3, artifact: "state/local.db-wal", wantOutputFragment: "Refusing runner-locald refresh"},
		{name: "missing authority while locald was loaded refuses", preflightExit: 3, localdWasLoaded: 1, wantOutputFragment: "Refusing runner-locald refresh"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			serviceRoot := filepath.Join(root, "service-root")
			launchAgents := filepath.Join(root, "launch-agents")
			staging := filepath.Join(root, "staging")
			if err := os.MkdirAll(staging, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(launchAgents, 0o700); err != nil {
				t.Fatal(err)
			}
			fakeLocald := filepath.Join(staging, "runner-locald")
			if err := os.WriteFile(fakeLocald, []byte("#!/bin/sh\nexit \"${RSR_B011_PREFLIGHT_EXIT:?}\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if fixture.artifact != "" {
				path := filepath.Join(serviceRoot, fixture.artifact)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			harness := "set -eu\nlocald_was_loaded=$1\nservice_root=$2\nlaunch_agents=$3\nstaging_directory=$4\nconfig_file=$5\n" + functions + "\npreflight_active_locald_restart\nprintf '%s\\n' 'after-preflight-sentinel'\n"
			command := exec.Command("sh", "-c", harness, "bug011-preflight", strconv.Itoa(fixture.localdWasLoaded), serviceRoot, launchAgents, staging, filepath.Join(root, "active.yaml"))
			command.Env = append(os.Environ(), "RSR_B011_PREFLIGHT_EXIT="+strconv.Itoa(fixture.preflightExit))
			output, runErr := command.CombinedOutput()
			if fixture.wantSuccess {
				if runErr != nil {
					t.Fatalf("preflight harness error=%v output=%s", runErr, output)
				}
				if !strings.Contains(string(output), "after-preflight-sentinel") {
					t.Fatalf("preflight harness did not reach the next installer step: output=%s", output)
				}
			} else {
				if runErr == nil {
					t.Fatalf("preflight harness unexpectedly succeeded: output=%s", output)
				}
				var exitErr *exec.ExitError
				if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("preflight harness error=%v, want exit status 1; output=%s", runErr, output)
				}
				if strings.Contains(string(output), "after-preflight-sentinel") {
					t.Fatalf("preflight refusal reached the next installer step: output=%s", output)
				}
			}
			if fixture.wantOutputFragment != "" && !strings.Contains(string(output), fixture.wantOutputFragment) {
				t.Fatalf("preflight harness output=%q, want %q", output, fixture.wantOutputFragment)
			}
			if strings.Contains(string(output), "launchctl") {
				t.Fatalf("isolated preflight harness attempted service control: %s", output)
			}
		})
	}
}

func TestBUG011ControlledRestartInstallerFreezesThenHardStopsOnlyAfterPlan(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	script := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("sh", "-n", script).Run(); err != nil {
		t.Fatalf("installer shell syntax: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"--b011-controlled-restart",
		"controlled_restart_suspend_locald() {",
		"controlled_restart_hard_stop_locald() {",
		"controlled_restart_prove_old_locald_socket_boundary() {",
		"controlled_restart_wait_for_launchd_throttle() {",
		"controlled_restart_enable_candidate_locald() {",
		"controlled_restart_read_state() {",
		"controlled_restart_refresh_failure_boundary() {",
		"controlled_restart_prepare_invoked=1",
		"controlled-restart-status --config \"$config_file\"",
		"migrated-without-plan)",
		"launchctl disable \"gui/$uid/$label\"",
		"launchctl kill SIGSTOP \"gui/$uid/$label\"",
		`"$staging_directory/runner-locald" prepare-controlled-restart --config "$config_file"`,
		"controlled_restart_plan_prepared=1",
		"launchctl bootout \"gui/$uid\" \"$plist\"",
		"controlled-restart-socket-boundary --config \"$config_file\"",
		"/usr/libexec/PlistBuddy -c 'Print :ThrottleInterval' \"$plist\"",
		"sleep \"$throttle\"",
		"launchctl enable \"gui/$uid/$label\"",
		"controlled_restart_restore_suspended_locald",
		"Controlled restart must use the active mac.yaml",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("controlled restart installer omits %q", required)
		}
	}

	suspend := strings.Index(text, "controlled_restart_suspend_locald\n")
	prepare := strings.Index(text, `"$staging_directory/runner-locald" prepare-controlled-restart --config "$config_file"`)
	prepared := strings.Index(text, "controlled_restart_plan_prepared=1")
	hardStop := strings.Index(text, "controlled_restart_hard_stop_locald\n")
	if suspend < 0 || prepare < 0 || prepared < 0 || hardStop < 0 || !(suspend < prepare && prepare < prepared && prepared < hardStop) {
		t.Fatalf("controlled restart ordering suspend=%d prepare=%d prepared=%d hard_stop=%d", suspend, prepare, prepared, hardStop)
	}
	suspendFunctionStart := strings.Index(text, "controlled_restart_suspend_locald() {")
	suspendFunctionEnd := strings.Index(text, "controlled_restart_restore_suspended_locald() {")
	hardStopFunctionStart := strings.Index(text, "controlled_restart_hard_stop_locald() {")
	hardStopFunctionEnd := strings.Index(text, "controlled_restart_prove_old_locald_socket_boundary() {")
	socketBoundaryFunctionStart := hardStopFunctionEnd
	socketBoundaryFunctionEnd := strings.Index(text, "controlled_restart_wait_for_launchd_throttle() {")
	throttleFunctionStart := socketBoundaryFunctionEnd
	throttleFunctionEnd := strings.Index(text, "controlled_restart_enable_candidate_locald() {")
	candidateEnableFunctionStart := throttleFunctionEnd
	candidateEnableFunctionEnd := strings.Index(text, "# This command opens the existing authority read-only")
	candidateEnableCall := strings.LastIndex(text, "\tif ! controlled_restart_enable_candidate_locald; then\n")
	if suspendFunctionStart < 0 || suspendFunctionEnd < 0 || hardStopFunctionStart < 0 || hardStopFunctionEnd < 0 || socketBoundaryFunctionStart < 0 || socketBoundaryFunctionEnd < 0 || throttleFunctionStart < 0 || throttleFunctionEnd < 0 || candidateEnableFunctionStart < 0 || candidateEnableFunctionEnd < 0 || candidateEnableCall < 0 {
		t.Fatalf("controlled restart launchd function boundaries suspend=%d/%d hard_stop=%d/%d socket=%d/%d throttle=%d/%d candidate_enable=%d/%d call=%d", suspendFunctionStart, suspendFunctionEnd, hardStopFunctionStart, hardStopFunctionEnd, socketBoundaryFunctionStart, socketBoundaryFunctionEnd, throttleFunctionStart, throttleFunctionEnd, candidateEnableFunctionStart, candidateEnableFunctionEnd, candidateEnableCall)
	}
	suspendFunction := text[suspendFunctionStart:suspendFunctionEnd]
	hardStopFunction := text[hardStopFunctionStart:hardStopFunctionEnd]
	socketBoundaryFunction := text[socketBoundaryFunctionStart:socketBoundaryFunctionEnd]
	throttleFunction := text[throttleFunctionStart:throttleFunctionEnd]
	candidateEnableFunction := text[candidateEnableFunctionStart:candidateEnableFunctionEnd]
	disable := strings.Index(suspendFunction, "launchctl disable \"gui/$uid/$label\"")
	freeze := strings.Index(suspendFunction, "launchctl kill SIGSTOP \"gui/$uid/$label\"")
	bootout := strings.Index(hardStopFunction, "launchctl bootout \"gui/$uid\" \"$plist\"")
	proveUnloaded := strings.Index(hardStopFunction, "if service_loaded \"$label\"; then")
	disableIntent := strings.Index(suspendFunction, "controlled_locald_disabled=1")
	freezeIntent := strings.Index(suspendFunction, "controlled_locald_suspended=1")
	clearSocket := strings.Index(socketBoundaryFunction, "controlled-restart-socket-boundary --config \"$config_file\"")
	readThrottle := strings.Index(throttleFunction, "/usr/libexec/PlistBuddy -c 'Print :ThrottleInterval' \"$plist\"")
	waitThrottle := strings.Index(throttleFunction, "sleep \"$throttle\"")
	enable := strings.Index(candidateEnableFunction, "launchctl enable \"gui/$uid/$label\"")
	callThrottle := strings.Index(candidateEnableFunction, "controlled_restart_wait_for_launchd_throttle")
	if disable < 0 || freeze < 0 || bootout < 0 || proveUnloaded < 0 || disableIntent < 0 || freezeIntent < 0 || clearSocket < 0 || readThrottle < 0 || waitThrottle < 0 || enable < 0 || callThrottle < 0 || !(disableIntent < disable && disable < freezeIntent && freezeIntent < freeze) || !(bootout < proveUnloaded) || !(readThrottle < waitThrottle) || !(enable < callThrottle) {
		t.Fatalf("controlled restart must record rollback before disable/freeze, then bootout/prove unload, clear socket, and hold ThrottleInterval after enable: disable_intent=%d disable=%d freeze_intent=%d freeze=%d bootout=%d prove_unloaded=%d clear_socket=%d read_throttle=%d wait_throttle=%d enable=%d call_throttle=%d", disableIntent, disable, freezeIntent, freeze, bootout, proveUnloaded, clearSocket, readThrottle, waitThrottle, enable, callThrottle)
	}
	if strings.Contains(hardStopFunction, "launchctl kill SIGKILL") {
		t.Fatal("controlled restart must not SIGKILL a still loaded KeepAlive label before bootout")
	}
	socketBoundaryCall := strings.LastIndex(text, "\tif ! controlled_restart_prove_old_locald_socket_boundary; then\n")
	if !(hardStop < socketBoundaryCall && socketBoundaryCall < candidateEnableCall) {
		t.Fatalf("candidate label enable must follow controlled hard-stop and old-socket proof: hard_stop=%d socket_boundary=%d candidate_enable=%d", hardStop, socketBoundaryCall, candidateEnableCall)
	}
	stateRead := strings.Index(text, "controlled_restart_state=$(controlled_restart_read_state)")
	stopIngress := strings.Index(text, "stop_agent_for_config_change com.remote-session-runner.local")
	if stateRead < 0 || stopIngress < 0 || stateRead > stopIngress {
		t.Fatalf("controlled restart must inspect durable state before ingress quiescence: state=%d ingress=%d", stateRead, stopIngress)
	}
	if strings.Contains(text, "controlled_restart_plan_prepared=0 ]; then\n\t\treturn 0") {
		t.Fatal("controlled restart restoration still relies only on an in-memory plan flag")
	}
	// The ordinary preflight remains present in the non-controlled branch. The
	// special path must not use it because its sole job is to reject the exact
	// retained-capacity state the controlled plan protects.
	branch := text[suspend : hardStop+len("controlled_restart_hard_stop_locald\n")]
	if strings.Contains(branch, "preflight_active_locald_restart") || strings.Contains(branch, "stop_agent_for_config_change com.remote-session-runner.locald") {
		t.Fatalf("controlled installer branch fell back to ordinary locald shutdown: %s", branch)
	}
}

func TestBUG013InstallerRunsExplicitMacStalledRecoveryBeforeNormalRestartPreflight(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	script := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("sh", "-n", script).Run(); err != nil {
		t.Fatalf("installer shell syntax: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"--recover-stalled",
		"--lost-pair",
		"--lost-session",
		"mac_recovery_sessions=''",
		"mac_recovery_resume_mode=0",
		"mac_recovery_candidate_boundary=0",
		"run_mac_recover_stalled() {",
		"install_staged_recovery_candidate_binaries() {",
		"capture_loaded_agent_pid() {",
		"wait_for_inert_or_absent_agent_pid() {",
		"mac_recovery_old_process_boundary_confirmed=0",
		`set -- recover-stalled --config "$config_file" --apply`,
		`set -- "$@" --lost-pair "$pair"`,
		`for session in $mac_recovery_sessions; do`,
		`set -- "$@" --lost-session "$session"`,
		"Stalled recovery must use the active mac.yaml",
		"Stalled Mac recovery did not complete; leaving candidate binaries installed and both LaunchAgents stopped.",
		"both unloaded with both private sockets absent for a safe retry.",
		"prior LaunchAgents were not restarted automatically.",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("stalled-recovery installer omits %q", required)
		}
	}

	branchStart := strings.LastIndex(text, `if [ "$mac_recover_stalled_mode" -eq 1 ]; then`)
	preflight := strings.LastIndex(text, "\tpreflight_active_locald_restart\n")
	branchEnd := strings.LastIndex(text[:preflight], "\tfi\n")
	if branchStart < 0 || preflight < 0 || branchEnd <= branchStart {
		t.Fatalf("could not isolate stalled-recovery installer branch: start=%d end=%d preflight=%d", branchStart, branchEnd, preflight)
	}
	branch := text[branchStart:branchEnd]
	recover := branchStart + strings.Index(branch, "run_mac_recover_stalled")
	candidateBoundary := branchStart + strings.Index(branch, "mac_recovery_candidate_boundary=1")
	installCandidate := branchStart + strings.Index(branch, "if ! install_staged_recovery_candidate_binaries; then")
	captureLocal := strings.LastIndex(text[:branchStart], `mac_recovery_local_pid=$(capture_loaded_agent_pid com.remote-session-runner.local)`)
	captureLocalD := strings.LastIndex(text[:branchStart], `mac_recovery_locald_pid=$(capture_loaded_agent_pid com.remote-session-runner.locald)`)
	boundaryIntent := strings.LastIndex(text[:branchStart], "mac_recovery_old_process_boundary_confirmed=0")
	ingressStop := strings.LastIndex(text[:branchStart], `stop_agent_for_config_change com.remote-session-runner.local "$launch_agents/com.remote-session-runner.local.plist" "$service_root/run/local-api.sock"`)
	waitLocal := strings.LastIndex(text[:branchStart], `wait_for_inert_or_absent_agent_pid "$mac_recovery_local_pid" com.remote-session-runner.local`)
	localdStop := strings.LastIndex(text[:branchStart], `stop_agent_for_config_change com.remote-session-runner.locald "$launch_agents/com.remote-session-runner.locald.plist" "$service_root/run/locald.sock"`)
	waitLocalD := strings.LastIndex(text[:branchStart], `wait_for_inert_or_absent_agent_pid "$mac_recovery_locald_pid" com.remote-session-runner.locald`)
	boundaryConfirmed := strings.LastIndex(text[:branchStart], "mac_recovery_old_process_boundary_confirmed=1")
	if captureLocal < 0 || captureLocalD < 0 || boundaryIntent < 0 || ingressStop < 0 || waitLocal < 0 || localdStop < 0 || waitLocalD < 0 || boundaryConfirmed < 0 || candidateBoundary < 0 || installCandidate < 0 || recover < 0 || !(captureLocal < captureLocalD && captureLocalD < boundaryIntent && boundaryIntent < ingressStop && ingressStop < waitLocal && waitLocal < localdStop && localdStop < waitLocalD && waitLocalD < boundaryConfirmed && boundaryConfirmed < branchStart && branchStart < candidateBoundary && candidateBoundary < installCandidate && installCandidate < recover && recover < preflight) {
		t.Fatalf("stalled recovery must capture/prove old boundaries, install candidate, then repair: capture_local=%d capture_locald=%d boundary_intent=%d ingress_stop=%d wait_local=%d locald_stop=%d wait_locald=%d boundary_confirmed=%d branch=%d candidate_boundary=%d install_candidate=%d recover=%d preflight=%d", captureLocal, captureLocalD, boundaryIntent, ingressStop, waitLocal, localdStop, waitLocalD, boundaryConfirmed, branchStart, candidateBoundary, installCandidate, recover, preflight)
	}
	if strings.Contains(branch, "controlled_restart_suspend_locald") || strings.Contains(branch, "prepare-controlled-restart") {
		t.Fatalf("stalled recovery must not enter the B011 queued-work handoff: %s", branch)
	}
	recoveryFunctionStart := strings.Index(text, "run_mac_recover_stalled() {")
	if recoveryFunctionStart < 0 {
		t.Fatal("could not locate Mac stalled-recovery forwarding function")
	}
	recoveryFunctionEnd := strings.Index(text[recoveryFunctionStart:], "\n}\n\n#")
	if recoveryFunctionEnd < 0 {
		t.Fatalf("could not isolate Mac stalled-recovery forwarding function end: start=%d end=%d", recoveryFunctionStart, recoveryFunctionEnd)
	}
	recoveryFunction := text[recoveryFunctionStart : recoveryFunctionStart+recoveryFunctionEnd]
	pairForward := strings.Index(recoveryFunction, `set -- "$@" --lost-pair "$pair"`)
	sessionLoop := strings.Index(recoveryFunction, `for session in $mac_recovery_sessions; do`)
	sessionForward := strings.Index(recoveryFunction, `set -- "$@" --lost-session "$session"`)
	candidateRun := strings.Index(recoveryFunction, `"$staging_directory/runner-locald" "$@"`)
	if pairForward < 0 || sessionLoop < 0 || sessionForward < 0 || candidateRun < 0 || !(pairForward < sessionLoop && sessionLoop < sessionForward && sessionForward < candidateRun) {
		t.Fatalf("Mac stalled recovery drops or misorders commandless sessions: pair=%d session_loop=%d session_forward=%d candidate=%d", pairForward, sessionLoop, sessionForward, candidateRun)
	}
	resumeGuard := `elif [ "$local_was_loaded" -eq 0 ] && [ "$locald_was_loaded" -eq 0 ] \
		&& [ ! -e "$service_root/run/local-api.sock" ] && [ ! -L "$service_root/run/local-api.sock" ] \
		&& [ ! -e "$service_root/run/locald.sock" ] && [ ! -L "$service_root/run/locald.sock" ]; then`
	if !strings.Contains(text, resumeGuard) || !strings.Contains(text, "mac_recovery_resume_mode=1") {
		t.Fatal("stalled recovery lacks its all-unloaded, socket-absent retry boundary")
	}
	installFunctionStart := strings.Index(text, "install_staged_recovery_candidate_binaries() {")
	if installFunctionStart < 0 {
		t.Fatal("could not locate Mac candidate install function")
	}
	installFunctionEnd := strings.Index(text[installFunctionStart:], "\n}\n\n# The controlled-restart")
	if installFunctionEnd < 0 {
		t.Fatalf("could not isolate Mac candidate install function end: start=%d end=%d", installFunctionStart, installFunctionEnd)
	}
	installFunction := text[installFunctionStart : installFunctionStart+installFunctionEnd]
	for _, required := range []string{
		`candidate_copy="$service_root/bin/.${name}.candidate.$$"`,
		`cp "$staged" "$candidate_copy"`,
		`mv -f "$candidate_copy" "$installed"`,
	} {
		if !strings.Contains(installFunction, required) {
			t.Fatalf("Mac candidate install omits stopped-path copy fragment %q", required)
		}
	}
	if strings.Contains(installFunction, "launchctl") {
		t.Fatalf("Mac candidate install must replace only stopped binary paths: %s", installFunction)
	}
	noRollbackGuard := `if [ "$status" -ne 0 ] && [ "$mac_recover_stalled_mode" -eq 1 ] && [ "$mac_recovery_candidate_boundary" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ]; then`
	if !strings.Contains(text, noRollbackGuard) {
		t.Fatal("Mac stalled recovery lacks the candidate-boundary no-rollback exit guard")
	}
	unsafeRestoreBranch := `elif [ "$status" -ne 0 ] && [ "$restore_prior_agents_on_failure" -eq 1 ] && [ "$candidate_activation_started" -eq 0 ] && [ "$mac_recovery_candidate_boundary" -eq 0 ] && [ "$mac_recovery_old_process_boundary_confirmed" -eq 0 ]; then`
	unsafeRestoreIndex := strings.Index(text, unsafeRestoreBranch)
	if unsafeRestoreIndex < 0 {
		t.Fatal("installer lacks the no-duplicate-process rollback boundary")
	}
	unsafeRestoreTail := text[unsafeRestoreIndex:]
	if next := strings.Index(unsafeRestoreTail, "\nelif "); next > 0 {
		unsafeRestoreTail = unsafeRestoreTail[:next]
	}
	if strings.Contains(unsafeRestoreTail, "\trestore_prior_agents") {
		t.Fatalf("unproven old process boundary must not restore old LaunchAgents: %s", unsafeRestoreTail)
	}
}

func TestBUG013InstallerStalledRecoveryProcessBoundaryFailsClosed(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	script := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "agent_pid_presence() {")
	end := strings.Index(text[start:], "\nwait_for_absent_path() {")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate stalled-recovery process-boundary helpers")
	}
	helpers := text[start : start+end]
	run := func(presence, pid string) ([]byte, error) {
		// Override only the small presence observation in this hermetic shell.
		// The production helper remains in the extracted text, while the real
		// /bin/ps call proves a claimed-live PID cannot pass on an empty state.
		harness := "set -eu\nsleep() { :; }\n" + helpers + "\nagent_pid_presence() { printf '%s\\n' '" + presence + "'; }\nwait_for_inert_or_absent_agent_pid '" + pid + "' test-agent\n"
		return exec.Command("sh", "-c", harness).CombinedOutput()
	}

	if output, err := run("absent", "1"); err != nil {
		t.Fatalf("absent process boundary error=%v output=%s", err, output)
	}
	if output, err := run("present", "2147483647"); err == nil || !strings.Contains(string(output), "Could not inspect a still-present LaunchAgent process state") {
		t.Fatalf("uninspectable present process error=%v output=%s", err, output)
	}
	if output, err := run("present", strconv.Itoa(os.Getpid())); err == nil || (!strings.Contains(string(output), "LaunchAgent process remained live after quiescence") && !strings.Contains(string(output), "Could not inspect a still-present LaunchAgent process state")) {
		t.Fatalf("live process boundary error=%v output=%s", err, output)
	}
}

// The installer must have rollback intent set before, rather than after, each
// launchctl call that can change the old executor's state. This harness sends
// HUP, INT, or TERM from a fake launchctl immediately after a successful
// disable or SIGSTOP. It proves the real EXIT-trap restoration route sees the
// intent and re-enables/kickstarts the old test label instead of stranding it.
func TestBUG011ControlledRestartSuspendRestoresAfterSignalAtEachLaunchctlBoundary(t *testing.T) {
	repositoryRoot := p154RepositoryRoot(t)
	script := filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "controlled_restart_suspend_locald() {")
	end := strings.Index(text, "# A prior local executor may own")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not isolate controlled restart suspend/restore functions: start=%d end=%d", start, end)
	}
	functions := text[start:end]

	for _, boundary := range []struct {
		name            string
		stage           string
		expectKickstart bool
	}{
		{name: "after_disable", stage: "disable"},
		{name: "after_sigstop", stage: "sigstop", expectKickstart: true},
	} {
		for _, signalName := range []string{"HUP", "INT", "TERM"} {
			t.Run(boundary.name+"_"+strings.ToLower(signalName), func(t *testing.T) {
				root := t.TempDir()
				bin := filepath.Join(root, "bin")
				agents := filepath.Join(root, "agents")
				if err := os.Mkdir(bin, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(agents, 0o700); err != nil {
					t.Fatal(err)
				}
				const label = "com.remote-session-runner.locald"
				if err := os.WriteFile(filepath.Join(agents, label+".plist"), []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				logPath := filepath.Join(root, "launchctl.log")
				fakeLaunchctl := filepath.Join(bin, "launchctl")
				fake := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$RSR_B011_LAUNCHCTL_LOG"
case "${1:-}" in
disable)
	if [ "$RSR_B011_LAUNCHCTL_SIGNAL_STAGE" = disable ]; then
		kill -"$RSR_B011_LAUNCHCTL_SIGNAL" "$PPID"
	fi
	;;
kill)
	if [ "${2:-}" = SIGSTOP ] && [ "$RSR_B011_LAUNCHCTL_SIGNAL_STAGE" = sigstop ]; then
		kill -"$RSR_B011_LAUNCHCTL_SIGNAL" "$PPID"
	fi
	;;
esac
exit 0
`
				if err := os.WriteFile(fakeLaunchctl, []byte(fake), 0o700); err != nil {
					t.Fatal(err)
				}
				harness := "set -eu\n" +
					"uid=501\n" +
					"locald_was_loaded=1\n" +
					"launch_agents=$1\n" +
					"staging_directory=$2\n" +
					"config_file=$3\n" +
					"repo_root=$4\n" +
					"controlled_locald_disabled=0\n" +
					"controlled_locald_suspended=0\n" +
					"controlled_restart_plan_prepared=0\n" +
					"controlled_restart_prepare_invoked=0\n" +
					"controlled_restart_old_agents_restore_allowed=1\n" +
					"candidate_activation_started=0\n" +
					"restore_prior_agents_on_failure=1\n" +
					"service_loaded() { launchctl print \"gui/$uid/$1\" >/dev/null 2>&1; }\n" +
					functions + "\n" +
					"on_exit() { status=$?; if [ \"$status\" -ne 0 ] && [ \"$restore_prior_agents_on_failure\" -eq 1 ] && [ \"$candidate_activation_started\" -eq 0 ]; then controlled_restart_restore_suspended_locald; fi; trap - EXIT; exit \"$status\"; }\n" +
					"trap on_exit EXIT\n" +
					"trap 'exit 129' HUP\n" +
					"trap 'exit 130' INT\n" +
					"trap 'exit 143' TERM\n" +
					"controlled_restart_suspend_locald\n" +
					"printf '%s\\n' after-suspend-sentinel\n"
				command := exec.Command("sh", "-c", harness, "bug011-suspend-signal", agents, root, filepath.Join(root, "mac.yaml"), repositoryRoot)
				command.Env = append(os.Environ(),
					"PATH="+bin+":"+os.Getenv("PATH"),
					"RSR_B011_LAUNCHCTL_LOG="+logPath,
					"RSR_B011_LAUNCHCTL_SIGNAL_STAGE="+boundary.stage,
					"RSR_B011_LAUNCHCTL_SIGNAL="+signalName,
				)
				output, runErr := command.CombinedOutput()
				if runErr == nil || strings.Contains(string(output), "after-suspend-sentinel") {
					t.Fatalf("signal boundary harness err=%v output=%s, want interrupted before sentinel", runErr, output)
				}
				log, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				logText := string(log)
				if !strings.Contains(logText, "disable gui/501/"+label) || !strings.Contains(logText, "enable gui/501/"+label) {
					t.Fatalf("signal boundary=%s signal=%s launchctl log=%q, want disable then rollback enable", boundary.stage, signalName, logText)
				}
				if boundary.expectKickstart {
					if !strings.Contains(logText, "kill SIGSTOP gui/501/"+label) || !strings.Contains(logText, "kickstart -k gui/501/"+label) {
						t.Fatalf("SIGSTOP boundary signal=%s launchctl log=%q, want frozen old label kickstarted", signalName, logText)
					}
				} else if strings.Contains(logText, "kickstart -k gui/501/"+label) {
					t.Fatalf("disable boundary signal=%s launchctl log=%q, should not kickstart before SIGSTOP", signalName, logText)
				}
			})
		}
	}
}

type p154MailboxHarness struct {
	now          time.Time
	databasePath string
	authority    *store.AuthorityStore
	owner        domain.ControllerIdentity
	api          *localapi.Server
	service      *Service
}

func newP154MailboxHarness(t *testing.T) *p154MailboxHarness {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "state", "authority.db")
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &p154MailboxHarness{now: time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC), databasePath: databasePath}
	h.authority, err = store.NewAuthorityStoreWithClock(db, func() time.Time { return h.now })
	if err != nil {
		t.Fatal(err)
	}
	ownerID, err := domain.NewControllerID(config.MacAccount)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	api, err := localapi.NewServer(localapi.ServerOptions{
		Authority: h.authority, Owner: owner, SocketPath: filepath.Join(root, "run", "local-api.sock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close(context.Background()) })
	definitions := []config.MailboxDefinition{
		{ID: store.DefaultMailboxID, Root: filepath.Join(root, "mailbox"), DurableOrphanCleanup: true},
		{ID: "analytics", Root: filepath.Join(root, "mailboxes", "analytics"), DurableOrphanCleanup: true},
	}
	runtimes, err := composeMailboxRuntimes(definitions, h.authority, owner, api, p154Resolver{}, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h.owner = owner
	h.api = api
	h.service = &Service{database: h.authority, mailboxes: runtimes}
	return h
}

func (h *p154MailboxHarness) createTerminalCommand(t *testing.T) domain.CommandID {
	t.Helper()
	ctx := context.Background()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	const createRequestID = "req-p154-terminal-create"
	const createKey = "key-p154-terminal-create"
	createRaw, err := json.Marshal(map[string]any{
		"request_id": createRequestID, "idempotency_key": createKey,
		"operation": "create_session", "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source":           map[string]string{"mode": "empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := h.api.CreateSessionIntent(ctx, mailbox.Request{
		MailboxID:               store.DefaultMailboxID,
		RequestID:               createRequestID,
		IdempotencyKey:          createKey,
		ExecutionIdempotencyKey: "p154-terminal-create",
		Operation:               "create_session",
		RawJSON:                 createRaw,
	})
	if err != nil || created.SessionID == "" {
		t.Fatalf("create terminal session intent=%+v err=%v", created, err)
	}
	sessionID, err := domain.NewSessionID(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err := h.authority.GetLocalIntentByResource(ctx, "create_session", created.SessionID, h.owner)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err = h.authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentDispatching, "p154-terminal-create-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, createIntent.IntentID, store.LocalIntentAccepted, "p154-terminal-create-accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.CreateSession(ctx, store.SessionCreate{
		SessionID: sessionID, Target: target, Environment: "mac-dev", Controller: h.owner,
		Source: domain.NewEmptySource(), Limits: domain.EffectiveSessionLimits{
			CommandTimeout: 30 * time.Minute, IdleTimeout: 30 * time.Minute,
			SessionMaxLifetime: 4 * time.Hour, OutputBytesPerCommand: 100 << 20,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionSession(ctx, sessionID, domain.SessionStateReady, "p154-terminal-ready"); err != nil {
		t.Fatal(err)
	}
	const submitRequestID = "req-p154-terminal-submit"
	const submitKey = "key-p154-terminal-submit"
	submitRaw, err := json.Marshal(map[string]any{
		"request_id": submitRequestID, "idempotency_key": submitKey,
		"operation": "submit_command", "session_id": string(sessionID),
		"script": "printf P154_TERMINAL_OUTPUT", "timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := h.api.SubmitCommandIntent(ctx, mailbox.Request{
		MailboxID:               store.DefaultMailboxID,
		RequestID:               submitRequestID,
		IdempotencyKey:          submitKey,
		ExecutionIdempotencyKey: "p154-terminal-submit",
		Operation:               "submit_command",
		SessionID:               string(sessionID),
		RawJSON:                 submitRaw,
	})
	if err != nil || submitted.CommandID == "" {
		t.Fatalf("submit terminal command intent=%+v err=%v", submitted, err)
	}
	submitIntent, err := h.authority.GetLocalIntentByResource(ctx, "submit_command", submitted.CommandID, h.owner)
	if err != nil {
		t.Fatal(err)
	}
	submitIntent, err = h.authority.TransitionLocalIntent(ctx, submitIntent.IntentID, store.LocalIntentDispatching, "p154-terminal-submit-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionLocalIntent(ctx, submitIntent.IntentID, store.LocalIntentAccepted, "p154-terminal-submit-accepted"); err != nil {
		t.Fatal(err)
	}
	command, _, err := h.authority.AcceptCommand(ctx, store.CommandAcceptance{
		CommandID: submitIntent.CommandID, SessionID: sessionID, RequestHash: submitIntent.RequestHash,
		IdempotencyKey: submitIntent.IdempotencyKey, Script: string(submitIntent.ScriptBytes),
		Timeout: 30 * time.Second, IntentOrdinal: *submitIntent.IntentOrdinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	output := []byte("P154_TERMINAL_OUTPUT\n")
	if _, err := h.authority.AppendCommandEvent(ctx, store.CommandEventAppend{CommandID: command.CommandID, Type: "stdout", Payload: output, ByteCount: int64(len(output))}); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	if _, err := h.authority.TransitionCommand(ctx, store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: &exitCode, OutputComplete: true}); err != nil {
		t.Fatal(err)
	}
	return command.CommandID
}

type p154Resolver struct{}

type p154NoopCloser struct{}

func (p154NoopCloser) Close() error { return nil }

type p154NoopIntentAcceptor struct{}

func (p154NoopIntentAcceptor) AcceptIntent(context.Context, dispatcher.AcceptIntentRequest) (dispatcher.IntentAcceptance, error) {
	return dispatcher.IntentAcceptance{}, errors.New("P154 test acceptor should not receive work")
}

func (p154Resolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error) {
	if environmentPresent || targetPresent || (mailboxID != store.DefaultMailboxID && mailboxID != "analytics") {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotFound
	}
	local, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		return config.MailboxExecutionSelection{}, err
	}
	return config.MailboxExecutionSelection{
		MailboxID: mailboxID, ContextName: "mac-local", Environment: "mac-dev", Target: local,
		Source: config.MailboxExecutionSelectionSourceInboxDefault,
	}, nil
}

type p154Response struct {
	InboxID                string `json:"inbox_id"`
	RequestState           string `json:"request_state"`
	CommandID              string `json:"command_id"`
	ResponseRevision       int64  `json:"response_revision"`
	AvailableEventSequence *int64 `json:"available_event_sequence"`
}

func p154WriteMailboxRequest(t *testing.T, importer *mailbox.Importer, requestID string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{requestID + mailbox.RequestSuffix: raw, requestID + mailbox.ReadySuffix: nil} {
		path := filepath.Join(importer.InboxPath(), name)
		if err := os.WriteFile(path, contents, mailbox.MailboxFileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mailbox.MailboxFileMode); err != nil {
			t.Fatal(err)
		}
	}
}

func p154ReadResponse(t *testing.T, runtime mailboxRuntime, requestID string) (p154Response, string) {
	t.Helper()
	response, path := p154ReadResponsePath(runtime, requestID)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response, path
}

func p154ReadResponsePath(runtime mailboxRuntime, requestID string) (p154Response, string) {
	root := runtime.importer.InboxPath()
	return p154Response{}, filepath.Join(filepath.Dir(root), "outbox", requestID+mailbox.RequestSuffix)
}

func p154WriteAck(t *testing.T, runtime mailboxRuntime, requestID string, revision int64, cursor *int64) {
	t.Helper()
	value := map[string]any{"request_id": requestID, "response_revision": revision, "available_event_sequence": cursor}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{requestID + mailbox.RequestSuffix: raw, requestID + mailbox.ReadySuffix: nil} {
		path := filepath.Join(runtime.ackImporter.AcksPath(), name)
		if err := os.WriteFile(path, contents, mailbox.MailboxFileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mailbox.MailboxFileMode); err != nil {
			t.Fatal(err)
		}
	}
}

func assertP154PathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q remains or cannot be inspected: %v", path, err)
	}
}

func assertP154PathPresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("path %q is missing: %v", path, err)
	}
}

func p154RepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P154 test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
}

func p154CopyOwnerOnlyConfig(t *testing.T, source string) string {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mac.yaml")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func p154MacSettings(root string) config.MacSettings {
	return config.MacSettings{
		ServiceRoot:    root,
		APISocket:      filepath.Join(root, "run", "local-api.sock"),
		LocalDSocket:   filepath.Join(root, "run", "locald.sock"),
		Database:       filepath.Join(root, "state", "local.db"),
		Workspaces:     filepath.Join(root, "workspaces"),
		ScriptTempRoot: filepath.Join(root, "tmp", "scripts"),
		Backups:        filepath.Join(root, "backups"),
	}
}
