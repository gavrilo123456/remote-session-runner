package mailboxclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestP165ClientReadsFrozenIngressDiagnosticOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	if err := os.Mkdir(root, directoryMode); err != nil {
		t.Fatal(err)
	}
	diagnostics := filepath.Join(root, "diagnostics")
	if err := os.Mkdir(diagnostics, directoryMode); err != nil {
		t.Fatal(err)
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	requestID := "req-p165-schema-invalid"
	valid := []byte(`{"inbox_id":"slidestud-io","request_id":"req-p165-schema-invalid","diagnostic_revision":1,"lifecycle_phase":"ingress_validation","accepted":false,"executed":false,"code":"invalid_request_schema","message":"request does not satisfy the mailbox request format","observed_at":"2026-10-01T12:00:00Z"}`)
	path := filepath.Join(diagnostics, requestID+".json")
	if err := os.WriteFile(path, valid, fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		t.Fatal(err)
	}

	diagnostic, err := client.ReadDiagnostic(requestID)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic.InboxID != "slidestud-io" || diagnostic.RequestID != requestID || diagnostic.DiagnosticRevision != 1 ||
		diagnostic.LifecyclePhase != "ingress_validation" || diagnostic.Accepted || diagnostic.Executed ||
		diagnostic.Code != "invalid_request_schema" || diagnostic.Message != "request does not satisfy the mailbox request format" ||
		diagnostic.ObservedAt != "2026-10-01T12:00:00Z" {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
	if waited, err := client.WaitDiagnostic(context.Background(), requestID); err != nil || waited != diagnostic {
		t.Fatalf("waited=%+v err=%v", waited, err)
	}
	if _, err := os.Stat(filepath.Join(root, "acks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("diagnostic read unexpectedly created an ACK path: %v", err)
	}
}

func TestBUG017ClientReadsStaticSchemaCorrectionWithoutRequestContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	if err := os.Mkdir(root, directoryMode); err != nil {
		t.Fatal(err)
	}
	diagnostics := filepath.Join(root, "diagnostics")
	if err := os.Mkdir(diagnostics, directoryMode); err != nil {
		t.Fatal(err)
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	const requestID = "req-bug017-client"
	raw := []byte(`{"inbox_id":"slidestud-io","request_id":"req-bug017-client","diagnostic_revision":1,"lifecycle_phase":"ingress_validation","accepted":false,"executed":false,"code":"invalid_request_schema","message":"request does not satisfy the mailbox request format","schema_detail":{"schema_version":"v1","json_pointer":"/script","expected":"required string","received_type":"missing","canonical_run_field":"script","canonical_run_type":"string","minimal_valid_run":{"request_id":"<new-request-id>","idempotency_key":"<new-idempotency-key>","operation":"run","script":"<shell script>"}},"observed_at":"2026-10-10T12:00:00Z"}`)
	path := filepath.Join(diagnostics, requestID+".json")
	if err := os.WriteFile(path, raw, fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, fileMode); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := client.ReadDiagnostic(requestID)
	if err != nil || diagnostic.SchemaDetail == nil {
		t.Fatalf("ReadDiagnostic diagnostic=%+v err=%v", diagnostic, err)
	}
	if detail := diagnostic.SchemaDetail; detail.SchemaVersion != "v1" || detail.JSONPointer != "/script" || detail.Expected != "required string" ||
		detail.ReceivedType != "missing" || detail.CanonicalRunField != "script" || detail.CanonicalRunType != "string" ||
		detail.MinimalValidRun.RequestID != "<new-request-id>" || detail.MinimalValidRun.IdempotencyKey != "<new-idempotency-key>" ||
		detail.MinimalValidRun.Operation != "run" || detail.MinimalValidRun.Script != "<shell script>" {
		t.Fatalf("schema detail=%+v", detail)
	}
}

func TestP165ClientRejectsUnsafeOrInvalidIngressDiagnostic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	if err := os.Mkdir(root, directoryMode); err != nil {
		t.Fatal(err)
	}
	diagnostics := filepath.Join(root, "diagnostics")
	if err := os.Mkdir(diagnostics, directoryMode); err != nil {
		t.Fatal(err)
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	valid := `{"inbox_id":"analytics","request_id":"%s","diagnostic_revision":1,"lifecycle_phase":"ingress_validation","accepted":false,"executed":false,"code":"malformed_json","message":"request is not valid JSON","observed_at":"2026-10-01T12:00:00Z"}`
	cases := []struct {
		name string
		raw  string
		mode os.FileMode
	}{
		{name: "world-readable", raw: valid, mode: 0o644},
		{name: "unknown-field", raw: valid[:len(valid)-1] + `,"untrusted":"value"}`, mode: fileMode},
		{name: "wrong-fixed-message", raw: `{"inbox_id":"analytics","request_id":"%s","diagnostic_revision":1,"lifecycle_phase":"ingress_validation","accepted":false,"executed":false,"code":"malformed_json","message":"untrusted parser text","observed_at":"2026-10-01T12:00:00Z"}`, mode: fileMode},
		{name: "accepted", raw: `{"inbox_id":"analytics","request_id":"%s","diagnostic_revision":1,"lifecycle_phase":"ingress_validation","accepted":true,"executed":false,"code":"malformed_json","message":"request is not valid JSON","observed_at":"2026-10-01T12:00:00Z"}`, mode: fileMode},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			requestID := "req-p165-" + testCase.name
			path := filepath.Join(diagnostics, requestID+".json")
			if err := os.WriteFile(path, []byte(fmt.Sprintf(testCase.raw, requestID)), fileMode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, testCase.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ReadDiagnostic(requestID); !errors.Is(err, ErrDiagnostic) {
				t.Fatalf("ReadDiagnostic error=%v, want ErrDiagnostic", err)
			}
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := client.WaitDiagnostic(ctx, "req-p165-missing"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitDiagnostic missing error=%v, want deadline", err)
	}
}
