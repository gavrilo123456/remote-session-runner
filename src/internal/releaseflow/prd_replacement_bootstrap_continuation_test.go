package releaseflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

func TestPublicPRDBootstrapWindowContinuationIsBoundedAndImmutable(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	release := testRelease()
	saveTestRelease(t, store, release)
	now := time.Now().UTC().Truncate(time.Second)
	saveTestAcceptance(t, store, "dev", release, now)
	saveTestAcceptance(t, store, "qas", release, now)
	approval := continuationTestApproval(release, now)
	approvalBytes, err := json.MarshalIndent(approval, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	approvalBytes = append(approvalBytes, '\n')
	approvalPath := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(approvalPath, approvalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	request := laneinstall.PRDReplacementBootstrapRequest{OperationID: prdBootstrapContinuationOperation,
		TemplateHostID: "sandbox-host", ReleaseID: release.ID, ExpectedReleaseSHA256: release.Digest,
		SSHHostPublicKey: releaseflowTestPublicKey("replacement-continuation-host")}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestPath, requestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	profile, knownHost, identityPath, knownHostsPath := continuationTestProfile(t, request)
	native := &NativePromotionBackend{Config: HostConfiguration{Hosts: map[string]HostProfile{"linux-host": profile}},
		ControllerRevision: strings.Repeat("b", 40)}
	previousBackend := promotionBackendForTests
	previousPlan := prdBootstrapContinuationPlanForTests
	previousKeys := prdBootstrapHostKeysForTests
	promotionBackendForTests = native
	plan := continuationTestPlan(release, approval)
	planCalls := 0
	prdBootstrapContinuationPlanForTests = func(_ context.Context, got *NativePromotionBackend, gotRelease Release, approvalSHA string) (PromotionPlan, error) {
		planCalls++
		if got != native || gotRelease.ID != release.ID || approvalSHA != digest(approvalBytes) {
			t.Fatalf("read-only preflight was not bound to the exact operation inputs")
		}
		return plan, nil
	}
	prdBootstrapHostKeysForTests = func(host, path string) ([]byte, error) {
		if host != "79.72.94.59" || path != knownHostsPath {
			t.Fatalf("continuation looked up an unexpected replacement host key: %s %s", host, path)
		}
		return knownHost, nil
	}
	t.Cleanup(func() {
		promotionBackendForTests, prdBootstrapContinuationPlanForTests, prdBootstrapHostKeysForTests = previousBackend, previousPlan, previousKeys
	})
	_ = identityPath

	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	originalController := *native
	originalController.ControllerRevision = prdBootstrapPreviousController
	previousHostSHA, err := replacementBootstrapHostControlSHA(&originalController, profile, request.SSHHostPublicKey, knownHost)
	if err != nil {
		t.Fatal(err)
	}
	journal := continuationTestJournal(request, requestBytes, release, approval, approvalBytes, previousHostSHA, now)
	if err := writePRDReplacementBootstrapCoordinatorJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}

	args := []string{"prd-bootstrap-window-continue", "--operation", request.OperationID, "--request-file", requestPath,
		"--approval-file", approvalPath, "--publisher-owner", approval.Owner, "--publisher-control", journal.PublisherControl,
		"--publishers-paused", "--no-new-accepted", "--owner-authorized-one-time-continuation", "--store-root", store.Root, "--json"}
	var stdout, stderr strings.Builder
	missingAuthorization := make([]string, 0, len(args)-1)
	for _, arg := range args {
		if arg != "--owner-authorized-one-time-continuation" {
			missingAuthorization = append(missingAuthorization, arg)
		}
	}
	if code := RunAdminCLI(missingAuthorization, &stdout, &stderr); code == 0 {
		t.Fatal("continuation command accepted the call without its exact owner authorization flag")
	}
	continuationPath, err := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(continuationPath); !os.IsNotExist(err) {
		t.Fatalf("unauthorized command created continuation state: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code != 0 {
		t.Fatalf("public continuation command failed: code=%d stderr=%s", code, stderr.String())
	}
	firstOutput := stdout.String()
	var result prdBootstrapContinuationResult
	if err := json.Unmarshal([]byte(firstOutput), &result); err != nil || result.OperationID != request.OperationID ||
		result.ReleaseID != release.ID || result.OriginalApprovalSHA256 != digest(approvalBytes) || result.RecordPath != continuationPath {
		t.Fatalf("continuation result is incomplete: %+v err=%v", result, err)
	}
	record, found, err := readPRDReplacementBootstrapContinuation(continuationPath)
	if err != nil || !found || record.OriginalApprovalSHA256 != journal.ApprovalSHA256 ||
		record.WindowStartUTC != record.PreflightAtUTC || result.RecordSHA256 != digest(mustRead(t, continuationPath)) {
		t.Fatalf("continuation did not preserve and bind the original approval and preflight: found=%t record=%+v err=%v", found, record, err)
	}
	info, err := os.Stat(continuationPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("continuation record is not owner-only: info=%v err=%v", info, err)
	}
	start, _ := time.Parse(time.RFC3339Nano, record.WindowStartUTC)
	end, _ := time.Parse(time.RFC3339Nano, record.WindowEndUTC)
	if !end.Equal(start.Add(4*time.Hour)) || !record.OwnerAuthorized || record.Scope != prdBootstrapContinuationScope {
		t.Fatalf("continuation scope/window is not the one-time four-hour Mac-route scope: %+v", record)
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code != 0 || stdout.String() != firstOutput {
		t.Fatalf("exact continuation retry changed the immutable record: code=%d stderr=%s", code, stderr.String())
	}
	if planCalls != 2 {
		t.Fatalf("continuation retries did not perform fresh read-only preflight: %d calls", planCalls)
	}
	if err := validatePRDReplacementBootstrapContinuation(record, journal, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, journal.PublisherControl, native.ControllerRevision,
		record.HostControlSHA256, store.Root, end); err == nil {
		t.Fatal("expired continuation remained valid at its exclusive end time")
	}

	badPlan := plan
	badPlan.HostBaselines = append([]HostObservation(nil), plan.HostBaselines...)
	badPlan.HostBaselines[1].WorkInventoryEmpty = false
	prdBootstrapContinuationPlanForTests = func(context.Context, *NativePromotionBackend, Release, string) (PromotionPlan, error) {
		return badPlan, nil
	}
	beforeBadPreflight := mustRead(t, continuationPath)
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "preflight failed") {
		t.Fatalf("continuation accepted nonempty fresh inventory: code=%d stderr=%s", code, stderr.String())
	}
	if got, readErr := os.ReadFile(continuationPath); readErr != nil || !bytes.Equal(got, beforeBadPreflight) {
		t.Fatalf("failed preflight modified immutable continuation: err=%v", readErr)
	}
	_ = identityPath
}

func TestPublicPRDBootstrapWindowRenewalAppendsExactDeadlineWithoutRewritingHistory(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	release := testRelease()
	release.ID = prdBootstrapWindowRenewalRelease
	bundles := make([]Bundle, 0, len(HostOrder))
	for _, host := range HostOrder {
		bundles = append(bundles, release.Bundles[host])
	}
	release.Digest, err = CanonicalReleaseDigest(release.ID, release.Source.Commit, release.Toolchain, bundles)
	if err != nil {
		t.Fatal(err)
	}
	release.ProvenanceSHA = digest(testProvenanceBytes(release))
	saveTestRelease(t, store, release)
	base := time.Date(2026, 10, 29, 23, 20, 0, 0, time.UTC)
	deadline, _ := time.Parse(time.RFC3339, prdBootstrapWindowRenewalDeadline)
	renewalTime := deadline.Add(-30 * time.Minute)
	saveTestAcceptance(t, store, "dev", release, base)
	saveTestAcceptance(t, store, "qas", release, base)
	approval := continuationTestApproval(release, base)
	approvalBytes, err := json.MarshalIndent(approval, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	approvalBytes = append(approvalBytes, '\n')
	approvalPath := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(approvalPath, approvalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	request := laneinstall.PRDReplacementBootstrapRequest{OperationID: prdBootstrapContinuationOperation,
		TemplateHostID: "sandbox-host", ReleaseID: release.ID, ExpectedReleaseSHA256: release.Digest,
		SSHHostPublicKey: releaseflowTestPublicKey("replacement-window-renewal"), PublishersPaused: true, NoNewAccepted: true,
		PublisherOwner: approval.Owner, PublisherControl: "owner-confirmed PRD clients paused"}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestPath, requestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	profile, knownHost, identityPath, knownHostsPath := continuationTestProfile(t, request)
	native := &NativePromotionBackend{Config: HostConfiguration{Hosts: map[string]HostProfile{"linux-host": profile}},
		ControllerRevision: strings.Repeat("b", 40)}
	previousBackend := promotionBackendForTests
	previousPlan := prdBootstrapContinuationPlanForTests
	previousKeys := prdBootstrapHostKeysForTests
	previousClock := prdBootstrapWindowRenewalNowForTests
	previousAfterController := prdBootstrapWindowRenewalAfterControllerForTests
	promotionBackendForTests = native
	plan := continuationTestPlan(release, approval)
	prdBootstrapContinuationPlanForTests = func(_ context.Context, got *NativePromotionBackend, gotRelease Release, approvalSHA string) (PromotionPlan, error) {
		if got != native || gotRelease.ID != release.ID || approvalSHA != digest(approvalBytes) {
			t.Fatalf("renewal preflight was not bound to the operation inputs")
		}
		return plan, nil
	}
	prdBootstrapHostKeysForTests = func(host, path string) ([]byte, error) {
		if host != "79.72.94.59" || path != knownHostsPath {
			t.Fatalf("renewal used unexpected host trust input")
		}
		return knownHost, nil
	}
	prdBootstrapWindowRenewalNowForTests = func() time.Time { return renewalTime }
	t.Cleanup(func() {
		promotionBackendForTests, prdBootstrapContinuationPlanForTests = previousBackend, previousPlan
		prdBootstrapHostKeysForTests, prdBootstrapWindowRenewalNowForTests = previousKeys, previousClock
		prdBootstrapWindowRenewalAfterControllerForTests = previousAfterController
	})
	originalBackend := *native
	originalBackend.ControllerRevision = prdBootstrapPreviousController
	originalHostSHA, err := replacementBootstrapHostControlSHA(&originalBackend, profile, request.SSHHostPublicKey, knownHost)
	if err != nil {
		t.Fatal(err)
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	journal := continuationTestJournal(request, requestBytes, release, approval, approvalBytes, originalHostSHA, base)
	if err := writePRDReplacementBootstrapCoordinatorJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	prdBootstrapHostKeysForTests("79.72.94.59", profile.KnownHostsFile)
	continuation, _, err := createPRDReplacementBootstrapContinuation(native, store, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, request.PublisherControl, plan, base)
	if err != nil {
		t.Fatal(err)
	}
	continuationPath, err := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, request.PublisherControl, continuationPath, base.Add(time.Minute)); err != nil {
		currentSHA, _ := replacementBootstrapHostControlSHA(native, profile, request.SSHHostPublicKey, knownHost)
		t.Fatalf("prepare final controller binding before renewal: %v (continuation=%s/%s current=%s/%s)", err,
			continuation.ControllerRevision, continuation.HostControlSHA256, native.ControllerRevision, currentSHA)
	}
	// Reproduce the live operation: seven delivered controllers were recorded
	// inside the old window, which expired before the renewal controller arrived.
	for index := 1; index < 7; index++ {
		native.ControllerRevision = fmt.Sprintf("%040x", index)
		if _, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
			approval, digest(approvalBytes), approval.Owner, request.PublisherControl, continuationPath, base.Add(time.Duration(index+1)*time.Minute)); err != nil {
			t.Fatalf("prepare prior controller %d: %v", index, err)
		}
	}
	native.ControllerRevision = strings.Repeat("c", 40)
	if _, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, request.PublisherControl, continuationPath, renewalTime); err == nil {
		t.Fatal("ordinary controller-update bypassed the expired continuation")
	}
	continuationBefore := mustRead(t, continuationPath)
	approvalBefore := mustRead(t, approvalPath)
	requestBefore := mustRead(t, requestPath)
	journalBefore := mustRead(t, journalPath)
	args := []string{"prd-bootstrap-window-renew", "--operation", request.OperationID, "--request-file", requestPath,
		"--approval-file", approvalPath, "--publisher-owner", approval.Owner, "--publisher-control", request.PublisherControl,
		"--publishers-paused", "--no-new-accepted", "--owner-authorized-deadline", prdBootstrapWindowRenewalDeadline,
		"--store-root", store.Root, "--json"}
	var stdout, stderr strings.Builder
	identityBefore := mustRead(t, identityPath)
	if err := os.WriteFile(identityPath, append(append([]byte(nil), identityBefore...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := RunAdminCLI(args, &stdout, &stderr); code == 0 {
		t.Fatal("renewal amended a controller after SSH identity changed")
	}
	if err := os.WriteFile(identityPath, identityBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	chain, found, err := readPRDReplacementBootstrapControllerUpdateChain(journalPath, journal)
	if err != nil || !found || len(chain) != 7 {
		t.Fatalf("rejected changed identity modified controller history: count=%d err=%v", len(chain), err)
	}
	changedProfile := profile
	changedProfile.GitIdentityFile = filepath.Join(t.TempDir(), "different-git-identity")
	native.Config.Hosts["linux-host"] = changedProfile
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code == 0 {
		t.Fatal("renewal amended a controller after a profile-only input changed")
	}
	native.Config.Hosts["linux-host"] = profile
	chain, found, err = readPRDReplacementBootstrapControllerUpdateChain(journalPath, journal)
	if err != nil || !found || len(chain) != 7 {
		t.Fatalf("rejected changed profile modified controller history: count=%d err=%v", len(chain), err)
	}
	interrupted := errors.New("interrupted after controller amendment")
	prdBootstrapWindowRenewalAfterControllerForTests = func() error { return interrupted }
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), interrupted.Error()) {
		t.Fatalf("renewal did not reach injected post-amendment interruption: code=%d stderr=%s", code, stderr.String())
	}
	chain, found, err = readPRDReplacementBootstrapControllerUpdateChain(journalPath, journal)
	if err != nil || !found || len(chain) != 8 || chain[7].ControllerRevision != native.ControllerRevision {
		t.Fatalf("interrupted renewal did not preserve exactly one final controller amendment: count=%d err=%v", len(chain), err)
	}
	renewalPath, err := prdReplacementBootstrapWindowRenewalPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(renewalPath); !os.IsNotExist(err) {
		t.Fatalf("interrupted renewal wrote a renewal record: %v", err)
	}
	prdBootstrapWindowRenewalAfterControllerForTests = nil
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code != 0 {
		t.Fatalf("public renewal command failed: code=%d stderr=%s", code, stderr.String())
	}
	firstOutput := stdout.String()
	var result prdBootstrapWindowRenewalResult
	if err := json.Unmarshal([]byte(firstOutput), &result); err != nil || result.OperationID != request.OperationID ||
		result.ReleaseID != release.ID || result.RecordPath == continuationPath ||
		result.PreviousDeadlineUTC != continuation.WindowEndUTC || result.AuthorizedDeadlineUTC != prdBootstrapWindowRenewalDeadline {
		t.Fatalf("renewal result was not bound to the original operation: %+v err=%v", result, err)
	}
	renewal, found, err := readPRDReplacementBootstrapWindowRenewal(result.RecordPath)
	if err != nil || !found || result.RecordSHA256 != digest(mustRead(t, result.RecordPath)) ||
		renewal.ContinuationSHA256 != digest(continuationBefore) || renewal.ControllerRevision != native.ControllerRevision {
		t.Fatalf("append-only renewal record was not verified: found=%t record=%+v err=%v", found, renewal, err)
	}
	info, err := os.Stat(result.RecordPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("renewal record is not owner-only: info=%v err=%v", info, err)
	}
	if !bytes.Equal(continuationBefore, mustRead(t, continuationPath)) || !bytes.Equal(approvalBefore, mustRead(t, approvalPath)) ||
		!bytes.Equal(requestBefore, mustRead(t, requestPath)) || !bytes.Equal(journalBefore, mustRead(t, journalPath)) {
		t.Fatal("renewal rewrote original inputs, continuation, or coordinator")
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code != 0 || stdout.String() != firstOutput {
		t.Fatalf("exact renewal retry changed the append-only record: code=%d stderr=%s", code, stderr.String())
	}
	native.ControllerRevision = strings.Repeat("d", 40)
	if _, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, request.PublisherControl, continuationPath, renewalTime); err == nil ||
		!strings.Contains(err.Error(), "bounded limit") {
		t.Fatalf("renewal bypassed the eight-controller history limit: %v", err)
	}
	native.ControllerRevision = strings.Repeat("c", 40)
	if err := validatePRDReplacementBootstrapContinuation(continuation, journal, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, request.PublisherControl, native.ControllerRevision,
		renewal.HostControlSHA256, store.Root, deadline.Add(-time.Second)); err != nil {
		t.Fatalf("renewed continuation was not valid before the authorized deadline: %v", err)
	}
	if err := validatePRDReplacementBootstrapContinuation(continuation, journal, request, digest(requestBytes), release,
		approval, digest(approvalBytes), approval.Owner, request.PublisherControl, native.ControllerRevision,
		renewal.HostControlSHA256, store.Root, deadline); err == nil {
		t.Fatal("renewal remained valid at its exclusive deadline")
	}
	changed := renewal
	changed.AuthorizedDeadlineUTC = deadline.Add(time.Second).Format(time.RFC3339)
	if validatePRDReplacementBootstrapWindowRenewalShape(changed) == nil {
		t.Fatal("renewal accepted a deadline outside the exact owner authorization")
	}
	macRunning := plan
	macRunning.HostBaselines = append([]HostObservation(nil), plan.HostBaselines...)
	for index := range macRunning.HostBaselines {
		if macRunning.HostBaselines[index].HostID == "mac-control-plane" {
			macRunning.HostBaselines[index].ServiceStopped = false
		}
	}
	if prdBootstrapContinuationPlanValid(macRunning, release.ID, release.Digest, &approval) {
		t.Fatal("renewal preflight accepted a running Mac PRD service")
	}
	badPlan := plan
	badPlan.HostBaselines = append([]HostObservation(nil), plan.HostBaselines...)
	badPlan.HostBaselines[1].WorkInventoryEmpty = false
	prdBootstrapContinuationPlanForTests = func(context.Context, *NativePromotionBackend, Release, string) (PromotionPlan, error) {
		return badPlan, nil
	}
	stdout.Reset()
	stderr.Reset()
	if code := RunAdminCLI(args, &stdout, &stderr); code == 0 {
		t.Fatal("idempotent renewal retry accepted a changed work inventory")
	}
	previousBootstrapPlan := prdBootstrapPlanForTests
	t.Cleanup(func() { prdBootstrapPlanForTests = previousBootstrapPlan })
	prdBootstrapPlanForTests = func(context.Context, *NativePromotionBackend, Release, string) (PromotionPlan, error) {
		return badPlan, nil
	}
	if _, err := executePRDReplacementBootstrap(context.Background(), native, store, release, approval, digest(approvalBytes),
		digest(requestBytes), request, continuationPath); err == nil {
		t.Fatal("bootstrap resume accepted changed work after renewal without a fresh matching preflight")
	}
	if !bytes.Equal(continuationBefore, mustRead(t, continuationPath)) || !bytes.Equal(approvalBefore, mustRead(t, approvalPath)) {
		t.Fatal("rejected changed-state retry modified approval or continuation history")
	}
}

func TestExpiredBootstrapResumesSameOperationThroughMacRouteContinuation(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "workflow"))
	if err != nil {
		t.Fatal(err)
	}
	release := testRelease()
	saveTestRelease(t, store, release)
	now := time.Now().UTC().Truncate(time.Second)
	saveTestAcceptance(t, store, "dev", release, now)
	saveTestAcceptance(t, store, "qas", release, now)
	approval := continuationTestApproval(release, now)
	approvalBytes, err := json.MarshalIndent(approval, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	approvalBytes = append(approvalBytes, '\n')
	approvalSHA := digest(approvalBytes)
	request := laneinstall.PRDReplacementBootstrapRequest{OperationID: prdBootstrapContinuationOperation,
		TemplateHostID: "sandbox-host", ReleaseID: release.ID, ExpectedReleaseSHA256: release.Digest,
		SSHHostPublicKey: releaseflowTestPublicKey("replacement-bootstrap-continuation"),
		PublishersPaused: true, NoNewAccepted: true, PublisherOwner: approval.Owner, PublisherControl: "owner-confirmed PRD clients paused"}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	profile, knownHost, _, knownHostsPath := continuationTestProfile(t, request)
	csr, err := laneinstall.GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	certificate := []byte("replacement public certificate")
	ca := []byte("replacement public CA")
	native := &NativePromotionBackend{Config: HostConfiguration{Hosts: map[string]HostProfile{"linux-host": profile}},
		ControllerRevision: strings.Repeat("c", 40)}
	originalBackend := *native
	originalBackend.ControllerRevision = prdBootstrapPreviousController
	originalHostSHA, err := replacementBootstrapHostControlSHA(&originalBackend, profile, request.SSHHostPublicKey, knownHost)
	if err != nil {
		t.Fatal(err)
	}
	journalPath, err := prdReplacementBootstrapCoordinatorJournalPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	journal := continuationTestJournal(request, requestBytes, release, approval, approvalBytes, originalHostSHA, now)
	journal.CSRRequestSHA256 = digest(csr.RequestPEM)
	journal.ServerCertSHA256 = digest(certificate)
	journal.ServerCASHA256 = digest(ca)
	if err := writePRDReplacementBootstrapCoordinatorJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	previousKeys := prdBootstrapHostKeysForTests
	prdBootstrapHostKeysForTests = func(host, path string) ([]byte, error) {
		if host != "79.72.94.59" || path != knownHostsPath {
			t.Fatalf("bootstrap continuation used unexpected host trust input")
		}
		return knownHost, nil
	}
	t.Cleanup(func() { prdBootstrapHostKeysForTests = previousKeys })
	if _, err := executePRDReplacementBootstrap(context.Background(), native, store, release, approval,
		approvalSHA, digest(requestBytes), request, ""); err == nil {
		t.Fatal("expired original approval resumed without a continuation record")
	}
	unchanged, found, err := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !found || !sameStrings(unchanged.CompletedStages, journal.CompletedStages) || unchanged.ApprovalSHA256 != approvalSHA {
		t.Fatalf("rejected expired-approval resume changed coordinator history: found=%t journal=%+v err=%v", found, unchanged, err)
	}
	currentPlan := continuationTestPlan(release, approval)
	continuation, _, err := createPRDReplacementBootstrapContinuation(native, store, request, digest(requestBytes), release,
		approval, approvalSHA, approval.Owner, request.PublisherControl, currentPlan, now)
	if err != nil {
		t.Fatal(err)
	}
	continuationPath, err := prdReplacementBootstrapContinuationPath(store.Root, request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createPRDReplacementBootstrapControllerUpdate(native, store, request, digest(requestBytes), release,
		approval, approvalSHA, approval.Owner, request.PublisherControl, continuationPath, now.Add(time.Minute)); err != nil {
		t.Fatalf("record controller amendment inside continuation window: %v", err)
	}

	previousCSR := prdBootstrapCSRForTests
	previousMaterial := prdBootstrapServerMaterialForTests
	previousMacRoute := prdBootstrapMacRouteForTests
	prdBootstrapCSRForTests = func(_ context.Context, _ *NativePromotionBackend, operationID string) (laneinstall.PRDReplacementServerCSRReport, error) {
		return laneinstall.PRDReplacementServerCSRReport{OperationID: operationID, RequestPEM: csr.RequestPEM, RequestSHA: digest(csr.RequestPEM)}, nil
	}
	prdBootstrapServerMaterialForTests = func(operationID string, requestPEM []byte, _ time.Time) (laneinstall.PRDReplacementServerMaterialReport, error) {
		return laneinstall.PRDReplacementServerMaterialReport{OperationID: operationID, RequestSHA256: digest(requestPEM),
			CertificatePEM: certificate, CertificateSHA: digest(certificate), CAPEM: ca, CASHA: digest(ca)}, nil
	}
	macRouteCalled := false
	prdBootstrapMacRouteForTests = func(_ context.Context, input laneinstall.PRDReplacementMacRouteInput, _ time.Time) (laneinstall.PRDReplacementMacRouteResult, error) {
		macRouteCalled = true
		if input.OperationID != request.OperationID || input.ReleaseID != release.ID {
			t.Fatal("Mac route continuation escaped the existing operation/release")
		}
		return laneinstall.PRDReplacementMacRouteResult{OperationID: request.OperationID, Stage: "complete",
			ConfigurationSHA256: digest([]byte("Mac route config")), JournalSHA256: digest([]byte("Mac route journal")),
			BackupManifestSHA256: digest([]byte("Mac route backup")), WorkInventorySHA256: digest([]byte("empty Mac work")), ServiceStopped: true}, nil
	}
	t.Cleanup(func() {
		prdBootstrapCSRForTests, prdBootstrapServerMaterialForTests, prdBootstrapMacRouteForTests = previousCSR, previousMaterial, previousMacRoute
	})
	linuxResult, _ := json.Marshal(laneinstall.PRDReplacementBootstrapResult{OperationID: request.OperationID, Stage: "linux_namespace_ready",
		Release: laneinstall.CatalogReleaseIdentity{ReleaseID: release.ID, ReleaseSHA256: release.Digest, HostID: "linux-host",
			BundleSHA256: release.Bundles["linux-host"].SHA256}, MacCandidateSHA256: digest([]byte("Mac route config")),
		JournalSHA256: journal.LinuxJournalSHA256})
	rootCorrection, _ := json.Marshal(laneinstall.PRDBootstrapRootModeCorrectionResult{OperationID: request.OperationID,
		ServiceRoot:  filepath.Join("/home", "ubuntu", ".local", "share", "remote-session-runner"),
		PreviousMode: 0o775, CurrentMode: 0o700, IntentSHA256: digest([]byte("intent")),
		CompletionSHA256: digest([]byte("completion")), ObservationSHA256: digest([]byte("observation"))})
	continuationBytes, err := os.ReadFile(continuationPath)
	if err != nil {
		t.Fatal(err)
	}
	continuationSHA := digest(continuationBytes)
	bootstrapRequest := request
	bootstrapRequest.ApprovalSHA256 = approvalSHA
	bootstrapRequest.ServerCSR256 = digest(csr.RequestPEM)
	bootstrapRequest.ServerCertificatePEM = append([]byte(nil), certificate...)
	bootstrapRequest.ServerCAPEM = append([]byte(nil), ca...)
	bootstrapRequestBytes, err := json.Marshal(bootstrapRequest)
	if err != nil {
		t.Fatal(err)
	}
	native.laneCommandRunner = func(_ context.Context, host string, args []string, _ []byte) ([]byte, error) {
		if host != "linux-host" {
			t.Fatalf("unexpected resume host: %s", host)
		}
		switch args[0] {
		case "catalog-prd-bootstrap-root-correct":
			if len(args) != 17 || args[1] != "--host" || args[2] != "linux-host" || args[3] != "--operation-id" ||
				args[4] != request.OperationID || args[5] != "--request-sha256" || args[6] != digest(requestBytes) ||
				args[7] != "--bootstrap-request-sha256" || args[8] != digest(bootstrapRequestBytes) ||
				args[9] != "--approval-sha256" || args[10] != approvalSHA || args[11] != "--release-id" ||
				args[12] != release.ID || args[13] != "--release-sha256" || args[14] != release.Digest ||
				args[15] != "--continuation-sha256" || args[16] != continuationSHA {
				t.Fatalf("root-mode correction lost its exact authorization bindings: %q", args)
			}
			return rootCorrection, nil
		case "catalog-prd-replacement-bootstrap":
			if len(args) != 2 || args[1] != "--request-stdin" {
				t.Fatalf("unexpected bootstrap resume command: %q", args)
			}
			return linuxResult, nil
		default:
			t.Fatalf("unexpected resume command: %q", args)
			return nil, ErrInvalidInput
		}
	}
	result, err := executePRDReplacementBootstrap(context.Background(), native, store, release, approval, approvalSHA,
		digest(requestBytes), request, continuationPath)
	if err != nil {
		t.Fatalf("resume same bootstrap under its bounded continuation: %v", err)
	}
	updated, found, err := readPRDReplacementBootstrapCoordinatorJournal(journalPath)
	if err != nil || !found || result.Stage != "ready_for_catalog_promotion" || !macRouteCalled ||
		!sameStrings(updated.CompletedStages, prdReplacementBootstrapStages) || updated.ApprovalSHA256 != approvalSHA ||
		updated.LinuxJournalSHA256 != journal.LinuxJournalSHA256 || continuation.OriginalApprovalSHA256 != approvalSHA {
		t.Fatalf("continued bootstrap did not finish while preserving original receipt identity: result=%+v journal=%+v found=%t err=%v", result, updated, found, err)
	}
	if result.RootModeCorrectionSHA256 != digest([]byte("completion")) {
		t.Fatalf("root-mode correction evidence was not returned: %+v", result)
	}
}

func continuationTestApproval(release Release, now time.Time) Approval {
	predecessors := make(map[string]string, len(HostOrder))
	for _, host := range HostOrder {
		predecessors[host] = digest([]byte("old-selector:" + host))
	}
	return Approval{FormatVersion: 1, ReleaseID: release.ID, ReleaseSHA256: release.Digest,
		HostSet: append([]string(nil), HostOrder...), WindowStart: now.Add(-3 * time.Hour).Format(time.RFC3339),
		WindowEnd: now.Add(-2 * time.Hour).Format(time.RFC3339), Owner: "Tomasz Walczuk", Scope: "replacement Linux bootstrap and four-host PRD deployment",
		PredecessorBundleSHA256: predecessors, BackupRecoveryPackageSHA256: digest([]byte("approved backups"))}
}

func continuationTestPlan(release Release, approval Approval) PromotionPlan {
	plan, _ := BuildPlan("prd", "all", release)
	plan.PredecessorBundleSHA256 = clonePRDReplacementStringMap(approval.PredecessorBundleSHA256)
	plan.PredecessorBundleSHA256["linux-host"] = digest([]byte("fresh-install predecessor"))
	plan.BackupRecoveryPackageSHA = digest([]byte("fresh preflight backup package"))
	required := promotionRequiredCapacity("prd", defaultPromotionOperatingBuffer)
	for _, host := range HostOrder {
		baseline := HostObservation{HostID: host, Lane: "prd", ServiceStopped: false,
			WorkInventorySHA: digest([]byte("empty:" + host)), WorkInventoryEmpty: true,
			ConfigurationSHA: digest([]byte("config:" + host)), SchemaVersion: 40, CapacityVerified: true,
			AvailableBytes: required + 100_000_000, RequiredBytes: required}
		if host == "linux-host" {
			baseline.ServiceStopped = true
			baseline.FreshInstallOperationID = prdBootstrapContinuationOperation
			baseline.FreshInstallEvidenceSHA256 = digest([]byte("fresh install baseline"))
			baseline.FreshInstallAbsenceSHA256 = digest([]byte("replacement namespace absence"))
			baseline.FreshInstallBootstrapReleaseID = release.ID
			baseline.FreshInstallBootstrapReleaseSHA256 = release.Digest
			baseline.FreshInstallBootstrapBundleSHA256 = release.Bundles[host].SHA256
			baseline.FreshInstallBootstrapJournalSHA256 = digest([]byte("bootstrap journal"))
		} else {
			if host == "mac-control-plane" {
				baseline.ServiceStopped = true
			}
			baseline.SelectedReleaseID = "cat-20261029-abcdef0"
			baseline.SelectedBundleSHA = approval.PredecessorBundleSHA256[host]
		}
		plan.HostBaselines = append(plan.HostBaselines, baseline)
	}
	return plan
}

func continuationTestProfile(t *testing.T, request laneinstall.PRDReplacementBootstrapRequest) (HostProfile, []byte, string, string) {
	t.Helper()
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
		KnownHostsFile: knownHostsPath, AllowFirstInstallPeerAbsence: true,
		Repository: "/home/ubuntu/projects/remote-session-runner", GoBinary: "/home/ubuntu/go/bin/go",
		RuntimeRoot: "/home/ubuntu/projects-runtime/runner-release-operations"}
	return profile, knownHost, identityPath, knownHostsPath
}

func continuationTestJournal(request laneinstall.PRDReplacementBootstrapRequest, requestBytes []byte, release Release,
	approval Approval, approvalBytes []byte, hostControlSHA string, now time.Time) PRDReplacementBootstrapCoordinatorJournal {
	return PRDReplacementBootstrapCoordinatorJournal{FormatVersion: prdReplacementCoordinatorJournalVersion,
		OperationID: request.OperationID, ReleaseID: release.ID, ReleaseSHA256: release.Digest,
		RequestSHA256: digest(requestBytes), ApprovalSHA256: digest(approvalBytes), PlanSHA256: digest([]byte("frozen original plan")),
		HostControlSHA256: hostControlSHA, PredecessorBundles: clonePRDReplacementStringMap(approval.PredecessorBundleSHA256),
		BackupPackageSHA256: approval.BackupRecoveryPackageSHA256, PublisherOwner: "Tomasz Walczuk", PublisherControl: "owner-confirmed PRD clients paused",
		CreatedAtUTC: now.Add(-4 * time.Hour).Format(time.RFC3339Nano), UpdatedAtUTC: now.Add(-time.Hour).Format(time.RFC3339Nano),
		CompletedStages:  append([]string(nil), prdReplacementBootstrapStages[:4]...),
		CSRRequestSHA256: digest([]byte("CSR")), ServerCertSHA256: digest([]byte("certificate")),
		ServerCASHA256: digest([]byte("CA")), LinuxJournalSHA256: digest([]byte("completed Linux namespace journal"))}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
