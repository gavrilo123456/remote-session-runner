package config

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestLanesL101NamespaceDerivationAndV3Identity(t *testing.T) {
	schemaBytes, err := os.ReadFile("schemas/v3/namespace.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties  map[string]json.RawMessage `json:"properties"`
		Definitions map[string]json.RawMessage `json:"$defs"`
		Required    []string                   `json:"required"`
		OneOf       []json.RawMessage          `json:"oneOf"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("decode V3 namespace schema: %v", err)
	}
	for _, key := range []string{"version", "release_lane", "mac", "linux"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("V3 namespace schema is missing %q", key)
		}
	}
	for _, key := range []string{"macNamespace", "linuxNamespace"} {
		if _, ok := schema.Definitions[key]; !ok {
			t.Fatalf("V3 namespace schema is missing $defs.%s", key)
		}
	}
	if !reflect.DeepEqual(schema.Required, []string{"version", "release_lane"}) || len(schema.OneOf) != 2 {
		t.Fatalf("V3 namespace schema identity/host exclusivity = required %v, oneOf %d; want version+release_lane and two host variants", schema.Required, len(schema.OneOf))
	}

	wantMacRoots := map[ReleaseLane]string{
		ReleaseLaneDev: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-dev",
		ReleaseLaneQAS: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-qas",
		ReleaseLanePRD: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner",
	}
	wantLinuxRoots := map[ReleaseLane]string{
		ReleaseLaneDev: "/home/ubuntu/.local/share/remote-session-runner-dev",
		ReleaseLaneQAS: "/home/ubuntu/.local/share/remote-session-runner-qas",
		ReleaseLanePRD: "/home/ubuntu/.local/share/remote-session-runner",
	}
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		mac, err := DeriveLaneNamespace(HostKindMac, lane, "")
		if err != nil {
			t.Fatalf("derive Mac %s namespace: %v", lane, err)
		}
		if mac.ServiceRoot != wantMacRoots[lane] || mac.ConfigFile != wantMacRoots[lane]+"/config/mac.yaml" ||
			mac.Database != wantMacRoots[lane]+"/state/local.db" || mac.APISocket != wantMacRoots[lane]+"/run/local-api.sock" ||
			mac.DatabaseWAL != mac.Database+"-wal" || mac.DatabaseSHM != mac.Database+"-shm" ||
			mac.LocalDSocket != wantMacRoots[lane]+"/run/locald.sock" || mac.MailboxRoot != wantMacRoots[lane]+"/mailbox" ||
			mac.LifecycleLock != wantMacRoots[lane]+"/run/runner-locald-lifecycle.lock" ||
			mac.RegistryRoot != "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-lane-registry" ||
			mac.RunnerBinary != wantMacRoots[lane]+"/bin/runner" || mac.LocalBinary != wantMacRoots[lane]+"/bin/runner-local" ||
			mac.LocalDBinary != wantMacRoots[lane]+"/bin/runner-locald" || mac.LogCapture != wantMacRoots[lane]+"/bin/runner-log-capture" {
			t.Fatalf("Mac %s namespace paths = %+v", lane, mac)
		}
		suffix := ""
		if lane != ReleaseLanePRD {
			suffix = "-" + string(lane)
		}
		if mac.LocalAgentLabel != "com.remote-session-runner.local"+suffix ||
			mac.LocalDAgentLabel != "com.remote-session-runner.locald"+suffix ||
			mac.LocalAgentPlist != "/Users/tomasz.walczuk/Library/LaunchAgents/"+mac.LocalAgentLabel+".plist" ||
			mac.LocalDAgentPlist != "/Users/tomasz.walczuk/Library/LaunchAgents/"+mac.LocalDAgentLabel+".plist" {
			t.Fatalf("Mac %s labels/plists = %+v", lane, mac)
		}
		assertV3NamespaceRoundTrip(t, mac)

		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			linux, err := DeriveLaneNamespace(HostKindLinux, lane, profile)
			if err != nil {
				t.Fatalf("derive Linux %s/%s namespace: %v", lane, profile, err)
			}
			wantDirectPort, wantQueuePort := 8443, 22
			wantRunnerUnit, wantQueueUnit, wantSlice := "runnerd.service", "", ""
			if lane == ReleaseLaneDev {
				wantDirectPort, wantQueuePort = 8444, 2224
				wantRunnerUnit, wantQueueUnit, wantSlice = "runnerd-dev.service", "runner-sshd-dev.service", "remote-session-runner-dev.slice"
			} else if lane == ReleaseLaneQAS {
				wantDirectPort, wantQueuePort = 8445, 2225
				wantRunnerUnit, wantQueueUnit, wantSlice = "runnerd-qas.service", "runner-sshd-qas.service", "remote-session-runner-qas.slice"
			}
			if linux.ServiceRoot != wantLinuxRoots[lane] || linux.RunnerUnit != wantRunnerUnit ||
				linux.QueuedSSHUnit != wantQueueUnit || linux.RunnerSlice != wantSlice ||
				linux.DirectPort != wantDirectPort || linux.QueuePort != wantQueuePort ||
				linux.DatabaseWAL != linux.Database+"-wal" || linux.DatabaseSHM != linux.Database+"-shm" ||
				linux.LifecycleLock != wantLinuxRoots[lane]+"/run/runnerd-lifecycle.lock" ||
				linux.RunnerEntrypoint != wantLinuxRoots[lane]+"/bin/runnerd-entrypoint.sh" ||
				linux.JournalIdentifier != strings.TrimSuffix(wantRunnerUnit, ".service") ||
				linux.QueuedSSHJournal != map[ReleaseLane]string{ReleaseLaneDev: "runner-sshd-dev", ReleaseLaneQAS: "runner-sshd-qas", ReleaseLanePRD: "sshd"}[lane] ||
				linux.RunnerBinary != wantLinuxRoots[lane]+"/bin/runnerd" ||
				linux.QueuedSSHBinary != wantLinuxRoots[lane]+"/bin/runner-ssh-bridge" ||
				linux.SessionAgent != wantLinuxRoots[lane]+"/bin/runner-session-agent" ||
				linux.LogCapture != wantLinuxRoots[lane]+"/bin/runner-log-capture" {
				t.Fatalf("Linux %s/%s fixed namespace = %+v", lane, profile, linux)
			}
			switch profile {
			case "linux-host":
				if !strings.Contains(linux.DirectEndpointURL, "129.151.232.40:") || !strings.HasPrefix(linux.DirectHTTPSBind, "10.0.0.200:") {
					t.Fatalf("linux-host endpoint = %+v", linux)
				}
			case "sandbox-host":
				if !strings.Contains(linux.DirectEndpointURL, "132.226.205.205:") || !strings.HasPrefix(linux.DirectHTTPSBind, "10.0.0.14:") {
					t.Fatalf("sandbox-host endpoint = %+v", linux)
				}
			case "factory-sandbox":
				if !strings.Contains(linux.DirectEndpointURL, "145.241.255.66:") || !strings.HasPrefix(linux.DirectHTTPSBind, "10.0.0.30:") {
					t.Fatalf("factory-sandbox endpoint = %+v", linux)
				}
			}
			forgedNamespace := linux
			forgedNamespace.DirectEndpointURL = "https://example.invalid:8444"
			if !ValidateV3NamespaceURL(linux, linux.DirectEndpointURL) || ValidateV3NamespaceURL(linux, "https://example.invalid:8444") || ValidateV3NamespaceURL(forgedNamespace, forgedNamespace.DirectEndpointURL) {
				t.Fatalf("URL validation did not pin %s/%s endpoint", lane, profile)
			}
			assertV3NamespaceRoundTrip(t, linux)
		}
	}

	// The V3 namespace implementation leaves the existing V1 and V2 parsers
	// intact. These fixtures are the previous compatibility contracts.
	if got := loadFixture(t, macConfigFixture).SchemaVersion(); got != VersionV1 {
		t.Fatalf("legacy Mac schema version = %d, want %d", got, VersionV1)
	}
	for name, contents := range map[string]string{"Mac": macV2ConfigFixture, "Linux": linuxV2ConfigFixture} {
		loaded, err := parse([]byte(contents))
		if err != nil || loaded.SchemaVersion() != VersionV2 {
			t.Fatalf("existing V2 %s parse = version %d, error %v", name, loaded.SchemaVersion(), err)
		}
	}
	devMac, err := DeriveLaneNamespace(HostKindMac, ReleaseLaneDev, "")
	if err != nil {
		t.Fatal(err)
	}
	partialV3, err := NamespaceDocumentYAML(devMac)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parse(partialV3); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("generic V1/V2 loader accepted the incomplete V3 namespace document: %v", err)
	}
}

func TestLanesL101RejectsNamespaceOverridesAndLocalDevQASTargets(t *testing.T) {
	devMac, err := DeriveLaneNamespace(HostKindMac, ReleaseLaneDev, "")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := NamespaceDocumentYAML(devMac)
	if err != nil {
		t.Fatal(err)
	}
	qasRoot := strings.Replace(devMac.ServiceRoot, "RemoteSessionRunner-dev", "RemoteSessionRunner-qas", 1)
	foreignPath := strings.Replace(string(doc), devMac.ServiceRoot, qasRoot, 1)
	if _, err := ParseV3Namespace([]byte(foreignPath)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted a path from a different release lane: %v", err)
	}
	wrongLabel := strings.Replace(string(doc), "com.remote-session-runner.local-dev", "com.remote-session-runner.local-qas", 1)
	if _, err := ParseV3Namespace([]byte(wrongLabel)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted a foreign launch-agent label: %v", err)
	}
	wrongLock := strings.Replace(string(doc), "runner-locald-lifecycle.lock", "runnerd-lifecycle.lock", 1)
	if _, err := ParseV3Namespace([]byte(wrongLock)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted a foreign lifecycle-lock path: %v", err)
	}
	devLinux, err := DeriveLaneNamespace(HostKindLinux, ReleaseLaneDev, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	linuxDoc, err := NamespaceDocumentYAML(devLinux)
	if err != nil {
		t.Fatal(err)
	}
	wrongPort := strings.Replace(string(linuxDoc), "queue_port: 2224", "queue_port: 2225", 1)
	if _, err := ParseV3Namespace([]byte(wrongPort)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted another lane's queued port: %v", err)
	}
	wrongRunner := strings.Replace(string(linuxDoc), "/bin/runnerd\n", "/bin/runner-locald\n", 1)
	if _, err := ParseV3Namespace([]byte(wrongRunner)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted a foreign executable path: %v", err)
	}
	unknownNamespaceField := strings.Replace(string(doc), "    staging:", "    untrusted_path: /tmp/other-lane\n    staging:", 1)
	if _, err := ParseV3Namespace([]byte(unknownNamespaceField)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted an undeclared path field: %v", err)
	}
	missingLane := strings.Replace(string(doc), "release_lane: dev\n", "", 1)
	if _, err := ParseV3Namespace([]byte(missingLane)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted a missing release_lane: %v", err)
	}
	invalidLane := strings.Replace(string(doc), "release_lane: dev", "release_lane: staging", 1)
	if _, err := ParseV3Namespace([]byte(invalidLane)); !errors.Is(err, ErrInvalidLaneIdentity) {
		t.Fatalf("V3 accepted an unsupported release lane: %v", err)
	}

	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS} {
		if err := ValidateV3Target(lane, domain.TargetKindLocal, "mac-workstation"); !errors.Is(err, ErrInvalidLaneIdentity) {
			t.Fatalf("%s accepted local target: %v", lane, err)
		}
		if err := ValidateV3Target(lane, domain.TargetKindRemote, "linux-host"); err != nil {
			t.Fatalf("%s rejected fixed remote target: %v", lane, err)
		}
		if err := ValidateV3Target(lane, domain.TargetKindRemote, "unknown-host"); !errors.Is(err, ErrInvalidLaneIdentity) {
			t.Fatalf("%s accepted unknown remote profile: %v", lane, err)
		}
	}
	if err := ValidateV3Target(ReleaseLanePRD, domain.TargetKindLocal, "mac-workstation"); err != nil {
		t.Fatalf("PRD compatibility target was rejected: %v", err)
	}
}

func assertV3NamespaceRoundTrip(t *testing.T, want LaneNamespace) {
	t.Helper()
	contents, err := NamespaceDocumentYAML(want)
	if err != nil {
		t.Fatalf("marshal %s namespace: %v", want.Lane, err)
	}
	got, err := ParseV3Namespace(contents)
	if err != nil {
		t.Fatalf("parse %s namespace: %v\n%s", want.Lane, err, contents)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("namespace round trip = %+v, want %+v", got, want)
	}
}
