package localapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/store"
)

func TestP094MailboxSessionCreateReadAndCorrelation(t *testing.T) {
	server, authority, db, _ := p063Server(t)
	root := filepath.Join(t.TempDir(), "mailbox")
	importer, err := mailbox.NewImporter(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := mailbox.NewOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	eventFiles, err := mailbox.NewEventFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
		Importer: importer, Authority: authority, Controller: p063Owner(t), Operations: server, Outbox: outbox, EventFiles: eventFiles,
		ExecutionResolver: testMailboxExecutionResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	for _, fixture := range []struct {
		name, requestID, key, environment, kind, profile string
	}{
		{name: "local", requestID: "req-p094-local-create", key: "key-p094-local", environment: "mac-dev", kind: "local", profile: "mac-workstation"},
		{name: "queued-remote", requestID: "req-p094-remote-create", key: "key-p094-remote", environment: "linux-dev", kind: "remote", profile: "linux-host"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			request := map[string]any{
				"request_id": fixture.requestID, "idempotency_key": fixture.key, "operation": "create_session",
				"environment":      fixture.environment,
				"execution_target": map[string]string{"kind": fixture.kind, "profile": fixture.profile},
				"source":           map[string]string{"mode": "empty"},
			}
			writeP094Request(t, importer, fixture.requestID, request)
			results, err := processor.Import(ctx)
			if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
				t.Fatalf("create import results=%+v err=%v", results, err)
			}
			response := readP094Response(t, outbox, fixture.requestID)
			if response.RequestID != fixture.requestID || response.Operation != "create_session" || response.RequestState != "accepted" || response.ResponseRevision != 1 || response.SessionID == "" || response.DeliveryState != "recorded" {
				t.Fatalf("create response = %+v", response)
			}
			if _, err := os.Stat(filepath.Join(importer.InboxPath(), fixture.requestID+mailbox.RequestSuffix)); !os.IsNotExist(err) {
				t.Fatalf("durably accepted inbox JSON remains: %v", err)
			}
			intent, err := authority.GetLocalIntentByResource(ctx, "create_session", response.SessionID, p063Owner(t))
			if err != nil || intent.Target.Kind() != domain.TargetKind(fixture.kind) || intent.DeliveryState != store.LocalIntentRecorded {
				t.Fatalf("stored %s intent=%+v err=%v", fixture.kind, intent, err)
			}
		})
	}

	localResponse := readP094Response(t, outbox, "req-p094-local-create")
	remoteResponse := readP094Response(t, outbox, "req-p094-remote-create")

	// A same-key retry with a new request ID returns the original session but
	// binds the response to the new exchange ID.
	writeP094Request(t, importer, "req-p094-local-retry", map[string]any{
		"request_id": "req-p094-local-retry", "idempotency_key": "key-p094-local", "operation": "create_session",
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source": map[string]string{"mode": "empty"},
	})
	results, err := processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("same-key retry results=%+v err=%v", results, err)
	}
	retryResponse := readP094Response(t, outbox, "req-p094-local-retry")
	if retryResponse.RequestID != "req-p094-local-retry" || retryResponse.SessionID != localResponse.SessionID || retryResponse.RequestState != "accepted" {
		t.Fatalf("same-key retry response = %+v; original=%+v", retryResponse, localResponse)
	}
	var localIntentCount int
	if err := db.QueryRow(`SELECT count(*) FROM local_intents WHERE operation = 'create_session'`).Scan(&localIntentCount); err != nil || localIntentCount != 2 {
		t.Fatalf("create intent count=%d err=%v, want one local and one queued-remote intent", localIntentCount, err)
	}

	// Local get_session freezes an active authoritative snapshot.
	localIntent, err := authority.GetLocalIntentByResource(ctx, "create_session", localResponse.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, localIntent.IntentID, store.LocalIntentDispatching, "p094-local-dispatch"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, localIntent.IntentID, store.LocalIntentAccepted, "p094-local-accepted"); err != nil {
		t.Fatal(err)
	}
	localTarget, _ := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	localSession, err := authority.CreateSession(ctx, store.SessionCreate{
		SessionID: domain.SessionID(localResponse.SessionID), Target: localTarget, Environment: "mac-dev",
		Controller: p063Owner(t), Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	})
	if err != nil || localSession.State != domain.SessionStateCreating {
		t.Fatalf("create local authority session=%+v err=%v", localSession, err)
	}
	writeP094Request(t, importer, "req-p094-local-get", map[string]any{
		"request_id": "req-p094-local-get", "operation": "get_session", "session_id": localResponse.SessionID,
	})
	results, err = processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("local get import results=%+v err=%v", results, err)
	}
	localRead := readP094Response(t, outbox, "req-p094-local-get")
	if localRead.RequestID != "req-p094-local-get" || localRead.RequestState != "complete" || localRead.SessionState != "creating" || localRead.SessionID != localResponse.SessionID {
		t.Fatalf("local get response = %+v", localRead)
	}

	// A queued-remote projection completes the earlier create response only
	// after the authoritative remote snapshot reports readiness.
	remoteIntent, err := authority.GetLocalIntentByResource(ctx, "create_session", remoteResponse.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, remoteIntent.IntentID, store.LocalIntentDispatching, "p094-remote-dispatch"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, remoteIntent.IntentID, store.LocalIntentUncertain, "p094-remote-uncertain"); err != nil {
		t.Fatal(err)
	}
	if err := processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	uncertainCreate := readP094Response(t, outbox, "req-p094-remote-create")
	if uncertainCreate.RequestState != "accepted" || uncertainCreate.ResponseRevision != 2 || uncertainCreate.DeliveryState != "uncertain" || uncertainCreate.SessionState != "" {
		t.Fatalf("uncertain remote create invented authority state: %+v", uncertainCreate)
	}
	if _, err := authority.TransitionLocalIntent(ctx, remoteIntent.IntentID, store.LocalIntentAccepted, "p094-remote-accepted"); err != nil {
		t.Fatal(err)
	}
	remoteTarget, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if _, err := authority.UpsertRemoteSessionProjection(ctx, store.RemoteSessionProjection{
		SessionID: domain.SessionID(remoteResponse.SessionID), Target: remoteTarget, Controller: remoteIntent.Controller,
		State: domain.SessionStateReady, Environment: remoteIntent.Environment, Source: remoteIntent.Source,
		Capabilities: p076APICapabilities(), ObservedAt: time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if err := processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	remoteCreate := readP094Response(t, outbox, "req-p094-remote-create")
	if remoteCreate.RequestID != "req-p094-remote-create" || remoteCreate.RequestState != "complete" || remoteCreate.ResponseRevision != 3 || remoteCreate.SessionState != "ready" || remoteCreate.SessionID != remoteResponse.SessionID {
		t.Fatalf("reconciled remote create response = %+v", remoteCreate)
	}
	writeP094Request(t, importer, "req-p094-remote-get", map[string]any{
		"request_id": "req-p094-remote-get", "operation": "get_session", "session_id": remoteResponse.SessionID,
	})
	results, err = processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("remote get import results=%+v err=%v", results, err)
	}
	remoteRead := readP094Response(t, outbox, "req-p094-remote-get")
	if remoteRead.RequestID != "req-p094-remote-get" || remoteRead.RequestState != "complete" || remoteRead.SessionState != "ready" || remoteRead.SessionID != remoteResponse.SessionID {
		t.Fatalf("remote get response = %+v", remoteRead)
	}

	// A session created directly at another authority has no Mac intent and
	// cannot be read through the Mac mailbox.
	directID := domain.SessionID("sess-p094-direct-created")
	directTarget, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	directController, _ := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, "p094-direct-client")
	if _, err := authority.CreateSession(ctx, store.SessionCreate{
		SessionID: directID, Target: directTarget, Environment: "linux-dev", Controller: directController,
		Source: domain.NewEmptySource(), Limits: p094SessionLimits(),
	}); err != nil {
		t.Fatal(err)
	}
	writeP094Request(t, importer, "req-p094-direct-get", map[string]any{
		"request_id": "req-p094-direct-get", "operation": "get_session", "session_id": directID,
	})
	results, err = processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("direct-created get results=%+v err=%v", results, err)
	}
	directRead := readP094Response(t, outbox, "req-p094-direct-get")
	if directRead.RequestID != "req-p094-direct-get" || directRead.RequestState != "rejected" || directRead.Error == nil || directRead.Error.Code != "resource_not_found" || directRead.SessionState != "" {
		t.Fatalf("direct-created session was exposed: %+v", directRead)
	}

	writeP094Request(t, importer, "req-p094-undelivered-create", map[string]any{
		"request_id": "req-p094-undelivered-create", "idempotency_key": "key-p094-undelivered", "operation": "create_session",
		"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
	})
	results, err = processor.Import(ctx)
	if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
		t.Fatalf("not-delivered create import results=%+v err=%v", results, err)
	}
	undeliveredAccepted := readP094Response(t, outbox, "req-p094-undelivered-create")
	undeliveredIntent, err := authority.GetLocalIntentByResource(ctx, "create_session", undeliveredAccepted.SessionID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(ctx, undeliveredIntent.IntentID, store.LocalIntentNotDelivered, "p094-proven-not-delivered"); err != nil {
		t.Fatal(err)
	}
	if err := processor.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	undelivered := readP094Response(t, outbox, "req-p094-undelivered-create")
	if undelivered.RequestState != "rejected" || undelivered.DeliveryState != "not_delivered" || undelivered.SessionState != "" || undelivered.Error == nil || undelivered.Error.Code != "resource_not_found" {
		t.Fatalf("not-delivered create response fabricated authority state: %+v", undelivered)
	}
}

type p094Response struct {
	RequestID        string `json:"request_id"`
	Operation        string `json:"operation"`
	RequestState     string `json:"request_state"`
	ResponseRevision int64  `json:"response_revision"`
	SessionID        string `json:"session_id"`
	SessionState     string `json:"session_state"`
	DeliveryState    string `json:"delivery_state"`
	Error            *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeP094Request(t *testing.T, importer *mailbox.Importer, requestID string, request any) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		path string
		data []byte
	}{
		{path: filepath.Join(importer.InboxPath(), requestID+mailbox.RequestSuffix), data: raw},
		{path: filepath.Join(importer.InboxPath(), requestID+mailbox.ReadySuffix)},
	} {
		output, err := os.OpenFile(file.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mailbox.MailboxFileMode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write(file.data); err != nil {
			_ = output.Close()
			t.Fatal(err)
		}
		if err := output.Sync(); err != nil {
			_ = output.Close()
			t.Fatal(err)
		}
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func readP094Response(t *testing.T, outbox *mailbox.Outbox, requestID string) p094Response {
	t.Helper()
	raw, err := outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var response p094Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func p094SessionLimits() domain.EffectiveSessionLimits {
	return domain.EffectiveSessionLimits{
		CommandTimeout: 30 * time.Minute, IdleTimeout: 30 * time.Minute,
		SessionMaxLifetime: 4 * time.Hour, OutputBytesPerCommand: 100 << 20,
	}
}
