package runnerlocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/store"
)

func TestP158ExternalMailboxPreflightIsNonMutatingAndPreparationCreatesOnlyLeaf(t *testing.T) {
	trustRoot := t.TempDir()
	project := filepath.Join(trustRoot, "slidestud.io")
	parent := filepath.Join(project, "tmp")
	for _, path := range []string{project, parent} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mailboxRoot := filepath.Join(parent, "mailbox-")

	if err := validateExternalMailboxTreeUnder(trustRoot, mailboxRoot); err != nil {
		t.Fatalf("non-mutating preflight: %v", err)
	}
	p158RequireAbsent(t, mailboxRoot)
	for _, path := range []string{project, parent} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("external ancestor changed during preflight: path=%s info=%v err=%v", path, info, err)
		}
	}

	if err := prepareExternalMailboxTreeUnder(trustRoot, mailboxRoot); err != nil {
		t.Fatalf("post-registration preparation: %v", err)
	}
	for _, path := range []string{project, parent} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("external ancestor changed during preparation: path=%s info=%v err=%v", path, info, err)
		}
	}
	for _, path := range mailboxTreePaths(mailboxRoot) {
		p158RequireOwnerOnlyDirectory(t, path)
	}
}

func TestP158ExternalMailboxPreparationClearsInheritedSetgidBit(t *testing.T) {
	trustRoot := t.TempDir()
	parent := filepath.Join(trustRoot, "setgid-parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o2755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetgid == 0 {
		t.Skip("filesystem does not retain a setgid directory bit")
	}

	mailboxRoot := filepath.Join(parent, "mailbox")
	if err := prepareExternalMailboxTreeUnder(trustRoot, mailboxRoot); err != nil {
		t.Fatalf("prepare under setgid parent: %v", err)
	}
	for _, path := range mailboxTreePaths(mailboxRoot) {
		p158RequireOwnerOnlyDirectory(t, path)
	}
}

func TestP158ExternalMailboxPreflightRejectsUnsafeOrMissingPathsWithoutMutation(t *testing.T) {
	t.Run("missing parent", func(t *testing.T) {
		trustRoot := t.TempDir()
		root := filepath.Join(trustRoot, "missing", "mailbox")
		p158RequireRejectedWithoutMailboxMutation(t, trustRoot, root)
	})

	t.Run("outside trusted root", func(t *testing.T) {
		trustRoot := t.TempDir()
		root := filepath.Join(t.TempDir(), "mailbox")
		p158RequireRejectedWithoutMailboxMutation(t, trustRoot, root)
	})

	t.Run("group writable ancestor", func(t *testing.T) {
		trustRoot := t.TempDir()
		parent := filepath.Join(trustRoot, "shared")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o775); err != nil {
			t.Fatal(err)
		}
		p158RequireRejectedWithoutMailboxMutation(t, trustRoot, filepath.Join(parent, "mailbox"))
	})

	t.Run("symlinked ancestor", func(t *testing.T) {
		trustRoot := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(trustRoot, "linked")); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(trustRoot, "linked", "mailbox")
		if err := validateExternalMailboxTreeUnder(trustRoot, root); err == nil {
			t.Fatal("preflight accepted a symlinked external ancestor")
		}
		if err := prepareExternalMailboxTreeUnder(trustRoot, root); err == nil {
			t.Fatal("preparation accepted a symlinked external ancestor")
		}
		p158RequireAbsent(t, filepath.Join(outside, "mailbox"))
	})

	t.Run("symlinked root", func(t *testing.T) {
		trustRoot := t.TempDir()
		outside := t.TempDir()
		root := filepath.Join(trustRoot, "mailbox")
		if err := os.Symlink(outside, root); err != nil {
			t.Fatal(err)
		}
		if err := validateExternalMailboxTreeUnder(trustRoot, root); err == nil {
			t.Fatal("preflight accepted a symlinked external mailbox root")
		}
		if err := prepareExternalMailboxTreeUnder(trustRoot, root); err == nil {
			t.Fatal("preparation accepted a symlinked external mailbox root")
		}
	})
}

func TestP158ExistingExternalMailboxDirectoriesMustAlreadyBeSafe(t *testing.T) {
	trustRoot := t.TempDir()
	root := filepath.Join(trustRoot, "mailbox")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalMailboxTreeUnder(trustRoot, root); err == nil {
		t.Fatal("preflight accepted a pre-existing mode-0755 mailbox root")
	}
	if err := prepareExternalMailboxTreeUnder(trustRoot, root); err == nil {
		t.Fatal("preparation accepted a pre-existing mode-0755 mailbox root")
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unsafe existing root was changed: info=%v err=%v", info, err)
	}

	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	diagnostics := filepath.Join(root, "diagnostics")
	if err := os.Mkdir(diagnostics, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(diagnostics, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalMailboxTreeUnder(trustRoot, root); err == nil {
		t.Fatal("preflight accepted an unsafe existing child")
	}
	if err := prepareExternalMailboxTreeUnder(trustRoot, root); err == nil {
		t.Fatal("preparation accepted an unsafe existing child")
	}
	info, err = os.Lstat(diagnostics)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unsafe existing child was changed: info=%v err=%v", info, err)
	}
}

func TestP158ActivationRegistersCandidateBeforeMailboxPreparation(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	if err := os.Mkdir(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	externalRoot := filepath.Join(root, "external", "mailbox")
	definitions := []config.MailboxDefinition{
		{ID: store.DefaultMailboxID, Root: filepath.Join(config.MacServiceRoot, "mailbox")},
		{ID: "slidestud-io", Root: externalRoot},
	}
	preparationFailure := errors.New("test mailbox preparation failure")
	called := false
	err := activateMailboxSetAtBoundary(context.Background(), databasePath, definitions, func() error {
		called = true
		return preparationFailure
	})
	if !called || !errors.Is(err, errMailboxTreePreparationAfterActivate) || !errors.Is(err, preparationFailure) {
		t.Fatalf("activation error=%v called=%t", err, called)
	}
	if err := store.ValidateConfiguredMailboxSetAtPath(context.Background(), databasePath,
		configuredMailboxDefinitions(definitions), legacyDefaultMailboxBaseline()); err != nil {
		t.Fatalf("registered candidate was not retained after preparation failure: %v", err)
	}
	if err := store.ValidateConfiguredMailboxSetAtPath(context.Background(), databasePath,
		legacyDefaultMailboxBaseline(), legacyDefaultMailboxBaseline()); !errors.Is(err, store.ErrMailboxConfigurationPending) {
		t.Fatalf("candidate registration did not block an old configuration: %v", err)
	}
	p158RequireAbsent(t, externalRoot)
}

func TestP158ActivationRegistersBeforeRealExternalTreePreparation(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", "authority.db")
	if err := os.Mkdir(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "project", "tmp")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	externalRoot := filepath.Join(parent, "mailbox")
	definitions := []config.MailboxDefinition{
		{ID: store.DefaultMailboxID, Root: filepath.Join(config.MacServiceRoot, "mailbox")},
		{ID: "slidestud-io", Root: externalRoot},
	}
	if err := activateMailboxSetAtBoundary(context.Background(), databasePath, definitions, func() error {
		if err := store.ValidateConfiguredMailboxSetAtPath(context.Background(), databasePath,
			configuredMailboxDefinitions(definitions), legacyDefaultMailboxBaseline()); err != nil {
			return fmt.Errorf("candidate was not durable before preparation: %w", err)
		}
		if _, err := os.Lstat(externalRoot); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("external root was visible before preparation: %w", err)
		}
		return prepareExternalMailboxTreeUnder(root, externalRoot)
	}); err != nil {
		t.Fatalf("activate candidate with real preparation: %v", err)
	}
	for _, path := range mailboxTreePaths(externalRoot) {
		p158RequireOwnerOnlyDirectory(t, path)
	}
}

func TestP158ListenerConflictLeavesCandidateRegistryUntouched(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	runDirectory, err := os.MkdirTemp("/tmp", "rsr-p158-listen-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDirectory) })
	if err := os.Chmod(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runDirectory, "local-api.sock")
	blockingAPI, err := localapi.NewServer(localapi.ServerOptions{
		Authority: h.authority, Owner: h.owner, SocketPath: socketPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := blockingAPI.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blockingAPI.Close(context.Background()) })
	candidateAPI, err := localapi.NewServer(localapi.ServerOptions{
		Authority: h.authority, Owner: h.owner, SocketPath: socketPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = candidateAPI.Close(context.Background()) })
	localDriver, err := dispatcher.NewLocalDriver(h.authority, p154NoopIntentAcceptor{}, "p158-listener-conflict", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	remoteResolver, err := dispatcher.NewRemoteCallerResolver(map[string]dispatcher.RemoteCaller{})
	if err != nil {
		t.Fatal(err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriverWithResolver(h.authority, remoteResolver, "p158-listener-conflict", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defaultDefinition := config.MailboxDefinition{ID: store.DefaultMailboxID, Root: filepath.Dir(h.service.mailboxes[0].importer.InboxPath())}
	externalRoot := filepath.Join(t.TempDir(), "external", "mailbox")
	candidateDefinitions := []config.MailboxDefinition{defaultDefinition, {ID: "slidestud-io", Root: externalRoot}}
	candidate := &Service{
		database: h.authority, dbCloser: p154NoopCloser{}, api: candidateAPI,
		localDriver: localDriver, remoteDriver: remoteDriver, mailboxes: h.service.mailboxes,
		mailboxDefinitions: configuredMailboxDefinitions(candidateDefinitions), legacyMailboxSet: configuredMailboxDefinitions([]config.MailboxDefinition{defaultDefinition}),
		pollInterval: time.Hour,
	}
	if err := candidate.Serve(ctx, io.Discard, io.Discard); err == nil {
		t.Fatal("candidate service unexpectedly started while its API socket was owned")
	}
	if err := h.authority.ValidateConfiguredMailboxSet(ctx, configuredMailboxDefinitions([]config.MailboxDefinition{defaultDefinition}), configuredMailboxDefinitions([]config.MailboxDefinition{defaultDefinition})); err != nil {
		t.Fatalf("listener failure registered the candidate mailbox set: %v", err)
	}
	p158RequireAbsent(t, externalRoot)
}

func TestP158DoctorMetricsUseConfiguredRootsWithoutCreatingMissingTree(t *testing.T) {
	ctx := context.Background()
	h := newP154MailboxHarness(t)
	defaultRuntime := h.service.mailboxes[0]
	marker := filepath.Join(defaultRuntime.importer.InboxPath(), "req-p158-metrics.ready")
	request := filepath.Join(defaultRuntime.importer.InboxPath(), "req-p158-metrics.json")
	if err := os.WriteFile(request, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(request, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o600); err != nil {
		t.Fatal(err)
	}
	externalRoot := filepath.Join(t.TempDir(), "missing", "mailbox")
	counts, readyTotal, err := mailboxBacklogByDefinitions(ctx, h.authority, []config.MailboxDefinition{
		{ID: store.DefaultMailboxID, Root: filepath.Dir(defaultRuntime.importer.InboxPath())},
		{ID: "slidestud-io", Root: externalRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	if readyTotal != 1 || counts[store.DefaultMailboxID] != 1 || counts["slidestud-io"] != 0 {
		t.Fatalf("configured mailbox metrics counts=%v ready_total=%d", counts, readyTotal)
	}
	p158RequireAbsent(t, externalRoot)
}

func TestP158DoctorClassifiesUnsafeMailboxPaths(t *testing.T) {
	report := macDoctorStartupFailureReport(fmt.Errorf("%w: external mailbox root is unsafe", errMacMailboxPathsNotReady))
	if report.Readiness != "not_ready" || len(report.Checks) != 1 || report.Checks[0].Component != "mailbox_paths" || report.Checks[0].Reason != "mailbox_paths_unsafe" {
		t.Fatalf("unsafe mailbox doctor report=%+v", report)
	}
}

func p158RequireRejectedWithoutMailboxMutation(t *testing.T, trustRoot, root string) {
	t.Helper()
	if err := validateExternalMailboxTreeUnder(trustRoot, root); err == nil {
		t.Fatal("preflight accepted an unsafe external mailbox path")
	}
	if err := prepareExternalMailboxTreeUnder(trustRoot, root); err == nil {
		t.Fatal("preparation accepted an unsafe external mailbox path")
	}
	p158RequireAbsent(t, root)
}

func p158RequireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected filesystem entry at %s: %v", path, err)
	}
}

func p158RequireOwnerOnlyDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || int(stat.Uid) != os.Geteuid() {
		t.Fatalf("owner-only mailbox directory %s info=%+v", path, info)
	}
}
