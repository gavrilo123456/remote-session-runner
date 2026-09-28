package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP111I01UnixResourceOperationContract(t *testing.T) {
	_, authority, db, client := p063Server(t)
	createBody := []byte(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"}}`)
	createdStatus, createdBody := p111UnixRequest(t, client, http.MethodPost, "/v1/sessions", createBody, "p111-unix-create")
	if createdStatus != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", createdStatus, createdBody)
	}
	var create struct {
		ResourceID      string         `json:"resource_id"`
		SessionID       string         `json:"session_id"`
		IntentID        string         `json:"intent_id"`
		AcceptanceScope string         `json:"acceptance_scope"`
		ExecutionTarget targetResponse `json:"execution_target"`
		KnownState      knownState     `json:"known_state"`
	}
	if err := json.Unmarshal(createdBody, &create); err != nil {
		t.Fatal(err)
	}
	if create.SessionID == "" || create.ResourceID != create.SessionID || create.IntentID == "" ||
		create.AcceptanceScope != "local_intent" || create.ExecutionTarget.Kind != string(domain.TargetKindLocal) ||
		create.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("create acceptance = %+v", create)
	}

	readSessionStatus, readSessionBody := p111UnixRequest(t, client, http.MethodGet, "/v1/sessions/"+create.SessionID, nil, "")
	if readSessionStatus != http.StatusOK {
		t.Fatalf("read session status=%d body=%s", readSessionStatus, readSessionBody)
	}
	var sessionRead map[string]json.RawMessage
	if err := json.Unmarshal(readSessionBody, &sessionRead); err != nil {
		t.Fatal(err)
	}
	var sessionView string
	if err := json.Unmarshal(sessionRead["view"], &sessionView); err != nil {
		t.Fatal(err)
	}
	var sessionResource map[string]json.RawMessage
	if err := json.Unmarshal(sessionRead["resource"], &sessionResource); err != nil {
		t.Fatal(err)
	}
	var readSessionID, sessionDelivery string
	if err := json.Unmarshal(sessionResource["session_id"], &readSessionID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sessionResource["delivery_state"], &sessionDelivery); err != nil {
		t.Fatal(err)
	}
	if sessionView != "local_intent" || readSessionID != create.SessionID || sessionDelivery != string(store.LocalIntentRecorded) {
		t.Fatalf("session read view=%q resource=%s", sessionView, sessionRead["resource"])
	}

	commandBody := []byte(`{"script":"printf p111-unix"}`)
	submitStatus, submitBody := p111UnixRequest(t, client, http.MethodPost, "/v1/sessions/"+create.SessionID+"/commands", commandBody, "p111-unix-submit")
	if submitStatus != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", submitStatus, submitBody)
	}
	var submit struct {
		ResourceID      string         `json:"resource_id"`
		CommandID       string         `json:"command_id"`
		SessionID       string         `json:"session_id"`
		IntentID        string         `json:"intent_id"`
		AcceptanceScope string         `json:"acceptance_scope"`
		ExecutionTarget targetResponse `json:"execution_target"`
		KnownState      knownState     `json:"known_state"`
	}
	if err := json.Unmarshal(submitBody, &submit); err != nil {
		t.Fatal(err)
	}
	if submit.CommandID == "" || submit.ResourceID != submit.CommandID || submit.SessionID != create.SessionID || submit.IntentID == "" ||
		submit.AcceptanceScope != "local_intent" || submit.ExecutionTarget.Kind != string(domain.TargetKindLocal) ||
		submit.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("submit acceptance = %+v", submit)
	}
	readCommandStatus, readCommandBody := p111UnixRequest(t, client, http.MethodGet, "/v1/commands/"+submit.CommandID, nil, "")
	if readCommandStatus != http.StatusOK {
		t.Fatalf("read command status=%d body=%s", readCommandStatus, readCommandBody)
	}
	var commandRead map[string]json.RawMessage
	if err := json.Unmarshal(readCommandBody, &commandRead); err != nil {
		t.Fatal(err)
	}
	var commandView string
	if err := json.Unmarshal(commandRead["view"], &commandView); err != nil {
		t.Fatal(err)
	}
	var commandResource map[string]json.RawMessage
	if err := json.Unmarshal(commandRead["resource"], &commandResource); err != nil {
		t.Fatal(err)
	}
	var readCommandID, commandDelivery string
	if err := json.Unmarshal(commandResource["command_id"], &readCommandID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(commandResource["delivery_state"], &commandDelivery); err != nil {
		t.Fatal(err)
	}
	if commandView != "local_intent" || readCommandID != submit.CommandID || commandDelivery != string(store.LocalIntentRecorded) {
		t.Fatalf("command read view=%q resource=%s", commandView, commandRead["resource"])
	}

	cancelStatus, cancelBody := p111UnixRequest(t, client, http.MethodPost, "/v1/commands/"+submit.CommandID+"/cancel", []byte(`{}`), "p111-unix-cancel")
	if cancelStatus != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancelStatus, cancelBody)
	}
	var cancel struct {
		ResourceID      string     `json:"resource_id"`
		CommandID       string     `json:"command_id"`
		SessionID       string     `json:"session_id"`
		IntentID        string     `json:"intent_id"`
		AcceptanceScope string     `json:"acceptance_scope"`
		KnownState      knownState `json:"known_state"`
	}
	if err := json.Unmarshal(cancelBody, &cancel); err != nil {
		t.Fatal(err)
	}
	if cancel.ResourceID != submit.CommandID || cancel.CommandID != submit.CommandID || cancel.SessionID != create.SessionID ||
		cancel.IntentID == "" || cancel.AcceptanceScope != "local_intent" || cancel.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("cancel acceptance = %+v", cancel)
	}

	closeStatus, closeBody := p111UnixRequest(t, client, http.MethodDelete, "/v1/sessions/"+create.SessionID, []byte(`{"policy":"cancel"}`), "p111-unix-close")
	if closeStatus != http.StatusAccepted {
		t.Fatalf("close status=%d body=%s", closeStatus, closeBody)
	}
	var closeAcceptance struct {
		ResourceID      string         `json:"resource_id"`
		SessionID       string         `json:"session_id"`
		IntentID        string         `json:"intent_id"`
		AcceptanceScope string         `json:"acceptance_scope"`
		ExecutionTarget targetResponse `json:"execution_target"`
		KnownState      knownState     `json:"known_state"`
	}
	if err := json.Unmarshal(closeBody, &closeAcceptance); err != nil {
		t.Fatal(err)
	}
	if closeAcceptance.ResourceID != create.SessionID || closeAcceptance.SessionID != create.SessionID || closeAcceptance.IntentID == "" ||
		closeAcceptance.AcceptanceScope != "local_intent" || closeAcceptance.ExecutionTarget.Kind != string(domain.TargetKindLocal) ||
		closeAcceptance.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("close acceptance = %+v", closeAcceptance)
	}

	runRequestBody := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"printf p111-run"}`)
	runStatus, runBody := p111UnixRequest(t, client, http.MethodPost, "/v1/jobs", runRequestBody, "p111-unix-run")
	if runStatus != http.StatusAccepted {
		t.Fatalf("run status=%d body=%s", runStatus, runBody)
	}
	var run struct {
		ResourceID      string         `json:"resource_id"`
		JobID           string         `json:"job_id"`
		SessionID       string         `json:"session_id"`
		CommandID       string         `json:"command_id"`
		IntentID        string         `json:"intent_id"`
		AcceptanceScope string         `json:"acceptance_scope"`
		ExecutionTarget targetResponse `json:"execution_target"`
		KnownState      knownState     `json:"known_state"`
	}
	if err := json.Unmarshal(runBody, &run); err != nil {
		t.Fatal(err)
	}
	if run.JobID == "" || run.ResourceID != run.JobID || run.SessionID == "" || run.CommandID == "" || run.IntentID == "" ||
		run.AcceptanceScope != "local_intent" || run.ExecutionTarget.Kind != string(domain.TargetKindRemote) ||
		run.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
		t.Fatalf("run acceptance = %+v", run)
	}
	readJobStatus, readJobBody := p111UnixRequest(t, client, http.MethodGet, "/v1/jobs/"+run.JobID, nil, "")
	if readJobStatus != http.StatusOK {
		t.Fatalf("get-job status=%d body=%s", readJobStatus, readJobBody)
	}
	var jobRead map[string]json.RawMessage
	if err := json.Unmarshal(readJobBody, &jobRead); err != nil {
		t.Fatal(err)
	}
	var jobView string
	if err := json.Unmarshal(jobRead["view"], &jobView); err != nil {
		t.Fatal(err)
	}
	var jobResource map[string]json.RawMessage
	if err := json.Unmarshal(jobRead["resource"], &jobResource); err != nil {
		t.Fatal(err)
	}
	var readJobID, jobDelivery string
	if err := json.Unmarshal(jobResource["job_id"], &readJobID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jobResource["delivery_state"], &jobDelivery); err != nil {
		t.Fatal(err)
	}
	if jobView != "local_intent" || readJobID != run.JobID || jobDelivery != string(store.LocalIntentRecorded) {
		t.Fatalf("job read view=%q resource=%s", jobView, jobRead["resource"])
	}

	var jobs, sessions, commands int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_commands").Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || sessions != 0 || commands != 0 {
		t.Fatalf("Unix API intents became execution authority without Router dispatch: jobs=%d sessions=%d commands=%d", jobs, sessions, commands)
	}
	if _, err := authority.GetLocalIntentByResource(context.Background(), "run", run.JobID, p063Owner(t)); err != nil {
		t.Fatalf("run intent is not readable from local authority: %v", err)
	}
}

func TestP111I04NeverDeliveredQueuedJobReadReturnsOnlyIntent(t *testing.T) {
	_, authority, db, client := p063Server(t)
	body := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"printf never-delivered"}`)
	status, responseBody := p111UnixRequest(t, client, http.MethodPost, "/v1/jobs", body, "p111-never-delivered")
	if status != http.StatusAccepted {
		t.Fatalf("queued run status=%d body=%s", status, responseBody)
	}
	var accepted struct {
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(responseBody, &accepted); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
	if err != nil {
		t.Fatal(err)
	}
	intent, err = authority.TransitionLocalIntent(context.Background(), intent.IntentID, store.LocalIntentNotDelivered, "p111_proven_not_delivered")
	if err != nil || intent.DeliveryState != store.LocalIntentNotDelivered {
		t.Fatalf("mark proven non-delivery = %+v err=%v", intent, err)
	}

	readStatus, readBody := p111UnixRequest(t, client, http.MethodGet, "/v1/jobs/"+accepted.JobID, nil, "")
	if readStatus != http.StatusOK {
		t.Fatalf("never-delivered job read status=%d body=%s", readStatus, readBody)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(readBody, &snapshot); err != nil {
		t.Fatal(err)
	}
	var view string
	if err := json.Unmarshal(snapshot["view"], &view); err != nil {
		t.Fatal(err)
	}
	var resource map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["resource"], &resource); err != nil {
		t.Fatal(err)
	}
	var jobID, sessionID, commandID, delivery string
	for field, target := range map[string]*string{
		"job_id": &jobID, "session_id": &sessionID, "command_id": &commandID, "delivery_state": &delivery,
	} {
		if err := json.Unmarshal(resource[field], target); err != nil {
			t.Fatalf("decode %s: %v", field, err)
		}
	}
	if view != "local_intent" || jobID != accepted.JobID || sessionID != accepted.SessionID || commandID != accepted.CommandID || delivery != string(store.LocalIntentNotDelivered) {
		t.Fatalf("never-delivered job snapshot view=%q resource=%s", view, snapshot["resource"])
	}
	for _, forbidden := range []string{"job_phase", "command_state", "teardown_state", "exit_code", "final_event_sequence", "authority"} {
		if _, exists := resource[forbidden]; exists {
			t.Fatalf("never-delivered job read fabricated %s: %s", forbidden, readBody)
		}
	}
	if _, err := authority.GetJob(context.Background(), domain.JobID(accepted.JobID)); err == nil {
		t.Fatal("never-delivered job created an authoritative exec_jobs row")
	} else if !errors.Is(err, store.ErrJobNotFound) {
		t.Fatalf("read authoritative never-delivered job error=%v, want ErrJobNotFound", err)
	}
	var jobs, sessions, commands int
	for table, target := range map[string]*int{"exec_jobs": &jobs, "exec_sessions": &sessions, "exec_commands": &commands} {
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if jobs != 0 || sessions != 0 || commands != 0 {
		t.Fatalf("never-delivered GET has execution rows: jobs=%d sessions=%d commands=%d", jobs, sessions, commands)
	}
}

func p111UnixRequest(t *testing.T, client *http.Client, method, path string, body []byte, key string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, "http://local"+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		request.Body = nil
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}
