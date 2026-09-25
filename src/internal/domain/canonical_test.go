package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestP009D05CanonicalMutationGoldenHash(t *testing.T) {
	first := []byte(`{"request_id":"req-first","idempotency_key":"key-1","operation":"create_session","environment":"mac-dev","execution_target":{"profile":"mac-workstation","kind":"local"}}`)
	second := []byte(`{
  "source": {"mode":"empty"},
  "execution_target": {"kind":"local", "profile":"mac-workstation"},
  "request_id": "req-retry",
  "environment": "mac-dev",
  "operation": "create_session",
  "idempotency_key": "different-lookup-key"
}`)

	canonical, err := CanonicalizeMutationRequestJSON("create_session", first, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"operation":"create_session","source":{"mode":"empty"}}`
	if string(canonical) != wantCanonical {
		t.Fatalf("canonical JSON = %s, want %s", canonical, wantCanonical)
	}

	firstHash, err := HashMutationRequestJSON("create_session", first, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := HashMutationRequestJSON("create_session", second, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if firstHash.Version() != CanonicalizationVersionV1 {
		t.Fatalf("canonicalization version = %d, want %d", firstHash.Version(), CanonicalizationVersionV1)
	}
	if CompareIdempotency(firstHash, secondHash) != IdempotencySamePayload {
		t.Fatalf("equivalent mailbox retries compare as %q, want %q", CompareIdempotency(firstHash, secondHash), IdempotencySamePayload)
	}
	directRequest := []byte(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`)
	directHash, err := HashMutationRequestJSON("create_session", directRequest, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if CompareIdempotency(firstHash, directHash) != IdempotencySamePayload {
		t.Fatal("direct request body and mailbox exchange produced different semantic hashes")
	}
	if got, want := firstHash.String(), "v1:062461f93338ddeb3335abc245547bb84be45fd3ef1eaa035bd8031ee9860fab"; got != want {
		t.Fatalf("canonical request hash = %q, want %q", got, want)
	}
}

func TestP009CanonicalSupportsEveryV1Mutation(t *testing.T) {
	cases := []struct {
		operation string
		request   string
	}{
		{"create_session", `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`},
		{"submit_command", `{"session_id":"session-1","script":"pwd"}`},
		{"cancel_command", `{"command_id":"command-1"}`},
		{"close_session", `{"session_id":"session-1"}`},
		{"run", `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"pwd"}`},
	}
	for _, test := range cases {
		t.Run(test.operation, func(t *testing.T) {
			if _, err := HashMutationRequestJSON(test.operation, []byte(test.request), CanonicalizationOptions{}); err != nil {
				t.Fatalf("hash %s mutation: %v", test.operation, err)
			}
		})
	}
}

func TestP009D05ChangedMutationFieldsConflict(t *testing.T) {
	base := []byte(`{"request_id":"req-1","idempotency_key":"same-key","operation":"submit_command","session_id":"session-1","script":"printf 'one'","timeout_seconds":60,"policy":{"output_bytes":100}}`)
	changedRequests := map[string][]byte{
		"session":   []byte(`{"request_id":"req-2","idempotency_key":"same-key","operation":"submit_command","session_id":"session-2","script":"printf 'one'","timeout_seconds":60,"policy":{"output_bytes":100}}`),
		"script":    []byte(`{"request_id":"req-2","idempotency_key":"same-key","operation":"submit_command","session_id":"session-1","script":"printf 'two'","timeout_seconds":60,"policy":{"output_bytes":100}}`),
		"timeout":   []byte(`{"request_id":"req-2","idempotency_key":"same-key","operation":"submit_command","session_id":"session-1","script":"printf 'one'","timeout_seconds":61,"policy":{"output_bytes":100}}`),
		"policy":    []byte(`{"request_id":"req-2","idempotency_key":"same-key","operation":"submit_command","session_id":"session-1","script":"printf 'one'","timeout_seconds":60,"policy":{"output_bytes":101}}`),
		"operation": []byte(`{"request_id":"req-2","idempotency_key":"same-key","operation":"run","session_id":"session-1","script":"printf 'one'","timeout_seconds":60,"policy":{"output_bytes":100}}`),
	}
	baseHash, err := HashMutationRequestJSON("submit_command", base, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range changedRequests {
		t.Run(name, func(t *testing.T) {
			operation := "submit_command"
			if name == "operation" {
				operation = "run"
			}
			changedHash, err := HashMutationRequestJSON(operation, request, CanonicalizationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result := CompareIdempotency(baseHash, changedHash); result != IdempotencyConflict {
				t.Fatalf("changed %s compares as %q, want %q", name, result, IdempotencyConflict)
			}
		})
	}
}

func TestP009D05CreateTargetEnvironmentAndSourceConflict(t *testing.T) {
	base := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"policy":{"max_lifetime_seconds":3600}}`)
	changedRequests := map[string][]byte{
		"environment": []byte(`{"environment":"mac-dev","execution_target":{"kind":"remote","profile":"linux-host"},"policy":{"max_lifetime_seconds":3600}}`),
		"target":      []byte(`{"environment":"linux-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"policy":{"max_lifetime_seconds":3600}}`),
		"source":      []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"git_revision","repository_alias":"fixture","requested_revision":"main"},"policy":{"max_lifetime_seconds":3600}}`),
		"policy":      []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"policy":{"max_lifetime_seconds":3601}}`),
	}
	baseHash, err := HashMutationRequestJSON("create_session", base, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range changedRequests {
		t.Run(name, func(t *testing.T) {
			changedHash, err := HashMutationRequestJSON("create_session", request, CanonicalizationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result := CompareIdempotency(baseHash, changedHash); result != IdempotencyConflict {
				t.Fatalf("changed %s compares as %q, want %q", name, result, IdempotencyConflict)
			}
		})
	}
}

func TestP009D05ArrayOrderRemainsSemantic(t *testing.T) {
	forward := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"policy":{"repository_aliases":["one","two"]}}`)
	reversed := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"policy":{"repository_aliases":["two","one"]}}`)
	forwardHash, err := HashMutationRequestJSON("create_session", forward, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reversedHash, err := HashMutationRequestJSON("create_session", reversed, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if CompareIdempotency(forwardHash, reversedHash) != IdempotencyConflict {
		t.Fatal("array order was discarded during canonicalization")
	}
}

func TestP009CanonicalResolvedDefaultsAndNumberSpelling(t *testing.T) {
	defaults := CanonicalizationOptions{Defaults: map[string]json.RawMessage{
		"timeout_seconds": json.RawMessage(`1800`),
	}}
	omitted := []byte(`{"operation":"submit_command","session_id":"session-1","script":"printf 'ok'"}`)
	explicit := []byte(`{"operation":"submit_command","session_id":"session-1","script":"printf 'ok'","timeout_seconds":1.8e3}`)
	omittedHash, err := HashMutationRequestJSON("submit_command", omitted, defaults)
	if err != nil {
		t.Fatal(err)
	}
	explicitHash, err := HashMutationRequestJSON("submit_command", explicit, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if CompareIdempotency(omittedHash, explicitHash) != IdempotencySamePayload {
		t.Fatalf("resolved omitted default and equivalent number compare as %q", CompareIdempotency(omittedHash, explicitHash))
	}

	canonical, err := CanonicalizeMutationRequestJSON("submit_command", explicit, defaults)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"operation":"submit_command","script":"printf 'ok'","session_id":"session-1","timeout_seconds":1.8e3}`
	if string(canonical) != want {
		t.Fatalf("canonical numeric JSON = %s, want %s", canonical, want)
	}

	runWithoutSource := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"pwd"}`)
	runWithEmptySource := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"pwd"}`)
	runDefaultHash, err := HashMutationRequestJSON("run", runWithoutSource, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runExplicitHash, err := HashMutationRequestJSON("run", runWithEmptySource, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if CompareIdempotency(runDefaultHash, runExplicitHash) != IdempotencySamePayload {
		t.Fatal("omitted run source did not normalize to the empty source")
	}
}

func TestP009CanonicalScriptUTF8BytesAreNotUnicodeNormalized(t *testing.T) {
	composed := []byte("{\"operation\":\"submit_command\",\"session_id\":\"s\",\"script\":\"é\"}")
	decomposed := []byte("{\"operation\":\"submit_command\",\"session_id\":\"s\",\"script\":\"é\"}")
	escaped := []byte(`{"operation":"submit_command","session_id":"s","script":"\u00e9"}`)
	emoji := []byte(`{"operation":"submit_command","session_id":"s","script":"\uD83D\uDE00"}`)
	rawEmoji := []byte("{\"operation\":\"submit_command\",\"session_id\":\"s\",\"script\":\"😀\"}")
	composedHash, err := HashMutationRequestJSON("submit_command", composed, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	decomposedHash, err := HashMutationRequestJSON("submit_command", decomposed, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	escapedHash, err := HashMutationRequestJSON("submit_command", escaped, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	emojiHash, err := HashMutationRequestJSON("submit_command", emoji, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawEmojiHash, err := HashMutationRequestJSON("submit_command", rawEmoji, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if CompareIdempotency(composedHash, decomposedHash) != IdempotencyConflict {
		t.Fatal("canonically different UTF-8 script bytes were normalized to the same request")
	}
	if CompareIdempotency(composedHash, escapedHash) != IdempotencySamePayload {
		t.Fatal("equivalent JSON Unicode escape changed decoded script bytes")
	}
	if CompareIdempotency(emojiHash, rawEmojiHash) != IdempotencySamePayload {
		t.Fatal("valid surrogate pair did not preserve its UTF-8 script code point")
	}
	canonical, err := CanonicalizeMutationRequestJSON("submit_command", composed, CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte("é")) {
		t.Fatalf("canonical script did not preserve its UTF-8 code point: %s", canonical)
	}
}

func TestP009CanonicalHashComparisonIncludesVersion(t *testing.T) {
	first, err := HashMutationRequestJSON("cancel_command", []byte(`{"operation":"cancel_command","command_id":"cmd-1"}`), CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	same, err := NewCanonicalHash(first.Version(), first.SHA256())
	if err != nil {
		t.Fatal(err)
	}
	otherVersion, err := NewCanonicalHash(first.Version()+1, first.SHA256())
	if err != nil {
		t.Fatal(err)
	}
	if CompareIdempotency(first, same) != IdempotencySamePayload {
		t.Fatal("identical versioned hash was not treated as the same payload")
	}
	if CompareIdempotency(first, otherVersion) != IdempotencyConflict {
		t.Fatal("different canonicalization versions were treated as the same payload")
	}
	if _, err := NewCanonicalHash(0, first.SHA256()); !errors.Is(err, ErrInvalidCanonicalHash) {
		t.Fatalf("zero version error = %v, want ErrInvalidCanonicalHash", err)
	}
	if _, err := NewCanonicalHash(first.Version(), first.SHA256()[:len(first.SHA256())-1]); !errors.Is(err, ErrInvalidCanonicalHash) {
		t.Fatalf("short digest error = %v, want ErrInvalidCanonicalHash", err)
	}
}

func TestP009CanonicalMutationRejectsAmbiguousOrInvalidJSON(t *testing.T) {
	invalid := map[string][]byte{
		"top-level array":         []byte(`[]`),
		"read operation":          []byte(`{"operation":"get_session"}`),
		"duplicate request key":   []byte(`{"operation":"cancel_command","command_id":"a","command_id":"b"}`),
		"trailing value":          []byte(`{"operation":"cancel_command","command_id":"a"} {}`),
		"invalid JSON":            []byte(`{"operation":"cancel_command",}`),
		"operation mismatch":      []byte(`{"operation":"cancel_command","command_id":"cmd-1"}`),
		"unpaired high surrogate": []byte(`{"operation":"submit_command","session_id":"s","script":"\uD800"}`),
		"unpaired low surrogate":  []byte(`{"operation":"submit_command","session_id":"s","script":"\uDC00"}`),
	}
	for name, request := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalizeMutationRequestJSON("submit_command", request, CanonicalizationOptions{}); !errors.Is(err, ErrInvalidCanonicalJSON) {
				t.Fatalf("canonicalization error = %v, want ErrInvalidCanonicalJSON", err)
			}
		})
	}

	invalidUTF8 := []byte(`{"operation":"submit_command","session_id":"s","script":"` + string([]byte{0xff}) + `"}`)
	if _, err := CanonicalizeMutationRequestJSON("submit_command", invalidUTF8, CanonicalizationOptions{}); !errors.Is(err, ErrInvalidCanonicalJSON) {
		t.Fatalf("invalid UTF-8 error = %v, want ErrInvalidCanonicalJSON", err)
	}
	reservedDefault := CanonicalizationOptions{Defaults: map[string]json.RawMessage{"request_id": json.RawMessage(`"unexpected"`)}}
	if _, err := CanonicalizeMutationRequestJSON("cancel_command", []byte(`{"operation":"cancel_command","command_id":"a"}`), reservedDefault); !errors.Is(err, ErrInvalidCanonicalDefaults) {
		t.Fatalf("reserved default error = %v, want ErrInvalidCanonicalDefaults", err)
	}
	malformedDefault := CanonicalizationOptions{Defaults: map[string]json.RawMessage{"timeout_seconds": json.RawMessage(`{`)}}
	if _, err := CanonicalizeMutationRequestJSON("submit_command", []byte(`{"operation":"submit_command","session_id":"s","script":"x"}`), malformedDefault); !errors.Is(err, ErrInvalidCanonicalDefaults) {
		t.Fatalf("malformed default error = %v, want ErrInvalidCanonicalDefaults", err)
	}
	invalidDefaultName := CanonicalizationOptions{Defaults: map[string]json.RawMessage{string([]byte{0xff}): json.RawMessage(`1`)}}
	if _, err := CanonicalizeMutationRequestJSON("submit_command", []byte(`{"operation":"submit_command","session_id":"s","script":"x"}`), invalidDefaultName); !errors.Is(err, ErrInvalidCanonicalDefaults) {
		t.Fatalf("invalid default name error = %v, want ErrInvalidCanonicalDefaults", err)
	}
}
