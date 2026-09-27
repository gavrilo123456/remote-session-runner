package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/sshbridge"
)

var errP105RemoteTripwireUsed = errors.New("P105 remote dependency tripwire was used by Mac ingress")

type p105RemoteTripwires struct {
	mu                 sync.Mutex
	credentialLoads    int
	remoteDialAttempts int
}

func (t *p105RemoteTripwires) loadCredentials() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.credentialLoads++
}

func (t *p105RemoteTripwires) dialRemote() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.remoteDialAttempts++
}

func (t *p105RemoteTripwires) counts() (int, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.credentialLoads, t.remoteDialAttempts
}

// p105TripwireOperations deliberately exposes an extra generic Call method in
// addition to SessionOperations. If mailbox ingress starts discovering or
// invoking remote capabilities through its injected interface, the counters
// make that architectural regression visible.
type p105TripwireOperations struct {
	mailbox.SessionOperations
	tripwires *p105RemoteTripwires
}

func (o *p105TripwireOperations) Call(context.Context, sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	o.tripwires.loadCredentials()
	o.tripwires.dialRemote()
	return sshbridge.ReplyFrame{}, errP105RemoteTripwireUsed
}

func TestP105I06MailboxHandlersCannotReachRemoteDependencies(t *testing.T) {
	for _, fixture := range []struct {
		name, kind, profile, environment string
	}{
		{name: "local", kind: "local", profile: "mac-workstation", environment: "mac-dev"},
		{name: "queued remote", kind: "remote", profile: "linux-host", environment: "linux-dev"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			h := newP095Harness(t)
			tripwires := &p105RemoteTripwires{}
			operations := &p105TripwireOperations{SessionOperations: h.server, tripwires: tripwires}
			processor, err := mailbox.NewSessionProcessor(mailbox.SessionProcessorOptions{
				Importer: h.importer, Authority: h.authority, Controller: p063Owner(t), Operations: operations,
				Outbox: h.outbox, EventFiles: h.eventFiles,
			})
			if err != nil {
				t.Fatal(err)
			}

			var sessionID, commandID string
			operationsToExercise := []struct {
				name  string
				body  map[string]any
				check func(p095Response, map[string]any)
			}{
				{
					name: "create_session",
					body: map[string]any{
						"idempotency_key": "key-p105-create-" + fixture.kind, "operation": "create_session",
						"environment":      fixture.environment,
						"execution_target": map[string]string{"kind": fixture.kind, "profile": fixture.profile},
						"source":           map[string]string{"mode": "empty"},
					},
					check: func(response p095Response, _ map[string]any) {
						if response.SessionID == "" {
							t.Fatal("create_session returned no session ID")
						}
						sessionID = response.SessionID
					},
				},
				{
					name: "get_session",
					body: map[string]any{"operation": "get_session", "session_id": "session"},
					check: func(response p095Response, _ map[string]any) {
						if response.SessionID != sessionID {
							t.Fatalf("get_session returned session %q, want %q", response.SessionID, sessionID)
						}
					},
				},
				{
					name: "submit_command",
					body: map[string]any{
						"idempotency_key": "key-p105-submit-" + fixture.kind, "operation": "submit_command",
						"session_id": "session", "script": "printf p105", "timeout_seconds": 30,
					},
					check: func(response p095Response, _ map[string]any) {
						if response.CommandID == "" || response.SessionID != sessionID {
							t.Fatalf("submit_command response = %+v", response)
						}
						commandID = response.CommandID
					},
				},
				{
					name: "get_command",
					body: map[string]any{"operation": "get_command", "command_id": "command"},
					check: func(response p095Response, _ map[string]any) {
						if response.CommandID != commandID {
							t.Fatalf("get_command returned command %q, want %q", response.CommandID, commandID)
						}
					},
				},
				{
					name: "cancel_command",
					body: map[string]any{"idempotency_key": "key-p105-cancel-" + fixture.kind, "operation": "cancel_command", "command_id": "command"},
					check: func(response p095Response, _ map[string]any) {
						if response.CommandID != commandID {
							t.Fatalf("cancel_command returned command %q, want %q", response.CommandID, commandID)
						}
					},
				},
				{
					name: "close_session",
					body: map[string]any{"idempotency_key": "key-p105-close-" + fixture.kind, "operation": "close_session", "session_id": "session", "close_policy": map[string]string{"policy": "cancel"}},
					check: func(response p095Response, _ map[string]any) {
						if response.SessionID != sessionID {
							t.Fatalf("close_session returned session %q, want %q", response.SessionID, sessionID)
						}
					},
				},
				{
					name: "run",
					body: map[string]any{
						"idempotency_key": "key-p105-run-" + fixture.kind, "operation": "run",
						"environment":      fixture.environment,
						"execution_target": map[string]string{"kind": fixture.kind, "profile": fixture.profile},
						"script":           "printf p105-run", "timeout_seconds": 30,
					},
					check: func(_ p095Response, response map[string]any) {
						if response["job_id"] == nil || response["job_id"] == "" || response["session_id"] == nil || response["session_id"] == "" || response["command_id"] == nil || response["command_id"] == "" {
							t.Fatalf("run response lacks stable job/session/command IDs: %+v", response)
						}
					},
				},
			}

			for index, operation := range operationsToExercise {
				requestID := fmt.Sprintf("req-p105-%s-%02d", fixture.kind, index)
				operation.body["request_id"] = requestID
				if operation.name == "get_session" {
					operation.body["session_id"] = sessionID
				}
				if operation.name == "submit_command" || operation.name == "close_session" {
					operation.body["session_id"] = sessionID
				}
				if operation.name == "get_command" || operation.name == "cancel_command" {
					operation.body["command_id"] = commandID
				}
				writeP094Request(t, h.importer, requestID, operation.body)
				results, err := processor.Import(context.Background())
				if err != nil || len(results) != 1 || !results[0].Durable || !results[0].PairRemoved {
					t.Fatalf("%s mailbox result=%+v err=%v", operation.name, results, err)
				}
				responseBytes, err := h.outbox.Read(requestID)
				if err != nil {
					t.Fatal(err)
				}
				var responseMap map[string]any
				if err := json.Unmarshal(responseBytes, &responseMap); err != nil {
					t.Fatal(err)
				}
				response := readP095Response(t, h.outbox, requestID)
				if response.RequestID != requestID || response.Operation != operation.name || response.RequestState == "rejected" {
					t.Fatalf("%s response = %+v", operation.name, response)
				}
				operation.check(response, responseMap)
			}

			if loads, dials := tripwires.counts(); loads != 0 || dials != 0 {
				t.Fatalf("Mac mailbox ingress used remote dependencies: credential_loads=%d remote_dials=%d", loads, dials)
			}
		})
	}
}

func TestP105I06UnixAPIHandlersRemainLocalForBothTargets(t *testing.T) {
	for _, fixture := range []struct {
		name, kind, profile, environment string
	}{
		{name: "local", kind: "local", profile: "mac-workstation", environment: "mac-dev"},
		{name: "queued remote", kind: "remote", profile: "linux-host", environment: "linux-dev"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, _, _, client := p063Server(t)
			body := fmt.Sprintf(`{"environment":%q,"execution_target":{"kind":%q,"profile":%q},"source":{"mode":"empty"}}`, fixture.environment, fixture.kind, fixture.profile)
			sessionID := p064CreateSession(t, client, body, "key-p105-api-session-"+fixture.kind)
			if response, err := client.Get("http://local/v1/sessions/" + sessionID); err != nil {
				t.Fatal(err)
			} else {
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("get session status = %d", response.StatusCode)
				}
			}
			commandID := p066SubmitCommand(t, client, sessionID, "key-p105-api-command-"+fixture.kind)
			if response, err := client.Get("http://local/v1/commands/" + commandID); err != nil {
				t.Fatal(err)
			} else {
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("get command status = %d", response.StatusCode)
				}
			}
			if response, err := client.Get("http://local/v1/commands/" + commandID + "/events"); err != nil {
				t.Fatal(err)
			} else {
				response.Body.Close()
				wantStatus := http.StatusNotFound
				if fixture.kind == "remote" {
					wantStatus = http.StatusConflict
				}
				if response.StatusCode != wantStatus {
					t.Fatalf("get events status = %d, want %d", response.StatusCode, wantStatus)
				}
			}
			cancelRequest, err := http.NewRequest(http.MethodPost, "http://local/v1/commands/"+commandID+"/cancel", strings.NewReader(`{"reason":"user_requested"}`))
			if err != nil {
				t.Fatal(err)
			}
			cancelRequest.Header.Set("Idempotency-Key", "key-p105-api-cancel-"+fixture.kind)
			cancelResponse, err := client.Do(cancelRequest)
			if err != nil {
				t.Fatal(err)
			}
			cancelResponse.Body.Close()
			if cancelResponse.StatusCode != http.StatusAccepted {
				t.Fatalf("cancel status = %d", cancelResponse.StatusCode)
			}
			closeRequest, err := http.NewRequest(http.MethodDelete, "http://local/v1/sessions/"+sessionID, strings.NewReader(`{"policy":"cancel"}`))
			if err != nil {
				t.Fatal(err)
			}
			closeRequest.Header.Set("Idempotency-Key", "key-p105-api-close-"+fixture.kind)
			closeResponse, err := client.Do(closeRequest)
			if err != nil {
				t.Fatal(err)
			}
			closeResponse.Body.Close()
			if closeResponse.StatusCode != http.StatusAccepted {
				t.Fatalf("close status = %d", closeResponse.StatusCode)
			}
			jobBody := fmt.Sprintf(`{"environment":%q,"execution_target":{"kind":%q,"profile":%q},"script":"printf p105"}`, fixture.environment, fixture.kind, fixture.profile)
			jobRequest, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(jobBody))
			if err != nil {
				t.Fatal(err)
			}
			jobRequest.Header.Set("Idempotency-Key", "key-p105-api-run-"+fixture.kind)
			jobResponse, err := client.Do(jobRequest)
			if err != nil {
				t.Fatal(err)
			}
			var accepted jobAcceptance
			if err := json.NewDecoder(jobResponse.Body).Decode(&accepted); err != nil {
				jobResponse.Body.Close()
				t.Fatal(err)
			}
			jobResponse.Body.Close()
			if jobResponse.StatusCode != http.StatusAccepted || accepted.JobID == "" {
				t.Fatalf("run status/body = %d/%+v", jobResponse.StatusCode, accepted)
			}
			jobRead, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
			if err != nil {
				t.Fatal(err)
			}
			jobRead.Body.Close()
			if jobRead.StatusCode != http.StatusOK {
				t.Fatalf("get job status = %d", jobRead.StatusCode)
			}
		})
	}
}

var _ mailbox.SessionOperations = (*p105TripwireOperations)(nil)
