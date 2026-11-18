package runnerlocal

import (
	"bytes"
	"strings"
	"testing"

	"remote-session-runner/src/internal/dispatcher"
)

func TestBUG034RemoteObservationLogsSafeStageAndCategory(t *testing.T) {
	service := &Service{remoteEndpoints: map[string]string{"linux-host": "79.72.94.59:22"}}
	for _, tc := range []struct {
		name, stage, category, wantStage, wantCategory string
	}{
		{"timeout", "final_job", "timeout", "final_job", "timeout"},
		{"transport", "command", "transport", "command", "transport"},
		{"invalid response", "initial_job", "invalid_response", "initial_job", "invalid_response"},
		{"unsafe values", "BUG034_SECRET_STAGE\nforged=true", "BUG034_SECRET_CATEGORY", "not_applicable", "not_applicable"},
		{"older issue", "", "", "not_applicable", "not_applicable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := &dispatcher.RemoteReconciliationIssue{
				Operation: "run", IntentID: "intent-bug034", JobID: "job-bug034",
				SessionID: "sess-bug034", CommandID: "cmd-bug034", TargetProfile: "linux-host",
				FailureClass: "remote_status_unavailable", RetryCount: 3,
				Stage: tc.stage, ObservationCategory: tc.category,
			}
			var output bytes.Buffer
			service.logRemoteReconciliationError(&output, issue)
			line := output.String()
			for _, want := range []string{
				"job_id=job-bug034", "endpoint=79.72.94.59:22", "failure_class=remote_status_unavailable",
				"retry_count=3", "stage=" + tc.wantStage, "observation_category=" + tc.wantCategory,
			} {
				if !strings.Contains(line, want) {
					t.Fatalf("diagnostic missing %q: %q", want, line)
				}
			}
			if strings.Contains(line, "BUG034_SECRET") || strings.Count(line, "\n") != 1 {
				t.Fatalf("untrusted observation diagnostics escaped the allowlist: %q", line)
			}
		})
	}
}
