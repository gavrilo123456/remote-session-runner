package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/lanecapacity"
)

func TestLanesL309CapacityAttestationUsesAnchoredMeasuredInventory(t *testing.T) {
	measurement, _ := l303LinuxMeasurement(t)
	source := &l309EvidenceSource{measurement: measurement, capacityRefs: []string{l303Digest}}
	publisher := &l309Publisher{recordID: l303RecordID}
	service := l309Service("linux-host", source, publisher)

	result, err := service.AttestCapacity(context.Background())
	if err != nil || result != l303RecordID {
		t.Fatalf("capacity attestation result=%v err=%v", result, err)
	}
	if source.measureCalls != 1 || publisher.calls != 1 || publisher.recordType != "capacity_measurement_attestation" {
		t.Fatalf("fixed measured operation counts source=%d publisher=%d type=%q", source.measureCalls, publisher.calls, publisher.recordType)
	}
	var payload capacityMeasurementPayload
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HostID != "linux-host" || payload.Measurement.Identity.HostID != "linux-host" || payload.EvidenceRefs[0] != l303Digest ||
		ValidateCapacityMeasurementAttestation(publisher.payload) != nil {
		t.Fatalf("published capacity payload is not bound to measured local inventory: %+v", payload)
	}
	method, ok := reflect.TypeOf((*HostAttestationService)(nil)).MethodByName("AttestCapacity")
	if !ok || method.Type.NumIn() != 2 {
		t.Fatal("capacity operation unexpectedly exposes caller-controlled observation inputs")
	}
}

func TestLanesL309CapacityAttestationRejectsWrongHostStaleAndCallerValues(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		edit func(*l309EvidenceSource)
	}{
		{"wrong host", func(source *l309EvidenceSource) { source.measurement.Identity.HostID = "sandbox-host" }},
		{"stale measurement", func(source *l309EvidenceSource) { source.measurement.ObservedAt = now.Add(-2 * time.Minute) }},
		{"path substituted for digest", func(source *l309EvidenceSource) { source.capacityRefs = []string{"/etc/passwd"} }},
		{"unordered evidence", func(source *l309EvidenceSource) {
			source.capacityRefs = []string{l303Digest, "sha256:" + strings.Repeat("a", 64)}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			measurement, _ := l303LinuxMeasurement(t)
			source := &l309EvidenceSource{measurement: measurement, capacityRefs: []string{l303Digest}}
			test.edit(source)
			publisher := &l309Publisher{recordID: l303RecordID}
			service := l309Service("linux-host", source, publisher)
			service.now = func() time.Time { return now }
			if _, err := service.AttestCapacity(context.Background()); !errors.Is(err, ErrHostAttestationUnavailable) {
				t.Fatalf("unsafe measured capacity evidence was accepted: %v", err)
			}
			if publisher.calls != 0 {
				t.Fatalf("invalid capacity evidence was published %d times", publisher.calls)
			}
		})
	}
}

func TestLanesL309PromotionAttestationUsesOpaqueAuthorizationAndLocalEvidence(t *testing.T) {
	service, source, publisher := l309PromotionFixture(t)
	recordID, err := service.AttestPromotion(context.Background(), l303RecordID)
	if err != nil || recordID != l303RecordID {
		t.Fatalf("promotion attestation=%q err=%v", recordID, err)
	}
	if !reflect.DeepEqual(source.authorizationCalls, []string{l303RecordID}) || source.installCalls != 1 || publisher.calls != 1 ||
		publisher.recordType != "promotion_attestation" {
		t.Fatalf("promotion did not use exactly one opaque authorization and local observation: source=%+v publisher=%+v", source, publisher)
	}
	if err := ValidatePromotionAttestationForAuthorization(l303RecordID, source.authorizationRaw, publisher.payload); err != nil {
		t.Fatalf("published installation result does not bind to verified authorization: %v", err)
	}
}

func TestLanesL309PromotionAttestationRejectsForeignAuthorizationAndIncompleteEvidence(t *testing.T) {
	cases := []struct {
		name string
		edit func(*l309EvidenceSource)
	}{
		{"foreign host authorization", func(source *l309EvidenceSource) {
			authorization := l303Authorization("factory-sandbox", "dev", nil)
			source.authorizationRaw = l309JSON(authorization)
		}},
		{"partial installed components", func(source *l309EvidenceSource) {
			source.install.Components = source.install.Components[:len(source.install.Components)-1]
		}},
		{"stale installation observation", func(source *l309EvidenceSource) {
			source.install.ObservedAt = time.Date(2026, 10, 17, 11, 58, 0, 0, time.UTC)
		}},
		{"caller path as evidence", func(source *l309EvidenceSource) { source.install.EvidenceRefs = []string{"/tmp/receipt.json"} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, source, publisher := l309PromotionFixture(t)
			test.edit(source)
			if _, err := service.AttestPromotion(context.Background(), l303RecordID); !errors.Is(err, ErrHostAttestationUnavailable) {
				t.Fatalf("unsafe installation evidence was accepted: %v", err)
			}
			if publisher.calls != 0 {
				t.Fatalf("unsafe installation evidence was published %d times", publisher.calls)
			}
		})
	}
}

func TestLanesL309IngressAttestationCoversCompleteFixedHostSurfaceSets(t *testing.T) {
	for _, hostID := range canonicalHostIDs {
		t.Run(hostID, func(t *testing.T) {
			source := l309IngressSource(hostID, l303Time())
			publisher := &l309Publisher{recordID: l303RecordID}
			service := l309Service(hostID, source, publisher)
			recordID, applied, err := service.AttestIngress(context.Background())
			if err != nil || recordID != l303RecordID || !applied {
				t.Fatalf("complete ingress attestation id=%q applied=%t err=%v", recordID, applied, err)
			}
			if publisher.calls != 1 || publisher.recordType != "ingress_admission_attestation" {
				t.Fatalf("unexpected ingress publication: %+v", publisher)
			}
			if err := ValidateIngressAdmissionAttestation(publisher.payload); err != nil {
				t.Fatalf("complete ingress payload rejected: %v", err)
			}
			var payload ingressAttestationPayload
			if err := json.Unmarshal(publisher.payload, &payload); err != nil {
				t.Fatal(err)
			}
			want := requiredIngressSurfaces(hostID)
			if len(payload.Surfaces) != len(want) {
				t.Fatalf("surface inventory length=%d want=%v", len(payload.Surfaces), want)
			}
			for i := range want {
				if payload.Surfaces[i].SurfaceID != want[i] || payload.Surfaces[i].Outcome != "applied" {
					t.Fatalf("surface[%d]=%+v want %q applied", i, payload.Surfaces[i], want[i])
				}
			}
		})
	}
}

func TestLanesL309IngressAttestationRejectsIncompleteAndInconsistentEvidence(t *testing.T) {
	cases := []struct {
		name string
		edit func(*l309EvidenceSource)
	}{
		{"missing surface", func(source *l309EvidenceSource) { source.surfaces = source.surfaces[:1] }},
		{"swapped surfaces", func(source *l309EvidenceSource) {
			source.surfaces[0], source.surfaces[1] = source.surfaces[1], source.surfaces[0]
		}},
		{"invalid evidence digest", func(source *l309EvidenceSource) { source.surfaces[0].EvidenceDigest = "/var/log/runner.log" }},
		{"foreign host directive", func(source *l309EvidenceSource) {
			directive := ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition, Lane: "dev", HostID: "sandbox-host",
				ConfigurationDigest: l303Digest, Role: "candidate_admission", TransitionEpoch: 1, AdmissionGeneration: 2,
				NotBefore: l303Time(), NotAfter: "2026-10-17T13:00:00Z", RequiredLedgerGeneration: 1}
			source.directiveRaw = l309JSON(directive)
		}},
		{"applied evidence after expiry", func(source *l309EvidenceSource) {
			source.directiveRaw = l309JSON(ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition,
				Lane: "dev", HostID: "linux-host", ConfigurationDigest: l303Digest, Role: "candidate_admission",
				TransitionEpoch: 1, AdmissionGeneration: 2, NotBefore: "2026-10-17T11:00:00Z", NotAfter: "2026-10-17T11:59:59Z",
				RequiredLedgerGeneration: 1})
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source := l309IngressSource("linux-host", l303Time())
			publisher := &l309Publisher{recordID: l303RecordID}
			service := l309Service("linux-host", source, publisher)
			service.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }
			test.edit(source)
			if _, _, err := service.AttestIngress(context.Background()); !errors.Is(err, ErrHostAttestationUnavailable) {
				t.Fatalf("unsafe ingress evidence was accepted: %v", err)
			}
			if publisher.calls != 0 {
				t.Fatalf("unsafe ingress evidence was published %d times", publisher.calls)
			}
		})
	}
}

func TestLanesL309ServiceConstructionRequiresRootAnchor(t *testing.T) {
	if _, err := NewHostAttestationService(nil, nil, nil); !errors.Is(err, ErrHostAttestationUnavailable) {
		t.Fatalf("service without a root-verified anchor was accepted: %v", err)
	}
}

func TestLanesL309NativePlatformFactsComeFromCurrentKernel(t *testing.T) {
	if os.Getenv("LANES_NATIVE_VALIDATION") != "1" {
		t.Skip("native observation runs only through the LANES N-tier dispatcher")
	}
	hostID := os.Getenv("LANES_NATIVE_HOST_ID")
	if hostID == "" {
		t.Fatal("LANES_NATIVE_HOST_ID must identify the executing host")
	}
	observed, err := observeNativeHostEvidence(hostID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantOS, wantArch, ok := canonicalHostPlatform(hostID)
	if !ok || observed.GOOS != wantOS || observed.GOARCH != wantArch || observed.CPUCount != runtime.NumCPU() ||
		observed.RootDeviceID == 0 || observed.RootCapacityBytes == 0 || observed.RootAvailableBytes > observed.RootCapacityBytes ||
		observed.ObservedAt.IsZero() {
		t.Fatalf("native facts do not match the selected host/platform: %+v", observed)
	}
}

type l309EvidenceSource struct {
	measurement        lanecapacity.Measurement
	capacityRefs       []string
	capacityErr        error
	measureCalls       int
	authorizationRaw   []byte
	authorizationCalls []string
	authorizationErr   error
	install            HostInstallObservation
	installErr         error
	installCalls       int
	directiveID        string
	directiveRaw       []byte
	directiveErr       error
	surfaces           []HostIngressSurfaceObservation
	ingressErr         error
}

func (s *l309EvidenceSource) MeasureCapacity(context.Context) (lanecapacity.Measurement, []string, error) {
	s.measureCalls++
	return s.measurement, append([]string(nil), s.capacityRefs...), s.capacityErr
}

func (s *l309EvidenceSource) VerifiedPromotionAuthorization(_ context.Context, recordID string) ([]byte, error) {
	s.authorizationCalls = append(s.authorizationCalls, recordID)
	return append([]byte(nil), s.authorizationRaw...), s.authorizationErr
}

func (s *l309EvidenceSource) ObserveInstallation(_ context.Context, authorization []byte) (HostInstallObservation, error) {
	s.installCalls++
	return s.install, s.installErr
}

func (s *l309EvidenceSource) VerifiedAppliedIngressDirective(context.Context) (string, []byte, error) {
	return s.directiveID, append([]byte(nil), s.directiveRaw...), s.directiveErr
}

func (s *l309EvidenceSource) ObserveIngressSurfaces(_ context.Context, directive []byte) ([]HostIngressSurfaceObservation, error) {
	return append([]HostIngressSurfaceObservation(nil), s.surfaces...), s.ingressErr
}

type l309Publisher struct {
	recordID   string
	recordType string
	payload    []byte
	calls      int
	err        error
}

func (p *l309Publisher) PublishHostAttestation(_ context.Context, recordType string, payload []byte) (string, error) {
	p.calls++
	p.recordType = recordType
	p.payload = append([]byte(nil), payload...)
	return p.recordID, p.err
}

func l309Service(hostID string, source *l309EvidenceSource, publisher *l309Publisher) *HostAttestationService {
	return &HostAttestationService{hostID: hostID, source: source, publisher: publisher,
		now: func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }}
}

func l309PromotionFixture(t *testing.T) (*HostAttestationService, *l309EvidenceSource, *l309Publisher) {
	t.Helper()
	authorization := l303Authorization("linux-host", "dev", nil)
	measurement, _ := l303LinuxMeasurement(t)
	source := &l309EvidenceSource{measurement: measurement, capacityRefs: []string{l303Digest},
		authorizationRaw: l302JSON(t, authorization), install: HostInstallObservation{Components: authorization.ComponentArtifacts,
			Outcome: "accepted", EvidenceRefs: []string{l303Digest}, ObservedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}}
	publisher := &l309Publisher{recordID: l303RecordID}
	return l309Service("linux-host", source, publisher), source, publisher
}

func l309IngressSource(hostID, timestamp string) *l309EvidenceSource {
	directive := ingressDirectivePayload{CandidateID: l303RecordID, TransitionID: l303Transition, Lane: "dev", HostID: hostID,
		ConfigurationDigest: l303Digest, Role: "candidate_admission", TransitionEpoch: 1, AdmissionGeneration: 2,
		NotBefore: timestamp, NotAfter: "2026-10-17T13:00:00Z", RequiredLedgerGeneration: 1}
	surfaces := make([]HostIngressSurfaceObservation, 0, len(requiredIngressSurfaces(hostID)))
	for _, surfaceID := range requiredIngressSurfaces(hostID) {
		surfaces = append(surfaces, HostIngressSurfaceObservation{SurfaceID: surfaceID, Outcome: "applied", EvidenceDigest: l303Digest})
	}
	return &l309EvidenceSource{directiveID: l303RecordID, directiveRaw: l309JSON(directive), surfaces: surfaces}
}

func l309JSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
