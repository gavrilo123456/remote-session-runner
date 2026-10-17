package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLanesL306PeerPoliciesRequireExactLinuxAndMacIdentities(t *testing.T) {
	linux := peerExpectation{
		kind: SecurityEndpointRelease, goos: "linux", hostID: "linux-host", transportUID: 995,
		transportUser: "runner-release-transport", transportUnit: "runner-release-transport.service",
		transportControlGroup: "/system.slice/runner-release-transport.service",
		transportExecutable:   "/usr/local/libexec/remote-session-runner/runner-release-transport",
	}
	linuxPeer := observedPeer{
		uid: 995, pid: 321, serviceAccount: true, unit: linux.transportUnit,
		controlGroup: linux.transportControlGroup, executablePath: linux.transportExecutable,
		executableUID: 0, executableMode: 0o755, executableDev: 7, executableInode: 9,
		executableHash: strings.Repeat("a", 64),
	}
	if err := validatePeerExpectation(linux); err != nil || !peerAllowed(linux, linuxPeer) {
		t.Fatalf("exact dedicated Linux transport rejected: peer=%+v err=%v", linuxPeer, err)
	}
	linuxBad := []struct {
		name string
		edit func(*observedPeer)
	}{
		{"arbitrary root", func(peer *observedPeer) { peer.uid = 0 }},
		{"shared ubuntu uid", func(peer *observedPeer) { peer.uid = 1000; peer.serviceAccount = false }},
		{"wrong systemd unit", func(peer *observedPeer) { peer.unit = "runnerd.service" }},
		{"wrong cgroup", func(peer *observedPeer) { peer.controlGroup = "/system.slice/runnerd.service" }},
		{"wrong executable", func(peer *observedPeer) { peer.executablePath = "/tmp/runner-release-transport" }},
		{"writable executable", func(peer *observedPeer) { peer.executableMode = 0o775 }},
		{"non-root executable", func(peer *observedPeer) { peer.executableUID = 995 }},
		{"unmeasured executable", func(peer *observedPeer) { peer.executableHash = "" }},
		{"missing inode binding", func(peer *observedPeer) { peer.executableInode = 0 }},
		{"shared service identity", func(peer *observedPeer) { peer.serviceAccount = false }},
	}
	for _, tc := range linuxBad {
		t.Run("linux/"+tc.name, func(t *testing.T) {
			bad := linuxPeer
			tc.edit(&bad)
			if peerAllowed(linux, bad) {
				t.Fatalf("unsafe Linux peer accepted: %+v", bad)
			}
		})
	}

	mac := peerExpectation{
		kind: SecurityEndpointRelease, goos: "darwin", hostID: "mac-control-plane", launchdLabel: "runner-release-transport",
		codeRequirement: `identifier "org.example.runner-release-transport" and anchor apple generic and certificate leaf[subject.OU] = "TEAMID1234"`,
	}
	macPeer := observedPeer{uid: 0, pid: 432, launchdLabel: mac.launchdLabel, codeRequirement: mac.codeRequirement, codeValid: true}
	if err := validatePeerExpectation(mac); err != nil || !peerAllowed(mac, macPeer) {
		t.Fatalf("exact root LaunchDaemon transport identity rejected: peer=%+v err=%v", macPeer, err)
	}
	macBad := []struct {
		name string
		edit func(*observedPeer)
	}{
		{"arbitrary root process", func(peer *observedPeer) { peer.codeValid = false }},
		{"wrong label", func(peer *observedPeer) { peer.launchdLabel = "runnerd" }},
		{"wrong code requirement", func(peer *observedPeer) {
			peer.codeRequirement = `identifier "org.example.other" and anchor apple generic and certificate leaf[subject.OU] = "TEAMID1234"`
		}},
	}
	for _, tc := range macBad {
		t.Run("mac/"+tc.name, func(t *testing.T) {
			bad := macPeer
			tc.edit(&bad)
			if peerAllowed(mac, bad) {
				t.Fatalf("unsafe Mac peer accepted: %+v", bad)
			}
		})
	}
	if !peerAllowed(peerExpectation{kind: SecurityEndpointObserve, goos: "darwin", runnerUID: 502}, observedPeer{uid: 502, pid: 90}) {
		t.Fatal("configured non-root Mac Runner identity was rejected")
	}
	if peerAllowed(peerExpectation{kind: SecurityEndpointObserve, goos: "linux", runnerUID: 1000}, observedPeer{uid: 0, pid: 91}) {
		t.Fatal("root was accepted on the Runner observe endpoint")
	}
}

func TestLanesL306ObserveEndpointAllowsOnlyConfiguredRunnerUID(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint32
		want bool
	}{
		{"configured Linux service", 1000, true},
		{"other shared user", 1001, false},
		{"root", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := peerExpectation{kind: SecurityEndpointObserve, goos: "linux", runnerUID: 1000}
			got := peerAllowed(policy, observedPeer{uid: tc.uid, pid: 77})
			if got != tc.want {
				t.Fatalf("observe peer uid=%d allowed=%t, want %t", tc.uid, got, tc.want)
			}
		})
	}
}

func TestLanesL306ReleaseEndpointRejectsCallerChosenPathsAndBytes(t *testing.T) {
	ops := &l306FakeOperations{}
	expectation := l306LinuxExpectation()
	endpoint := &SecurityEndpoint{kind: SecurityEndpointRelease, ops: ops, expectation: expectation}
	baseID := strings.Repeat("a", 64)
	validCases := []SecurityEndpointRequest{
		{Version: 1, Operation: SecurityOperationBeginTimeProbe, Body: json.RawMessage(`{}`)},
		{Version: 1, Operation: SecurityOperationAcceptTimeProbe, Body: json.RawMessage(`{"challenge_id":"challenge-1234567890","response":{"sig":"x"}}`)},
		{Version: 1, Operation: SecurityOperationVerifyReleaseRecord, Body: json.RawMessage(`{"record_id":"` + baseID + `","envelope":{"record":"bytes"}}`)},
		{Version: 1, Operation: SecurityOperationRetrieveOutbox, Body: json.RawMessage(`{"record_id":"` + baseID + `"}`)},
		{Version: 1, Operation: SecurityOperationAttestCapacity, Body: json.RawMessage(`{}`)},
		{Version: 1, Operation: SecurityOperationAttestPKIRequest, Body: json.RawMessage(`{"pki_issuance_mandate_id":"mandate-1234567890"}`)},
	}
	for _, request := range validCases {
		if _, err := endpoint.dispatch(context.Background(), request); err != nil {
			t.Errorf("valid fixed operation %s rejected: %v", request.Operation, err)
		}
	}
	if ops.begin != 1 || ops.accept != 1 || ops.verify != 1 || ops.outbox != 1 || ops.capacity != 1 || ops.pki != 1 {
		t.Fatalf("fixed-operation dispatch counts mismatch: %+v", ops)
	}
	for _, request := range []SecurityEndpointRequest{
		{Version: 1, Operation: SecurityOperationAttestCapacity, Body: json.RawMessage(`{"path":"/etc/passwd"}`)},
		{Version: 1, Operation: SecurityOperationAttestPKIRequest, Body: json.RawMessage(`{"pki_issuance_mandate_id":"mandate-1234567890","csr":"caller bytes"}`)},
		{Version: 1, Operation: SecurityOperationVerifyReleaseRecord, Body: json.RawMessage(`{"record_id":"` + baseID + `","envelope":{},"path":"/tmp/evidence"}`)},
		{Version: 1, Operation: "shell", Body: json.RawMessage(`{}`)},
	} {
		if _, err := endpoint.dispatch(context.Background(), request); err == nil {
			t.Errorf("unsafe or unknown operation accepted: %+v", request)
		}
	}
	if ops.capacity != 1 || ops.pki != 1 || ops.verify != 1 {
		t.Fatalf("rejected requests reached an operation backend: %+v", ops)
	}
}

func TestLanesL306AuthoritySnapshotIsPublicImmutableAndDetached(t *testing.T) {
	fixture := newL305Fixture(t)
	before, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.anchor.AuthoritySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HostID != before.HostID || snapshot.Generation != before.Generation || len(snapshot.AuthorityKeys) != len(before.AuthorityKeys) ||
		!reflect.DeepEqual(snapshot.RevocationLedger, before.RevocationLedger) {
		t.Fatalf("authority view does not match durable public state: %+v", snapshot)
	}
	serialized, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(serialized, []byte("private_key")) || bytes.Contains(serialized, []byte("privateKey")) {
		t.Fatalf("authority snapshot contains private material or failed to encode: %s err=%v", serialized, err)
	}
	snapshot.AuthorityKeys[0].PublicKey[0] ^= 0xff
	snapshot.RevocationLedger.RevokedKeyIDs = append(snapshot.RevocationLedger.RevokedKeyIDs, "caller-mutation")
	snapshot.DirectiveHighWater["caller"] = AnchorDirectiveHighWater{RecordID: "caller"}
	after, err := fixture.anchor.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("mutating the returned authority view altered the durable anchor snapshot")
	}
}

func TestLanesL306ProtocolEnvelopeIsBoundedAndClosed(t *testing.T) {
	if _, err := readSecurityEndpointRequest(bytes.NewBufferString(`{"version":1,"operation":"capacity-attest","body":{},"path":"/tmp"}`)); !errors.Is(err, ErrSecurityEndpointProtocol) {
		t.Fatalf("open endpoint envelope error=%v", err)
	}
	if _, err := readSecurityEndpointRequest(bytes.NewReader(bytes.Repeat([]byte{'x'}, securityEndpointMaxMessageBytes+1))); !errors.Is(err, ErrSecurityEndpointProtocol) {
		t.Fatalf("oversized endpoint envelope error=%v", err)
	}
	if _, err := readSecurityEndpointRequest(bytes.NewBufferString(`{"version":1,"operation":"capacity-attest","body":{}} {}`)); !errors.Is(err, ErrSecurityEndpointProtocol) {
		t.Fatalf("trailing endpoint JSON error=%v", err)
	}
	request, err := readSecurityEndpointRequest(bytes.NewBufferString(`{"version":1,"operation":"capacity-attest"}`))
	if err != nil || !emptyEndpointBody(request.Body) {
		t.Fatalf("omitted empty body was not normalized: request=%+v err=%v", request, err)
	}
}

func TestLanesL306EndpointOperationMatricesAreDisjoint(t *testing.T) {
	ops := &l306FakeOperations{}
	observe := &SecurityEndpoint{kind: SecurityEndpointObserve, ops: ops, expectation: peerExpectation{kind: SecurityEndpointObserve, goos: "linux", hostID: "linux-host", runnerUID: 1000}}
	body := json.RawMessage(`{"envelope":{"id":"x"},"time_probe":{"nonce":"n"},"expected_host_id":"linux-host","lane":"dev","resolved_configuration_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if _, err := observe.dispatch(context.Background(), SecurityEndpointRequest{Version: 1, Operation: SecurityOperationObserveRecord, Body: body}); err != nil || ops.observe != 1 {
		t.Fatalf("valid observe operation failed: calls=%d err=%v", ops.observe, err)
	}
	if _, err := observe.dispatch(context.Background(), SecurityEndpointRequest{Version: 1, Operation: SecurityOperationBeginTimeProbe, Body: json.RawMessage(`{}`)}); !errors.Is(err, ErrSecurityEndpointDenied) {
		t.Fatalf("observe endpoint accepted transport operation: %v", err)
	}
	authority := &SecurityEndpoint{kind: SecurityEndpointAuthorityView, ops: ops, expectation: peerExpectation{kind: SecurityEndpointAuthorityView, goos: "darwin", hostID: "mac-control-plane", launchdLabel: "runner-release-authority-agent", codeRequirement: `identifier "org.example.runner-release-authority-agent" and anchor apple generic and certificate leaf[subject.OU] = "TEAMID1234"`}}
	if _, err := authority.dispatch(context.Background(), SecurityEndpointRequest{Version: 1, Operation: SecurityOperationAuthoritySnapshot, Body: json.RawMessage(`{}`)}); err != nil || ops.snapshot != 1 {
		t.Fatalf("valid authority snapshot operation failed: calls=%d err=%v", ops.snapshot, err)
	}
	if _, err := authority.dispatch(context.Background(), SecurityEndpointRequest{Version: 1, Operation: SecurityOperationObserveRecord, Body: body}); !errors.Is(err, ErrSecurityEndpointDenied) {
		t.Fatalf("authority endpoint accepted observe mutation: %v", err)
	}
	wrongHost := json.RawMessage(`{"envelope":{"id":"x"},"time_probe":{"nonce":"n"},"expected_host_id":"factory-sandbox","lane":"dev","resolved_configuration_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if _, err := observe.dispatch(context.Background(), SecurityEndpointRequest{Version: 1, Operation: SecurityOperationObserveRecord, Body: wrongHost}); !errors.Is(err, ErrSecurityEndpointProtocol) || ops.observe != 1 {
		t.Fatalf("observe endpoint accepted a caller-selected host: calls=%d err=%v", ops.observe, err)
	}
	if _, err := observe.dispatch(context.Background(), SecurityEndpointRequest{Version: 2, Operation: SecurityOperationObserveRecord, Body: body}); !errors.Is(err, ErrSecurityEndpointProtocol) {
		t.Fatalf("unsupported protocol version error=%v", err)
	}
}

func TestLanesL306PeerExpectationRejectsIncompletePlatformRules(t *testing.T) {
	invalid := []peerExpectation{
		{kind: SecurityEndpointObserve, goos: "linux", runnerUID: 0},
		{kind: SecurityEndpointAuthorityView, goos: "linux"},
		{kind: SecurityEndpointRelease, goos: "linux", transportUID: 990, transportUser: "ubuntu", transportUnit: "runner-release-transport.service", transportControlGroup: "/system.slice/runner-release-transport.service", transportExecutable: "/opt/runner/transport"},
		{kind: SecurityEndpointRelease, goos: "linux", transportUID: 990, transportUser: "runner-release-transport", transportUnit: "runnerd.service", transportControlGroup: "/system.slice/runnerd.service", transportExecutable: "/opt/runner/transport"},
		{kind: SecurityEndpointRelease, goos: "darwin", launchdLabel: "runnerd", codeRequirement: `identifier "org.example.runnerd" and anchor apple generic and certificate leaf[subject.OU] = "TEAMID1234"`},
		{kind: SecurityEndpointRelease, goos: "darwin", launchdLabel: "runner-release-transport", codeRequirement: `identifier "org.example.runner-release-transport"`},
	}
	for _, expectation := range invalid {
		if err := validatePeerExpectation(expectation); err == nil {
			t.Errorf("incomplete peer expectation accepted: %+v", expectation)
		}
	}
	if _, err := newSecurityEndpoint(SecurityEndpointRelease, &l306FakeOperations{}, peerExpectation{kind: SecurityEndpointObserve, goos: runtime.GOOS, hostID: "linux-host", runnerUID: 1000}); !errors.Is(err, ErrSecurityEndpointUnavailable) {
		t.Fatalf("mismatched endpoint expectation error=%v", err)
	}
}

func TestLanesL306FixedEndpointPathInventory(t *testing.T) {
	want := map[string]string{
		"darwin/observe":           "/var/run/remote-session-runner/security-anchor.sock",
		"darwin/authority-state":   "/var/run/remote-session-runner/security-anchor-authority-state.sock",
		"darwin/release-transport": "/var/run/remote-session-runner/security-anchor-release-transport.sock",
		"linux/observe":            "/var/run/remote-session-runner/security-anchor.sock",
		"linux/release-transport":  "/var/run/remote-session-runner/security-anchor-release-transport.sock",
	}
	for key, expected := range want {
		parts := strings.SplitN(key, "/", 2)
		path, err := endpointSocketPath(SecurityEndpointKind(parts[1]), parts[0])
		if err != nil || path != expected {
			t.Errorf("socket path %s=%q err=%v want %q", key, path, err, expected)
		}
	}
	for _, invalid := range []struct {
		kind SecurityEndpointKind
		os   string
	}{{SecurityEndpointAuthorityView, "linux"}, {"generic-admin", "darwin"}, {SecurityEndpointRelease, "windows"}} {
		if _, err := endpointSocketPath(invalid.kind, invalid.os); !errors.Is(err, ErrSecurityEndpointUnavailable) {
			t.Errorf("unsupported socket inventory %s/%s error=%v", invalid.os, invalid.kind, err)
		}
	}
}

func TestLanesL306NativePeerEvidence(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Fatalf("native peer test unsupported on %s", runtime.GOOS)
	}
	if runtime.GOOS == "darwin" {
		t.Setenv("TMPDIR", "/private/tmp")
	}
	path := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	if runtime.GOOS == "linux" {
		uid := uint32(os.Geteuid())
		expectation := peerExpectation{kind: SecurityEndpointObserve, goos: "linux", hostID: "linux-host", runnerUID: uid}
		peer, err := inspectUnixPeer(server, expectation)
		if err != nil || peer.pid != os.Getpid() || peer.uid != uid {
			t.Fatalf("Linux SO_PEERCRED mismatch: peer=%+v err=%v", peer, err)
		}
		if peerAllowed(expectation, peer) != (uid != 0) {
			t.Fatalf("Linux observe endpoint root policy mismatch: uid=%d peer=%+v", uid, peer)
		}
		transport := l306LinuxExpectation()
		transport.transportUID = 65530
		candidate, inspectErr := inspectUnixPeer(server, transport)
		if inspectErr == nil && peerAllowed(transport, candidate) {
			t.Fatalf("native shared-user process was accepted as dedicated transport: %+v", candidate)
		}
		return
	}
	runner := peerExpectation{kind: SecurityEndpointObserve, goos: "darwin", hostID: "mac-control-plane", runnerUID: uint32(os.Geteuid())}
	runnerPeer, runnerErr := inspectUnixPeer(server, runner)
	if runnerErr != nil || runnerPeer.pid != os.Getpid() || runnerPeer.uid != uint32(os.Geteuid()) {
		t.Fatalf("macOS LOCAL_PEERCRED mismatch: peer=%+v err=%v", runnerPeer, runnerErr)
	}
	if peerAllowed(runner, runnerPeer) != (os.Geteuid() != 0) {
		t.Fatalf("macOS observe endpoint root policy mismatch: peer=%+v", runnerPeer)
	}

	expectation := peerExpectation{
		kind: SecurityEndpointRelease, goos: "darwin", hostID: "mac-control-plane", launchdLabel: "runner-release-transport",
		codeRequirement: `identifier "org.example.runner-release-transport" and anchor apple generic and certificate leaf[subject.OU] = "TEAMID1234"`,
	}
	peer, inspectErr := inspectUnixPeer(server, expectation)
	if peer.pid != os.Getpid() || peer.uid != uint32(os.Geteuid()) {
		t.Fatalf("macOS LOCAL_PEERTOKEN identity mismatch: peer=%+v err=%v", peer, inspectErr)
	}
	var tokenFound bool
	for _, word := range peer.auditToken {
		if word != 0 {
			tokenFound = true
			break
		}
	}
	if !tokenFound || inspectErr == nil || peerAllowed(expectation, peer) {
		t.Fatalf("unapproved native Mac process was not rejected by audit identity policy: peer=%+v err=%v", peer, inspectErr)
	}
}

type l306FakeOperations struct {
	observe  int
	snapshot int
	begin    int
	accept   int
	verify   int
	outbox   int
	capacity int
	pki      int
}

func (f *l306FakeOperations) ObserveRecord(context.Context, ObserveRecordRequest) (any, error) {
	f.observe++
	return map[string]string{"status": "verified"}, nil
}
func (f *l306FakeOperations) AuthoritySnapshot(context.Context) (AnchorAuthoritySnapshot, error) {
	f.snapshot++
	return AnchorAuthoritySnapshot{HostID: "mac-control-plane"}, nil
}
func (f *l306FakeOperations) BeginTimeProbe(context.Context) (TimeProbeChallenge, error) {
	f.begin++
	return TimeProbeChallenge{ID: "0123456789abcdef0123456789abcdef", Nonce: "abc"}, nil
}
func (f *l306FakeOperations) AcceptTimeProbe(context.Context, TimeProbeAcceptRequest) (any, error) {
	f.accept++
	return map[string]string{"status": "accepted"}, nil
}
func (f *l306FakeOperations) VerifyAndCacheRecord(context.Context, AddressedRecordRequest) (any, error) {
	f.verify++
	return map[string]string{"status": "cached"}, nil
}
func (f *l306FakeOperations) RetrieveOutboxRecord(context.Context, OutboxRetrieveRequest) (any, error) {
	f.outbox++
	return map[string]string{"record_id": strings.Repeat("a", 64)}, nil
}
func (f *l306FakeOperations) AttestCapacity(context.Context) (any, error) {
	f.capacity++
	return map[string]string{"status": "attested"}, nil
}
func (f *l306FakeOperations) AttestPKIRequest(context.Context, PKIRequestAttestationRequest) (any, error) {
	f.pki++
	return map[string]string{"status": "attested"}, nil
}

func l306LinuxExpectation() peerExpectation {
	return peerExpectation{
		kind: SecurityEndpointRelease, goos: "linux", hostID: "linux-host", transportUID: 990,
		transportUser: "runner-release-transport", transportUnit: "runner-release-transport.service",
		transportControlGroup: "/system.slice/runner-release-transport.service",
		transportExecutable:   "/usr/local/libexec/remote-session-runner/runner-release-transport",
	}
}
