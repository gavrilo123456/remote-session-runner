package localapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"remote-session-runner/src/internal/store"
)

func TestP079LocalJobReadShowsAuthorityOutcomeAfterQueuedAcceptance(t *testing.T) {
	_, authority, _, client := p063Server(t)
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"echo p079"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p079-authority")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted jobAcceptance
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || json.Unmarshal(data, &accepted) != nil {
		t.Fatalf("accept status=%d body=%s", response.StatusCode, data)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptJob(context.Background(), store.JobAcceptance{
		JobID: intent.JobID, SessionID: intent.SessionID, CommandID: intent.CommandID, Controller: intent.Controller,
		IdempotencyKey: intent.IdempotencyKey, RequestHash: intent.RequestHash, Environment: intent.Environment,
		Target: intent.Target, Source: intent.Source, Script: string(intent.ScriptBytes), CanonicalPayload: intent.PayloadJSON,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentDispatching, "p079-dispatching"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentAccepted, "p079-accepted"); err != nil {
		t.Fatal(err)
	}
	readResponse, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	readData, _ := io.ReadAll(readResponse.Body)
	readResponse.Body.Close()
	var read jobAuthorityRead
	if readResponse.StatusCode != http.StatusOK || json.Unmarshal(readData, &read) != nil || read.View != "authority" || read.IsStale || read.Resource.JobID != accepted.JobID || read.Resource.Authority != "local" || read.Resource.JobPhase != string(store.JobPhaseCreatingSession) {
		t.Fatalf("authority job read status=%d body=%s decoded=%+v", readResponse.StatusCode, readData, read)
	}
}

func TestP079ProvenNeverDeliveredJobReadRemainsIntentOutcome(t *testing.T) {
	_, authority, _, client := p063Server(t)
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"echo never"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p079-never-delivered")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var accepted jobAcceptance
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || json.Unmarshal(data, &accepted) != nil {
		t.Fatalf("accept status=%d body=%s", response.StatusCode, data)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentNotDelivered, "p079-proven-not-delivered"); err != nil {
		t.Fatal(err)
	}
	readResponse, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	readData, _ := io.ReadAll(readResponse.Body)
	readResponse.Body.Close()
	var read jobRead
	if readResponse.StatusCode != http.StatusOK || json.Unmarshal(readData, &read) != nil || read.View != "local_intent" || read.Resource.DeliveryState != string(store.LocalIntentNotDelivered) || read.Resource.JobID != accepted.JobID {
		t.Fatalf("never-delivered job read status=%d body=%s decoded=%+v", readResponse.StatusCode, readData, read)
	}
	if _, err := authority.GetJob(context.Background(), intent.JobID); err == nil {
		t.Fatal("never-delivered intent fabricated an authority job")
	}
}
