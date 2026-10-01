package dispatcher

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

const p161SecretStatusText = "BUG003_SECRET_STATUS_TEXT"

// TestBUG003AcceptedRemoteRunStatusDiagnosticsAreDurableAndRedacted proves
// that retry diagnostics do not change delivery semantics, survive repeated
// reconciliation, and never expose an underlying bridge error through the
// safe operational error surface.
func TestBUG003AcceptedRemoteRunStatusDiagnosticsAreDurableAndRedacted(t *testing.T) {
	ctx := context.Background()
	authority := p068Authority(t)
	intent := p149AcceptRemoteRun(t, authority, "bug003-status")
	caller := &p161SecretStatusCaller{}
	driver, err := NewRemoteDriverWithClock(authority, caller, "router-bug003", time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	var firstObserved time.Time
	for wantAttempt := 1; wantAttempt <= 2; wantAttempt++ {
		err := driver.ReconcileAcceptedRun(ctx, intent.IntentID)
		if !errors.Is(err, ErrRemoteResponse) {
			t.Fatalf("attempt %d reconciliation error=%v, want ErrRemoteResponse", wantAttempt, err)
		}
		if strings.Contains(err.Error(), p161SecretStatusText) {
			t.Fatalf("attempt %d safe reconciliation error leaked secret: %q", wantAttempt, err)
		}
		issues := RemoteReconciliationIssues(err)
		if len(issues) != 1 {
			t.Fatalf("attempt %d safe issues=%+v, want one", wantAttempt, issues)
		}
		issue := issues[0]
		if issue.Operation != "run" || issue.IntentID != string(intent.IntentID) || issue.JobID != string(intent.JobID) ||
			issue.SessionID != string(intent.SessionID) || issue.CommandID != string(intent.CommandID) ||
			issue.TargetProfile != "linux-host" || issue.FailureClass != store.RemoteStatusFailureCodeUnavailable || issue.RetryCount != wantAttempt {
			t.Fatalf("attempt %d safe issue=%+v", wantAttempt, issue)
		}

		stored, readErr := authority.GetLocalIntent(ctx, intent.IntentID)
		if readErr != nil || stored.DeliveryState != store.LocalIntentAccepted || stored.RemoteStatusFailureAt == nil ||
			stored.RemoteStatusFailureCode != store.RemoteStatusFailureCodeUnavailable || stored.RemoteStatusFailureAttempts != wantAttempt {
			t.Fatalf("attempt %d stored status marker=%+v err=%v", wantAttempt, stored, readErr)
		}
		if wantAttempt == 1 {
			firstObserved = *stored.RemoteStatusFailureAt
		} else if !stored.RemoteStatusFailureAt.Equal(firstObserved) {
			t.Fatalf("attempt %d changed the bounded-status deadline anchor: got=%s want=%s", wantAttempt, stored.RemoteStatusFailureAt, firstObserved)
		}
	}
	if caller.getJobCalls != 2 {
		t.Fatalf("status reads=%d, want 2", caller.getJobCalls)
	}
}

func TestBUG003SafeIssueExtractionHandlesNonComparableErrors(t *testing.T) {
	if issues := RemoteReconciliationIssues(p161NonComparableError{"transport", "detail"}); len(issues) != 0 {
		t.Fatalf("unwrapped non-comparable error produced issues=%+v", issues)
	}
}

type p161SecretStatusCaller struct {
	getJobCalls int
}

type p161NonComparableError []string

func (e p161NonComparableError) Error() string { return "non-comparable transport error" }

func (c *p161SecretStatusCaller) Call(_ context.Context, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if request.Operation != sshbridge.OperationGetJob {
		return sshbridge.ReplyFrame{}, errors.New("unexpected BUG-003 bridge operation")
	}
	c.getJobCalls++
	return sshbridge.ReplyFrame{}, errors.New(p161SecretStatusText)
}
