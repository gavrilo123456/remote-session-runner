package sshbridge

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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/v1/*.json testdata/p004/valid/*.json testdata/p004/invalid/*.json
var p004BridgeFiles embed.FS

const p004BridgeSchemaBase = "https://remote-session-runner.invalid/src/internal/sshbridge/schemas/v1/"

var p004BridgeSchemaNames = []string{"request", "reply"}

type p004BridgeFixtureFile struct {
	Cases []struct {
		Name     string          `json:"name"`
		Document json.RawMessage `json:"document"`
	} `json:"cases"`
}

func p004BridgeDecode(data []byte) (any, error) {
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

func p004BridgeSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for _, name := range p004BridgeSchemaNames {
		filename := name + ".schema.json"
		data, err := p004BridgeFiles.ReadFile(path.Join("schemas/v1", filename))
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		document, err := p004BridgeDecode(data)
		if err != nil {
			t.Fatalf("decode %s: %v", filename, err)
		}
		if err := compiler.AddResource(p004BridgeSchemaBase+filename, document); err != nil {
			t.Fatalf("register %s: %v", filename, err)
		}
	}
	compiled := make(map[string]*jsonschema.Schema, len(p004BridgeSchemaNames))
	for _, name := range p004BridgeSchemaNames {
		filename := name + ".schema.json"
		schema, err := compiler.Compile(p004BridgeSchemaBase + filename)
		if err != nil {
			t.Fatalf("compile %s: %v", filename, err)
		}
		compiled[name] = schema
	}
	return compiled
}

func p004BridgeSemantic(name string, value any) error {
	if name != "reply" {
		return nil
	}
	reply, ok := value.(map[string]any)
	if !ok || reply["response_type"] != "event" {
		return nil
	}
	event, ok := reply["payload"].(map[string]any)
	if !ok || (event["type"] != "stdout" && event["type"] != "stderr") {
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
	if len(decoded) > 16*1024 {
		return fmt.Errorf("output event has %d raw bytes; maximum is 16384", len(decoded))
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

func TestP004BridgeFramesRoundTripAndValidateFixtures(t *testing.T) {
	compiled := p004BridgeSchemas(t)
	coverage := make(map[string]map[string]int, len(p004BridgeSchemaNames))
	for _, name := range p004BridgeSchemaNames {
		coverage[name] = map[string]int{"valid": 0, "invalid": 0}
	}
	err := fs.WalkDir(p004BridgeFiles, "testdata/p004", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(filename, ".json") {
			return walkErr
		}
		parts := strings.Split(path.Base(filename), "--")
		if len(parts) != 2 {
			return fmt.Errorf("fixture %q must use schema--case.json", filename)
		}
		schema, ok := compiled[parts[0]]
		if !ok {
			return fmt.Errorf("fixture %q refers to unknown schema %q", filename, parts[0])
		}
		kind := "invalid"
		if strings.Contains(filename, "/valid/") {
			kind = "valid"
		}
		data, err := p004BridgeFiles.ReadFile(filename)
		if err != nil {
			return err
		}
		var fixture p004BridgeFixtureFile
		if err := json.Unmarshal(data, &fixture); err != nil {
			return fmt.Errorf("decode fixture file %s: %w", filename, err)
		}
		if len(fixture.Cases) == 0 {
			return fmt.Errorf("fixture file %s contains no cases", filename)
		}
		for _, testCase := range fixture.Cases {
			value, err := p004BridgeDecode(testCase.Document)
			if err != nil {
				return fmt.Errorf("decode %s case %q: %w", filename, testCase.Name, err)
			}
			validationErr := schema.Validate(value)
			if validationErr == nil {
				validationErr = p004BridgeSemantic(parts[0], value)
			}
			if kind == "valid" && validationErr != nil {
				return fmt.Errorf("valid fixture %s case %q rejected: %w", filename, testCase.Name, validationErr)
			}
			if kind == "invalid" && validationErr == nil {
				return fmt.Errorf("invalid fixture %s case %q was accepted", filename, testCase.Name)
			}
			if kind == "valid" {
				encoded, err := json.Marshal(value)
				if err != nil {
					return fmt.Errorf("marshal %s case %q: %w", filename, testCase.Name, err)
				}
				roundTripped, err := p004BridgeDecode(encoded)
				if err != nil || !reflect.DeepEqual(value, roundTripped) {
					return fmt.Errorf("JSON round trip changed %s case %q: %v", filename, testCase.Name, err)
				}
			}
			coverage[parts[0]][kind]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range p004BridgeSchemaNames {
		for _, kind := range []string{"valid", "invalid"} {
			if coverage[name][kind] == 0 {
				t.Errorf("schema %s has no %s fixture", name, kind)
			}
		}
	}
}

func TestP004BridgeOperationVocabulary(t *testing.T) {
	data, err := p004BridgeFiles.ReadFile("testdata/p004/valid/request--operations.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture p004BridgeFixtureFile
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(fixture.Cases))
	for _, testCase := range fixture.Cases {
		value, err := p004BridgeDecode(testCase.Document)
		if err != nil {
			t.Fatal(err)
		}
		request, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("request fixture %q is not an object", testCase.Name)
		}
		operation, ok := request["operation"].(string)
		if !ok {
			t.Fatalf("request fixture %q has no operation", testCase.Name)
		}
		got = append(got, operation)
	}
	sort.Strings(got)
	want := []string{"cancel_command", "close_session", "create_or_resume_session", "get_command", "get_job", "get_session", "hello", "ping", "run_or_resume_job", "stream_command_events", "submit_or_resume_command"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request operation fixtures = %v, want %v", got, want)
	}
}

func TestP004BridgeOutputChunkLimit(t *testing.T) {
	schema := p004BridgeSchemas(t)["reply"]
	for _, size := range []int{16 * 1024, 16*1024 + 1} {
		payload := map[string]any{
			"command_id":  "cmd-limit",
			"sequence":    json.Number("2"),
			"type":        "stdout",
			"timestamp":   "2026-09-25T12:00:00Z",
			"encoding":    "base64",
			"data_base64": base64.StdEncoding.EncodeToString(make([]byte, size)),
			"byte_count":  json.Number(strconv.Itoa(size)),
		}
		frame := map[string]any{"protocol_version": json.Number("1"), "request_id": "r-limit", "response_type": "event", "payload": payload}
		validationErr := schema.Validate(frame)
		if size <= 16*1024 {
			if validationErr != nil {
				t.Errorf("maximum-size event with %d bytes rejected: %v", size, validationErr)
			}
			if err := p004BridgeSemantic("reply", frame); err != nil {
				t.Errorf("maximum-size event with %d bytes failed semantic check: %v", size, err)
			}
		} else if validationErr == nil {
			if semanticErr := p004BridgeSemantic("reply", frame); semanticErr == nil {
				t.Errorf("event with %d bytes exceeded the 16384-byte limit without rejection", size)
			}
		}
	}
}
