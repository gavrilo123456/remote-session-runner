package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestCompactPreparedRequestBodyRestoresPreparedDigest(t *testing.T) {
	want := []byte(`{"environment":"sandbox-host-dev","timeout_seconds":5}`)
	digest := sha256.Sum256(want)
	input := json.RawMessage("{\n  \"environment\": \"sandbox-host-dev\",\n  \"timeout_seconds\": 5\n}")
	got, err := compactPreparedRequestBody(input, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("compact prepared body: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("compacted body = %s, want %s", got, want)
	}
}

func TestCompactPreparedRequestBodyRejectsDigestMismatch(t *testing.T) {
	if _, err := compactPreparedRequestBody(json.RawMessage(`{"environment":"sandbox-host-dev"}`), "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
}
