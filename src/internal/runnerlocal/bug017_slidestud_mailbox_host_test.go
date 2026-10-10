//go:build bug017host

package runnerlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"remote-session-runner/src/internal/mailboxclient"
)

const bug017SlideStudioMailboxRoot = "/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-"

// TestBUG017SlideStudioExplicitSandboxRun proves that an installed Mac ingress
// accepts the documented V1 script form through the real external mailbox.
// It is opt-in because it creates one harmless remote uname command and ACK.
func TestBUG017SlideStudioExplicitSandboxRun(t *testing.T) {
	if os.Getenv("RSR_BUG017_SLIDESTUD_GATE") != "1" {
		t.Skip("set RSR_BUG017_SLIDESTUD_GATE=1 to run the installed BUG-017 mailbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("BUG-017 installed mailbox gate must run on the selected Mac, got %s", runtime.GOOS)
	}
	client, err := mailboxclient.New(bug017SlideStudioMailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	requestID := fmt.Sprintf("req-bug017-slidestud-sandbox-%x", time.Now().UTC().UnixNano())
	request, err := json.Marshal(map[string]any{
		"request_id": requestID, "idempotency_key": "key-" + requestID, "operation": "run",
		"repository_alias": "slidestud-io", "environment": "sandbox-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "sandbox-host"},
		"script":           "uname -a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteRequest(requestID, request); err != nil {
		t.Fatalf("publish native BUG-017 request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var response mailboxclient.Response
	for {
		response, err = client.WaitResponse(ctx, requestID)
		if err != nil {
			t.Fatalf("wait for BUG-017 response: %v", err)
		}
		if response.RequestState == "complete" || response.RequestState == "rejected" || response.RequestState == "indeterminate" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if response.RequestState != "complete" || response.CommandState != "succeeded" || response.ExitCode == nil || *response.ExitCode != 0 ||
		response.ResolvedEnvironment != "sandbox-dev" || response.ResolvedExecutionTarget == nil || response.ResolvedExecutionTarget.Kind != "remote" ||
		response.ResolvedExecutionTarget.Profile != "sandbox-host" || response.ExecutionSelectionSource != "request_override" ||
		response.OutputComplete == nil || !*response.OutputComplete || response.OutputTruncated == nil || *response.OutputTruncated ||
		response.FinalEventSequence == nil || response.AvailableEventSequence == nil || *response.FinalEventSequence != *response.AvailableEventSequence || response.Stdout == "" {
		t.Fatalf("unexpected terminal response: %+v", response)
	}
	events, err := client.ReadEventsThroughCursor(response)
	if err != nil || len(events) == 0 {
		t.Fatalf("read event prefix events=%d err=%v", len(events), err)
	}
	if err := client.WriteAcknowledgment(requestID, response); err != nil {
		t.Fatalf("publish BUG-017 ACK: %v", err)
	}
	ackReady := filepath.Join(bug017SlideStudioMailboxRoot, "acks", requestID+".ready")
	for {
		_, err := os.Lstat(ackReady)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Logf("BUG-017 complete: request_id=%s command_id=%s target=%s/%s", requestID, response.CommandID, response.ResolvedExecutionTarget.Kind, response.ResolvedExecutionTarget.Profile)
}
