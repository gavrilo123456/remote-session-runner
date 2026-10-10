package mailbox

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP153ProcessorResolvesMailboxSelectionAndRejectsBeforeOperations(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)

	defaultRequest := p153ProcessorRequest(t, "req-p153-default-run", "key-p153-default-run", "run", map[string]any{
		"script": "printf default",
	})
	p153Process(t, ctx, processor, defaultRequest)
	if len(operations.runRequests) != 1 {
		t.Fatalf("default run calls=%d, want one", len(operations.runRequests))
	}
	p153AssertProcessorSelection(t, operations.runRequests[0].ExecutionSelection, resolver.defaultSelection)
	p153AssertResponseSelection(t, outbox, defaultRequest.RequestID, "analytics", "inbox_default", "linux-dev", "remote", "linux-host")
	p153AssertStoredSelection(t, authority, "analytics", defaultRequest.RequestID, resolver.defaultSelection)

	buildTarget := p153Target(t, domain.TargetKindRemote, "linux-build-host")
	overrideRequest := p153ProcessorRequest(t, "req-p153-override-run", "key-p153-override-run", "run", map[string]any{
		"environment":      "linux-build-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-build-host"},
		"repository_alias": "analytics-dbt",
		"script":           "printf override",
	})
	p153Process(t, ctx, processor, overrideRequest)
	if len(operations.runRequests) != 2 {
		t.Fatalf("override run calls=%d, want two", len(operations.runRequests))
	}
	if operations.runRequests[1].ExecutionSelection == nil || operations.runRequests[1].ExecutionSelection.Source != store.MailboxExecutionSelectionRequestOverride ||
		operations.runRequests[1].ExecutionSelection.Target.Kind() != buildTarget.Kind() || operations.runRequests[1].ExecutionSelection.Target.Profile() != buildTarget.Profile() ||
		operations.runRequests[1].ExecutionSelection.RepositoryAlias != "analytics-dbt" {
		t.Fatalf("override selection=%+v", operations.runRequests[1].ExecutionSelection)
	}
	p153AssertResponseSelection(t, outbox, overrideRequest.RequestID, "analytics", "request_override", "linux-build-dev", "remote", "linux-build-host")
	expectedOverride := resolver.overrideSelection
	expectedOverride.RepositoryAlias = "analytics-dbt"
	p153AssertStoredSelection(t, authority, "analytics", overrideRequest.RequestID, expectedOverride)

	invalid := []struct {
		name       string
		request    Request
		wantCode   string
		wantReason string
	}{
		{
			name: "partial pair",
			request: p153ProcessorRequest(t, "req-p153-partial", "key-p153-partial", "run", map[string]any{
				"environment": "linux-dev", "script": "printf partial",
			}),
			wantCode: "invalid_request", wantReason: "environment and execution target must be supplied together",
		},
		{
			name: "mismatched pair",
			request: p153ProcessorRequest(t, "req-p153-mismatch", "key-p153-mismatch", "run", map[string]any{
				"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-build-host"}, "script": "printf mismatch",
			}),
			wantCode: "environment_target_mismatch", wantReason: "environment and execution target do not identify a configured context",
		},
		{
			name: "disallowed pair",
			request: p153ProcessorRequest(t, "req-p153-disallowed", "key-p153-disallowed", "run", map[string]any{
				"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": "printf disallowed",
			}),
			wantCode: "invalid_request", wantReason: "execution context is not allowed for this mailbox",
		},
		{
			name: "disallowed repository alias",
			request: p153ProcessorRequest(t, "req-p153-alias", "key-p153-alias", "run", map[string]any{
				"repository_alias": "outside-scope", "script": "printf alias",
			}),
			wantCode: "invalid_request", wantReason: "repository alias is not allowed for this mailbox",
		},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			beforeCalls := len(operations.runRequests)
			p153Process(t, ctx, processor, testCase.request)
			if len(operations.runRequests) != beforeCalls {
				t.Fatalf("rejected selection reached RunJobIntent: before=%d after=%d", beforeCalls, len(operations.runRequests))
			}
			response := p153Response(t, outbox, testCase.request.RequestID)
			errorValue, ok := response["error"].(map[string]any)
			if !ok || errorValue["code"] != testCase.wantCode || errorValue["message"] != testCase.wantReason {
				t.Fatalf("rejected response=%+v", response)
			}
			ref, err := store.NewMailboxExchangeRef("analytics", testCase.request.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
			if err != nil || record.Selection != nil || record.State != store.MailboxExchangeRejected {
				t.Fatalf("rejected exchange=%+v err=%v", record, err)
			}
		})
	}
}

func TestBUG018MacLocalContextUsesConfiguredTupleAndSafelyExplainsMismatch(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	resolver.allowMacLocalOverride = true
	processor, operations, _, outbox := newP153ProcessorHarness(t, resolver)

	accepted := p153ProcessorRequest(t, "req-bug018-mac-local", "key-bug018-mac-local", "run", map[string]any{
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"repository_alias": "analytics-dbt", "script": "printf 'BUG018_MAC_LOCAL_OK\\n'",
	})
	p153Process(t, ctx, processor, accepted)
	if len(operations.runRequests) != 1 {
		t.Fatalf("configured Mac-local request calls=%d, want one", len(operations.runRequests))
	}
	selection := operations.runRequests[0].ExecutionSelection
	if selection == nil || selection.ContextName != "analytics-mac" || selection.Environment != "mac-dev" || selection.Target.Kind() != domain.TargetKindLocal || selection.Target.Profile() != "mac-workstation" {
		t.Fatalf("configured Mac-local selection=%+v", selection)
	}

	rejected := p153ProcessorRequest(t, "req-bug018-wrong-names", "key-bug018-wrong-names", "run", map[string]any{
		"environment": "mac-local", "execution_target": map[string]string{"kind": "local", "profile": "mac-local"},
		"repository_alias": "analytics-dbt", "script": "PRIVATE_SCRIPT_MUST_NOT_APPEAR",
	})
	p153Process(t, ctx, processor, rejected)
	if len(operations.runRequests) != 1 {
		t.Fatalf("mismatched context reached RunJobIntent: calls=%d", len(operations.runRequests))
	}
	response := p153Response(t, outbox, rejected.RequestID)
	errorValue, ok := response["error"].(map[string]any)
	if !ok || errorValue["code"] != "environment_target_mismatch" {
		t.Fatalf("mismatched response error=%+v", response)
	}
	details, ok := errorValue["details"].(map[string]any)
	if !ok || details["requested_environment"] != "mac-local" {
		t.Fatalf("mismatched response details=%+v", response)
	}
	requestedTarget, ok := details["requested_execution_target"].(map[string]any)
	if !ok || requestedTarget["kind"] != "local" || requestedTarget["profile"] != "mac-local" {
		t.Fatalf("mismatched requested target=%+v", details)
	}
	allowed, ok := details["allowed_contexts"].([]any)
	if !ok || len(allowed) != 3 {
		t.Fatalf("mismatched allowed contexts=%+v", details)
	}
	mac, ok := allowed[2].(map[string]any)
	if !ok || mac["name"] != "analytics-mac" || mac["environment"] != "mac-dev" {
		t.Fatalf("mismatched Mac correction=%+v", allowed)
	}
	macTarget, ok := mac["execution_target"].(map[string]any)
	if !ok || macTarget["kind"] != "local" || macTarget["profile"] != "mac-workstation" {
		t.Fatalf("mismatched Mac correction target=%+v", mac)
	}
	if encoded, err := json.Marshal(response); err != nil || bytes.Contains(encoded, []byte("PRIVATE_SCRIPT_MUST_NOT_APPEAR")) {
		t.Fatalf("mismatched response leaked request content: %s err=%v", encoded, err)
	}
}

func TestP153ProcessorCreatesSessionWithAllowedOverride(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)
	request := p153ProcessorRequest(t, "req-p153-create-override", "key-p153-create-override", "create_session", map[string]any{
		"environment": "linux-build-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-build-host"},
		"repository_alias": "analytics-dbt",
	})

	p153Process(t, ctx, processor, request)
	if len(operations.createRequests) != 1 {
		t.Fatalf("create override calls=%d, want one", len(operations.createRequests))
	}
	want := resolver.overrideSelection
	want.RepositoryAlias = "analytics-dbt"
	p153AssertProcessorSelection(t, operations.createRequests[0].ExecutionSelection, want)
	p153AssertStoredSelection(t, authority, "analytics", request.RequestID, want)
	p153AssertResponseSelection(t, outbox, request.RequestID, "analytics", "request_override", "linux-build-dev", "remote", "linux-build-host")
}

func TestP153ProcessorRejectsResolverSelectionForAnotherMailbox(t *testing.T) {
	ctx := context.Background()
	resolver := p153WrongMailboxResolver{selection: p153DefaultSelection(t)}
	processor, operations, authority, _ := newP153ProcessorHarness(t, resolver)
	request := p153ProcessorRequest(t, "req-p153-wrong-mailbox", "key-p153-wrong-mailbox", "run", map[string]any{"script": "printf no"})

	processed, err := processor.process(ctx, request)
	if err == nil || processed {
		t.Fatalf("wrong-mailbox resolver processed=%t err=%v", processed, err)
	}
	if len(operations.runRequests) != 0 {
		t.Fatalf("wrong-mailbox resolver reached RunJobIntent: %+v", operations.runRequests)
	}
	ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetMailboxExchangeInMailbox(ctx, ref); err == nil {
		t.Fatal("wrong-mailbox resolver created an exchange")
	}
}

func TestP153ProcessorFailsClosedWhenResolverSelectionDoesNotMatchRequest(t *testing.T) {
	ctx := context.Background()
	resolver := p153MismatchedRequestResolver{selection: p153DefaultSelection(t)}
	processor, operations, authority, _ := newP153ProcessorHarness(t, resolver)
	request := p153ProcessorRequest(t, "req-p153-mismatched-resolver", "key-p153-mismatched-resolver", "run", map[string]any{
		"environment": "linux-build-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-build-host"}, "script": "printf no",
	})

	processed, err := processor.process(ctx, request)
	if err == nil || processed {
		t.Fatalf("mismatched resolver processed=%t err=%v", processed, err)
	}
	if len(operations.runRequests) != 0 {
		t.Fatalf("mismatched resolver reached RunJobIntent: %+v", operations.runRequests)
	}
	ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GetMailboxExchangeInMailbox(ctx, ref); err == nil {
		t.Fatal("mismatched resolver created an exchange")
	}
}

func TestP153ProcessorRejectsCreateSelectionBeforeOperations(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)
	cases := []struct {
		name       string
		id         string
		fields     map[string]any
		wantCode   string
		wantReason string
	}{
		{
			name: "partial pair", id: "partial", fields: map[string]any{"environment": "linux-dev"},
			wantCode: "invalid_request", wantReason: "environment and execution target must be supplied together",
		},
		{
			name: "mismatched pair", id: "mismatch", fields: map[string]any{
				"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-build-host"},
			},
			wantCode: "environment_target_mismatch", wantReason: "environment and execution target do not identify a configured context",
		},
		{
			name: "disallowed pair", id: "disallowed", fields: map[string]any{
				"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
			},
			wantCode: "invalid_request", wantReason: "execution context is not allowed for this mailbox",
		},
		{
			name: "disallowed repository alias", id: "alias", fields: map[string]any{"repository_alias": "outside-scope"},
			wantCode: "invalid_request", wantReason: "repository alias is not allowed for this mailbox",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := p153ProcessorRequest(t, "req-p153-create-invalid-"+testCase.id, "key-p153-create-invalid-"+testCase.id, "create_session", testCase.fields)
			before := len(operations.createRequests)
			p153Process(t, ctx, processor, request)
			if len(operations.createRequests) != before {
				t.Fatalf("rejected create selection reached CreateSessionIntent: before=%d after=%d", before, len(operations.createRequests))
			}
			response := p153Response(t, outbox, request.RequestID)
			errorValue, ok := response["error"].(map[string]any)
			if !ok || errorValue["code"] != testCase.wantCode || errorValue["message"] != testCase.wantReason {
				t.Fatalf("rejected create response=%+v", response)
			}
			ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
			if err != nil || record.Selection != nil || record.SelectionState != store.MailboxExecutionSelectionRejected || record.State != store.MailboxExchangeRejected {
				t.Fatalf("rejected create exchange=%+v err=%v", record, err)
			}
		})
	}
}

func TestP153ProcessorRetainedDefaultAndExistingSessionStayImmutable(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)

	create := p153ProcessorRequest(t, "req-p153-create", "key-p153-create", "create_session", map[string]any{})
	p153Process(t, ctx, processor, create)
	if len(operations.createRequests) != 1 {
		t.Fatalf("create calls=%d, want one", len(operations.createRequests))
	}
	p153AssertProcessorSelection(t, operations.createRequests[0].ExecutionSelection, resolver.defaultSelection)
	p153AssertResponseSelection(t, outbox, create.RequestID, "analytics", "inbox_default", "linux-dev", "remote", "linux-host")

	// Change the mutable resolver default after accepting the session. A submit
	// keeps the established session identity and never resolves a replacement
	// execution context.
	resolver.defaultSelection = resolver.overrideSelection
	resolverCallsBeforeSubmit := resolver.calls
	submit := p153ProcessorRequest(t, "req-p153-submit", "key-p153-submit", "submit_command", map[string]any{
		"session_id": "sess-p153-create", "script": "printf immutable",
	})
	p153Process(t, ctx, processor, submit)
	if resolver.calls != resolverCallsBeforeSubmit || len(operations.submitRequests) != 1 {
		t.Fatalf("submit unexpectedly resolved a new target: resolver calls=%d/%d submits=%d", resolver.calls, resolverCallsBeforeSubmit, len(operations.submitRequests))
	}
	if operations.submitTargets[submit.SessionID].Profile() != "linux-host" {
		t.Fatalf("existing session target=%+v, want original linux-host", operations.submitTargets[submit.SessionID])
	}

	// Restore the original default for the first request. Its retained
	// same-key retry must keep that persisted selection even after the mutable
	// resolver moves back to the build host.
	resolver.defaultSelection = p153DefaultSelection(t)
	retryFirst := p153ProcessorRequest(t, "req-p153-retry-first", "key-p153-retry", "run", map[string]any{"script": "printf retry"})
	p153Process(t, ctx, processor, retryFirst)
	resolver.defaultSelection = resolver.overrideSelection
	resolverCallsBeforeRetry := resolver.calls
	retrySecond := p153ProcessorRequest(t, "req-p153-retry-second", "key-p153-retry", "run", map[string]any{"script": "printf retry"})
	p153Process(t, ctx, processor, retrySecond)
	if resolver.calls != resolverCallsBeforeRetry || len(operations.runRequests) != 2 {
		t.Fatalf("retained retry calls=%d runs=%d, want unchanged resolver and two operation calls", resolver.calls, len(operations.runRequests))
	}
	p153AssertProcessorSelection(t, operations.runRequests[1].ExecutionSelection, p153DefaultSelection(t))
	p153AssertStoredSelection(t, authority, "analytics", retrySecond.RequestID, p153DefaultSelection(t))
	p153AssertResponseSelection(t, outbox, retrySecond.RequestID, "analytics", "inbox_default", "linux-dev", "remote", "linux-host")
}

func TestP153ProcessorRetainsExplicitSelectionWhenSameKeyRetryOmitsPair(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)
	original := p153ProcessorRequest(t, "req-p153-explicit-original", "key-p153-explicit-to-omitted", "run", map[string]any{
		"environment": "linux-build-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-build-host"},
		"script": "printf retained-explicit",
	})
	p153Process(t, ctx, processor, original)
	if len(operations.runRequests) != 1 {
		t.Fatalf("original explicit run calls=%d, want one", len(operations.runRequests))
	}

	// A same-key retry can omit the raw pair. It must retain the original
	// explicit build-host choice rather than resolve today's default host.
	resolver.defaultSelection = p153DefaultSelection(t)
	retry := p153ProcessorRequest(t, "req-p153-explicit-omitted-retry", original.IdempotencyKey, "run", map[string]any{
		"script": "printf retained-explicit",
	})
	p153Process(t, ctx, processor, retry)
	if len(operations.runRequests) != 2 {
		t.Fatalf("omitted retry run calls=%d, want two", len(operations.runRequests))
	}
	p153AssertProcessorSelection(t, operations.runRequests[1].ExecutionSelection, resolver.overrideSelection)
	p153AssertStoredSelection(t, authority, "analytics", retry.RequestID, resolver.overrideSelection)
	p153AssertResponseSelection(t, outbox, retry.RequestID, "analytics", "request_override", "linux-build-dev", "remote", "linux-build-host")
}

func TestP153ProcessorResumesV25AcceptedExplicitNewWorkWithoutInventingSelection(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)

	cases := []struct {
		name      string
		operation string
		fields    map[string]any
	}{
		{
			name: "create session", operation: "create_session",
			fields: map[string]any{
				"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
			},
		},
		{
			name: "run", operation: "run",
			fields: map[string]any{
				"environment": "linux-dev", "execution_target": map[string]string{"kind": "remote", "profile": "linux-host"}, "script": "printf legacy",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := p153ProcessorRequest(t, "req-p153-v25-"+testCase.operation, "key-p153-v25-"+testCase.operation, testCase.operation, testCase.fields)
			p153PersistLegacyAcceptedNewWork(t, ctx, authority, request)

			callsBefore := resolver.calls
			p153Process(t, ctx, processor, request)
			if resolver.calls != callsBefore+1 {
				t.Fatalf("legacy retained request resolver calls=%d, want %d", resolver.calls, callsBefore+1)
			}
			if testCase.operation == "create_session" && len(operations.createRequests) != 1 {
				t.Fatalf("legacy create calls=%d, want one", len(operations.createRequests))
			}
			if testCase.operation == "run" && len(operations.runRequests) != 1 {
				t.Fatalf("legacy run calls=%d, want one", len(operations.runRequests))
			}
			ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
			if err != nil || record.Selection != nil {
				t.Fatalf("legacy record selection=%+v err=%v, want absent", record.Selection, err)
			}
			response := p153Response(t, outbox, request.RequestID)
			if _, exists := response["resolved_environment"]; exists {
				t.Fatalf("legacy response invented selection: %+v", response)
			}
		})
	}
}

func TestP153SelectionFailureCannotResumeAfterCrashBeforeResponsePublication(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)
	request := p153ProcessorRequest(t, "req-p153-crash-selection-error", "key-p153-crash-selection-error", "run", map[string]any{
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": "printf reject",
	})

	// Invoke only durable acceptance, modeling a crash between a selection
	// rejection receipt and its outbox response publication.
	_, _, _, selectionError, legacyExplicitSelection, err := processor.acceptNewWorkExchange(ctx, request)
	if err != nil || selectionError == nil || legacyExplicitSelection {
		t.Fatalf("pre-publication selection result error=%+v legacy=%t err=%v", selectionError, legacyExplicitSelection, err)
	}
	ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := authority.GetMailboxExchangeInMailbox(ctx, ref)
	if err != nil || record.SelectionState != store.MailboxExecutionSelectionRejected {
		t.Fatalf("pre-publication record state=%q err=%v, want rejected provenance", record.SelectionState, err)
	}

	// Simulate a later configuration change that would now allow the original
	// pair. The durable rejected provenance still prevents execution.
	resolver.allowMacLocalOverride = true

	p153Process(t, ctx, processor, request)
	if len(operations.runRequests) != 0 {
		t.Fatalf("selection failure resumed work: %+v", operations.runRequests)
	}
	response := p153Response(t, outbox, request.RequestID)
	errorValue, ok := response["error"].(map[string]any)
	if !ok || errorValue["code"] != "invalid_request" || errorValue["message"] != "mailbox execution selection was rejected" {
		t.Fatalf("post-crash rejection response=%+v", response)
	}
}

func TestP153OmittedDefaultSelectionRejectionStaysTerminalAfterConfigurationChange(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, _, outbox := newP153ProcessorHarness(t, resolver)
	request := p153ProcessorRequest(t, "req-p153-crash-omitted-default", "key-p153-crash-omitted-default", "run", map[string]any{
		"repository_alias": "outside-scope", "script": "printf reject",
	})

	_, _, _, selectionError, legacyExplicitSelection, err := processor.acceptNewWorkExchange(ctx, request)
	if err != nil || selectionError == nil || legacyExplicitSelection {
		t.Fatalf("pre-publication omitted-default result error=%+v legacy=%t err=%v", selectionError, legacyExplicitSelection, err)
	}

	// A new configuration could resolve the omitted pair, which changes its
	// normal canonical defaults. Recovery must compare the original raw receipt
	// and publish rejection rather than report a receipt conflict or run it.
	resolver.allowOutsideAlias = true
	p153Process(t, ctx, processor, request)
	if len(operations.runRequests) != 0 {
		t.Fatalf("omitted-default selection failure resumed work: %+v", operations.runRequests)
	}
	response := p153Response(t, outbox, request.RequestID)
	errorValue, ok := response["error"].(map[string]any)
	if !ok || errorValue["code"] != "invalid_request" || errorValue["message"] != "mailbox execution selection was rejected" {
		t.Fatalf("omitted-default post-crash rejection response=%+v", response)
	}
}

func TestP153PublishedSelectionRejectionStaysTerminalForSameKeyRetryAfterConfigurationChange(t *testing.T) {
	ctx := context.Background()
	resolver := newP153Resolver(t)
	processor, operations, authority, outbox := newP153ProcessorHarness(t, resolver)
	original := p153ProcessorRequest(t, "req-p153-terminal-selection-error", "key-p153-terminal-selection-error", "run", map[string]any{
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": "printf reject",
	})
	p153Process(t, ctx, processor, original)
	originalRef, err := store.NewMailboxExchangeRef("analytics", original.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	originalRecord, err := authority.GetMailboxExchangeInMailbox(ctx, originalRef)
	if err != nil || originalRecord.State != store.MailboxExchangeRejected || originalRecord.SelectionState != store.MailboxExecutionSelectionRejected {
		t.Fatalf("published rejected exchange=%+v err=%v", originalRecord, err)
	}

	// The same payload arrives with a new request ID after configuration would
	// otherwise allow it. The active same-key rejection remains terminal and
	// must not be converted into a new local intent.
	resolver.allowMacLocalOverride = true
	retry := p153ProcessorRequest(t, "req-p153-terminal-selection-retry", original.IdempotencyKey, "run", map[string]any{
		"environment": "mac-dev", "execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": "printf reject",
	})
	p153Process(t, ctx, processor, retry)
	if len(operations.runRequests) != 0 {
		t.Fatalf("published selection rejection retried work: %+v", operations.runRequests)
	}
	response := p153Response(t, outbox, retry.RequestID)
	errorValue, ok := response["error"].(map[string]any)
	if !ok || errorValue["code"] != "invalid_request" || errorValue["message"] != "mailbox execution selection was rejected" {
		t.Fatalf("same-key terminal rejection response=%+v", response)
	}
	retryRef, err := store.NewMailboxExchangeRef("analytics", retry.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	retryRecord, err := authority.GetMailboxExchangeInMailbox(ctx, retryRef)
	if err != nil || retryRecord.State != store.MailboxExchangeRejected || retryRecord.Selection != nil || retryRecord.SelectionState != store.MailboxExecutionSelectionRejected {
		t.Fatalf("same-key terminal rejection record=%+v err=%v", retryRecord, err)
	}
}

func p153PersistLegacyAcceptedNewWork(t *testing.T, ctx context.Context, authority *store.AuthorityStore, request Request) {
	t.Helper()
	payload, hash, err := receiptCanonical(request)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.NewMailboxExchangeRef("analytics", request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	_, duplicate, conflict, err := authority.AcceptMailboxExchangeWithConflictReceiptInMailbox(ctx, ref, store.MailboxExchangeCreate{
		MailboxID: "analytics", RequestID: request.RequestID, Operation: request.Operation, Controller: owner,
		IdempotencyKey: request.IdempotencyKey, RequestHash: hash, CanonicalPayload: payload,
	})
	if err != nil || duplicate || conflict {
		t.Fatalf("persist v25 accepted exchange duplicate=%t conflict=%t err=%v", duplicate, conflict, err)
	}
}

type p153Resolver struct {
	defaultSelection      config.MailboxExecutionSelection
	overrideSelection     config.MailboxExecutionSelection
	allowMacLocalOverride bool
	allowOutsideAlias     bool
	calls                 int
}

type p153WrongMailboxResolver struct {
	selection config.MailboxExecutionSelection
}

func (r p153WrongMailboxResolver) ResolveMailboxExecution(string, bool, string, bool, domain.ExecutionTarget, string) (config.MailboxExecutionSelection, error) {
	selection := r.selection
	selection.MailboxID = "other"
	return selection, nil
}

type p153MismatchedRequestResolver struct {
	selection config.MailboxExecutionSelection
}

func (r p153MismatchedRequestResolver) ResolveMailboxExecution(string, bool, string, bool, domain.ExecutionTarget, string) (config.MailboxExecutionSelection, error) {
	return r.selection, nil
}

func newP153Resolver(t *testing.T) *p153Resolver {
	t.Helper()
	return &p153Resolver{
		defaultSelection:  p153DefaultSelection(t),
		overrideSelection: config.MailboxExecutionSelection{MailboxID: "analytics", ContextName: "analytics-build", Environment: "linux-build-dev", Target: p153Target(t, domain.TargetKindRemote, "linux-build-host"), Source: config.MailboxExecutionSelectionSourceRequestOverride, RepositoryAliases: []string{"analytics-dbt"}},
	}
}

func p153DefaultSelection(t *testing.T) config.MailboxExecutionSelection {
	t.Helper()
	return config.MailboxExecutionSelection{
		MailboxID: "analytics", ContextName: "analytics-current", Environment: "linux-dev",
		Target: p153Target(t, domain.TargetKindRemote, "linux-host"), Source: config.MailboxExecutionSelectionSourceInboxDefault,
		RepositoryAliases: []string{"analytics-dbt"},
	}
}

func (r *p153Resolver) ResolveMailboxExecution(mailboxID string, environmentPresent bool, environment string, targetPresent bool, target domain.ExecutionTarget, repositoryAlias string) (config.MailboxExecutionSelection, error) {
	r.calls++
	if mailboxID != "analytics" {
		return config.MailboxExecutionSelection{}, config.ErrMailboxNotConfigured
	}
	if repositoryAlias != "" && repositoryAlias != "analytics-dbt" && !r.allowOutsideAlias {
		return config.MailboxExecutionSelection{}, config.ErrMailboxRepositoryAliasNotAllowed
	}
	if environmentPresent != targetPresent {
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionPairRequired
	}
	if !environmentPresent {
		selection := r.defaultSelection
		p153SetRepositoryAlias(&selection, repositoryAlias)
		return selection, nil
	}
	if environment == "linux-build-dev" && target.Kind() == domain.TargetKindRemote && target.Profile() == "linux-build-host" {
		selection := r.overrideSelection
		p153SetRepositoryAlias(&selection, repositoryAlias)
		return selection, nil
	}
	if environment == "linux-dev" && target.Kind() == domain.TargetKindRemote && target.Profile() == "linux-host" {
		selection := r.defaultSelection
		selection.Source = config.MailboxExecutionSelectionSourceRequestOverride
		p153SetRepositoryAlias(&selection, repositoryAlias)
		return selection, nil
	}
	if environment == "mac-dev" && target.Kind() == domain.TargetKindLocal && target.Profile() == "mac-workstation" {
		if r.allowMacLocalOverride {
			selection := r.defaultSelection
			selection.ContextName = "analytics-mac"
			selection.Environment = environment
			selection.Target = target
			selection.Source = config.MailboxExecutionSelectionSourceRequestOverride
			p153SetRepositoryAlias(&selection, repositoryAlias)
			return selection, nil
		}
		return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotAllowed
	}
	return config.MailboxExecutionSelection{}, config.ErrMailboxExecutionContextNotFound
}

func (r *p153Resolver) AllowedMailboxExecutionContexts(mailboxID string) []config.MailboxExecutionContext {
	if mailboxID != "analytics" {
		return nil
	}
	contexts := []config.MailboxExecutionContext{
		{Name: r.defaultSelection.ContextName, Environment: r.defaultSelection.Environment, Target: r.defaultSelection.Target},
		{Name: r.overrideSelection.ContextName, Environment: r.overrideSelection.Environment, Target: r.overrideSelection.Target},
	}
	if r.allowMacLocalOverride {
		target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
		if err != nil {
			panic(err)
		}
		contexts = append(contexts, config.MailboxExecutionContext{Name: "analytics-mac", Environment: "mac-dev", Target: target})
	}
	return contexts
}

func p153SetRepositoryAlias(selection *config.MailboxExecutionSelection, alias string) {
	selection.RepositoryAlias = alias
	if alias == "" {
		return
	}
	for _, configured := range selection.RepositoryAliases {
		if configured == alias {
			return
		}
	}
	selection.RepositoryAliases = append(selection.RepositoryAliases, alias)
}

type p153Operations struct {
	owner          domain.ControllerIdentity
	createRequests []Request
	runRequests    []Request
	submitRequests []Request
	sessionTargets map[string]domain.ExecutionTarget
	submitTargets  map[string]domain.ExecutionTarget
}

func (o *p153Operations) SessionController() domain.ControllerIdentity { return o.owner }

func (o *p153Operations) CreateSessionIntent(_ context.Context, request Request) (SessionIntent, error) {
	o.createRequests = append(o.createRequests, request)
	if request.ExecutionSelection != nil {
		o.sessionTargets["sess-p153-create"] = request.ExecutionSelection.Target
	}
	return SessionIntent{SessionID: "sess-p153-create", DeliveryState: "recorded"}, nil
}

func (o *p153Operations) GetSession(_ context.Context, sessionID string) (SessionSnapshot, error) {
	return SessionSnapshot{SessionID: sessionID, SessionState: "ready", DeliveryState: "accepted"}, nil
}

func (o *p153Operations) SubmitCommandIntent(_ context.Context, request Request) (CommandIntent, error) {
	o.submitRequests = append(o.submitRequests, request)
	if request.ExecutionSelection != nil {
		return CommandIntent{}, &SessionOperationError{Code: "invalid_request", Message: "submit must retain session target"}
	}
	o.submitTargets[request.SessionID] = o.sessionTargets[request.SessionID]
	return CommandIntent{CommandID: "cmd-p153-submit", SessionID: request.SessionID, DeliveryState: "recorded"}, nil
}

func (o *p153Operations) GetCommandSnapshot(_ context.Context, commandID string) (CommandSnapshot, error) {
	return CommandSnapshot{CommandID: domain.CommandID(commandID), SessionID: "sess-p153-create", DeliveryState: "accepted"}, nil
}

func (o *p153Operations) CancelCommandIntent(_ context.Context, request Request) (CommandIntent, error) {
	return CommandIntent{CommandID: request.CommandID, SessionID: "sess-p153-create", DeliveryState: "recorded"}, nil
}

func (o *p153Operations) GetCancelCommandSnapshot(_ context.Context, commandID, _ string) (CancelCommandSnapshot, error) {
	return CancelCommandSnapshot{CommandID: commandID, SessionID: "sess-p153-create", CancelDeliveryState: "accepted", CommandDeliveryState: "accepted", CommandState: "queued"}, nil
}

func (o *p153Operations) CloseSessionIntent(_ context.Context, request Request) (SessionIntent, error) {
	return SessionIntent{SessionID: request.SessionID, DeliveryState: "recorded"}, nil
}

func (o *p153Operations) GetCloseSessionSnapshot(_ context.Context, sessionID, _ string) (CloseSessionSnapshot, error) {
	return CloseSessionSnapshot{SessionID: sessionID, CloseDeliveryState: "accepted", SessionDeliveryState: "accepted", SessionState: "ready"}, nil
}

func (o *p153Operations) RunJobIntent(_ context.Context, request Request) (RunIntent, error) {
	o.runRequests = append(o.runRequests, request)
	return RunIntent{JobID: "job-p153-run", SessionID: "sess-p153-run", CommandID: "cmd-p153-run", DeliveryState: "recorded"}, nil
}

func (o *p153Operations) GetRunSnapshot(_ context.Context, jobID string) (RunSnapshot, error) {
	return RunSnapshot{JobID: jobID, SessionID: "sess-p153-run", CommandID: "cmd-p153-run", DeliveryState: "accepted"}, nil
}

func newP153ProcessorHarness(t *testing.T, resolver MailboxExecutionResolver) (*SessionProcessor, *p153Operations, *store.AuthorityStore, *Outbox) {
	t.Helper()
	ctx := context.Background()
	root := testfixture.New(t)
	database, err := store.Open(ctx, root.Path()+"/state/p153-processor.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	authority, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	mailboxRoot := root.Path() + "/mailbox"
	importer, err := New(Options{MailboxID: "analytics", Root: mailboxRoot})
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := NewOutbox(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	events, err := NewEventFiles(mailboxRoot)
	if err != nil {
		t.Fatal(err)
	}
	operations := &p153Operations{owner: owner, sessionTargets: make(map[string]domain.ExecutionTarget), submitTargets: make(map[string]domain.ExecutionTarget)}
	processor, err := NewSessionProcessor(SessionProcessorOptions{
		MailboxID: "analytics", Importer: importer, Authority: authority, Controller: owner, Operations: operations,
		Outbox: outbox, EventFiles: events, ExecutionResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	return processor, operations, authority, outbox
}

func p153ProcessorRequest(t *testing.T, requestID, key, operation string, fields map[string]any) Request {
	t.Helper()
	payload := make(map[string]any, len(fields)+3)
	payload["request_id"] = requestID
	payload["idempotency_key"] = key
	payload["operation"] = operation
	for name, value := range fields {
		payload[name] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{MailboxID: "analytics", RequestID: requestID, IdempotencyKey: key, Operation: operation, RawJSON: raw}
	if environment, ok := fields["environment"].(string); ok {
		request.Environment, request.EnvironmentPresent = environment, true
	}
	if targetRaw, ok := fields["execution_target"].(map[string]string); ok {
		target, err := domain.NewExecutionTarget(domain.TargetKind(targetRaw["kind"]), targetRaw["profile"])
		if err != nil {
			t.Fatal(err)
		}
		request.ExecutionTarget, request.ExecutionTargetPresent = target, true
	}
	if alias, ok := fields["repository_alias"].(string); ok {
		request.RepositoryAlias = alias
	}
	if script, ok := fields["script"].(string); ok {
		request.Script = script
	}
	if sessionID, ok := fields["session_id"].(string); ok {
		request.SessionID = sessionID
	}
	return request
}

func p153Process(t *testing.T, ctx context.Context, processor *SessionProcessor, request Request) {
	t.Helper()
	processed, err := processor.process(ctx, request)
	if err != nil || !processed {
		t.Fatalf("process %s/%s processed=%t err=%v", request.Operation, request.RequestID, processed, err)
	}
}

func p153Target(t *testing.T, kind domain.TargetKind, profile string) domain.ExecutionTarget {
	t.Helper()
	target, err := domain.NewExecutionTarget(kind, profile)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func p153AssertProcessorSelection(t *testing.T, got *store.MailboxExecutionSelection, want config.MailboxExecutionSelection) {
	t.Helper()
	if got == nil || got.ContextName != want.ContextName || got.Environment != want.Environment || got.Target.Kind() != want.Target.Kind() ||
		got.Target.Profile() != want.Target.Profile() || got.Source != want.Source || got.RepositoryAlias != want.RepositoryAlias || len(got.RepositoryAliases) != len(want.RepositoryAliases) {
		t.Fatalf("selection=%+v, want %+v", got, want)
	}
	for index := range want.RepositoryAliases {
		if got.RepositoryAliases[index] != want.RepositoryAliases[index] {
			t.Fatalf("selection aliases=%v, want %v", got.RepositoryAliases, want.RepositoryAliases)
		}
	}
}

func p153AssertStoredSelection(t *testing.T, authority *store.AuthorityStore, mailboxID, requestID string, want config.MailboxExecutionSelection) {
	t.Helper()
	ref, err := store.NewMailboxExchangeRef(mailboxID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := authority.GetMailboxExchangeInMailbox(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	p153AssertProcessorSelection(t, record.Selection, want)
}

func p153Response(t *testing.T, outbox *Outbox, requestID string) map[string]any {
	t.Helper()
	raw, err := outbox.Read(requestID)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func p153AssertResponseSelection(t *testing.T, outbox *Outbox, requestID, mailboxID, source, environment, kind, profile string) {
	t.Helper()
	response := p153Response(t, outbox, requestID)
	target, ok := response["resolved_execution_target"].(map[string]any)
	if !ok || response["inbox_id"] != mailboxID || response["execution_selection_source"] != source || response["resolved_environment"] != environment || target["kind"] != kind || target["profile"] != profile {
		t.Fatalf("selection response=%+v", response)
	}
}
