package mailbox

import (
	"errors"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP153ImporterRetainsNewWorkSelectionPresence(t *testing.T) {
	importer, err := NewImporter(p081MailboxRoot(t), nil)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name               string
		requestID          string
		raw                string
		environmentPresent bool
		environment        string
		targetPresent      bool
		targetKind         domain.TargetKind
		targetProfile      string
		repositoryAlias    string
	}{
		{
			name:               "create session uses inbox default when pair is omitted",
			requestID:          "req-p153-create-default",
			raw:                `{"request_id":"req-p153-create-default","idempotency_key":"key-p153-create-default","operation":"create_session"}`,
			environmentPresent: false,
			targetPresent:      false,
		},
		{
			name:               "run retains environment only partial override",
			requestID:          "req-p153-run-environment-only",
			raw:                `{"request_id":"req-p153-run-environment-only","idempotency_key":"key-p153-run-environment-only","operation":"run","environment":"linux-dev","script":"echo partial"}`,
			environmentPresent: true,
			environment:        "linux-dev",
			targetPresent:      false,
		},
		{
			name:               "run retains target only partial override",
			requestID:          "req-p153-run-target-only",
			raw:                `{"request_id":"req-p153-run-target-only","idempotency_key":"key-p153-run-target-only","operation":"run","execution_target":{"kind":"remote","profile":"linux-host"},"script":"echo partial"}`,
			environmentPresent: false,
			targetPresent:      true,
			targetKind:         domain.TargetKindRemote,
			targetProfile:      "linux-host",
		},
		{
			name:               "complete request override retains bounded alias metadata",
			requestID:          "req-p153-complete-override",
			raw:                `{"request_id":"req-p153-complete-override","idempotency_key":"key-p153-complete-override","operation":"run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"repository_alias":"analytics-dbt","script":"echo override"}`,
			environmentPresent: true,
			environment:        "linux-dev",
			targetPresent:      true,
			targetKind:         domain.TargetKindRemote,
			targetProfile:      "linux-host",
			repositoryAlias:    "analytics-dbt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := importer.validateRequest(test.requestID, []byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if request.EnvironmentPresent != test.environmentPresent || request.Environment != test.environment ||
				request.ExecutionTargetPresent != test.targetPresent || request.RepositoryAlias != test.repositoryAlias {
				t.Fatalf("selection fields = %+v", request)
			}
			if request.ExecutionTarget.Kind() != test.targetKind || request.ExecutionTarget.Profile() != test.targetProfile {
				t.Fatalf("execution target = (%q, %q), want (%q, %q)", request.ExecutionTarget.Kind(), request.ExecutionTarget.Profile(), test.targetKind, test.targetProfile)
			}
			if string(request.RawJSON) != test.raw {
				t.Fatalf("RawJSON changed: got %s want %s", request.RawJSON, test.raw)
			}
		})
	}
}

func TestP153ImporterRestrictsRepositoryAliasToNewWorkRequests(t *testing.T) {
	importer, err := NewImporter(p081MailboxRoot(t), nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		requestID string
		raw       string
	}{
		{
			requestID: "req-p153-invalid-alias",
			raw:       `{"request_id":"req-p153-invalid-alias","idempotency_key":"key-p153-invalid-alias","operation":"run","repository_alias":"Analytics","script":"echo invalid"}`,
		},
		{
			requestID: "req-p153-long-alias",
			raw:       `{"request_id":"req-p153-long-alias","idempotency_key":"key-p153-long-alias","operation":"run","repository_alias":"` + strings.Repeat("a", 64) + `","script":"echo invalid"}`,
		},
		{
			requestID: "req-p153-other-operation",
			raw:       `{"request_id":"req-p153-other-operation","operation":"get_session","session_id":"sess-p153","repository_alias":"analytics-dbt"}`,
		},
	} {
		if _, err := importer.validateRequest(test.requestID, []byte(test.raw)); !errors.Is(err, ErrMailboxSchema) {
			t.Fatalf("validateRequest(%s) error = %v, want ErrMailboxSchema", test.requestID, err)
		}
	}
}
