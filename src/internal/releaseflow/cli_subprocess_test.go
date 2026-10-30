package releaseflow

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
)

func TestPRDReplacementBootstrapCommandUsesPinnedLinuxHostRPCAndRedactsPayload(t *testing.T) {
	workflowStore, err := NewStore(filepath.Join(t.TempDir(), "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prdReplacementBootstrapCoordinatorJournalPath(workflowStore.Root, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("prepare owner-only coordinator journal path: %v", err)
	}
	release := testRelease()
	saveTestRelease(t, workflowStore, release)
	now := time.Now().UTC()
	saveTestAcceptance(t, workflowStore, "dev", release, now)
	saveTestAcceptance(t, workflowStore, "qas", release, now)
	predecessors := make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		predecessors[host] = digest([]byte("previous:" + host))
	}
	absence := testPRDAbsenceReport(t)
	absence.SelectedLane = config.ReleaseLanePRD
	absence.ObservedAtUTC = now.Format(time.RFC3339Nano)
	absence.EvidenceSHA256 = absenceReportDigest(absence)
	predecessors["linux-host"] = promotionPRDAbsencePredecessorSHA(absence)
	plan, err := BuildPlan("prd", "all", release)
	if err != nil {
		t.Fatal(err)
	}
	plan.PredecessorBundleSHA256 = predecessors
	plan.BackupRecoveryPackageSHA = digest([]byte("current prd backup package"))
	for _, host := range HostOrder {
		baseline := HostObservation{HostID: host, Lane: "prd", ServiceStopped: true,
			WorkInventoryEmpty: true, WorkInventorySHA: digest([]byte("empty:" + host)),
			CapacityVerified: true, AvailableBytes: 1_000_000_000, RequiredBytes: promotionRequiredCapacity("prd", defaultPromotionOperatingBuffer)}
		if host == "linux-host" {
			baseline.SelectedNamespaceAbsence = &absence
		} else {
			baseline.ConfigurationSHA, baseline.SchemaVersion = digest([]byte("config:"+host)), 40
		}
		plan.HostBaselines = append(plan.HostBaselines, baseline)
	}
	approval := Approval{FormatVersion: 1, ReleaseID: release.ID, ReleaseSHA256: release.Digest,
		HostSet: append([]string(nil), HostOrder...), WindowStart: now.Add(-time.Minute).Format(time.RFC3339),
		WindowEnd: now.Add(time.Hour).Format(time.RFC3339), Owner: "Tomasz Walczuk", Scope: "replacement PRD bootstrap and cohort",
		PredecessorBundleSHA256: predecessors, BackupRecoveryPackageSHA256: plan.BackupRecoveryPackageSHA}
	approvalBytes, err := json.MarshalIndent(approval, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	approvalPath := filepath.Join(t.TempDir(), "prd-approval.json")
	approvalFileBytes := append(append([]byte(nil), approvalBytes...), '\n')
	if err := os.WriteFile(approvalPath, approvalFileBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	request := laneinstall.PRDReplacementBootstrapRequest{
		OperationID: prdBootstrapControllerUpdateOperation, TemplateHostID: "sandbox-host",
		ReleaseID: release.ID, ExpectedReleaseSHA256: release.Digest,
		SSHHostPublicKey: releaseflowTestPublicKey("replacement-bootstrap-host"),
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "bootstrap.json")
	if err := os.WriteFile(requestPath, requestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	csrCalled := false
	csrCalls := 0
	bootstrapCalls := 0
	interruptFirstBootstrap := true
	csr, err := laneinstall.GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	sharedTrust := newBUG028CLISharedTrust(t, now)
	serverCA := sharedTrust.replacement
	serverCertificate, _, err := laneinstall.SignPRDReplacementServerCSR(csr.RequestPEM, serverCA, sharedTrust.replacementKey, now)
	if err != nil {
		t.Fatal(err)
	}
	expectedApprovalSHA := digest(approvalFileBytes)
	hostFiles := t.TempDir()
	identityFile := filepath.Join(hostFiles, "linux.key")
	knownHostsFile := filepath.Join(hostFiles, "known_hosts")
	if err := os.WriteFile(identityFile, []byte("fixture private identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHostsFile, []byte("79.72.94.59 "+request.SSHHostPublicKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := HostProfile{HostID: "linux-host", Target: replacementLinuxTarget, IdentityFile: identityFile,
		KnownHostsFile: knownHostsFile, AllowFirstInstallPeerAbsence: true}
	backend := &NativePromotionBackend{Config: HostConfiguration{Hosts: map[string]HostProfile{"linux-host": profile}},
		ControllerRevision: prdBootstrapPreviousController,
		laneCommandRunner: func(_ context.Context, host string, args []string, stdin []byte) ([]byte, error) {
			if len(args) == 3 && args[0] == "catalog-prd-replacement-csr" && args[1] == "--operation-id" &&
				args[2] == request.OperationID && host == "linux-host" && len(stdin) == 0 {
				journalPath, pathErr := prdReplacementBootstrapCoordinatorJournalPath(workflowStore.Root, request.OperationID)
				journal, exists, journalErr := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
				expectedStages := 1
				if csrCalls == 1 {
					expectedStages = 3
				} else if csrCalls > 1 {
					expectedStages = len(prdReplacementBootstrapStages)
				}
				if pathErr != nil || journalErr != nil || !exists || len(journal.CompletedStages) != expectedStages {
					t.Fatalf("coordinator intent was not durable before remote CSR creation: exists=%t completed=%v errors=%v/%v", exists, journal.CompletedStages, pathErr, journalErr)
				}
				csrCalled = true
				csrCalls++
				encoded, _ := json.Marshal(laneinstall.PRDReplacementServerCSRReport{OperationID: request.OperationID,
					RequestPEM: csr.RequestPEM, RequestSHA: digest(csr.RequestPEM)})
				return encoded, nil
			}
			bootstrapCalls++
			called = true
			var forwarded laneinstall.PRDReplacementBootstrapRequest
			if json.Unmarshal(stdin, &forwarded) != nil || host != "linux-host" || len(args) != 2 ||
				args[0] != "catalog-prd-replacement-bootstrap" || args[1] != "--request-stdin" ||
				forwarded.OperationID != request.OperationID || forwarded.ReleaseID != request.ReleaseID ||
				forwarded.ExpectedReleaseSHA256 != request.ExpectedReleaseSHA256 ||
				forwarded.SSHHostPublicKey != request.SSHHostPublicKey ||
				forwarded.ServerCSR256 != digest(csr.RequestPEM) ||
				!bytes.Equal(forwarded.ServerCertificatePEM, serverCertificate) || !bytes.Equal(forwarded.ServerCAPEM, serverCA) ||
				forwarded.ApprovalSHA256 != expectedApprovalSHA || forwarded.PublisherOwner != "Tomasz Walczuk" ||
				forwarded.PublisherControl != "clients-held" || !forwarded.PublishersPaused || !forwarded.NoNewAccepted {
				t.Fatalf("bootstrap RPC scope/control payload mismatch: host=%q args=%q bytes=%d", host, args, len(stdin))
			}
			if interruptFirstBootstrap {
				interruptFirstBootstrap = false
				return nil, errors.New("simulated interruption after durable server material")
			}
			result, _ := json.Marshal(laneinstall.PRDReplacementBootstrapResult{
				OperationID: request.OperationID, Stage: "linux_namespace_ready",
				Release:            laneinstall.CatalogReleaseIdentity{ReleaseID: request.ReleaseID, ReleaseSHA256: request.ExpectedReleaseSHA256, HostID: "linux-host", BundleSHA256: release.Bundles["linux-host"].SHA256},
				MacCandidateSHA256: digest([]byte("mac candidate")), JournalSHA256: strings.Repeat("b", 64),
			})
			return result, nil
		}}
	previous := promotionBackendForTests
	previousPlan := prdBootstrapPlanForTests
	previousKeys := prdBootstrapHostKeysForTests
	previousMacRoute := prdBootstrapMacRouteForTests
	previousCSR := prdBootstrapCSRForTests
	previousMaterial := prdBootstrapServerMaterialForTests
	promotionBackendForTests = backend
	planCalls := 0
	prdBootstrapPlanForTests = func(_ context.Context, _ *NativePromotionBackend, got Release, _ string) (PromotionPlan, error) {
		planCalls++
		if got.ID != release.ID {
			t.Fatalf("bootstrap planned a different release %s", got.ID)
		}
		return plan, nil
	}
	prdBootstrapHostKeysForTests = func(host, path string) ([]byte, error) {
		if host != "79.72.94.59" || path != profile.KnownHostsFile {
			t.Fatalf("known_hosts lookup was not bound to configured replacement: host=%q path=%q", host, path)
		}
		return []byte(host + " " + request.SSHHostPublicKey + "\n"), nil
	}
	prdBootstrapCSRForTests = nil
	prdBootstrapServerMaterialForTests = func(operationID string, requestPEM []byte, _ time.Time) (laneinstall.PRDReplacementServerMaterialReport, error) {
		if operationID != request.OperationID || !bytes.Equal(requestPEM, csr.RequestPEM) {
			t.Fatal("server certificate issuance was not bound to the operation CSR")
		}
		return laneinstall.PRDReplacementServerMaterialReport{OperationID: operationID, RequestSHA256: digest(requestPEM),
			CertificatePEM: serverCertificate, CertificateSHA: digest(serverCertificate), CAPEM: serverCA, CASHA: digest(serverCA)}, nil
	}
	macRouteCalled := false
	prdBootstrapMacRouteForTests = func(_ context.Context, input laneinstall.PRDReplacementMacRouteInput, _ time.Time) (laneinstall.PRDReplacementMacRouteResult, error) {
		macRouteCalled = true
		if input.OperationID != request.OperationID || input.ReleaseID != release.ID ||
			!bytes.Equal(input.ServerCertificatePEM, serverCertificate) || !bytes.Equal(input.ServerCAPEM, serverCA) {
			t.Fatal("Mac route did not receive the exact public certificate material")
		}
		installedTrustSHA, err := sharedTrust.apply(input, now)
		if err != nil {
			return laneinstall.PRDReplacementMacRouteResult{}, err
		}
		return laneinstall.PRDReplacementMacRouteResult{OperationID: request.OperationID, Stage: "complete",
			ConfigurationSHA256: digest([]byte("mac candidate")), JournalSHA256: strings.Repeat("c", 64),
			ServerCASHA256:       installedTrustSHA,
			BackupManifestSHA256: strings.Repeat("d", 64), WorkInventorySHA256: strings.Repeat("e", 64), ServiceStopped: true}, nil
	}
	t.Cleanup(func() {
		promotionBackendForTests, prdBootstrapPlanForTests = previous, previousPlan
		prdBootstrapHostKeysForTests, prdBootstrapMacRouteForTests = previousKeys, previousMacRoute
		prdBootstrapCSRForTests, prdBootstrapServerMaterialForTests = previousCSR, previousMaterial
	})
	bootstrapArgs := []string{"prd-replacement-bootstrap", "--request-file", requestPath, "--approval-file", approvalPath,
		"--publisher-owner", "Tomasz Walczuk", "--publisher-control", "clients-held", "--publishers-paused", "--no-new-accepted",
		"--store-root", workflowStore.Root, "--json"}
	updateArgs := []string{"prd-bootstrap-controller-update", "--operation", request.OperationID, "--request-file", requestPath,
		"--approval-file", approvalPath, "--publisher-owner", "Tomasz Walczuk", "--publisher-control", "clients-held",
		"--publishers-paused", "--no-new-accepted", "--store-root", workflowStore.Root, "--json"}
	var stdout, stderr strings.Builder
	code := RunAdminCLI(bootstrapArgs, &stdout, &stderr)
	if code == 0 || !called || !csrCalled || macRouteCalled || !strings.Contains(stderr.String(), "simulated interruption") {
		t.Fatalf("bootstrap did not stop at the durable server-material boundary: code=%d csr=%t remote=%t mac=%t stderr=%s", code, csrCalled, called, macRouteCalled, stderr.String())
	}
	journalPath := filepath.Join(workflowStore.Root, "operations", "prd-replacement-bootstrap", request.OperationID+".json")
	durable, exists, err := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !exists || !sameStrings(durable.CompletedStages, prdReplacementBootstrapStages[:3]) {
		t.Fatalf("server-material checkpoint was not preserved: journal=%+v exists=%t err=%v", durable, exists, err)
	}
	backend.ControllerRevision = strings.Repeat("b", 40)
	identityBytes, err := os.ReadFile(identityFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityFile, []byte("changed non-controller SSH key"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(updateArgs, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "reconstructed previous host_control_sha256") {
		t.Fatalf("controller amendment accepted changed SSH identity: code=%d stderr=%s", code, stderr.String())
	}
	updatePath := prdReplacementBootstrapControllerUpdatePath(journalPath)
	if _, found, readErr := readPRDReplacementBootstrapControllerUpdate(updatePath); readErr != nil || found {
		t.Fatalf("failed amendment left a record: found=%t err=%v", found, readErr)
	}
	if err := os.WriteFile(identityFile, identityBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(updateArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("supported controller amendment failed: code=%d stderr=%s", code, stderr.String())
	}
	firstUpdateOutput := stdout.String()
	var update PRDReplacementBootstrapControllerUpdate
	if err := json.Unmarshal([]byte(firstUpdateOutput), &update); err != nil || update.OperationID != request.OperationID ||
		update.PreviousControllerRevision != prdBootstrapPreviousController || update.PreviousHostControlSHA256 != durable.HostControlSHA256 ||
		update.ControllerRevision != backend.ControllerRevision || update.HostControlSHA256 == durable.HostControlSHA256 {
		t.Fatalf("controller amendment record is incomplete: record=%+v err=%v", update, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(updateArgs, &stdout, &stderr); code != 0 || stdout.String() != firstUpdateOutput {
		t.Fatalf("repeated amendment was not idempotent: code=%d stderr=%s", code, stderr.String())
	}
	firstRecordBytes, err := os.ReadFile(updatePath)
	if err != nil {
		t.Fatal(err)
	}
	backend.ControllerRevision = strings.Repeat("c", 40)
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(updateArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("append successor controller amendment: code=%d stderr=%s", code, stderr.String())
	}
	var successor PRDReplacementBootstrapControllerUpdate
	if err := json.Unmarshal([]byte(stdout.String()), &successor); err != nil ||
		successor.PreviousControllerRevision != update.ControllerRevision ||
		successor.PreviousHostControlSHA256 != update.HostControlSHA256 || successor.ControllerRevision != backend.ControllerRevision {
		t.Fatalf("successor amendment does not continue the exact prior controller identity: record=%+v err=%v", successor, err)
	}
	if current, readErr := os.ReadFile(updatePath); readErr != nil || !bytes.Equal(current, firstRecordBytes) {
		t.Fatalf("original controller amendment was modified: err=%v", readErr)
	}
	successorPath := prdReplacementBootstrapControllerUpdateRevisionPath(journalPath, backend.ControllerRevision)
	if _, found, readErr := readPRDReplacementBootstrapControllerUpdate(successorPath); readErr != nil || !found {
		t.Fatalf("successor amendment is not durably recorded: found=%t err=%v", found, readErr)
	}
	secondUpdateOutput := stdout.String()
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(updateArgs, &stdout, &stderr); code != 0 || stdout.String() != secondUpdateOutput {
		t.Fatalf("repeated successor amendment was not idempotent: code=%d stderr=%s", code, stderr.String())
	}
	chain, found, err := readPRDReplacementBootstrapControllerUpdateChain(journalPath, durable)
	if err != nil || !found || len(chain) != 2 {
		t.Fatalf("read existing controller amendment chain: found=%t entries=%d err=%v", found, len(chain), err)
	}
	for index := 0; len(chain) < prdBootstrapMaxControllerUpdates; index++ {
		backend.ControllerRevision = strings.Repeat(string(rune('1'+index)), 40)
		stdout.Reset()
		stderr.Reset()
		if code := RunAdminCLI(updateArgs, &stdout, &stderr); code != 0 {
			t.Fatalf("append bounded controller amendment %d: code=%d stderr=%s", index+3, code, stderr.String())
		}
		chain, found, err = readPRDReplacementBootstrapControllerUpdateChain(journalPath, durable)
		if err != nil || !found || len(chain) != index+3 {
			t.Fatalf("controller amendment chain after append %d: found=%t entries=%d err=%v", index+3, found, len(chain), err)
		}
	}
	backend.ControllerRevision = strings.Repeat("7", 40)
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(updateArgs, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "bounded limit") {
		t.Fatalf("controller amendment beyond the bound was not refused: code=%d stderr=%s", code, stderr.String())
	}
	if _, exists, readErr := readPRDReplacementBootstrapControllerUpdate(
		prdReplacementBootstrapControllerUpdateRevisionPath(journalPath, backend.ControllerRevision)); readErr != nil || exists {
		t.Fatalf("over-limit controller amendment left a record: exists=%t err=%v", exists, readErr)
	}
	backend.ControllerRevision = chain[len(chain)-1].ControllerRevision
	// Resume rejects drift in the SSH identity, original request, and approval
	// bytes before making another remote call.
	if err := os.WriteFile(identityFile, []byte("post-amendment identity drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(bootstrapArgs, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "host profile, SSH identity, or trust inputs") {
		t.Fatalf("resume accepted SSH identity drift: code=%d stderr=%s", code, stderr.String())
	}
	if err := os.WriteFile(identityFile, identityBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	conflictingRequest := request
	conflictingRequest.TemplateHostID = "factory-sandbox"
	conflictingBytes, err := json.Marshal(conflictingRequest)
	if err != nil || os.WriteFile(requestPath, conflictingBytes, 0o600) != nil {
		t.Fatal("write conflicting request")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(bootstrapArgs, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "request_sha256") {
		t.Fatalf("resume accepted request drift: code=%d stderr=%s", code, stderr.String())
	}
	if err := os.WriteFile(requestPath, requestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvalPath, append(append([]byte(nil), approvalFileBytes...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(bootstrapArgs, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "approval_sha256") {
		t.Fatalf("resume accepted approval-byte drift: code=%d stderr=%s", code, stderr.String())
	}
	if err := os.WriteFile(approvalPath, approvalFileBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(bootstrapArgs, &stdout, &stderr); code != 0 || !called || !csrCalled || !macRouteCalled {
		t.Fatalf("amended bootstrap resume failed: code=%d csr=%t remote=%t mac=%t stderr=%s", code, csrCalled, called, macRouteCalled, stderr.String())
	}
	if strings.Contains(stdout.String(), "private-key-stays-on-linux") || strings.Contains(stderr.String(), "private-key-stays-on-linux") {
		t.Fatal("bootstrap command exposed a private-key marker in output")
	}
	var result laneinstall.PRDReplacementBootstrapResult
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil || result.Stage != "ready_for_catalog_promotion" || result.MacRoute == nil {
		t.Fatalf("bootstrap result=%q err=%v", stdout.String(), err)
	}
	if bootstrapCalls != 2 || planCalls != 1 {
		t.Fatalf("resume counts: bootstrap=%d plan=%d", bootstrapCalls, planCalls)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(bootstrapArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("exact completion retry failed: code=%d stderr=%s", code, stderr.String())
	}
	if bootstrapCalls != 3 || planCalls != 1 {
		t.Fatalf("completion retry counts: bootstrap=%d plan=%d", bootstrapCalls, planCalls)
	}
	durable, exists, err = readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !exists || validatePRDReplacementBootstrapCoordinatorJournal(durable) != nil || len(durable.CompletedStages) != len(prdReplacementBootstrapStages) {
		t.Fatalf("coordinator journal did not reach complete stage: journal=%+v exists=%t err=%v", durable, exists, err)
	}
	if update.CoordinatorBindingsSHA256 != prdReplacementBootstrapStableBindingsSHA(durable) {
		t.Fatal("controller amendment no longer binds to the completed coordinator journal")
	}
	conflictingRequest = request
	conflictingRequest.TemplateHostID = "factory-sandbox"
	conflictingBytes, err = json.Marshal(conflictingRequest)
	if err != nil || os.WriteFile(requestPath, conflictingBytes, 0o600) != nil {
		t.Fatal("write conflicting completed retry")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(bootstrapArgs, &stdout, &stderr); code == 0 {
		t.Fatal("conflicting completed retry was accepted")
	}
	if bootstrapCalls != 3 || planCalls != 1 {
		t.Fatal("conflicting retry reached mutation or replanning")
	}
}

func TestReplacementHostKeyPinRequiresOneExactEd25519Key(t *testing.T) {
	wanted := releaseflowTestPublicKey("replacement-bootstrap-host")
	if !replacementHostKeyPinned(wanted, []byte("79.72.94.59 "+wanted+"\n")) {
		t.Fatal("exact pinned replacement host key was rejected")
	}
	if replacementHostKeyPinned(wanted, []byte("79.72.94.59 ssh-ed25519 AQID\n")) {
		t.Fatal("malformed or mismatched host key was accepted")
	}
	other := releaseflowTestPublicKey("other-host-key")
	if replacementHostKeyPinned(wanted, []byte("79.72.94.59 "+wanted+"\n79.72.94.59 "+other+"\n")) {
		t.Fatal("ambiguous known_hosts keys were accepted")
	}
}

func releaseflowTestPublicKey(identity string) string {
	seed := sha256.Sum256([]byte(identity))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	var blob bytes.Buffer
	_ = binary.Write(&blob, binary.BigEndian, uint32(len("ssh-ed25519")))
	blob.WriteString("ssh-ed25519")
	_ = binary.Write(&blob, binary.BigEndian, uint32(len(public)))
	blob.Write(public)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob.Bytes()) + " fixture"
}

type promotionCLIBackendState struct {
	Selected               map[string]bool   `json:"selected"`
	Prepared               bool              `json:"prepared"`
	FailPrepareAfterEffect bool              `json:"fail_prepare_after_effect"`
	FailSwitchAfterEffect  string            `json:"fail_switch_after_effect"`
	FailBranch             bool              `json:"fail_branch"`
	ExistingHolds          map[string]string `json:"existing_holds,omitempty"`
	PlanPredecessors       map[string]string `json:"plan_predecessors,omitempty"`
	PlanBackupDigest       string            `json:"plan_backup_digest,omitempty"`
}

type promotionCLIPlanOverrideBackend struct {
	*fakeBackend
	predecessors map[string]string
	backupDigest string
}

func (b *promotionCLIPlanOverrideBackend) Plan(ctx context.Context, lane string, release Release, approval string) (PromotionPlan, error) {
	plan, err := b.fakeBackend.Plan(ctx, lane, release, approval)
	if err != nil {
		return plan, err
	}
	if b.predecessors != nil {
		plan.PredecessorBundleSHA256 = b.predecessors
	}
	if b.backupDigest != "" {
		plan.BackupRecoveryPackageSHA = b.backupDigest
	}
	return plan, nil
}

// TestPromotionCLIHelper is the subprocess entry point for public-command
// tests. The fixture seam exists only in this _test.go binary; production
// commands have no environment-variable or fixture-root backend switch.
func TestPromotionCLIHelper(t *testing.T) {
	if os.Getenv("PROMO_CLI_HELPER") != "1" {
		return
	}
	statePath := os.Getenv("PROMO_CLI_STATE")
	state := promotionCLIBackendState{Selected: map[string]bool{}}
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			os.Exit(90)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		os.Exit(90)
	}
	backend := &fakeBackend{selected: state.Selected, prepared: state.Prepared,
		failPrepareAfterEffect: state.FailPrepareAfterEffect, failSwitchAfterEffect: state.FailSwitchAfterEffect,
		failBranch: state.FailBranch, existingHolds: state.ExistingHolds}
	var promotionBackend Backend = backend
	if state.PlanPredecessors != nil || state.PlanBackupDigest != "" {
		promotionBackend = &promotionCLIPlanOverrideBackend{fakeBackend: backend, predecessors: state.PlanPredecessors, backupDigest: state.PlanBackupDigest}
	}
	promotionBackendForTests = promotionBackend
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(91)
	}
	var code int
	command := os.Args[separator+1]
	if command == "runner-release" {
		code = RunAdminCLI(os.Args[separator+2:], os.Stdout, os.Stderr)
	} else if command == "promote-to" {
		code = RunPromoteCLI(os.Args[separator+2:], os.Stdout, os.Stderr)
	} else {
		os.Exit(91)
	}
	state = promotionCLIBackendState{Selected: backend.selected, Prepared: backend.prepared,
		FailPrepareAfterEffect: backend.failPrepareAfterEffect, FailSwitchAfterEffect: backend.failSwitchAfterEffect,
		FailBranch: backend.failBranch, ExistingHolds: backend.existingHolds}
	data, err := json.Marshal(state)
	if err != nil || os.WriteFile(statePath, data, 0o600) != nil {
		os.Exit(92)
	}
	os.Exit(code)
}

func preparePRDResumeCLI(t *testing.T, store *Store, release Release, opID string, renewalApproval bool) (Operation, Approval, string, string, promotionCLIBackendState) {
	t.Helper()
	plan, err := BuildPlan("prd", "all", release)
	if err != nil {
		t.Fatal(err)
	}
	plan.PredecessorBundleSHA256 = make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		plan.PredecessorBundleSHA256[host] = digest([]byte("original predecessor:" + host))
	}
	plan.BackupRecoveryPackageSHA = digest([]byte("original backup package"))
	oldApprovalSHA := digest([]byte("original approval"))
	op, err := NewOperation(opID, "prd", "all", release, "profile-v1", oldApprovalSHA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	op.CohortOperationID = opID
	op.PlanSnapshot = &plan
	op.Stage = StageBlocked
	op.LastError = "Sandbox switch failed after freshness guard"
	op.Hosts[0].SwitchIntent = true
	op.Hosts[0].Switched = true
	op.Hosts[0].Verified = true
	op.Hosts[0].SelectorOperation = "linux-selector-operation"
	op.Hosts[0].ObservedBundleSHA = release.Bundles["linux-host"].SHA256
	if err := AppendEvent(&op, "operation-created", "", false, "recorded", map[string]any{"plan": plan}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(&op, "host-verified", "linux-host", false, "verified", map[string]string{"bundle": release.Bundles["linux-host"].SHA256}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(&op, "switch-host", "sandbox-host", true, "intent", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(&op, "switch-host", "sandbox-host", false, "failed", map[string]string{"reason": "preflight expired"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOperation(op); err != nil {
		t.Fatal(err)
	}
	predecessors := make(map[string]string, len(plan.PredecessorBundleSHA256))
	for host, hash := range plan.PredecessorBundleSHA256 {
		predecessors[host] = hash
	}
	if renewalApproval {
		predecessors["linux-host"] = release.Bundles["linux-host"].SHA256
	}
	currentBackupDigest := digest([]byte("fresh plan package after Linux switch"))
	approval := Approval{FormatVersion: 1, ReleaseID: release.ID, ReleaseSHA256: release.Digest,
		HostSet: append([]string(nil), HostOrder...), WindowStart: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		WindowEnd: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Owner: "owner", Scope: "fixture renewal",
		PredecessorBundleSHA256: plan.PredecessorBundleSHA256, BackupRecoveryPackageSHA256: plan.BackupRecoveryPackageSHA}
	approvalPath := filepath.Join(t.TempDir(), "renewed-approval.json")
	encoded, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvalPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	holds := make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		holds[host] = op.CohortOperationID
	}
	state := promotionCLIBackendState{Selected: map[string]bool{"linux-host": true}, ExistingHolds: holds,
		PlanPredecessors: predecessors, PlanBackupDigest: currentBackupDigest}
	return op, approval, approvalPath, oldApprovalSHA, state
}

func TestBuildCLIHelper(t *testing.T) {
	if os.Getenv("PROMO_BUILD_CLI_HELPER") != "1" {
		return
	}
	repository := os.Getenv("PROMO_BUILD_REPOSITORY")
	controller := os.Getenv("PROMO_BUILD_CONTROLLER")
	artifactRoot := os.Getenv("PROMO_BUILD_ARTIFACT_ROOT")
	buildBackendForTests = &fakeBuildBackend{root: artifactRoot}
	buildCLIEnvironmentForTests = &buildCLIEnvironment{Repository: repository, ControllerRevision: controller,
		Resolver: GitResolver{Repository: repository, Primary: "origin", Mirror: "github", TempRoot: os.TempDir()}}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) || os.Args[separator+1] != "build-release" {
		os.Exit(91)
	}
	os.Exit(RunBuildCLI(os.Args[separator+2:], os.Stdout, os.Stderr))
}

func runPromotionCLIChild(t *testing.T, statePath string, args ...string) (string, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commandArgs := []string{"-test.run=^TestPromotionCLIHelper$", "--"}
	commandArgs = append(commandArgs, args...)
	cmd := exec.Command(executable, commandArgs...)
	cmd.Env = append(os.Environ(), "PROMO_CLI_HELPER=1", "PROMO_CLI_STATE="+statePath)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func writePromotionCLIState(t *testing.T, path string, state promotionCLIBackendState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func saveCLIRelease(t *testing.T, store *Store) Release {
	t.Helper()
	release := testRelease()
	saveTestRelease(t, store, release)
	return release
}

func cliPromotionArgs(lane, target, release, operation, root string) []string {
	return []string{lane, target, "--release", release, "--operation-id", operation, "--profile", "profile-v1",
		"--store-root", root, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control",
		"--publishers-paused", "--no-new-accepted", "--json"}
}

func TestPublicPromoteSubprocessInterruptionRetryAndRollback(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, FailSwitchAfterEffect: "linux-host"})
	opID := strings.Repeat("1", 32)
	args := cliPromotionArgs("dev", "all", release.ID, opID, store.Root)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err == nil {
		t.Fatalf("expected interruption after the first selector effect; output=%s", output)
	}
	op, loadErr := store.LoadOperation("dev", opID)
	if loadErr != nil || op.Stage != StageBlocked {
		t.Fatalf("interrupted operation not durably blocked: stage=%s err=%v output=%s", op.Stage, loadErr, output)
	}
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state promotionCLIBackendState
	if json.Unmarshal(stateData, &state) != nil || !state.Selected["linux-host"] {
		t.Fatalf("fixture did not preserve the completed selector effect: %s", stateData)
	}
	output, err = runPromotionCLIChild(t, statePath, "promote-to", "dev", "all", "--release", release.ID,
		"--resume", opID, "--profile", "profile-v1", "--store-root", store.Root,
		"--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control", "--publishers-paused", "--no-new-accepted", "--json")
	if err != nil {
		t.Fatalf("resume failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageComplete {
		t.Fatalf("resumed public command did not complete: stage=%s err=%v output=%s", op.Stage, err, output)
	}
	output, err = runPromotionCLIChild(t, statePath, "runner-release", "rollback", "--lane", "dev", "--operation", opID,
		"--store-root", store.Root, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control",
		"--publishers-paused", "--no-new-accepted", "--json")
	if err != nil {
		t.Fatalf("public rollback failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageRollbackComplete {
		t.Fatalf("public rollback did not complete: stage=%s err=%v output=%s", op.Stage, err, output)
	}
}

func TestPublicPromotePrepareOnlyThenResumeUsesSameOperation(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	opID := strings.Repeat("d", 32)
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}})
	args := cliPromotionArgs("dev", "all", release.ID, opID, store.Root)
	args = append(args, "--prepare-only")
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err != nil || !strings.Contains(output, `"outcome":"prepared_hold_active"`) {
		t.Fatalf("public prepare-only command failed: err=%v output=%s", err, output)
	}
	op, err := store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StagePrepared || op.CohortOperationID != opID {
		t.Fatalf("prepare-only operation is not durably prepared: op=%+v err=%v", op, err)
	}
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state promotionCLIBackendState
	if json.Unmarshal(stateBytes, &state) != nil || len(state.Selected) != 0 {
		t.Fatalf("prepare-only command switched a selector: %s", stateBytes)
	}

	resumeArgs := []string{"promote-to", "dev", "all", "--release", release.ID, "--resume", opID,
		"--profile", "profile-v1", "--store-root", store.Root, "--publisher-owner", "fixture-owner",
		"--publisher-control", "fixture-pause-control", "--publishers-paused", "--no-new-accepted", "--json"}
	output, err = runPromotionCLIChild(t, statePath, resumeArgs...)
	if err != nil {
		t.Fatalf("public resume after prepare-only failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageComplete {
		t.Fatalf("public resume did not complete the same operation: stage=%s err=%v output=%s", op.Stage, err, output)
	}
}

func TestPublicPromoteSubprocessRetriesPartialPreparation(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, FailPrepareAfterEffect: true})
	opID := strings.Repeat("2", 32)
	args := cliPromotionArgs("dev", "all", release.ID, opID, store.Root)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err == nil {
		t.Fatalf("expected partial common-preparation interruption; output=%s", output)
	}
	op, loadErr := store.LoadOperation("dev", opID)
	if loadErr != nil || op.Stage != StageBlocked {
		t.Fatalf("partial preparation was not recorded: stage=%s err=%v", op.Stage, loadErr)
	}
	resumeArgs := []string{"promote-to", "dev", "all", "--release", release.ID, "--resume", opID, "--profile", "profile-v1",
		"--store-root", store.Root, "--publisher-owner", "fixture-owner", "--publisher-control", "fixture-pause-control",
		"--publishers-paused", "--no-new-accepted", "--json"}
	output, err = runPromotionCLIChild(t, statePath, resumeArgs...)
	if err != nil {
		t.Fatalf("retry after partial preparation failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageComplete {
		t.Fatalf("retry did not finish the exact operation: stage=%s err=%v", op.Stage, err)
	}
}

func TestPublicPromoteSubprocessSupersedesOneCompletedHold(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	predecessor := strings.Repeat("c", 32)
	holds := make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		holds[host] = predecessor
	}
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, ExistingHolds: holds})
	opID := strings.Repeat("d", 32)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"},
		cliPromotionArgs("dev", "all", release.ID, opID, store.Root)...)...)
	if err != nil {
		t.Fatalf("public command rejected a validated completed hold: %v: %s", err, output)
	}
	op, err := store.LoadOperation("dev", opID)
	if err != nil || op.Stage != StageComplete || op.PreviousCohortOperationID != predecessor {
		t.Fatalf("public command did not complete the durable hold handover: stage=%s predecessor=%s err=%v output=%s",
			op.Stage, op.PreviousCohortOperationID, err, output)
	}
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state promotionCLIBackendState
	if json.Unmarshal(stateData, &state) != nil {
		t.Fatalf("could not read updated backend state: %s", stateData)
	}
	for _, host := range HostOrder {
		if state.ExistingHolds[host] != op.CohortOperationID {
			t.Fatalf("successor hold on %s = %q, want %q", host, state.ExistingHolds[host], op.CohortOperationID)
		}
	}
}

func TestPublicPromotionCLIRejectsExpiredPRDApproval(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	plan, err := BuildPlan("prd", "all", release)
	if err != nil {
		t.Fatal(err)
	}
	predecessors := map[string]string{}
	for _, host := range HostOrder {
		predecessors[host] = digest([]byte("predecessor:" + host))
	}
	approval := Approval{FormatVersion: 1, ReleaseID: release.ID, ReleaseSHA256: release.Digest, HostSet: append([]string(nil), HostOrder...),
		WindowStart: "2020-01-01T00:00:00Z", WindowEnd: "2020-01-01T01:00:00Z", Owner: "owner", Scope: "fixture only",
		PredecessorBundleSHA256: predecessors, BackupRecoveryPackageSHA256: plan.BackupRecoveryPackageSHA}
	approvalPath := filepath.Join(t.TempDir(), "expired-approval.json")
	encoded, _ := json.Marshal(approval)
	if err := os.WriteFile(approvalPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"promote-to", "prd", "all", "--release", release.ID, "--approval-file", approvalPath,
		"--store-root", store.Root, "--json"}
	output, err := runPromotionCLIChild(t, filepath.Join(t.TempDir(), "state.json"), args...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(output, ErrApprovalRequired.Error()) {
		t.Fatalf("expired PRD approval was not rejected before effects: err=%v output=%s", err, output)
	}
	if entries, readErr := os.ReadDir(filepath.Join(store.Root, "operations", "prd")); readErr == nil && len(entries) != 0 {
		t.Fatalf("expired approval created a deployment operation: %v", entries)
	}
}

func TestPublicPRDResumeUsesPersistedApprovalBaselineAfterPartialSwitch(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	saveTestAcceptance(t, store, "qas", release, time.Now())
	opID := strings.Repeat("e", 32)
	op, approval, approvalPath, oldApprovalSHA, state := preparePRDResumeCLI(t, store, release, opID, true)
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, state)
	args := []string{"promote-to", "prd", "all", "--release", release.ID, "--resume", opID, "--approval-file", approvalPath,
		"--profile", "profile-v1", "--store-root", store.Root, "--publisher-owner", "owner", "--publisher-control", "fixture pause",
		"--publishers-paused", "--no-new-accepted", "--json"}
	output, err := runPromotionCLIChild(t, statePath, args...)
	if err != nil {
		t.Fatalf("resumed PRD command rejected the persisted approval baseline after Linux switched: %v: %s", err, output)
	}
	op, err = store.LoadOperation("prd", opID)
	if err != nil || op.Stage != StageComplete {
		t.Fatalf("resumed PRD operation did not complete: stage=%s err=%v output=%s", op.Stage, err, output)
	}
	_, renewedSHA, err := readApprovalFile(approvalPath)
	if err != nil {
		t.Fatal(err)
	}
	if op.ApprovalSHA != renewedSHA || op.ApprovalSHA == oldApprovalSHA {
		t.Fatalf("renewed approval was not durably applied: current=%s renewed=%s original=%s", op.ApprovalSHA, renewedSHA, oldApprovalSHA)
	}
	found := false
	for _, event := range op.Events {
		if event.Action != "approval-renewed" {
			continue
		}
		var evidence map[string]string
		if err := json.Unmarshal(event.Evidence, &evidence); err != nil {
			t.Fatal(err)
		}
		if event.Outcome == "applied" && evidence["previous_approval_sha256"] == oldApprovalSHA &&
			evidence["current_approval_sha256"] == renewedSHA && evidence["plan_snapshot_sha256"] == hashJSON(*op.PlanSnapshot) {
			found = true
		}
	}
	if !found {
		t.Fatalf("renewal event did not preserve both approval identities and original plan binding: %+v", op.Events)
	}
	if !sameStringMap(approval.PredecessorBundleSHA256, op.PlanSnapshot.PredecessorBundleSHA256) ||
		approval.BackupRecoveryPackageSHA256 != op.PlanSnapshot.BackupRecoveryPackageSHA {
		t.Fatal("fixture approval did not remain bound to the operation's original plan snapshot")
	}
}

func TestPublicPRDResumeRejectsApprovalOutsidePersistedBaseline(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	saveTestAcceptance(t, store, "qas", release, time.Now())
	opID := strings.Repeat("f", 32)
	op, approval, approvalPath, oldApprovalSHA, state := preparePRDResumeCLI(t, store, release, opID, true)
	approval.PredecessorBundleSHA256["sandbox-host"] = digest([]byte("unapproved predecessor"))
	encoded, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvalPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, state)
	args := []string{"promote-to", "prd", "all", "--release", release.ID, "--resume", opID, "--approval-file", approvalPath,
		"--profile", "profile-v1", "--store-root", store.Root, "--publisher-owner", "owner", "--publisher-control", "fixture pause",
		"--publishers-paused", "--no-new-accepted", "--json"}
	output, err := runPromotionCLIChild(t, statePath, args...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(output, "does not match the operation's authorized plan") {
		t.Fatalf("resumed PRD command did not reject an approval with a changed predecessor: err=%v output=%s", err, output)
	}
	loaded, err := store.LoadOperation("prd", opID)
	if err != nil || loaded.Stage != StageBlocked || loaded.ApprovalSHA != oldApprovalSHA || len(loaded.Events) != len(op.Events) {
		t.Fatalf("rejected approval caused operation effects: stage=%s approval=%s events=%d err=%v", loaded.Stage, loaded.ApprovalSHA, len(loaded.Events), err)
	}
}

func TestPublicBranchSyncCommandRetriesGitOnly(t *testing.T) {
	store := testStore(t)
	release := saveCLIRelease(t, store)
	saveTestAcceptance(t, store, "dev", release, time.Now())
	statePath := filepath.Join(t.TempDir(), "backend-state.json")
	writePromotionCLIState(t, statePath, promotionCLIBackendState{Selected: map[string]bool{}, FailBranch: true})
	opID := strings.Repeat("3", 32)
	args := cliPromotionArgs("qas", "all", release.ID, opID, store.Root)
	output, err := runPromotionCLIChild(t, statePath, append([]string{"promote-to"}, args...)...)
	if err != nil || !strings.Contains(output, "runtime_accepted_branch_sync_pending") {
		t.Fatalf("expected accepted runtime with Git-only pending status: err=%v output=%s", err, output)
	}
	op, err := store.LoadOperation("qas", opID)
	if err != nil || op.Stage != StageBranchSyncPending || op.BranchSync != "pending" {
		t.Fatalf("branch race was not represented as pending: stage=%s sync=%s err=%v", op.Stage, op.BranchSync, err)
	}
	output, err = runPromotionCLIChild(t, statePath, "runner-release", "branch-sync", "--lane", "qas", "--operation", opID, "--store-root", store.Root, "--json")
	if err != nil {
		t.Fatalf("Git-only branch synchronization retry failed: %v: %s", err, output)
	}
	op, err = store.LoadOperation("qas", opID)
	if err != nil || op.Stage != StageComplete || op.BranchSync != "complete" {
		t.Fatalf("branch-only retry did not complete: stage=%s sync=%s err=%v", op.Stage, op.BranchSync, err)
	}
}

func TestAcceptanceExpiryFixtureClock(t *testing.T) {
	// The CLI approval expiry check is wall-clock-bound by design; this fixed
	// reference makes the boundary rule itself explicit in test diagnostics.
	if time.Now().Before(time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC)) {
		t.Fatal("unexpected system time before expired fixture")
	}
}

func TestPublicCommandHelpDescribesEffectsAndGuards(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := RunBuildCLI([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "without selecting or restarting") {
		t.Fatalf("build-release help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunPromoteCLI([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "all four hosts") || !strings.Contains(stdout.String(), "owner approval") {
		t.Fatalf("promote-to help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI([]string{"help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "branch-sync") || !strings.Contains(stdout.String(), "does not restore databases") {
		t.Fatalf("runner-release help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPublicBuildCLISealsAndPublishesFrozenBranchCommit(t *testing.T) {
	root := t.TempDir()
	resolver, commit := buildSourceFixture(t, root)
	store, err := NewStore(filepath.Join(root, "runtime", "operations"))
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hostsPath := writeTestHostConfiguration(t, root)
	if _, err := LoadHostConfiguration(hostsPath); err != nil {
		data, _ := os.ReadFile(hostsPath)
		t.Fatalf("fixture host configuration rejected before subprocess: %v; %s", err, data)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestBuildCLIHelper$", "--", "build-release", "--branch", "dev", "--commit", commit,
		"--release-id", "release-cli-public", "--store-root", store.Root, "--hosts-file", hostsPath}
	for _, receipt := range fixtureSourceTestReceipts(commit) {
		data, marshalErr := json.MarshalIndent(receipt, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		path := filepath.Join(root, "source-test-"+receipt.HostID+".json")
		if writeErr := os.WriteFile(path, append(data, '\n'), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		args = append(args, "--source-test-receipt", path)
	}
	args = append(args, "--json")
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), "PROMO_BUILD_CLI_HELPER=1", "PROMO_BUILD_REPOSITORY="+resolver.Repository,
		"PROMO_BUILD_CONTROLLER="+strings.Repeat("b", 40), "PROMO_BUILD_ARTIFACT_ROOT="+artifactRoot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("public build-release failed: %v: %s", err, output)
	}
	var cliResult BuildResult
	if err := json.Unmarshal(output, &cliResult); err != nil {
		t.Fatalf("decode public build result: %v: %s", err, output)
	}
	release, err := store.LoadRelease("release-cli-public")
	if err != nil {
		t.Fatalf("sealed public-command release missing: %v: %s", err, output)
	}
	buildRecord, err := store.LoadBuild(cliResult.Record.BuildID)
	if err != nil {
		t.Fatalf("public build operation record missing: %v", err)
	}
	if release.Source.Commit != commit || release.Source.Branch != "dev" || !release.Sealed || len(buildRecord.PublishedHosts) != len(HostOrder) || len(buildRecord.QualificationReceiptRefs) != len(HostOrder) {
		t.Fatalf("public build did not freeze and publish the expected release: %+v", release)
	}
	var statusOutput, statusErrors strings.Builder
	if code := RunAdminCLI([]string{"status", "--release", release.ID, "--store-root", store.Root, "--json"}, &statusOutput, &statusErrors); code != 0 || !strings.Contains(statusOutput.String(), "qualification_receipt_refs") {
		t.Fatalf("release status omitted verified provenance: code=%d stdout=%s stderr=%s", code, statusOutput.String(), statusErrors.String())
	}
	for _, host := range HostOrder {
		if release.Bundles[host].SourceRevision != commit || release.Bundles[host].HostID != host {
			t.Fatalf("public build manifest has wrong source or host bundle for %s: %+v", host, release.Bundles[host])
		}
	}
}

func writeTestHostConfiguration(t *testing.T, root string) string {
	t.Helper()
	configRoot := filepath.Join(root, "host-config")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := make(map[string]HostProfile, len(HostOrder))
	for _, host := range HostOrder {
		identity := filepath.Join(configRoot, host+".key")
		knownHosts := filepath.Join(configRoot, host+".known_hosts")
		for path, body := range map[string][]byte{identity: []byte("test identity\n"), knownHosts: []byte("test host key\n")} {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		profile := HostProfile{HostID: host, GitIdentityFile: identity, GitKnownHostsFile: knownHosts,
			Repository: filepath.Join(root, "worker", host), GoBinary: "/usr/bin/go", RuntimeRoot: filepath.Join(root, "runtime", host)}
		if host != "mac-control-plane" {
			profile.Target = "ubuntu@" + host
			profile.IdentityFile = identity
			profile.KnownHostsFile = knownHosts
		}
		hosts[host] = profile
	}
	data, err := json.Marshal(HostConfiguration{FormatVersion: 1, Hosts: hosts})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configRoot, "runner-release-hosts.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
