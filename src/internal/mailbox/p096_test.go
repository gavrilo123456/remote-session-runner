package mailbox

import "testing"

func TestP096M08DeduplicationWarningMatchesV1ResponseSchema(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "expired key warning",
			raw:  `{"request_id":"req-p096-expired","operation":"create_session","request_state":"accepted","response_revision":1,"session_id":"sess-p096","delivery_state":"recorded","idempotency_warning":"deduplication_not_guaranteed"}`,
			want: true,
		},
		{
			name: "unknown warning rejected",
			raw:  `{"request_id":"req-p096-invalid-warning","operation":"submit_command","request_state":"accepted","response_revision":1,"idempotency_warning":"safe_to_retry"}`,
			want: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := p004MailboxDecode([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			err = compiled["response"].Validate(value)
			if (err == nil) != test.want {
				t.Fatalf("response schema validation error=%v, want valid=%v", err, test.want)
			}
		})
	}
}
