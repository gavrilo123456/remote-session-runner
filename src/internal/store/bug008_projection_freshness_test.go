package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestBUG008MarkAcceptedRemoteRunProjectionsStaleMatchesFullIntentIdentity(t *testing.T) {
	ctx := context.Background()
	authority := p077Store(t)
	when := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)

	matchingSource, err := domain.NewGitRevisionSource("bug008-repository", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	matchingIntent, matchingProjection := pBUG008AcceptedRemoteRun(t, "matching", matchingSource, matchingSource, when)
	if _, err := authority.CreateLocalIntent(ctx, matchingIntent); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.UpsertRemoteJobProjection(ctx, matchingProjection); err != nil {
		t.Fatal(err)
	}

	intentSource, err := domain.NewGitRevisionSource("bug008-repository", "fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	mismatchedIntent, mismatchedProjection := pBUG008AcceptedRemoteRun(t, "wrong-source", intentSource, matchingSource, when)
	if _, err := authority.CreateLocalIntent(ctx, mismatchedIntent); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.UpsertRemoteJobProjection(ctx, mismatchedProjection); err != nil {
		t.Fatal(err)
	}

	marked, err := authority.MarkAcceptedRemoteRunProjectionsStale(ctx)
	if err != nil || marked != 1 {
		t.Fatalf("marked accepted remote projections=%d err=%v, want one exact-source projection", marked, err)
	}
	matching, err := authority.GetRemoteJobProjection(ctx, matchingIntent.JobID)
	if err != nil || !matching.IsStale {
		t.Fatalf("matching projection=%+v err=%v, want stale", matching, err)
	}
	mismatched, err := authority.GetRemoteJobProjection(ctx, mismatchedIntent.JobID)
	if err != nil || mismatched.IsStale {
		t.Fatalf("source-mismatched projection=%+v err=%v, want fresh", mismatched, err)
	}
	marked, err = authority.MarkAcceptedRemoteRunProjectionsStale(ctx)
	if err != nil || marked != 0 {
		t.Fatalf("second freshness initialization marked=%d err=%v, want zero", marked, err)
	}
}

func pBUG008AcceptedRemoteRun(t *testing.T, suffix string, intentSource, projectionSource domain.Source, observedAt time.Time) (LocalIntentCreate, RemoteJobProjection) {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	jobID := domain.JobID("job-bug008-freshness-" + suffix)
	sessionID := domain.SessionID("sess-bug008-freshness-" + suffix)
	commandID := domain.CommandID("cmd-bug008-freshness-" + suffix)
	script := "printf bug008-freshness"
	payload, err := json.Marshal(map[string]any{
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": string(domain.TargetKindRemote), "profile": target.Profile()},
		"job_id":           string(jobID),
		"session_id":       string(sessionID),
		"command_id":       string(commandID),
		"script":           script,
		"source":           pBUG008SourceDocument(intentSource),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state := domain.CommandStateQueued
	return LocalIntentCreate{
		IntentID: domain.IntentID("intent-bug008-freshness-" + suffix), Operation: localIntentRunOperation,
		ResourceID: string(jobID), JobID: jobID, SessionID: sessionID, CommandID: commandID,
		Target: target, Environment: "linux-dev", Controller: controller, Source: intentSource,
		RequestHash: hash, IdempotencyKey: "key-bug008-freshness-" + suffix, PayloadJSON: canonical,
		ScriptBytes: []byte(script), DeliveryState: LocalIntentAccepted,
	}, RemoteJobProjection{
		JobID: jobID, SessionID: sessionID, CommandID: commandID, Phase: JobPhaseAwaitingCommand,
		CommandState: &state, TeardownState: JobTeardownPending, Target: target, Controller: controller,
		Environment: "linux-dev", Source: projectionSource, Capabilities: p076Capabilities(), ObservedAt: observedAt,
	}
}

func pBUG008SourceDocument(source domain.Source) map[string]string {
	document := map[string]string{"mode": string(source.Mode())}
	if source.Mode() == domain.SourceModeGitRevision {
		document["repository_alias"] = source.RepositoryAlias()
		document["requested_revision"] = source.RequestedRevision()
	}
	return document
}
