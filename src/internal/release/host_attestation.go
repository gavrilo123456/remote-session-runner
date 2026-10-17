package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"

	"remote-session-runner/src/internal/lanecapacity"
)

var ErrHostAttestationUnavailable = errors.New("host attestation evidence unavailable")

const hostEvidenceMaxAge = time.Minute

// HostInstallObservation is the fixed helper's read-only result for one
// already verified promotion authorization. It contains measured identities
// and content digests, never paths or caller-provided installation values.
type HostInstallObservation struct {
	Components   []componentArtifact
	Outcome      string
	EvidenceRefs []string
	ObservedAt   time.Time
}

type HostIngressSurfaceObservation struct {
	SurfaceID      string
	Outcome        string
	EvidenceDigest string
}

// HostAttestationEvidenceSource reads only the local protected inventory,
// verified record cache, installed component state, and ingress surfaces. Its
// methods accept no paths, measured values, or surface inventories from the
// request peer. Verified record methods must enforce anchor, signature, ledger,
// host, lane, and time checks before returning payload bytes.
type HostAttestationEvidenceSource interface {
	MeasureCapacity(context.Context) (lanecapacity.Measurement, []string, error)
	VerifiedPromotionAuthorization(context.Context, string) ([]byte, error)
	ObserveInstallation(context.Context, []byte) (HostInstallObservation, error)
	VerifiedAppliedIngressDirective(context.Context) (string, []byte, error)
	ObserveIngressSurfaces(context.Context, []byte) ([]HostIngressSurfaceObservation, error)
}

// HostAttestationRecordPublisher is the root signer/outbox boundary. It sees
// only a closed payload and record type; it receives no paths, keys, or source
// data. The signer validates the local host again when publishing.
type HostAttestationRecordPublisher interface {
	PublishHostAttestation(context.Context, string, []byte) (string, error)
}

type HostAttestationService struct {
	hostID    string
	source    HostAttestationEvidenceSource
	publisher HostAttestationRecordPublisher
	now       func() time.Time
}

// NewHostAttestationService can be constructed only by the root helper from
// its fixed security anchor. The anchor supplies the immutable canonical host
// identity; native platform facts must match that identity before the service
// is exposed through the constrained release endpoint.
func NewHostAttestationService(anchor *SecurityAnchor, source HostAttestationEvidenceSource,
	publisher HostAttestationRecordPublisher) (*HostAttestationService, error) {
	if os.Geteuid() != 0 || anchor == nil || source == nil || publisher == nil {
		return nil, fmt.Errorf("%w: root helper, fixed anchor, observer, and signer required", ErrHostAttestationUnavailable)
	}
	state, err := anchor.Snapshot()
	if err != nil || !isCanonicalHostID(state.HostID) || len(state.HostAttestationKeys) == 0 {
		return nil, fmt.Errorf("%w: initialized host-attestation anchor required", ErrHostAttestationUnavailable)
	}
	if _, err := observeNativeHostEvidence(state.HostID, time.Now()); err != nil {
		return nil, fmt.Errorf("%w: anchor identity does not match native host: %v", ErrHostAttestationUnavailable, err)
	}
	return &HostAttestationService{hostID: state.HostID, source: source, publisher: publisher, now: time.Now}, nil
}

// AttestCapacity implements the fixed no-body release operation. Inventory is
// independently measured by the local evidence source and bound to this
// helper's anchored host before the signed record is published.
func (s *HostAttestationService) AttestCapacity(ctx context.Context) (any, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	measurement, evidenceRefs, err := s.source.MeasureCapacity(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: capacity measurement: %v", ErrHostAttestationUnavailable, err)
	}
	now := s.now().UTC()
	if measurement.Identity.HostID != s.hostID || !hostEvidenceTimeValid(measurement.ObservedAt, measurement.ExpiresAt, now) {
		return nil, fmt.Errorf("%w: capacity identity or freshness mismatch", ErrHostAttestationUnavailable)
	}
	digest, err := measurement.EvaluationDigest()
	if err != nil || !sortedUnique(evidenceRefs) || len(evidenceRefs) == 0 {
		return nil, fmt.Errorf("%w: incomplete capacity evidence", ErrHostAttestationUnavailable)
	}
	payload := capacityMeasurementPayload{HostID: s.hostID, Measurement: measurement,
		EvidenceRefs: append([]string(nil), evidenceRefs...), EvaluationDigest: digest}
	raw, err := json.Marshal(payload)
	if err != nil || ValidateCapacityMeasurementAttestation(raw) != nil {
		return nil, fmt.Errorf("%w: invalid capacity attestation payload", ErrHostAttestationUnavailable)
	}
	return s.publish(ctx, "capacity_measurement_attestation", raw)
}

// AttestPromotion accepts only an opaque verified authorization ID. The local
// observer obtains installation evidence itself and the result is bound back
// to the complete signed authorization before publication.
func (s *HostAttestationService) AttestPromotion(ctx context.Context, authorizationID string) (string, error) {
	if err := s.ready(ctx); err != nil {
		return "", err
	}
	if !isLowerHexDigest(authorizationID) {
		return "", fmt.Errorf("%w: invalid promotion request", ErrHostAttestationUnavailable)
	}
	authorizationRaw, err := s.source.VerifiedPromotionAuthorization(ctx, authorizationID)
	if err != nil || ValidatePromotionAuthorization(authorizationRaw) != nil {
		return "", fmt.Errorf("%w: promotion authorization is not verified", ErrHostAttestationUnavailable)
	}
	var authorization promotionAuthorizationPayload
	if decodeStrict(authorizationRaw, &authorization) != nil || authorization.HostID != s.hostID {
		return "", fmt.Errorf("%w: authorization is not for the anchored host", ErrHostAttestationUnavailable)
	}
	now := s.now().UTC()
	notBefore, _ := time.Parse(time.RFC3339Nano, authorization.NotBefore)
	notAfter, _ := time.Parse(time.RFC3339Nano, authorization.NotAfter)
	if now.Before(notBefore) || !now.Before(notAfter) {
		return "", fmt.Errorf("%w: promotion authorization is outside its validity window", ErrHostAttestationUnavailable)
	}
	observed, err := s.source.ObserveInstallation(ctx, append([]byte(nil), authorizationRaw...))
	if err != nil || !hostEvidenceTimestampFresh(observed.ObservedAt, now) ||
		(observed.Outcome != "accepted" && observed.Outcome != "rejected") || !sortedUnique(observed.EvidenceRefs) || len(observed.EvidenceRefs) == 0 {
		return "", fmt.Errorf("%w: incomplete local installation evidence", ErrHostAttestationUnavailable)
	}
	payload := promotionAttestationPayload{AuthorizationID: authorizationID, CandidateID: authorization.CandidateManifestID,
		TransitionID: authorization.LaneTransitionID, CapacityPolicyID: authorization.CapacityPolicyID,
		Lane: authorization.ReleaseLane, HostID: s.hostID,
		Components: append([]componentArtifact(nil), observed.Components...), ConfigurationDigest: authorization.ConfigurationDigest,
		InstallationGeneration: authorization.InstallationGeneration, Outcome: observed.Outcome,
		EvidenceRefs: append([]string(nil), observed.EvidenceRefs...), ObservedAt: observed.ObservedAt.UTC().Format(time.RFC3339Nano)}
	raw, err := json.Marshal(payload)
	if err != nil || ValidatePromotionAttestationForAuthorization(authorizationID, authorizationRaw, raw) != nil {
		return "", fmt.Errorf("%w: installation evidence does not match authorization", ErrHostAttestationUnavailable)
	}
	return s.publish(ctx, "promotion_attestation", raw)
}

// AttestIngress takes no lane, directive, or surface values from its caller.
// It reads the helper's currently applied verified directive, probes the
// complete fixed host surface set, and reports both complete and rejected
// outcomes as signed evidence.
func (s *HostAttestationService) AttestIngress(ctx context.Context) (string, bool, error) {
	if err := s.ready(ctx); err != nil {
		return "", false, err
	}
	directiveID, directiveRaw, err := s.source.VerifiedAppliedIngressDirective(ctx)
	if err != nil || !isLowerHexDigest(directiveID) || ValidateIngressAdmissionDirective(directiveRaw) != nil {
		return "", false, fmt.Errorf("%w: current applied directive unavailable", ErrHostAttestationUnavailable)
	}
	var directive ingressDirectivePayload
	if decodeStrict(directiveRaw, &directive) != nil || directive.HostID != s.hostID {
		return "", false, fmt.Errorf("%w: ingress directive is not for the anchored host", ErrHostAttestationUnavailable)
	}
	now := s.now().UTC()
	notBefore, _ := time.Parse(time.RFC3339Nano, directive.NotBefore)
	notAfter, _ := time.Parse(time.RFC3339Nano, directive.NotAfter)
	if now.Before(notBefore) {
		return "", false, fmt.Errorf("%w: ingress directive is not yet valid", ErrHostAttestationUnavailable)
	}
	observations, err := s.source.ObserveIngressSurfaces(ctx, append([]byte(nil), directiveRaw...))
	if err != nil {
		return "", false, fmt.Errorf("%w: ingress surface observation: %v", ErrHostAttestationUnavailable, err)
	}
	want := requiredIngressSurfaces(s.hostID)
	if len(observations) != len(want) {
		return "", false, fmt.Errorf("%w: incomplete ingress surface inventory", ErrHostAttestationUnavailable)
	}
	surfaces := make([]ingressSurfacePayload, len(want))
	for i, observation := range observations {
		if observation.SurfaceID != want[i] || !isDigest(observation.EvidenceDigest) ||
			(observation.Outcome != "applied" && observation.Outcome != "rejected" && observation.Outcome != "expired") ||
			(!now.Before(notAfter) && observation.Outcome == "applied") {
			return "", false, fmt.Errorf("%w: unsafe or inconsistent ingress evidence", ErrHostAttestationUnavailable)
		}
		surfaces[i] = ingressSurfacePayload{SurfaceID: observation.SurfaceID, Outcome: observation.Outcome,
			EvidenceDigest: observation.EvidenceDigest}
	}
	payload := ingressAttestationPayload{DirectiveID: directiveID, CandidateID: directive.CandidateID,
		TransitionID: directive.TransitionID, Lane: directive.Lane, HostID: s.hostID,
		ConfigurationDigest: directive.ConfigurationDigest, Role: directive.Role,
		TransitionEpoch: directive.TransitionEpoch, AdmissionGeneration: directive.AdmissionGeneration,
		LeaseGeneration: directive.LeaseGeneration, Surfaces: surfaces, ObservedAt: now.Format(time.RFC3339Nano)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", false, fmt.Errorf("%w: encode ingress evidence", ErrHostAttestationUnavailable)
	}
	applied, err := ValidateIngressAdmissionAttestationForDirective(directiveID, directiveRaw, raw)
	if err != nil {
		return "", false, err
	}
	recordID, err := s.publish(ctx, "ingress_admission_attestation", raw)
	return recordID, applied, err
}

func (s *HostAttestationService) ready(ctx context.Context) error {
	if s == nil || s.hostID == "" || s.source == nil || s.publisher == nil || s.now == nil || ctx == nil {
		return ErrHostAttestationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (s *HostAttestationService) publish(ctx context.Context, recordType string, payload []byte) (string, error) {
	recordID, err := s.publisher.PublishHostAttestation(ctx, recordType, append([]byte(nil), payload...))
	if err != nil {
		return "", fmt.Errorf("%w: signed record publication failed: %v", ErrHostAttestationUnavailable, err)
	}
	if !isLowerHexDigest(recordID) {
		return "", fmt.Errorf("%w: signer returned an invalid record ID", ErrHostAttestationUnavailable)
	}
	return recordID, nil
}

func hostEvidenceTimeValid(observed, expires, now time.Time) bool {
	return hostEvidenceTimestampFresh(observed, now) && expires.After(now)
}

func hostEvidenceTimestampFresh(observed, now time.Time) bool {
	if observed.IsZero() || !observed.Equal(observed.UTC()) {
		return false
	}
	return !observed.After(now.Add(time.Minute)) && now.Sub(observed) <= hostEvidenceMaxAge
}

// NativeHostEvidence records a small independently observed native platform
// fingerprint. It deliberately does not claim lane capacity: configured lane
// and backup volumes must be measured by the fixed host inventory source.
type NativeHostEvidence struct {
	HostID             string
	GOOS               string
	GOARCH             string
	CPUCount           int
	RootDeviceID       uint64
	RootCapacityBytes  uint64
	RootAvailableBytes uint64
	ObservedAt         time.Time
}

func observeNativeHostEvidence(hostID string, now time.Time) (NativeHostEvidence, error) {
	wantOS, wantArch, ok := canonicalHostPlatform(hostID)
	if !ok || runtime.GOOS != wantOS || runtime.GOARCH != wantArch {
		return NativeHostEvidence{}, fmt.Errorf("host identity does not match native platform")
	}
	var fs unix.Statfs_t
	if err := unix.Statfs("/", &fs); err != nil {
		return NativeHostEvidence{}, fmt.Errorf("observe fixed root filesystem: %w", err)
	}
	blockSize := uint64(fs.Bsize)
	blocks := uint64(fs.Blocks)
	available := uint64(fs.Bavail)
	if blockSize == 0 || blocks == 0 || available > blocks || blocks > ^uint64(0)/blockSize {
		return NativeHostEvidence{}, fmt.Errorf("native root filesystem capacity is invalid")
	}
	var root unix.Stat_t
	if err := unix.Stat("/", &root); err != nil {
		return NativeHostEvidence{}, fmt.Errorf("observe fixed root device: %w", err)
	}
	if root.Dev == 0 || runtime.NumCPU() <= 0 || now.IsZero() {
		return NativeHostEvidence{}, fmt.Errorf("native host facts are incomplete")
	}
	return NativeHostEvidence{HostID: hostID, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CPUCount: runtime.NumCPU(),
		RootDeviceID: uint64(root.Dev), RootCapacityBytes: blocks * blockSize,
		RootAvailableBytes: available * blockSize, ObservedAt: now.UTC()}, nil
}

func canonicalHostPlatform(hostID string) (string, string, bool) {
	switch hostID {
	case "mac-control-plane":
		return "darwin", "arm64", true
	case "linux-host", "factory-sandbox":
		return "linux", "amd64", true
	case "sandbox-host":
		return "linux", "arm64", true
	default:
		return "", "", false
	}
}
