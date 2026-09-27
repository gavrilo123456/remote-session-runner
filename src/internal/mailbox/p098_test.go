package mailbox

import (
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestP098ClosePolicySchemaAndCanonicalIdempotency(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	for _, test := range []struct {
		name  string
		raw   string
		valid bool
	}{
		{
			name:  "omitted policy defaults in importer",
			raw:   `{"request_id":"req-p098-close-default","idempotency_key":"key-p098-close-default","operation":"close_session","session_id":"sess-p098"}`,
			valid: true,
		},
		{
			name:  "explicit policy object",
			raw:   `{"request_id":"req-p098-close-explicit","idempotency_key":"key-p098-close-explicit","operation":"close_session","session_id":"sess-p098","close_policy":{"policy":"cancel"}}`,
			valid: true,
		},
		{
			name:  "unknown policy member rejected",
			raw:   `{"request_id":"req-p098-close-unknown","idempotency_key":"key-p098-close-unknown","operation":"close_session","session_id":"sess-p098","close_policy":{"policy":"cancel","extra":true}}`,
			valid: false,
		},
		{
			name:  "policy must be object",
			raw:   `{"request_id":"req-p098-close-string","idempotency_key":"key-p098-close-string","operation":"close_session","session_id":"sess-p098","close_policy":"cancel"}`,
			valid: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := p004MailboxDecode([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			err = compiled["request"].Validate(value)
			if (err == nil) != test.valid {
				t.Fatalf("request schema error=%v, want valid=%v", err, test.valid)
			}
		})
	}

	omittedPayload, omittedHash, omittedErr := receiptCanonical(Request{
		RequestID: "req-p098-close-default", IdempotencyKey: "key-p098-close", Operation: "close_session",
		SessionID: "sess-p098", ClosePolicy: "cancel",
		RawJSON: []byte(`{"request_id":"req-p098-close-default","idempotency_key":"key-p098-close","operation":"close_session","session_id":"sess-p098"}`),
	})
	explicitPayload, explicitHash, explicitErr := receiptCanonical(Request{
		RequestID: "req-p098-close-explicit", IdempotencyKey: "key-p098-close", Operation: "close_session",
		SessionID: "sess-p098", ClosePolicy: "cancel",
		RawJSON: []byte(`{"request_id":"req-p098-close-explicit","idempotency_key":"key-p098-close","operation":"close_session","session_id":"sess-p098","close_policy":{"policy":"cancel"}}`),
	})
	if omittedErr != nil || explicitErr != nil || string(omittedPayload) != string(explicitPayload) || domain.CompareIdempotency(omittedHash, explicitHash) != domain.IdempotencySamePayload {
		t.Fatalf("omitted/default cancel differ canonically: omitted=%s explicit=%s errors=%v/%v", omittedPayload, explicitPayload, omittedErr, explicitErr)
	}
	_, drainHash, err := receiptCanonical(Request{
		RequestID: "req-p098-close-drain", IdempotencyKey: "key-p098-close", Operation: "close_session",
		SessionID: "sess-p098", ClosePolicy: "drain",
		RawJSON: []byte(`{"request_id":"req-p098-close-drain","idempotency_key":"key-p098-close","operation":"close_session","session_id":"sess-p098","close_policy":{"policy":"drain"}}`),
	})
	if err != nil || domain.CompareIdempotency(omittedHash, drainHash) != domain.IdempotencyConflict {
		t.Fatalf("changed policy did not change canonical hash: err=%v", err)
	}
}

func TestP098CancelAndCloseResponseShapesMatchV1Schema(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	for _, raw := range []string{
		`{"request_id":"req-p098-cancel-accepted","operation":"cancel_command","request_state":"accepted","response_revision":1,"command_id":"cmd-p098","session_id":"sess-p098","delivery_state":"uncertain"}`,
		`{"request_id":"req-p098-cancel-complete","operation":"cancel_command","request_state":"complete","response_revision":2,"command_id":"cmd-p098","session_id":"sess-p098","delivery_state":"accepted"}`,
		`{"request_id":"req-p098-close-accepted","operation":"close_session","request_state":"accepted","response_revision":1,"session_id":"sess-p098","delivery_state":"recorded"}`,
		`{"request_id":"req-p098-close-complete","operation":"close_session","request_state":"complete","response_revision":2,"session_id":"sess-p098","session_state":"closed","delivery_state":"accepted","teardown_outcome":"closed"}`,
		`{"request_id":"req-p098-close-not-created","operation":"close_session","request_state":"complete","response_revision":2,"session_id":"sess-p098","delivery_state":"not_delivered","teardown_outcome":"not_created"}`,
		`{"request_id":"req-p098-close-rejected","operation":"close_session","request_state":"rejected","response_revision":2,"session_id":"sess-p098","delivery_state":"not_delivered","error":{"code":"runtime_unavailable","message":"close request was proven not delivered","retryable":false}}`,
	} {
		value, err := p004MailboxDecode([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiled["response"].Validate(value); err != nil {
			t.Fatalf("response shape failed schema validation: %s: %v", raw, err)
		}
		if err := p004MailboxSemantic("response", value); err != nil {
			t.Fatalf("response semantic validation failed: %s: %v", raw, err)
		}
	}
}
