package laneinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/lanecapacity"
)

var (
	ErrInvalidPreflight   = errors.New("lane preflight rejected the selected host, lane, profile, or measured policy")
	ErrStagedStart        = errors.New("staged lane start failed")
	ErrRollbackUnverified = errors.New("lane rollback did not restore the captured service state")
)

const stagedRollbackTimeout = 30 * time.Second

// PreflightInput combines only validated configuration references and a fresh
// capacity measurement with the fixed lane builders. It contains no key or
// certificate bytes and accepts no install path, service label, port, or
// controller override.
type PreflightInput struct {
	HostID       string
	Lane         config.ReleaseLane
	Profiles     config.ValidatedV3Profiles
	PeerProfiles []config.ValidatedV3Profiles
	Policy       lanecapacity.Policy
	Measurement  lanecapacity.Measurement
	Live         lanecapacity.LiveObservation
	Now          time.Time
}

// LanePreflightPlan is a validated, non-mutating candidate description. Its
// fields are private so callers cannot edit a checked path or artifact before
// handing it to the staged-start protocol.
type LanePreflightPlan struct {
	hostID         string
	lane           config.ReleaseLane
	service        Plan
	artifacts      []Artifact
	directRoutes   []DirectTLSPlan
	queuedSSHPorts []int
	measurementID  string
	policyDigest   string
	planDigest     string
}

type PreflightArtifact struct {
	Kind        ArtifactKind `json:"kind"`
	InstallPath string       `json:"install_path"`
	Mode        uint32       `json:"mode"`
	Owner       string       `json:"owner,omitempty"`
	Group       string       `json:"group,omitempty"`
	SHA256      string       `json:"sha256"`
}

type PreflightEndpoint struct {
	Profile string `json:"profile"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Bind    string `json:"bind"`
}

// PreflightReport is the safe dry-run output. Credential references and
// contents are never included; artifact hashes are over rendered bytes.
type PreflightReport struct {
	Status            string              `json:"status"`
	Action            string              `json:"action"`
	HostID            string              `json:"host_id"`
	Lane              config.ReleaseLane  `json:"lane"`
	ServiceRoot       string              `json:"service_root"`
	QueuePorts        []int               `json:"queued_ssh_ports"`
	DirectEndpoints   []PreflightEndpoint `json:"direct_endpoints"`
	ResourceControls  string              `json:"resource_controls"`
	ArtifactCount     int                 `json:"artifact_count"`
	Artifacts         []PreflightArtifact `json:"artifacts"`
	NativeAcceptance  string              `json:"native_acceptance"`
	ProductionChanges string              `json:"production_changes"`
}

// BuildLanePreflightPlan validates one fixed Dev/QAS installation proposal
// without reading credentials or changing services, resources, storage, or
// mailbox state. Linux plans include measured resource-control artifacts and
// quota proposals; Mac plans include the measured APFS control contract.
func BuildLanePreflightPlan(input PreflightInput) (LanePreflightPlan, error) {
	if (input.Lane != config.ReleaseLaneDev && input.Lane != config.ReleaseLaneQAS) || input.Now.IsZero() {
		return LanePreflightPlan{}, ErrInvalidPreflight
	}
	tuple, exists := canonicalHosts[input.HostID]
	if !exists || input.Profiles.Lane() != input.Lane || input.Profiles.Host() != tuple.kind {
		return LanePreflightPlan{}, ErrInvalidPreflight
	}
	if tuple.kind == config.HostKindMac {
		if input.HostID != "mac-control-plane" || input.Profiles.RemoteProfile() != "" {
			return LanePreflightPlan{}, ErrInvalidPreflight
		}
	} else if input.Profiles.RemoteProfile() != input.HostID {
		return LanePreflightPlan{}, ErrInvalidPreflight
	}
	if input.Policy.Scope != lanecapacity.ScopeLaneRuntime || input.Policy.HostID != input.HostID || input.Policy.Lane != string(input.Lane) ||
		input.Policy.HostManagement != nil || input.Policy.LaneRuntime == nil || input.Measurement.Identity.HostID != input.HostID {
		return LanePreflightPlan{}, ErrInvalidPreflight
	}

	service, err := BuildPlan(input.HostID, input.Lane)
	if err != nil {
		return LanePreflightPlan{}, fmt.Errorf("%w: fixed service plan", ErrInvalidPreflight)
	}
	if err := validatePreflightProfiles(input.Profiles, input.PeerProfiles, service); err != nil {
		return LanePreflightPlan{}, err
	}

	artifacts := append([]Artifact(nil), service.Artifacts...)
	directRoutes := make([]DirectTLSPlan, 0, 3)
	queuePorts := make([]int, 0, 3)
	if tuple.kind == config.HostKindMac {
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			route, buildErr := BuildDirectTLSPlan(input.Lane, profile)
			if buildErr != nil {
				return LanePreflightPlan{}, fmt.Errorf("%w: direct endpoint map", ErrInvalidPreflight)
			}
			directRoutes = append(directRoutes, route)
			ns, nsErr := config.DeriveLaneNamespace(config.HostKindLinux, input.Lane, profile)
			if nsErr != nil {
				return LanePreflightPlan{}, fmt.Errorf("%w: fixed queued profile", ErrInvalidPreflight)
			}
			queuePorts = append(queuePorts, ns.QueuePort)
		}
		if _, err := BuildMacStorageControlPlan(input.Policy, input.Measurement, input.Live, input.Now); err != nil {
			return LanePreflightPlan{}, fmt.Errorf("%w: Mac storage preflight: %v", ErrInvalidPreflight, err)
		}
	} else {
		route, buildErr := BuildDirectTLSPlan(input.Lane, input.HostID)
		if buildErr != nil {
			return LanePreflightPlan{}, fmt.Errorf("%w: direct endpoint map", ErrInvalidPreflight)
		}
		directRoutes = append(directRoutes, route)
		queuePorts = append(queuePorts, service.Namespace.QueuePort)
		controls, controlErr := BuildLinuxControlArtifacts(input.Policy, input.Measurement, input.Live, input.Now)
		if controlErr != nil {
			return LanePreflightPlan{}, fmt.Errorf("%w: Linux capacity preflight: %v", ErrInvalidPreflight, controlErr)
		}
		quota, quotaErr := BuildLinuxProjectQuotaPlan(input.Policy, input.Measurement, input.Live, input.Now)
		if quotaErr != nil {
			return LanePreflightPlan{}, fmt.Errorf("%w: Linux quota preflight: %v", ErrInvalidPreflight, quotaErr)
		}
		artifacts = append(artifacts, controls...)
		artifacts = append(artifacts, quota.Artifacts...)
	}

	slices.Sort(queuePorts)
	queuePorts = slices.Compact(queuePorts)
	if err := validatePreflightArtifacts(service, artifacts); err != nil {
		return LanePreflightPlan{}, err
	}
	plan := LanePreflightPlan{
		hostID: input.HostID, lane: input.Lane, service: service,
		artifacts: cloneArtifacts(artifacts), directRoutes: append([]DirectTLSPlan(nil), directRoutes...),
		queuedSSHPorts: append([]int(nil), queuePorts...), measurementID: input.Measurement.ID,
		policyDigest: input.Policy.EvaluationDigest,
	}
	plan.planDigest, err = plan.integrityDigest()
	if err != nil {
		return LanePreflightPlan{}, fmt.Errorf("%w: digest fixed plan", ErrInvalidPreflight)
	}
	return plan, nil
}

func validatePreflightProfiles(profiles config.ValidatedV3Profiles, peers []config.ValidatedV3Profiles, service Plan) error {
	if service.Namespace.Host == config.HostKindMac {
		peerByProfile := make(map[string]config.ValidatedV3Profiles, len(peers))
		for _, peer := range peers {
			if peer.Host() != config.HostKindLinux || peer.Lane() != service.Lane {
				return ErrInvalidPreflight
			}
			profile := peer.RemoteProfile()
			if _, duplicate := peerByProfile[profile]; duplicate {
				return ErrInvalidPreflight
			}
			peerByProfile[profile] = peer
		}
		if len(peerByProfile) != 3 {
			return ErrInvalidPreflight
		}
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			binding, ok := profiles.MacProfile(profile)
			if !ok {
				return ErrInvalidPreflight
			}
			peer, ok := peerByProfile[profile]
			if !ok || config.ValidateV3MacLinuxProfilePair(profiles, peer, profile) != nil {
				return ErrInvalidPreflight
			}
			route, err := BuildDirectTLSPlan(service.Lane, profile)
			if err != nil || !matchesMacPreflightRoute(binding, route) {
				return ErrInvalidPreflight
			}
			ns, err := config.DeriveLaneNamespace(config.HostKindLinux, service.Lane, profile)
			if err != nil || binding.Queued.Host != route.PublicIP || binding.Queued.Port != ns.QueuePort ||
				(binding.Queued.Port != 2224 && binding.Queued.Port != 2225) {
				return ErrInvalidPreflight
			}
		}
		return nil
	}
	if len(peers) != 1 || peers[0].Host() != config.HostKindMac || peers[0].Lane() != service.Lane ||
		config.ValidateV3MacLinuxProfilePair(peers[0], profiles, service.HostID) != nil {
		return ErrInvalidPreflight
	}
	linux, ok := profiles.LinuxProfileBinding()
	if !ok {
		return ErrInvalidPreflight
	}
	route, err := BuildDirectTLSPlan(service.Lane, service.HostID)
	if err != nil || !matchesLinuxPreflightRoute(linux.Direct, route) {
		return ErrInvalidPreflight
	}
	bindHost, _, err := net.SplitHostPort(service.Namespace.DirectHTTPSBind)
	if err != nil {
		return ErrInvalidPreflight
	}
	wantQueueBind := net.JoinHostPort(bindHost, fmt.Sprint(service.Namespace.QueuePort))
	wantHostKey := filepath.Join(service.Namespace.SSHAuthorityPath, "ssh_host_ed25519_key")
	if linux.Queued.Host != route.PublicIP || linux.Queued.Port != service.Namespace.QueuePort ||
		linux.Queued.Bind != wantQueueBind || linux.Queued.HostKeyFile != wantHostKey ||
		linux.Queued.Socket != service.Namespace.PrivateSocket ||
		(linux.Queued.Port != 2224 && linux.Queued.Port != 2225) {
		return ErrInvalidPreflight
	}
	return nil
}

func matchesMacPreflightRoute(binding config.V3MacRemoteProfile, route DirectTLSPlan) bool {
	direct := binding.Direct
	return direct.Name == route.Name && direct.URL == route.PublicURL && direct.ClientURISAN == route.ClientURISAN &&
		direct.ServerURISAN == route.ServerURISAN && direct.ControllerID == route.ControllerID && direct.ControllerType == route.ControllerType
}

func matchesLinuxPreflightRoute(direct config.V3LinuxDirectCredentials, route DirectTLSPlan) bool {
	return direct.Name == route.Name && direct.URL == route.PublicURL && direct.Bind == route.Bind &&
		direct.ClientURISAN == route.ClientURISAN && direct.ServerURISAN == route.ServerURISAN &&
		direct.ControllerID == route.ControllerID && direct.ControllerType == route.ControllerType
}

func validatePreflightArtifacts(service Plan, artifacts []Artifact) error {
	prd, err := config.DeriveLaneNamespace(service.Namespace.Host, config.ReleaseLanePRD, service.Namespace.RemoteProfile)
	if err != nil {
		return fmt.Errorf("%w: derive protected PRD namespace", ErrInvalidPreflight)
	}
	for _, artifact := range artifacts {
		if artifact.InstallPath == "" || artifact.Contents == "" ||
			containsPathReference(artifact.InstallPath, prd.ServiceRoot) || containsPathReference(artifact.InstallPath, prd.MailboxRoot) ||
			strings.Contains(artifact.InstallPath, "/etc/ssh/sshd_config") ||
			strings.Contains(artifact.InstallPath, "/home/ubuntu/.ssh/authorized_keys") ||
			containsPathReference(artifact.Contents, prd.ServiceRoot) || containsPathReference(artifact.Contents, prd.MailboxRoot) ||
			strings.Contains(artifact.Contents, "/home/ubuntu/.ssh/authorized_keys") ||
			strings.Contains(artifact.Contents, "Port 22\n") {
			return fmt.Errorf("%w: artifact could select PRD or host-global SSH state", ErrInvalidPreflight)
		}
		if artifact.Kind == ArtifactSystemdUnit && strings.Contains(artifact.InstallPath, service.Namespace.RunnerUnit) &&
			!strings.Contains(artifact.Contents, "Slice="+service.Namespace.RunnerSlice+"\n") {
			return fmt.Errorf("%w: lane service escaped its fixed resource slice", ErrInvalidPreflight)
		}
		if artifact.Kind == ArtifactSystemdDropIn && strings.Contains(artifact.InstallPath, ".service.d/") &&
			!strings.Contains(artifact.Contents, "Slice="+service.Namespace.RunnerSlice+"\n") {
			return fmt.Errorf("%w: lane service drop-in escaped its fixed resource slice", ErrInvalidPreflight)
		}
		if artifact.Kind == ArtifactSystemdDropIn && strings.Contains(artifact.InstallPath, ".slice.d/") &&
			!strings.HasPrefix(artifact.Contents, "[Slice]\n") {
			return fmt.Errorf("%w: resource drop-in is not a slice contract", ErrInvalidPreflight)
		}
	}
	return nil
}

func containsPathReference(value, root string) bool {
	if root == "" {
		return false
	}
	for offset := 0; offset < len(value); {
		index := strings.Index(value[offset:], root)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(root)
		leftOK := index == 0 || strings.ContainsRune("=:\"' \t\r\n,", rune(value[index-1]))
		rightOK := end == len(value) || strings.ContainsRune("/\"' \t\r\n,;)", rune(value[end]))
		if leftOK && rightOK {
			return true
		}
		offset = index + 1
	}
	return false
}

func cloneArtifacts(artifacts []Artifact) []Artifact { return append([]Artifact(nil), artifacts...) }

func (p LanePreflightPlan) integrityDigest() (string, error) {
	data, err := json.Marshal(struct {
		HostID        string
		Lane          config.ReleaseLane
		Service       Plan
		Artifacts     []Artifact
		DirectRoutes  []DirectTLSPlan
		QueuePorts    []int
		MeasurementID string
		PolicyDigest  string
	}{p.hostID, p.lane, p.service, p.artifacts, p.directRoutes, p.queuedSSHPorts, p.measurementID, p.policyDigest})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (p LanePreflightPlan) valid() bool {
	if p.hostID == "" || p.lane == "" || p.planDigest == "" || len(p.artifacts) == 0 {
		return false
	}
	digest, err := p.integrityDigest()
	return err == nil && digest == p.planDigest
}

func (p LanePreflightPlan) Artifacts() []Artifact { return cloneArtifacts(p.artifacts) }

// Report returns paths, permissions, and content hashes only. It deliberately
// omits credential references, certificate fingerprints, and file contents.
func (p LanePreflightPlan) Report() (PreflightReport, error) {
	if !p.valid() {
		return PreflightReport{}, ErrInvalidPreflight
	}
	report := PreflightReport{
		Status: "validated_dry_run", Action: "none", HostID: p.hostID, Lane: p.lane,
		ServiceRoot: p.service.Namespace.ServiceRoot, QueuePorts: append([]int(nil), p.queuedSSHPorts...),
		ResourceControls: "linux-cgroup-v2-and-project-quota",
		NativeAcceptance: "not_performed", ProductionChanges: "none",
	}
	if p.service.Namespace.Host == config.HostKindMac {
		report.ResourceControls = "measured-apfs-quota-and-headroom"
	}
	for _, route := range p.directRoutes {
		report.DirectEndpoints = append(report.DirectEndpoints, PreflightEndpoint{Profile: route.Profile, Name: route.Name, URL: route.PublicURL, Bind: route.Bind})
	}
	for _, artifact := range p.artifacts {
		digest := sha256.Sum256([]byte(artifact.Contents))
		report.Artifacts = append(report.Artifacts, PreflightArtifact{
			Kind: artifact.Kind, InstallPath: artifact.InstallPath, Mode: artifact.Mode, Owner: artifact.Owner,
			Group: artifact.Group, SHA256: hex.EncodeToString(digest[:]),
		})
	}
	report.ArtifactCount = len(report.Artifacts)
	return report, nil
}

type StageReceipt string

// StagedStartBackend is intentionally narrow: every operation is bound to the
// already validated fixed plan. PrepareRollback must capture prior target
// state before any stage mutation. StartCandidateClosed has no open/admit
// operation; implementations used by F must run only in disposable fixtures.
type StagedStartBackend interface {
	PrepareRollback(context.Context, LanePreflightPlan) (StageReceipt, error)
	StageCandidate(context.Context, StageReceipt, []Artifact) error
	StartCandidateClosed(context.Context, StageReceipt) error
	VerifyCandidateHealthyClosed(context.Context, StageReceipt) error
	RollbackCandidate(context.Context, StageReceipt) error
	VerifyRollback(context.Context, StageReceipt) error
}

// RunStagedStart performs only stage, admission-closed start, and health proof.
// It never opens a candidate or changes an active pointer. Any failure after a
// rollback receipt exists invokes rollback and verifies restoration.
func RunStagedStart(ctx context.Context, plan LanePreflightPlan, backend StagedStartBackend) error {
	if !plan.valid() || backend == nil || ctx == nil {
		return ErrInvalidPreflight
	}
	receipt, err := backend.PrepareRollback(ctx, plan)
	if err != nil {
		return errors.Join(ErrStagedStart, err)
	}
	if receipt == "" {
		return errors.Join(ErrStagedStart, errors.New("missing rollback receipt"))
	}
	fail := func(cause error) error {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stagedRollbackTimeout)
		defer cancel()
		rollbackErr := backend.RollbackCandidate(rollbackCtx, receipt)
		verifyErr := backend.VerifyRollback(rollbackCtx, receipt)
		if rollbackErr != nil || verifyErr != nil {
			return errors.Join(ErrRollbackUnverified, ErrStagedStart, cause, rollbackErr, verifyErr)
		}
		return errors.Join(ErrStagedStart, cause)
	}
	if err := backend.StageCandidate(ctx, receipt, plan.Artifacts()); err != nil {
		return fail(err)
	}
	if err := backend.StartCandidateClosed(ctx, receipt); err != nil {
		return fail(err)
	}
	if err := backend.VerifyCandidateHealthyClosed(ctx, receipt); err != nil {
		return fail(err)
	}
	return nil
}
