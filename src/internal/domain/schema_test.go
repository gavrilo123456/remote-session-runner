package domain

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/v1
var p002SchemaFiles embed.FS

const p002SchemaBaseURI = "https://remote-session-runner.invalid/schemas/v1/"

var p002SchemaNames = []string{
	"states",
	"execution-target",
	"source",
	"session",
	"command",
	"command-event",
	"error",
}

func compileP002Schemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for _, name := range p002SchemaNames {
		filename := name + ".schema.json"
		data, err := p002SchemaFiles.ReadFile(path.Join("schemas/v1", filename))
		if err != nil {
			t.Fatalf("read schema %s: %v", filename, err)
		}
		document, err := decodeP002JSON(data)
		if err != nil {
			t.Fatalf("decode schema %s: %v", filename, err)
		}
		if err := compiler.AddResource(p002SchemaBaseURI+filename, document); err != nil {
			t.Fatalf("register schema %s: %v", filename, err)
		}
	}

	compiled := make(map[string]*jsonschema.Schema, len(p002SchemaNames))
	for _, name := range p002SchemaNames {
		filename := name + ".schema.json"
		schema, err := compiler.Compile(p002SchemaBaseURI + filename)
		if err != nil {
			t.Fatalf("compile schema %s: %v", filename, err)
		}
		compiled[name] = schema
	}
	return compiled
}

func decodeP002JSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, fmt.Errorf("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return value, nil
}

func validateP002Fixture(schemaName string, value any) error {
	if schemaName != "command-event" {
		return nil
	}
	event, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("command event must be an object")
	}
	eventType, _ := event["type"].(string)
	if eventType != "stdout" && eventType != "stderr" {
		return nil
	}
	encoded, ok := event["data_base64"].(string)
	if !ok {
		return fmt.Errorf("output event has no base64 payload")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode output event base64: %w", err)
	}
	count, ok := event["byte_count"].(json.Number)
	if !ok {
		return fmt.Errorf("output event has no numeric byte_count")
	}
	byteCount, err := strconv.ParseInt(string(count), 10, 64)
	if err != nil {
		return fmt.Errorf("parse output event byte_count: %w", err)
	}
	if byteCount != int64(len(decoded)) {
		return fmt.Errorf("output event byte_count is %d, decoded payload has %d bytes", byteCount, len(decoded))
	}
	return nil
}

func TestP002SchemasCompile(t *testing.T) {
	compiled := compileP002Schemas(t)
	if len(compiled) != len(p002SchemaNames) {
		t.Fatalf("compiled %d schemas, want %d", len(compiled), len(p002SchemaNames))
	}
}

func TestP002GoldenFixtures(t *testing.T) {
	compiled := compileP002Schemas(t)
	coverage := make(map[string]map[string]int, len(p002SchemaNames))
	for _, name := range p002SchemaNames {
		coverage[name] = map[string]int{"valid": 0, "invalid": 0}
	}

	err := fs.WalkDir(p002SchemaFiles, "schemas/v1/fixtures", func(fixturePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(fixturePath, ".json") {
			return nil
		}
		parts := strings.Split(path.Base(fixturePath), "--")
		if len(parts) != 2 {
			return fmt.Errorf("fixture filename %q must use schema--case.json", fixturePath)
		}
		schemaName := parts[0]
		schema, ok := compiled[schemaName]
		if !ok {
			return fmt.Errorf("fixture %q refers to unknown schema %q", fixturePath, schemaName)
		}
		kind := "invalid"
		if strings.Contains(fixturePath, "/valid/") {
			kind = "valid"
		}
		data, err := p002SchemaFiles.ReadFile(fixturePath)
		if err != nil {
			return err
		}
		value, err := decodeP002JSON(data)
		if err != nil {
			return fmt.Errorf("decode fixture %s: %w", fixturePath, err)
		}
		validationErr := schema.Validate(value)
		if validationErr == nil {
			validationErr = validateP002Fixture(schemaName, value)
		}
		if kind == "valid" && validationErr != nil {
			return fmt.Errorf("valid fixture %s rejected: %w", fixturePath, validationErr)
		}
		if kind == "invalid" && validationErr == nil {
			return fmt.Errorf("invalid fixture %s was accepted", fixturePath)
		}
		coverage[schemaName][kind]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range p002SchemaNames {
		for _, kind := range []string{"valid", "invalid"} {
			if coverage[name][kind] == 0 {
				t.Errorf("schema %s has no %s golden fixture", name, kind)
			}
		}
	}
}

func TestP002ErrorEnvelopeCodes(t *testing.T) {
	schema := compileP002Schemas(t)["error"]
	codes := []string{
		"invalid_request",
		"environment_target_mismatch",
		"environment_forbidden",
		"controller_mismatch",
		"session_not_ready",
		"idempotency_conflict",
		"resource_not_found",
		"quota_exceeded",
		"runtime_unavailable",
		"transport_uncertain",
		"event_history_unavailable",
		"retention_expired",
	}
	sort.Strings(codes)
	for _, code := range codes {
		value := map[string]any{"code": code, "message": "contract test", "retryable": false}
		if err := schema.Validate(value); err != nil {
			t.Errorf("required error code %q rejected: %v", code, err)
		}
	}
	unknown := map[string]any{"code": "not-a-runner-error", "message": "contract test", "retryable": false}
	if err := schema.Validate(unknown); err == nil {
		t.Error("unknown error code was accepted")
	}
}
