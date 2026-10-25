package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
)

func TestCompactPreparedRequestBodyRestoresPreparedDigest(t *testing.T) {
	want := []byte(`{"environment":"sandbox-host-dev","timeout_seconds":5}`)
	digest := sha256.Sum256(want)
	input := json.RawMessage("{\n  \"environment\": \"sandbox-host-dev\",\n  \"timeout_seconds\": 5\n}")
	got, err := compactPreparedRequestBody(input, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("compact prepared body: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("compacted body = %s, want %s", got, want)
	}
}

func TestCompactPreparedRequestBodyRejectsDigestMismatch(t *testing.T) {
	if _, err := compactPreparedRequestBody(json.RawMessage(`{"environment":"sandbox-host-dev"}`), "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
}

func TestProbeOutcomePrintsEvidenceAndFailsAcceptanceWhenAnyGateFails(t *testing.T) {
	exitCode := 0
	finalSequence := int64(2)
	base := outcome{Phase: "complete", CommandState: "succeeded", ExitCode: &exitCode, FinalEventSequence: &finalSequence,
		OutputComplete: true, TeardownState: "closed"}
	if !probeOutcomeAccepted(base) {
		t.Fatal("complete, zero-exit, complete-output, closed-teardown probe was rejected")
	}
	tests := []struct {
		name   string
		mutate func(*outcome)
	}{
		{"failed job", func(value *outcome) { value.Phase = "failed" }},
		{"lost job", func(value *outcome) { value.Phase = "lost" }},
		{"nonzero exit", func(value *outcome) { code := 3; value.ExitCode = &code }},
		{"missing exit", func(value *outcome) { value.ExitCode = nil }},
		{"incomplete output", func(value *outcome) { value.OutputComplete = false }},
		{"truncated output", func(value *outcome) { value.OutputTruncated = true }},
		{"missing final sequence", func(value *outcome) { value.FinalEventSequence = nil }},
		{"unconfirmed teardown", func(value *outcome) { value.TeardownState = "lost" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			var output bytes.Buffer
			if err := printProbeOutcomeTo(&output, value); !errors.Is(err, errProbeNotAccepted) {
				t.Fatalf("failure result error=%v, want acceptance failure", err)
			}
			if output.Len() == 0 {
				t.Fatal("diagnostic evidence was not emitted")
			}
			var emitted outcome
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &emitted); err != nil || !reflect.DeepEqual(emitted, value) {
				t.Fatalf("failure evidence was not emitted exactly once: decoded=%+v err=%v output=%q", emitted, err, output.String())
			}
			if probeOutcomeAccepted(value) {
				t.Fatal("incomplete or unsuccessful command was accepted")
			}
		})
	}
}

func TestDecodeObservedJobResponseRequiresExactTerminalResourceIdentity(t *testing.T) {
	const jobID = "job-705974bc8f474d77898670837be15b61"
	good := response{status: http.StatusOK, body: []byte(`{"resource":{"job_id":"job-705974bc8f474d77898670837be15b61","session_id":"sess-11111111111111111111111111111111","command_id":"cmd-22222222222222222222222222222222","phase":"complete","command_state":"succeeded","exit_code":0,"final_event_sequence":4,"output_complete":true,"output_truncated":false,"teardown_state":"closed"}}`)}
	got, err := decodeObservedJobResponse(good, jobID)
	if err != nil || got.JobID != jobID || !probeOutcomeAccepted(outcome{Phase: got.Phase, CommandState: got.CommandState,
		ExitCode: got.ExitCode, FinalEventSequence: got.FinalEventSequence, OutputComplete: got.OutputComplete,
		OutputTruncated: got.OutputTruncated, TeardownState: got.TeardownState}) {
		t.Fatalf("valid exact status response was rejected: %+v, %v", got, err)
	}
	if _, err := decodeObservedJobResponse(response{status: http.StatusNotFound, body: []byte(`{"code":"job_not_found"}`)}, jobID); err == nil ||
		!strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "job_not_found") {
		t.Fatalf("missing job did not return bounded status classification: %v", err)
	}
	if _, err := decodeObservedJobResponse(response{status: http.StatusServiceUnavailable, body: []byte(`{"code":"maintenance_admission_closed"}`)}, jobID); err == nil ||
		!strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "maintenance_admission_closed") {
		t.Fatalf("closed maintenance status route lost its bounded response classification: %v", err)
	}
	if _, err := decodeObservedJobResponse(response{status: http.StatusOK, body: []byte(`{"resource":{"job_id":"job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)}, jobID); err == nil {
		t.Fatal("status result from a different job was accepted")
	}
}

func TestObservedWorkerControllerBindingSupportsLegacyAndKeepsV3Strict(t *testing.T) {
	tests := []struct {
		name       string
		version    int
		transport  string
		directID   string
		controller string
		want       bool
	}{
		{"V1 direct uses authenticated Mac identity", 1, "direct-mtls", "", "tomasz.walczuk", true},
		{"V2 direct uses authenticated Mac identity", 2, "direct-mtls", "", "tomasz.walczuk", true},
		{"legacy direct rejects another identity", 2, "direct-mtls", "", "other-controller", false},
		{"V3 direct requires explicit endpoint identity", 3, "direct-mtls", "tomasz.walczuk", "tomasz.walczuk", true},
		{"V3 direct rejects missing endpoint identity", 3, "direct-mtls", "", "tomasz.walczuk", false},
		{"V3 direct rejects mismatch", 3, "direct-mtls", "configured-controller", "tomasz.walczuk", false},
		{"legacy queued uses authenticated Mac identity", 2, "queued-ssh", "", "tomasz.walczuk", true},
		{"V3 queued uses authenticated Mac identity", 3, "queued-ssh", "", "tomasz.walczuk", true},
		{"queued rejects another identity", 3, "queued-ssh", "", "other-controller", false},
		{"unknown transport rejected", 2, "other", "", "tomasz.walczuk", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := observedWorkerControllerMatches(test.version, test.transport, test.directID, test.controller); got != test.want {
				t.Fatalf("observedWorkerControllerMatches(%d, %q, %q, %q) = %v, want %v", test.version,
					test.transport, test.directID, test.controller, got, test.want)
			}
		})
	}
}

func TestResolveObservedMacRemoteEnvironmentUsesUniqueLegacyPolicy(t *testing.T) {
	load := func(t *testing.T, contents string) config.Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mac.yaml")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.LoadFile(path)
		if err != nil {
			t.Fatalf("load Mac route fixture: %v", err)
		}
		return loaded
	}
	loaded := load(t, observedMacRemoteV2Fixture)
	for _, transport := range []struct {
		name       string
		kind       domain.ControllerType
		controller string
	}{
		{"direct-mtls", domain.ControllerTypeDirectMTLS, config.MacAccount},
		{"queued-ssh", domain.ControllerTypeQueuedMac, config.MacAccount},
	} {
		name, err := resolveObservedMacRemoteEnvironment(loaded, "linux-host", "prd", transport.name, transport.kind, transport.controller)
		if err != nil || name != "linux-dev" {
			t.Fatalf("legacy %s environment = %q, err=%v; want unique linux-dev", transport.name, name, err)
		}
	}
	if _, err := resolveObservedMacRemoteEnvironment(loaded, "linux-host", "prd", "direct-mtls", domain.ControllerTypeDirectMTLS, "other-controller"); err == nil {
		t.Fatal("legacy route accepted a controller not allowed by its policy")
	}
	ambiguous := strings.Replace(observedMacRemoteV2Fixture, "execution_contexts:\n", "  linux-legacy:\n    base_system: Ubuntu 20.04.6 LTS\n    host_class: Ubuntu Linux host\n    effective_account: ubuntu\n    allowed_targets: [{kind: remote, profile: linux-host}]\n    allowed_source_modes: [empty]\n    allowed_repository_aliases: []\n    allowed_controllers: [{type: direct_mtls, id: tomasz.walczuk}]\nexecution_contexts:\n", 1)
	if _, err := resolveObservedMacRemoteEnvironment(load(t, ambiguous), "linux-host", "prd", "direct-mtls", domain.ControllerTypeDirectMTLS, config.MacAccount); err == nil {
		t.Fatal("ambiguous legacy route policy was accepted")
	}
}

func TestObservedMacEnvironmentNameKeepsV3LaneRestriction(t *testing.T) {
	if !observedMacEnvironmentNameAllowed(config.VersionV2, "legacy-linux-policy", "linux-host", "prd") {
		t.Fatal("legacy policy name was rejected")
	}
	if !observedMacEnvironmentNameAllowed(config.VersionV3, "linux-host-prd", "linux-host", "prd") {
		t.Fatal("correct V3 lane environment was rejected")
	}
	if observedMacEnvironmentNameAllowed(config.VersionV3, "linux-host-dev", "linux-host", "prd") ||
		observedMacEnvironmentNameAllowed(config.VersionV3, "another-linux-host-prd", "linux-host", "prd") {
		t.Fatal("V3 environment outside the requested host/lane was allowed")
	}
}

const observedMacRemoteV2Fixture = `version: 2
mac:
  account: tomasz.walczuk
  service_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
  api_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock"
  locald_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/locald.sock"
  sqlite: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db"
  workspaces: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/workspaces"
  script_temp_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/tmp/scripts"
  backups: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups"
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets: [{kind: local, profile: mac-workstation}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: local_user, id: tomasz.walczuk}]
  linux-dev:
    base_system: Ubuntu 20.04.6 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - {type: queued_mac, id: tomasz.walczuk}
      - {type: direct_mtls, id: tomasz.walczuk}
execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
remote_hosts:
  linux-host:
    account: ubuntu
    direct_endpoint:
      name: linux-direct
      url: https://129.151.232.40:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local]
`
