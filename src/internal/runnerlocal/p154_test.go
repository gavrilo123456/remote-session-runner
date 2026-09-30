package runnerlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	activationBoundary := "candidate_activation_started=1"
	configHandoff := `mv -f "$config_stage" "$config_file"`
	handoffComplete := "candidate_config_handed_off=1"
	recoveryGuard := `if [ "$candidate_activation_started" -eq 1 ] && [ "$candidate_config_handed_off" -eq 0 ]; then`
	if !strings.Contains(text, "ensure_private_service_directory") || !strings.Contains(text, "ensure_launch_agents_directory") ||
		!strings.Contains(text, staticValidate) || !strings.Contains(text, finalValidate) ||
		!strings.Contains(text, checkMailboxDirectories) ||
		!strings.Contains(text, activateMailboxSet) ||
		!strings.Contains(text, quiesceLocal) || !strings.Contains(text, quiesceLocalD) ||
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
	localDIndex := strings.Index(text, quiesceLocalD)
	finalIndex := strings.Index(text, finalValidate)
	activationIndex := strings.Index(text, activationBoundary)
	activateMailboxSetIndex := strings.Index(text, activateMailboxSet)
	configHandoffIndex := strings.Index(text, configHandoff)
	handoffCompleteIndex := strings.LastIndex(text, handoffComplete)
	recoveryGuardIndex := strings.Index(text, recoveryGuard)
	replacementLoop := strings.LastIndex(text, "for name in com.remote-session-runner.locald com.remote-session-runner.local; do")
	if replacementLoop < 0 {
		t.Fatal("installer lacks LaunchAgent replacement loop")
	}
	bootstrapIndex := replacementLoop + strings.Index(text[replacementLoop:], "launchctl bootstrap")
	if staticIndex < 0 || checkPathsIndex < 0 || localIndex < 0 || localDIndex < 0 || finalIndex < 0 || activationIndex < 0 || activateMailboxSetIndex < 0 || configHandoffIndex < 0 || handoffCompleteIndex < 0 || recoveryGuardIndex < 0 || bootstrapIndex < replacementLoop {
		t.Fatalf("installer sequence indexes static=%d check_paths=%d local=%d locald=%d final=%d activation=%d activate_mailboxes=%d handoff=%d handoff_complete=%d recovery_guard=%d bootstrap=%d", staticIndex, checkPathsIndex, localIndex, localDIndex, finalIndex, activationIndex, activateMailboxSetIndex, configHandoffIndex, handoffCompleteIndex, recoveryGuardIndex, bootstrapIndex)
	}
	if !(staticIndex < checkPathsIndex && checkPathsIndex < localIndex && localIndex < localDIndex && localDIndex < finalIndex && finalIndex < activationIndex && activationIndex < activateMailboxSetIndex && activateMailboxSetIndex < configHandoffIndex && configHandoffIndex < handoffCompleteIndex && handoffCompleteIndex < bootstrapIndex && recoveryGuardIndex < configHandoffIndex) {
		t.Fatalf("installer must statically validate, check mailbox paths without creating them, quiesce local then locald, validate retained ingress, cross activation boundary, register candidate inboxes and create any missing tree, preserve pre-handoff recovery config, hand off config, then bootstrap: static=%d check_paths=%d local=%d locald=%d final=%d activation=%d activate_mailboxes=%d handoff=%d handoff_complete=%d recovery_guard=%d bootstrap=%d", staticIndex, checkPathsIndex, localIndex, localDIndex, finalIndex, activationIndex, activateMailboxSetIndex, configHandoffIndex, handoffCompleteIndex, recoveryGuardIndex, bootstrapIndex)
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
		{ID: store.DefaultMailboxID, Root: filepath.Join(root, "mailbox")},
		{ID: "analytics", Root: filepath.Join(root, "mailboxes", "analytics")},
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
