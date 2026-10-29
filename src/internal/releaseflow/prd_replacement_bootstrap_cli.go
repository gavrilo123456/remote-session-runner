package releaseflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

func executePRDReplacementBootstrap(ctx context.Context, native *NativePromotionBackend, workflowStore *Store,
	release Release, approval Approval, approvalSHA, requestSHA string,
	request laneinstall.PRDReplacementBootstrapRequest, continuationFile string) (laneinstall.PRDReplacementBootstrapResult, error) {
	if native == nil || workflowStore == nil || ValidateApprovalScope(approval, release) != nil ||
		request.OperationID == "" || request.ReleaseID != release.ID || request.ExpectedReleaseSHA256 != release.Digest ||
		request.ApprovalSHA256 != "" || request.ServerCSR256 != "" || len(request.ServerCertificatePEM) != 0 ||
		len(request.ServerCAPEM) != 0 || !request.PublishersPaused || !request.NoNewAccepted ||
		request.PublisherOwner == "" || request.PublisherControl == "" || requestSHA == "" {
		return laneinstall.PRDReplacementBootstrapResult{}, ErrInvalidInput
	}
	profile, ok := native.Config.Hosts["linux-host"]
	if !ok || profile.HostID != "linux-host" || profile.Target != replacementLinuxTarget ||
		!profile.AllowFirstInstallPeerAbsence || profile.KnownHostsFile == "" {
		return laneinstall.PRDReplacementBootstrapResult{}, ErrInvalidInput
	}
	lookup := prdBootstrapHostKeysForTests
	if lookup == nil {
		lookup = lookupPinnedReplacementHostKeys
	}
	knownHostOutput, err := lookup("79.72.94.59", profile.KnownHostsFile)
	if err != nil || !replacementHostKeyPinned(request.SSHHostPublicKey, knownHostOutput) {
		return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(ErrOperationCorrupted, err)
	}
	hostControlSHA, err := replacementBootstrapHostControlSHA(native, profile, request.SSHHostPublicKey, knownHostOutput)
	if err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}
	path, err := prdReplacementBootstrapCoordinatorJournalPath(workflowStore.Root, request.OperationID)
	if err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}
	journal, exists, err := readPRDReplacementBootstrapCoordinatorJournal(path)
	if err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}
	if ValidateApproval(approval, release, time.Now()) != nil {
		if !exists {
			return laneinstall.PRDReplacementBootstrapResult{}, ErrApprovalRequired
		}
		continuationPath, pathErr := prdReplacementBootstrapContinuationPath(workflowStore.Root, request.OperationID)
		if pathErr != nil || continuationFile != continuationPath {
			return laneinstall.PRDReplacementBootstrapResult{}, fmt.Errorf("expired bootstrap approval requires the operation's supported continuation record: %w", ErrApprovalRequired)
		}
		continuation, found, readErr := readPRDReplacementBootstrapContinuation(continuationPath)
		if readErr != nil || !found {
			return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(ErrApprovalRequired, readErr)
		}
		if err := validatePRDReplacementBootstrapContinuation(continuation, journal, request, requestSHA, release, approval,
			approvalSHA, request.PublisherOwner, request.PublisherControl, native.ControllerRevision, hostControlSHA, time.Now().UTC()); err != nil {
			return laneinstall.PRDReplacementBootstrapResult{}, fmt.Errorf("validate bootstrap continuation: %w", err)
		}
	}
	var plan PromotionPlan
	if exists {
		if fields := bootstrapCoordinatorIdentityMismatches(journal, request, requestSHA, release, approvalSHA,
			approval, request.PublisherOwner, request.PublisherControl); len(fields) != 0 {
			return laneinstall.PRDReplacementBootstrapResult{}, fmt.Errorf("bootstrap resume identity mismatch: %s", strings.Join(fields, ", "))
		}
		if !sameStringMap(journal.PredecessorBundles, approval.PredecessorBundleSHA256) {
			return laneinstall.PRDReplacementBootstrapResult{}, fmt.Errorf("bootstrap resume identity mismatch: predecessor_bundle_sha256")
		}
		if err := validatePRDReplacementBootstrapControllerUpdateForResume(native, journal, workflowStore.Root, request, requestSHA,
			release, approval, approvalSHA, request.PublisherOwner, request.PublisherControl, hostControlSHA, profile, knownHostOutput); err != nil {
			return laneinstall.PRDReplacementBootstrapResult{}, err
		}
	} else {
		if prdBootstrapPlanForTests != nil {
			plan, err = prdBootstrapPlanForTests(ctx, native, release, approvalSHA)
		} else {
			plan, err = native.Plan(ctx, "prd", release, approvalSHA)
		}
		if err != nil || !sameStringMap(approval.PredecessorBundleSHA256, plan.PredecessorBundleSHA256) ||
			approval.BackupRecoveryPackageSHA256 != plan.BackupRecoveryPackageSHA {
			return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(ErrApprovalRequired, err)
		}
		if !replacementBootstrapPlanIsComplete(plan, release) {
			return laneinstall.PRDReplacementBootstrapResult{}, ErrOperationCorrupted
		}
	}
	if len(request.ServerCertificatePEM) != 0 || len(request.ServerCAPEM) != 0 || request.ServerCSR256 != "" {
		return laneinstall.PRDReplacementBootstrapResult{}, ErrInvalidInput
	}
	if !exists {
		now := time.Now().UTC()
		journal = PRDReplacementBootstrapCoordinatorJournal{
			FormatVersion: prdReplacementCoordinatorJournalVersion, OperationID: request.OperationID,
			ReleaseID: release.ID, ReleaseSHA256: release.Digest, RequestSHA256: requestSHA, ApprovalSHA256: approvalSHA,
			PlanSHA256: hashJSON(plan), HostControlSHA256: hostControlSHA,
			PredecessorBundles:  clonePRDReplacementStringMap(plan.PredecessorBundleSHA256),
			BackupPackageSHA256: plan.BackupRecoveryPackageSHA, PublisherOwner: request.PublisherOwner,
			PublisherControl: request.PublisherControl, CreatedAtUTC: now.Format(time.RFC3339Nano),
			UpdatedAtUTC: now.Format(time.RFC3339Nano), CompletedStages: []string{prdReplacementBootstrapStages[0]},
		}
		if err := writePRDReplacementBootstrapCoordinatorJournal(path, journal); err != nil {
			return laneinstall.PRDReplacementBootstrapResult{}, fmt.Errorf("persist replacement bootstrap intent: %+v: %w", journal, err)
		}
	}
	if !sameStringMap(approval.PredecessorBundleSHA256, journal.PredecessorBundles) ||
		approval.BackupRecoveryPackageSHA256 != journal.BackupPackageSHA256 {
		return laneinstall.PRDReplacementBootstrapResult{}, ErrApprovalRequired
	}

	csrReport, err := obtainPRDReplacementServerCSR(ctx, native, request.OperationID)
	if err != nil || csrReport.OperationID != request.OperationID || len(csrReport.RequestPEM) == 0 ||
		digest(csrReport.RequestPEM) != csrReport.RequestSHA || !digestPattern.MatchString(csrReport.RequestSHA) {
		return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(ErrOperationCorrupted, err)
	}
	if err := recordPRDReplacementBootstrapStage(path, &journal, "server_csr_ready", func() error {
		if journal.CSRRequestSHA256 != "" && journal.CSRRequestSHA256 != csrReport.RequestSHA {
			return ErrOperationConflict
		}
		journal.CSRRequestSHA256 = csrReport.RequestSHA
		return nil
	}); err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}

	var serverMaterial laneinstall.PRDReplacementServerMaterialReport
	if prdBootstrapServerMaterialForTests != nil {
		serverMaterial, err = prdBootstrapServerMaterialForTests(request.OperationID, csrReport.RequestPEM, time.Now().UTC())
	} else {
		serverMaterial, err = laneinstall.PreparePRDReplacementServerMaterial(request.OperationID, csrReport.RequestPEM, time.Now().UTC())
	}
	if err != nil || serverMaterial.OperationID != request.OperationID || serverMaterial.RequestSHA256 != csrReport.RequestSHA ||
		digest(serverMaterial.CertificatePEM) != serverMaterial.CertificateSHA || digest(serverMaterial.CAPEM) != serverMaterial.CASHA ||
		!digestPattern.MatchString(serverMaterial.CertificateSHA) || !digestPattern.MatchString(serverMaterial.CASHA) {
		return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(ErrOperationCorrupted, err)
	}
	if err := recordPRDReplacementBootstrapStage(path, &journal, "server_material_ready", func() error {
		if journal.ServerCertSHA256 != "" && journal.ServerCertSHA256 != serverMaterial.CertificateSHA ||
			journal.ServerCASHA256 != "" && journal.ServerCASHA256 != serverMaterial.CASHA {
			return ErrOperationConflict
		}
		journal.ServerCertSHA256, journal.ServerCASHA256 = serverMaterial.CertificateSHA, serverMaterial.CASHA
		return nil
	}); err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}

	request.ApprovalSHA256 = approvalSHA
	request.ServerCSR256 = csrReport.RequestSHA
	request.ServerCertificatePEM = append([]byte(nil), serverMaterial.CertificatePEM...)
	request.ServerCAPEM = append([]byte(nil), serverMaterial.CAPEM...)
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}
	out, err := native.runLane(ctx, "linux-host", []string{"catalog-prd-replacement-bootstrap", "--request-stdin"}, requestBytes)
	if err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(errors.New("replacement PRD bootstrap did not complete"), err)
	}
	var result laneinstall.PRDReplacementBootstrapResult
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		result.OperationID != request.OperationID || result.Release.ReleaseID != request.ReleaseID ||
		result.Release.ReleaseSHA256 != request.ExpectedReleaseSHA256 || result.Release.HostID != "linux-host" ||
		result.Release.BundleSHA256 != release.Bundles["linux-host"].SHA256 || result.Stage != "linux_namespace_ready" ||
		!digestPattern.MatchString(result.JournalSHA256) || !digestPattern.MatchString(result.MacCandidateSHA256) {
		return laneinstall.PRDReplacementBootstrapResult{}, ErrOperationCorrupted
	}
	if err := recordPRDReplacementBootstrapStage(path, &journal, "linux_namespace_ready", func() error {
		if journal.LinuxJournalSHA256 != "" && journal.LinuxJournalSHA256 != result.JournalSHA256 {
			return ErrOperationConflict
		}
		journal.LinuxJournalSHA256 = result.JournalSHA256
		return nil
	}); err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}

	macInput := laneinstall.PRDReplacementMacRouteInput{OperationID: request.OperationID,
		MacConfiguration: request.MacConfiguration, ReleaseID: request.ReleaseID,
		ExpectedReleaseSHA256: request.ExpectedReleaseSHA256, ServerCertificatePEM: request.ServerCertificatePEM,
		ServerCAPEM: request.ServerCAPEM, ClientCertificatePEM: request.ClientCertificatePEM,
		ClientCAPEM: request.ClientCAPEM, ClientPrincipalMap: request.ClientPrincipalMap,
		DispatcherPublicKey: request.DispatcherPublicKey, SSHHostPublicKey: request.SSHHostPublicKey}
	var macResult laneinstall.PRDReplacementMacRouteResult
	if prdBootstrapMacRouteForTests != nil {
		macResult, err = prdBootstrapMacRouteForTests(ctx, macInput, time.Now().UTC())
	} else {
		macResult, err = laneinstall.RunPRDReplacementMacRoute(ctx, macInput, time.Now().UTC())
	}
	if err != nil || macResult.OperationID != request.OperationID || macResult.Stage != "complete" || !macResult.ServiceStopped ||
		macResult.ConfigurationSHA256 != result.MacCandidateSHA256 || !digestPattern.MatchString(macResult.JournalSHA256) ||
		!digestPattern.MatchString(macResult.BackupManifestSHA256) || !digestPattern.MatchString(macResult.WorkInventorySHA256) {
		return laneinstall.PRDReplacementBootstrapResult{}, errors.Join(errors.New("replacement Linux namespace is prepared but Mac PRD route postflight is incomplete"), err)
	}
	if err := recordPRDReplacementBootstrapStage(path, &journal, "mac_route_complete", func() error {
		if journal.MacJournalSHA256 != "" && journal.MacJournalSHA256 != macResult.JournalSHA256 {
			return ErrOperationConflict
		}
		journal.MacJournalSHA256 = macResult.JournalSHA256
		return nil
	}); err != nil {
		return laneinstall.PRDReplacementBootstrapResult{}, err
	}
	result.MacRoute = &macResult
	result.Stage = "ready_for_catalog_promotion"
	return result, nil
}

func replacementBootstrapHostControlSHA(native *NativePromotionBackend, profile HostProfile, serverHostKey string,
	knownHostOutput []byte) (string, error) {
	if native == nil || !shaPattern.MatchString(native.ControllerRevision) || ownerPrivateKeyFile(profile.IdentityFile) != nil ||
		ownerFile(profile.KnownHostsFile, 0) != nil || len(knownHostOutput) == 0 || len(knownHostOutput) > 64<<10 {
		return "", ErrOperationCorrupted
	}
	identityBytes, err := os.ReadFile(profile.IdentityFile)
	if err != nil || len(identityBytes) == 0 || len(identityBytes) > 64<<10 {
		return "", ErrOperationCorrupted
	}
	knownHostsBytes, err := os.ReadFile(profile.KnownHostsFile)
	if err != nil || len(knownHostsBytes) == 0 || len(knownHostsBytes) > 64<<10 {
		return "", ErrOperationCorrupted
	}
	canonicalHostKey, ok := canonicalEd25519HostKey(serverHostKey)
	if !ok {
		return "", ErrOperationCorrupted
	}
	return hashJSON(map[string]string{
		"host_id": profile.HostID, "target": profile.Target, "identity_file": profile.IdentityFile,
		"identity_sha256": digest(identityBytes), "known_hosts_file": profile.KnownHostsFile,
		"known_hosts_sha256": digest(knownHostsBytes), "pinned_host_key_sha256": digest(knownHostOutput),
		"controller_revision": native.ControllerRevision, "repository": profile.Repository,
		"go_binary": profile.GoBinary, "runtime_root": profile.RuntimeRoot, "server_host_key": canonicalHostKey,
	}), nil
}

func clonePRDReplacementStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func replacementBootstrapPlanIsComplete(plan PromotionPlan, release Release) bool {
	if plan.Lane != "prd" || plan.Target != "all" || plan.ReleaseID != release.ID || plan.ReleaseSHA256 != release.Digest ||
		!sameStrings(plan.AffectedHosts, HostOrder) || !sameStrings(plan.HostOrder, HostOrder) ||
		len(plan.HostBaselines) != len(HostOrder) || len(plan.PredecessorBundleSHA256) != len(HostOrder) ||
		len(plan.ExistingHolds) != 0 || !digestPattern.MatchString(plan.BackupRecoveryPackageSHA) {
		return false
	}
	for index, host := range HostOrder {
		baseline := plan.HostBaselines[index]
		if baseline.HostID != host || baseline.Lane != "prd" || !baseline.WorkInventoryEmpty || !baseline.CapacityVerified ||
			baseline.AvailableBytes < baseline.RequiredBytes || !digestPattern.MatchString(plan.PredecessorBundleSHA256[host]) {
			return false
		}
		if host == "linux-host" {
			if baseline.SelectedNamespaceAbsence == nil || baseline.SelectedNamespaceAbsence.State != "absent" ||
				promotionPRDAbsencePredecessorSHA(*baseline.SelectedNamespaceAbsence) != plan.PredecessorBundleSHA256[host] {
				return false
			}
			continue
		}
		if !digestPattern.MatchString(baseline.ConfigurationSHA) || baseline.SchemaVersion <= 0 ||
			!digestPattern.MatchString(baseline.WorkInventorySHA) {
			return false
		}
	}
	return true
}

func recordPRDReplacementBootstrapStage(path string, journal *PRDReplacementBootstrapCoordinatorJournal,
	stage string, update func() error) error {
	if journal == nil || update == nil || validatePRDReplacementBootstrapCoordinatorJournal(*journal) != nil {
		return ErrOperationCorrupted
	}
	stageIndex := -1
	for index, candidate := range prdReplacementBootstrapStages {
		if candidate == stage {
			stageIndex = index
			break
		}
	}
	if stageIndex < 1 {
		return ErrOperationCorrupted
	}
	if len(journal.CompletedStages) > stageIndex {
		return update()
	}
	if len(journal.CompletedStages) != stageIndex {
		return ErrOperationCorrupted
	}
	if err := update(); err != nil {
		return err
	}
	if err := appendPRDReplacementBootstrapStage(journal, stage, time.Now().UTC()); err != nil {
		return err
	}
	return writePRDReplacementBootstrapCoordinatorJournal(path, *journal)
}
