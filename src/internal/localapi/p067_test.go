package localapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP067RunGetJobStableCorrelationForLocalAndQueuedRemote(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		body   string
		target string
	}{
		{name: "local", body: `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"printf local","timeout_seconds":9}`, target: "local"},
		{name: "queued remote", body: `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"git_revision","repository_alias":"runner","requested_revision":"main"},"script":"printf remote","timeout_seconds":11}`, target: "remote"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, authority, db, client := p063Server(t)
			request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Idempotency-Key", "p067-run-"+fixture.name)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("run status = %d, body=%s", response.StatusCode, data)
			}
			var accepted jobAcceptance
			if err := json.Unmarshal(data, &accepted); err != nil {
				t.Fatal(err)
			}
			if accepted.ResourceID == "" || accepted.ResourceID != accepted.JobID || accepted.SessionID == "" || accepted.CommandID == "" || accepted.IntentID == "" || accepted.AcceptanceScope != "local_intent" || accepted.ExecutionTarget.Kind != fixture.target || accepted.KnownState.DeliveryState != "recorded" {
				t.Fatalf("job acceptance = %+v", accepted)
			}
			if !strings.HasPrefix(accepted.JobID, "job-") || !strings.HasPrefix(accepted.SessionID, "sess-") || !strings.HasPrefix(accepted.CommandID, "cmd-") || !strings.HasPrefix(accepted.IntentID, "intent-") {
				t.Fatalf("job identities = %+v", accepted)
			}
			intent, err := authority.GetLocalIntentByResource(context.Background(), "run", accepted.JobID, p063Owner(t))
			if err != nil {
				t.Fatal(err)
			}
			if intent.JobID != domain.JobID(accepted.JobID) || intent.SessionID != domain.SessionID(accepted.SessionID) || intent.CommandID != domain.CommandID(accepted.CommandID) || intent.Target.Kind() != domain.TargetKind(fixture.target) || string(intent.ScriptBytes) == "" {
				t.Fatalf("job intent = %+v", intent)
			}
			getResponse, err := client.Get("http://local/v1/jobs/" + accepted.JobID)
			if err != nil {
				t.Fatal(err)
			}
			getData, _ := io.ReadAll(getResponse.Body)
			getResponse.Body.Close()
			if getResponse.StatusCode != http.StatusOK {
				t.Fatalf("get job status = %d, body=%s", getResponse.StatusCode, getData)
			}
			var read jobRead
			if err := json.Unmarshal(getData, &read); err != nil {
				t.Fatal(err)
			}
			if read.View != "local_intent" || read.IsStale || read.Resource.JobID != accepted.JobID || read.Resource.SessionID != accepted.SessionID || read.Resource.CommandID != accepted.CommandID || read.Resource.ExecutionTarget.Kind != fixture.target || read.Resource.DeliveryState != "recorded" {
				t.Fatalf("job read = %+v", read)
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
				t.Fatalf("local run ingress created authority state: jobs=%d sessions=%d commands=%d", jobs, sessions, commands)
			}

			retry, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			retry.Header.Set("Idempotency-Key", "p067-run-"+fixture.name)
			retryResponse, err := client.Do(retry)
			if err != nil {
				t.Fatal(err)
			}
			retryData, _ := io.ReadAll(retryResponse.Body)
			retryResponse.Body.Close()
			var retryAccepted jobAcceptance
			if retryResponse.StatusCode != http.StatusAccepted || json.Unmarshal(retryData, &retryAccepted) != nil || retryAccepted.JobID != accepted.JobID || retryAccepted.SessionID != accepted.SessionID || retryAccepted.CommandID != accepted.CommandID || retryAccepted.IntentID != accepted.IntentID {
				t.Fatalf("job retry status/body = %d/%s", retryResponse.StatusCode, retryData)
			}

			changedBody := strings.Replace(fixture.body, "printf local", "printf changed", 1)
			if fixture.target == "remote" {
				changedBody = strings.Replace(fixture.body, "printf remote", "printf changed", 1)
			}
			conflict, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(changedBody))
			if err != nil {
				t.Fatal(err)
			}
			conflict.Header.Set("Idempotency-Key", "p067-run-"+fixture.name)
			conflictResponse, err := client.Do(conflict)
			if err != nil {
				t.Fatal(err)
			}
			defer conflictResponse.Body.Close()
			if conflictResponse.StatusCode != http.StatusConflict {
				body, _ := io.ReadAll(conflictResponse.Body)
				t.Fatalf("changed run status = %d, body=%s", conflictResponse.StatusCode, body)
			}
		})
	}
}

func TestP067OversizeBodyAndScriptLeaveNoRunIntent(t *testing.T) {
	_, _, db, client := p063Server(t)
	scriptBody := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"` + strings.Repeat("x", domain.MaxScriptUTF8Bytes+1) + `"}`
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(scriptBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p067-oversize-script")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("oversize script status = %d, body=%s", response.StatusCode, body)
	}
	response.Body.Close()
	bodyPadding := strings.Repeat("x", int(domain.MaxSerializedRequestBytes))
	body := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"script":"echo body","policy":{"padding":"` + bodyPadding + `"}}`
	request, err = http.NewRequest(http.MethodPost, "http://local/v1/jobs", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p067-oversize-body")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("oversize body status = %d, body=%s", response.StatusCode, data)
	}
	response.Body.Close()
	var intents int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents WHERE operation = 'run'").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("oversize requests inserted %d run intents", intents)
	}
}

func TestP067DirectAuthorityJobIsNotVisibleAsLocalIntent(t *testing.T) {
	_, authority, _, client := p063Server(t)
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller := p063Owner(t)
	source := domain.NewEmptySource()
	rawPayload := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"echo direct"}`)
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", rawPayload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptJob(context.Background(), store.JobAcceptance{JobID: "job-direct-p067", SessionID: "sess-direct-p067", CommandID: "cmd-direct-p067", Controller: controller, IdempotencyKey: "direct-p067", RequestHash: hash, Environment: "linux-dev", Target: target, Source: source, Script: "echo direct", CanonicalPayload: canonical, IdempotencyRetention: time.Hour}); err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://local/v1/jobs/job-direct-p067")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("direct job read status = %d, body=%s", response.StatusCode, body)
	}
}
