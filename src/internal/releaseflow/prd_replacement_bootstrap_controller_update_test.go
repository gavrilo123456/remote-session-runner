package releaseflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

func TestControllerAmendmentCanResumeAfterLinuxNamespaceReady(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	release := testRelease()
	now := time.Now().UTC().Truncate(time.Second)
	predecessors := make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		predecessors[host] = digest([]byte("predecessor:" + host))
	}
	request := laneinstall.PRDReplacementBootstrapRequest{
		OperationID: prdBootstrapControllerUpdateOperation, TemplateHostID: "sandbox-host",
		ReleaseID: release.ID, ExpectedReleaseSHA256: release.Digest,
		SSHHostPublicKey: releaseflowTestPublicKey("replacement-bootstrap-host"),
		PublishersPaused: true, NoNewAccepted: true, PublisherOwner: "Tomasz Walczuk", PublisherControl: "owner-confirmed clients paused",
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	profileDir := t.TempDir()
	identityPath := filepath.Join(profileDir, "linux.key")
	knownHostsPath := filepath.Join(profileDir, "known_hosts")
	if err := os.WriteFile(identityPath, []byte("private identity fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	knownHost := []byte("79.72.94.59 " + request.SSHHostPublicKey + "\n")
	if err := os.WriteFile(knownHostsPath, knownHost, 0o600); err != nil {
		t.Fatal(err)
	}
	profile := HostProfile{HostID: "linux-host", Target: replacementLinuxTarget, IdentityFile: identityPath,
		KnownHostsFile: knownHostsPath, AllowFirstInstallPeerAbsence: true}
	native := &NativePromotionBackend{Config: HostConfiguration{Hosts: map[string]HostProfile{"linux-host": profile}},
		ControllerRevision: strings.Repeat("b", 40)}
	previous := *native
	previous.ControllerRevision = prdBootstrapPreviousController
	prdBootstrapHostKeysForTests = func(host, path string) ([]byte, error) {
		if host != "79.72.94.59" || path != knownHostsPath {
			t.Fatalf("host key lookup escaped the replacement profile: host=%q path=%q", host, path)
		}
		return knownHost, nil
	}
	t.Cleanup(func() { prdBootstrapHostKeysForTests = nil })
	previousHostSHA, err := replacementBootstrapHostControlSHA(&previous, profile, request.SSHHostPublicKey, knownHost)
	if err != nil {
		t.Fatal(err)
	}
	path, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	approval := Approval{FormatVersion: 1, ReleaseID: release.ID, ReleaseSHA256: release.Digest,
		HostSet: append([]string(nil), HostOrder...), WindowStart: now.Add(-time.Minute).Format(time.RFC3339),
		WindowEnd: now.Add(time.Hour).Format(time.RFC3339), Owner: "Tomasz Walczuk", Scope: "replacement PRD bootstrap",
		PredecessorBundleSHA256: predecessors, BackupRecoveryPackageSHA256: digest([]byte("backup package"))}
	approvalSHA := digest([]byte("exact approval bytes"))
	journal := PRDReplacementBootstrapCoordinatorJournal{
		FormatVersion: prdReplacementCoordinatorJournalVersion, OperationID: request.OperationID,
		ReleaseID: release.ID, ReleaseSHA256: release.Digest, RequestSHA256: digest(requestBytes),
		ApprovalSHA256: approvalSHA, PlanSHA256: digest([]byte("frozen plan")), HostControlSHA256: previousHostSHA,
		PredecessorBundles: predecessors, BackupPackageSHA256: approval.BackupRecoveryPackageSHA256,
		PublisherOwner: request.PublisherOwner, PublisherControl: request.PublisherControl,
		CreatedAtUTC: now.Format(time.RFC3339Nano), UpdatedAtUTC: now.Format(time.RFC3339Nano),
		CompletedStages:  append([]string(nil), prdReplacementBootstrapStages[:4]...),
		CSRRequestSHA256: digest([]byte("CSR")), ServerCertSHA256: digest([]byte("certificate")),
		ServerCASHA256: digest([]byte("CA")), LinuxJournalSHA256: digest([]byte("completed Linux namespace journal")),
	}
	if err := writePRDReplacementBootstrapCoordinatorJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	update, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, approvalSHA, request.PublisherOwner, request.PublisherControl, now.Add(time.Second))
	if err != nil {
		t.Fatalf("amend controller at exact linux_namespace_ready checkpoint: %v", err)
	}
	if update.PreviousControllerRevision != prdBootstrapPreviousController || update.PreviousHostControlSHA256 != previousHostSHA ||
		update.ControllerRevision != native.ControllerRevision || update.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(journal) {
		t.Fatalf("amendment did not preserve exact predecessor/controller/coordinator bindings: %+v", update)
	}
	retry, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, approvalSHA, request.PublisherOwner, request.PublisherControl, now.Add(2*time.Second))
	if err != nil || !samePRDReplacementBootstrapControllerUpdate(update, retry) {
		t.Fatalf("interrupted amendment retry was not idempotent: update=%+v retry=%+v err=%v", update, retry, err)
	}
	if _, found, err := readPRDReplacementBootstrapControllerUpdate(prdReplacementBootstrapControllerUpdatePath(path)); err != nil || !found {
		t.Fatalf("read durable controller amendment: %v", err)
	}

	// The amendment can be retried only while every non-controller binding is
	// unchanged. A changed SSH identity invalidates reconstruction of the
	// original host-control hash and cannot produce a successor record.
	if err := os.WriteFile(identityPath, []byte("changed SSH identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	native.ControllerRevision = strings.Repeat("c", 40)
	if _, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, approvalSHA, request.PublisherOwner, request.PublisherControl, now.Add(3*time.Second)); err == nil {
		t.Fatal("controller amendment accepted changed non-controller SSH identity")
	}
	chain, found, err := readPRDReplacementBootstrapControllerUpdateChain(path, journal)
	if err != nil || !found || len(chain) != 1 || chain[0].ControllerRevision != update.ControllerRevision {
		t.Fatalf("rejected conflicting amendment changed history: found=%t chain=%+v err=%v", found, chain, err)
	}
}

func TestControllerAmendmentCheckpointRejectsMacRouteAndSkippedStages(t *testing.T) {
	base := PRDReplacementBootstrapCoordinatorJournal{
		CSRRequestSHA256: digest([]byte("CSR")), ServerCertSHA256: digest([]byte("certificate")),
		ServerCASHA256: digest([]byte("CA")), LinuxJournalSHA256: digest([]byte("Linux journal")),
	}
	cases := []struct {
		name   string
		stages []string
		linux  string
		mac    string
		want   bool
	}{
		{name: "server material ready", stages: prdReplacementBootstrapStages[:3], want: true},
		{name: "Linux namespace ready", stages: prdReplacementBootstrapStages[:4], linux: base.LinuxJournalSHA256, want: true},
		{name: "unexpected Linux journal before namespace stage", stages: prdReplacementBootstrapStages[:3], linux: base.LinuxJournalSHA256},
		{name: "missing Linux journal", stages: prdReplacementBootstrapStages[:4]},
		{name: "Mac route already started", stages: prdReplacementBootstrapStages[:4], linux: base.LinuxJournalSHA256, mac: digest([]byte("Mac journal"))},
		{name: "Mac route complete", stages: prdReplacementBootstrapStages, linux: base.LinuxJournalSHA256, mac: digest([]byte("Mac journal"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			journal := base
			journal.CompletedStages = append([]string(nil), tc.stages...)
			journal.LinuxJournalSHA256 = tc.linux
			journal.MacJournalSHA256 = tc.mac
			if got := prdReplacementBootstrapControllerUpdateCheckpointAllowed(journal); got != tc.want {
				t.Fatalf("checkpoint allowed=%t, want %t", got, tc.want)
			}
		})
	}
}
