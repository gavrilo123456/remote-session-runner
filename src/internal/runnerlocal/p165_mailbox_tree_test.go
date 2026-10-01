package runnerlocal

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/mailbox"
)

func TestP165MailboxTreeIncludesPrivateDiagnosticsDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	paths := mailboxTreePaths(root)
	want := filepath.Join(root, "diagnostics")
	for _, path := range paths {
		if path == want {
			return
		}
	}
	t.Fatalf("mailbox tree paths=%v, missing %s", paths, want)
}

func TestP165ExternalMailboxPreparationCreatesPrivateDiagnosticsDirectory(t *testing.T) {
	trustRoot := t.TempDir()
	parent := filepath.Join(trustRoot, "workspace")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "mailbox")
	if err := prepareExternalMailboxTreeUnder(trustRoot, root); err != nil {
		t.Fatalf("prepare external mailbox tree: %v", err)
	}
	p158RequireOwnerOnlyDirectory(t, filepath.Join(root, "diagnostics"))
}

func TestP165ExternalMailboxValidationRejectsUnsafeDiagnosticsDirectory(t *testing.T) {
	trustRoot := t.TempDir()
	root := filepath.Join(trustRoot, "mailbox")
	if err := os.Mkdir(root, 0o700); err != nil {
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
		t.Fatal("preflight accepted an unsafe diagnostics directory")
	}
	info, err := os.Lstat(diagnostics)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("unsafe diagnostics directory changed: info=%v err=%v", info, err)
	}
}

func TestP165MailboxRuntimeWiresPrivateDiagnosticsCleaner(t *testing.T) {
	harness := newP154MailboxHarness(t)
	for _, runtime := range harness.service.mailboxes {
		if runtime.artifactCleaner.Diagnostics == nil {
			t.Fatalf("mailbox %s has no diagnostics cleaner", runtime.id)
		}
		root := filepath.Dir(runtime.importer.InboxPath())
		p158RequireOwnerOnlyDirectory(t, filepath.Join(root, "diagnostics"))
	}
}

func TestP165MacInstallerCreatesAndChecksPrivateDiagnosticsDirectory(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	installer, err := os.ReadFile(filepath.Join(repositoryRoot, "deploy", "macos", "install-launchagents.sh"))
	if err != nil {
		t.Fatal(err)
	}
	hostGate, err := os.ReadFile(filepath.Join(repositoryRoot, "deploy", "macos", "test-launchagents.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"installer": string(installer),
		"host gate": string(hostGate),
	} {
		if !strings.Contains(content, "$service_root/mailbox/diagnostics") {
			t.Fatalf("%s does not prepare or validate the private diagnostics directory", name)
		}
	}
}

func TestP165MailboxIngressDiagnosticLogUsesOnlyFixedSafeFields(t *testing.T) {
	const secret = "Authorization: Bearer never-log-this"
	var stderr bytes.Buffer
	service := &Service{}
	service.logMailboxIngressDiagnosticEvents(&stderr, "slidestud-io", []mailbox.IngressDiagnosticEvent{
		{
			MailboxID:    "other-mailbox",
			RequestID:    "req-p165-diagnostic",
			FailureClass: "invalid_request_schema",
		},
		{
			MailboxID:    secret,
			RequestID:    "req-p165\n" + secret,
			FailureClass: secret,
		},
	})
	got := stderr.String()
	want := "runner-local: mailbox_ingress_rejected mailbox=slidestud-io request_id=req-p165-diagnostic idempotency_key=unavailable execution_target=not_selected remote_command_id=not_created lifecycle_phase=ingress_validation failure_class=invalid_request_schema\n" +
		"runner-local: mailbox_ingress_rejected mailbox=slidestud-io request_id=unavailable idempotency_key=unavailable execution_target=not_selected remote_command_id=not_created lifecycle_phase=ingress_validation failure_class=recovery_failed\n"
	if got != want {
		t.Fatalf("ingress diagnostic log=%q, want %q", got, want)
	}
	if strings.Contains(got, secret) || strings.Contains(got, "other-mailbox") {
		t.Fatalf("ingress diagnostic log leaked untrusted event data: %q", got)
	}
}
