package laneinstall

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestReadCatalogProbeTerminalResultProvesExactSuccessfulReleasedProbe(t *testing.T) {
	ctx := context.Background()
	path, binding := seedCatalogProbeResultFixture(t, "complete")
	result, err := ReadCatalogProbeTerminalResult(ctx, path, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Terminal || !result.Accepted || result.SchemaVersion != store.CurrentSchemaVersion ||
		result.JobID != binding.JobID || result.CommandState != string(domain.CommandStateSucceeded) ||
		result.SessionState != string(domain.SessionStateClosed) || result.ExitCode == nil || *result.ExitCode != 0 ||
		result.FinalEventSequence == nil || *result.FinalEventSequence != 4 || !result.OutputComplete || result.OutputTruncated ||
		result.OutputBytes != int64(len(catalogMaintenanceProbeOutput)) || result.OutputSHA256 != probeExpectedOutputSHA256() ||
		!result.CommandStopConfirmed || !result.CommandSlotReleased || !result.SessionCleanupConfirmed ||
		!result.SessionReservationReleased || !result.WorkInventory.Empty() || result.EvidenceSHA256 == "" {
		t.Fatalf("read-only probe result omitted a required proof: %+v", result)
	}
	if len(result.EventTypes) != 4 || result.EventTypes[0] != "command_queued" || result.EventTypes[1] != "command_started" ||
		result.EventTypes[2] != "stdout" || result.EventTypes[3] != "command_succeeded" {
		t.Fatalf("event summary=%v", result.EventTypes)
	}
}

func TestReadCatalogProbeTerminalResultRejectsScopeAndTerminalEvidenceDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, path string, binding *CatalogProbeResultBinding)
	}{
		{name: "wrong request identity", mutate: func(_ *testing.T, _ string, binding *CatalogProbeResultBinding) { binding.IdempotencyKey += "-other" }},
		{name: "wrong bound job", mutate: func(_ *testing.T, _ string, binding *CatalogProbeResultBinding) {
			binding.JobID = "job-00000000000000000000000000000000"
		}},
		{name: "truncated output", mutate: func(t *testing.T, path string, _ *CatalogProbeResultBinding) {
			probeResultFixtureExec(t, path, `UPDATE exec_jobs SET output_truncated=1 WHERE job_id='job-11111111111111111111111111111111'`)
		}},
		{name: "wrong output payload", mutate: func(t *testing.T, path string, _ *CatalogProbeResultBinding) {
			probeResultFixtureExec(t, path, `UPDATE exec_command_events SET payload=x'626164' , byte_count=3 WHERE command_id='cmd-22222222222222222222222222222222' AND event_type='stdout'`)
		}},
		{name: "unreleased reservation", mutate: func(t *testing.T, path string, _ *CatalogProbeResultBinding) {
			probeResultFixtureExec(t, path, `UPDATE exec_capacity_reservations SET cleanup_confirmed_at=NULL,released_at=NULL WHERE session_id='sess-33333333333333333333333333333333'`)
		}},
		{name: "unsettled unrelated work", mutate: func(t *testing.T, path string, _ *CatalogProbeResultBinding) {
			probeResultFixtureExec(t, path, `INSERT INTO exec_sessions(session_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,state,command_timeout_ns,idle_timeout_ns,session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at) VALUES('sess-44444444444444444444444444444444','remote','linux-host','linux-dev','direct_mtls','test-controller','empty','ready',5000000000,5000000000,60000000000,4096,'2026-10-26T07:00:00Z','2026-10-26T07:00:00Z','2026-10-26T08:00:00Z')`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, binding := seedCatalogProbeResultFixture(t, "complete")
			test.mutate(t, path, &binding)
			if _, err := ReadCatalogProbeTerminalResult(context.Background(), path, binding); err == nil {
				t.Fatal("inconsistent probe result was accepted")
			}
		})
	}
}

func TestReadCatalogProbeTerminalResultDoesNotCallAcceptedButNonterminalWorkComplete(t *testing.T) {
	path, binding := seedCatalogProbeResultFixture(t, "awaiting_command")
	result, err := ReadCatalogProbeTerminalResult(context.Background(), path, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Terminal || result.Accepted {
		t.Fatalf("nonterminal probe became acceptance: %+v", result)
	}
}

func TestValidCatalogProbeResultBindingAllowsOnlyTheExactAuthorizedPRDRetry(t *testing.T) {
	_, binding := seedCatalogProbeResultFixture(t, "complete")
	binding.HostID = "linux-host"
	binding.Lane = config.ReleaseLanePRD
	binding.OperationID = "63471d41af07b7afaf577d6a0989cc03"
	binding.ControllerID = config.MacAccount
	binding.Environment = "linux-dev"
	binding.IdempotencyKey = "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host-retry-1"
	binding.JobID = "job-d2c85d259d07b6a5d3c2055f87e34bc8"
	if !validCatalogProbeResultBinding(binding) {
		t.Fatal("exact authorized Linux PRD retry scope was rejected")
	}
	binding.JobID = "job-99999999999999999999999999999999"
	if validCatalogProbeResultBinding(binding) {
		t.Fatal("different retry job identity was accepted")
	}
	binding.OperationID = "64471d41af07b7afaf577d6a0989cc03"
	binding.JobID = "job-d2c85d259d07b6a5d3c2055f87e34bc8"
	if validCatalogProbeResultBinding(binding) {
		t.Fatal("retry suffix was accepted for another operation")
	}
}

func seedCatalogProbeResultFixture(t *testing.T, phase string) (string, CatalogProbeResultBinding) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "remote.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	stamp := created.Format(time.RFC3339Nano)
	released := created.Add(5 * time.Second).Format(time.RFC3339Nano)
	jobID := "job-11111111111111111111111111111111"
	sessionID := "sess-33333333333333333333333333333333"
	commandID := "cmd-22222222222222222222222222222222"
	operationID := strings.Repeat("a", 32)
	key := catalogProbeIdempotencyKey(operationID, "linux-host", CatalogProbeDirectMTLS)
	scriptBytes := []byte(catalogMaintenanceProbeScript)
	scriptHash := sha256.Sum256(scriptBytes)
	requestHash := make([]byte, sha256.Size)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO exec_sessions(session_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,state,
command_timeout_ns,idle_timeout_ns,session_max_lifetime_ns,output_bytes_per_command,created_at,updated_at,expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sessionID, "remote", "linux-host", "linux-host-dev", "direct_mtls", "test-controller",
		"empty", "closed", int64(5*time.Second), int64(time.Minute), int64(time.Hour), 4096, stamp, released, released)
	exec(`INSERT INTO exec_capacity_reservations(session_id,host_key,reserved_at,cleanup_confirmed_at,released_at) VALUES(?,?,?,?,?)`,
		sessionID, "linux-host", stamp, released, released)
	exec(`INSERT INTO exec_jobs(job_id,session_id,command_id,controller_type,controller_id,target_kind,target_profile,environment,source_mode,
request_hash_version,request_hash,idempotency_key,payload_json,script_bytes,script_sha256,phase,command_state,exit_code,
final_event_sequence,output_truncated,output_complete,output_unavailable_reason,teardown_state,teardown_reason,created_at,updated_at,ingress)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, sessionID, commandID, "direct_mtls", "test-controller", "remote",
		"linux-host", "linux-host-dev", "empty", 1, requestHash, key, []byte(`{"script":"printf 'catalog release maintenance probe\\n'"}`),
		scriptBytes, scriptHash[:], phase, "succeeded", 0, 4, 0, 1, "", "closed", "runtime_closed", stamp, released, "direct_mtls")
	exec(`INSERT INTO exec_commands(command_id,session_id,ordinal,request_hash_version,request_hash,script_bytes,script_sha256,state,timeout_ns,
exit_code,final_event_sequence,output_truncated,output_complete,output_unavailable_reason,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, commandID, sessionID, 1, 1, requestHash, scriptBytes, scriptHash[:], "succeeded",
		int64(5*time.Second), 0, 4, 0, 1, "", stamp, released)
	exec(`INSERT INTO exec_command_events(command_id,sequence,event_type,payload,byte_count,occurred_at) VALUES(?,1,'command_queued',x'',0,?)`, commandID, stamp)
	exec(`INSERT INTO exec_command_events(command_id,sequence,event_type,payload,byte_count,occurred_at) VALUES(?,2,'command_started',x'',0,?)`, commandID, released)
	exec(`INSERT INTO exec_command_events(command_id,sequence,event_type,payload,byte_count,occurred_at) VALUES(?,3,'stdout',?,?,?)`,
		commandID, []byte(catalogMaintenanceProbeOutput), len(catalogMaintenanceProbeOutput), released)
	exec(`INSERT INTO exec_command_events(command_id,sequence,event_type,payload,byte_count,occurred_at) VALUES(?,4,'command_succeeded',x'',0,?)`, commandID, released)
	exec(`INSERT INTO exec_command_slots(command_id,host_key,reserved_at,stop_confirmed_at,released_at) VALUES(?,?,?,?,?)`,
		commandID, "linux-host", stamp, released, released)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, CatalogProbeResultBinding{
		HostID: "linux-host", Lane: config.ReleaseLaneDev, Transport: CatalogProbeDirectMTLS, OperationID: operationID,
		ControllerType: domain.ControllerTypeDirectMTLS, ControllerID: "test-controller", Environment: "linux-host-dev",
		ConfigurationSHA256: strings.Repeat("b", 64), IdempotencyKey: key, RequestSHA256: strings.Repeat("c", 64),
		ScopeCreatedAt: created.Add(-time.Second).Format(time.RFC3339Nano), ScopeExpiresAt: created.Add(time.Minute).Format(time.RFC3339Nano), JobID: jobID,
	}
}

func probeResultFixtureExec(t *testing.T, path, query string) {
	t.Helper()
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}
