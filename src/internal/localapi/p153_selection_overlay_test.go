package localapi

import (
	"context"
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

func TestP153MailboxTrustedSelectionOverridesRawTargetAndRecordsAudit(t *testing.T) {
	ctx := context.Background()
	h := newP095Harness(t)
	macTarget := p153Target(t, domain.TargetKindLocal, "mac-workstation")
	linuxTarget := p153Target(t, domain.TargetKindRemote, "linux-host")
	rawTarget := p153Target(t, domain.TargetKindRemote, "untrusted-host")

	createRequestID := "req-p153-overlay-create"
	createKey := "key-p153-overlay-create"
	createRaw := p153MailboxRaw(t, map[string]any{
		"request_id": createRequestID, "idempotency_key": createKey, "operation": "create_session",
		"environment": "untrusted-dev", "execution_target": map[string]string{"kind": "remote", "profile": "untrusted-host"},
		"repository_alias": "analytics-dbt", "source": map[string]string{"mode": "empty"},
	})
	created, err := h.server.CreateSessionIntent(ctx, mailbox.Request{
		MailboxID: "analytics", RequestID: createRequestID, IdempotencyKey: createKey,
		ExecutionIdempotencyKey: pMailboxTestExecutionKey(createKey), Operation: "create_session",
		Environment: "untrusted-dev", EnvironmentPresent: true, ExecutionTargetPresent: true, ExecutionTarget: rawTarget,
		RepositoryAlias: "analytics-dbt", ExecutionSelection: &store.MailboxExecutionSelection{
			ContextName: "mac-local", Environment: "mac-dev", Target: macTarget,
			Source: store.MailboxExecutionSelectionInboxDefault, RepositoryAlias: "analytics-dbt", RepositoryAliases: []string{"analytics-dbt"},
		},
		RawJSON: createRaw,
	})
	if err != nil || created.SessionID == "" {
		t.Fatalf("CreateSessionIntent() result=%+v err=%v", created, err)
	}
	createIntent, err := h.authority.GetLocalIntentByResource(ctx, "create_session", created.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	p153AssertIntentSelection(t, createIntent, "mac-dev", macTarget, "repository_alias")

	runRequestID := "req-p153-overlay-run"
	runKey := "key-p153-overlay-run"
	const script = "printf 'p153 trusted selection\\n'"
	runRaw := p153MailboxRaw(t, map[string]any{
		"request_id": runRequestID, "idempotency_key": runKey, "operation": "run",
		"environment": "untrusted-dev", "execution_target": map[string]string{"kind": "remote", "profile": "untrusted-host"},
		"repository_alias": "analytics-dbt", "source": map[string]string{"mode": "empty"}, "script": script,
	})
	run, err := h.server.RunJobIntent(ctx, mailbox.Request{
		MailboxID: "analytics", RequestID: runRequestID, IdempotencyKey: runKey,
		ExecutionIdempotencyKey: pMailboxTestExecutionKey(runKey), Operation: "run", Script: script,
		Environment: "untrusted-dev", EnvironmentPresent: true, ExecutionTargetPresent: true, ExecutionTarget: rawTarget,
		RepositoryAlias: "analytics-dbt", ExecutionSelection: &store.MailboxExecutionSelection{
			ContextName: "ubuntu-current", Environment: "linux-dev", Target: linuxTarget,
			Source: store.MailboxExecutionSelectionRequestOverride, RepositoryAlias: "analytics-dbt", RepositoryAliases: []string{"analytics-dbt"},
		},
		RawJSON: runRaw,
	})
	if err != nil || run.JobID == "" {
		t.Fatalf("RunJobIntent() result=%+v err=%v", run, err)
	}
	runIntent, err := h.authority.GetLocalIntentByResource(ctx, "run", run.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	p153AssertIntentSelection(t, runIntent, "linux-dev", linuxTarget, "repository_alias")

	records, err := h.authority.ListAuditRecords(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("audit records=%+v, want create and run", records)
	}
	byAction := make(map[audit.Action]*audit.MailboxSelection)
	for _, record := range records {
		if record.Ingress == audit.IngressMailbox && record.Outcome == audit.OutcomeAllowed {
			byAction[record.Action] = record.MailboxSelection
		}
	}
	if selection := byAction[audit.ActionCreate]; selection == nil || selection.InboxID != "analytics" ||
		selection.ContextName != "mac-local" || selection.Environment != "mac-dev" ||
		selection.TargetKind != domain.TargetKindLocal || selection.TargetProfile != "mac-workstation" ||
		selection.Source != audit.MailboxSelectionSourceInboxDefault || selection.RepositoryAlias != "analytics-dbt" {
		t.Fatalf("create audit selection=%+v", selection)
	}
	if selection := byAction[audit.ActionRun]; selection == nil || selection.InboxID != "analytics" ||
		selection.ContextName != "ubuntu-current" || selection.Environment != "linux-dev" ||
		selection.TargetKind != domain.TargetKindRemote || selection.TargetProfile != "linux-host" ||
		selection.Source != audit.MailboxSelectionSourceRequestOverride || selection.RepositoryAlias != "analytics-dbt" {
		t.Fatalf("run audit selection=%+v", selection)
	}
}

func TestP153MailboxBodyKeepsLegacyExplicitSelectionWithoutTrustedOverlay(t *testing.T) {
	target := p153Target(t, domain.TargetKindRemote, "linux-host")
	createRaw := p153MailboxRaw(t, map[string]any{
		"request_id": "req-p153-legacy-create", "idempotency_key": "key-p153-legacy-create", "operation": "create_session",
		"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
	})
	createBody, err := mailboxCreateSessionBody(mailbox.Request{
		RequestID: "req-p153-legacy-create", IdempotencyKey: "key-p153-legacy-create", Operation: "create_session", RawJSON: createRaw,
	})
	if err != nil {
		t.Fatalf("legacy create body error=%v", err)
	}
	var create createSessionRequest
	if err := json.Unmarshal(createBody, &create); err != nil || create.Environment != "linux-dev" ||
		create.ExecutionTarget.Kind != string(target.Kind()) || create.ExecutionTarget.Profile != target.Profile() {
		t.Fatalf("legacy create body=%s decoded=%+v err=%v", createBody, create, err)
	}

	const script = "printf 'legacy run\\n'"
	runRaw := p153MailboxRaw(t, map[string]any{
		"request_id": "req-p153-legacy-run", "idempotency_key": "key-p153-legacy-run", "operation": "run",
		"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "script": script,
	})
	runBody, err := mailboxRunJobBody(mailbox.Request{
		RequestID: "req-p153-legacy-run", IdempotencyKey: "key-p153-legacy-run", Operation: "run", Environment: "linux-dev", Script: script, RawJSON: runRaw,
	})
	if err != nil {
		t.Fatalf("legacy run body error=%v", err)
	}
	var run createJobRequest
	if err := json.Unmarshal(runBody, &run); err != nil || run.Environment != "linux-dev" ||
		run.ExecutionTarget.Kind != string(target.Kind()) || run.ExecutionTarget.Profile != target.Profile() || run.Script == nil || *run.Script != script {
		t.Fatalf("legacy run body=%s decoded=%+v err=%v", runBody, run, err)
	}
}

func p153Target(t *testing.T, kind domain.TargetKind, profile string) domain.ExecutionTarget {
	t.Helper()
	target, err := domain.NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func p153MailboxRaw(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func p153AssertIntentSelection(t *testing.T, intent store.LocalIntentRecord, environment string, target domain.ExecutionTarget, forbiddenTopLevelField string) {
	t.Helper()
	if intent.Environment != environment || intent.Target.Kind() != target.Kind() || intent.Target.Profile() != target.Profile() {
		t.Fatalf("intent selection=%s/%s/%s, want %s/%s/%s", intent.Environment, intent.Target.Kind(), intent.Target.Profile(), environment, target.Kind(), target.Profile())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(intent.PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload[forbiddenTopLevelField]; exists {
		t.Fatalf("canonical intent payload leaked mailbox metadata %q: %s", forbiddenTopLevelField, intent.PayloadJSON)
	}
}
