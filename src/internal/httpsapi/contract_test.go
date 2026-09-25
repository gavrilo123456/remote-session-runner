package httpsapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"remote-session-runner/src/internal/domain"
)

//go:embed openapi/v1/openapi.json testdata/p003/valid/contract-cases.json testdata/p003/invalid/contract-cases.json
var p003ContractFiles embed.FS

const p003OpenAPIURL = "https://remote-session-runner.invalid/src/internal/httpsapi/openapi/v1/openapi.json"

type p003Fixture struct {
	Case          string          `json:"case"`
	Violation     string          `json:"violation,omitempty"`
	Ingress       string          `json:"ingress"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	Request       json.RawMessage `json:"request,omitempty"`
	TargetKind    string          `json:"target_kind"`
	ExpectedScope string          `json:"expected_scope,omitempty"`
	Status        int             `json:"status,omitempty"`
	Response      json.RawMessage `json:"response,omitempty"`
}

type p003Operation struct {
	method      string
	path        string
	operationID string
	status      string
}

var p003ExpectedOperations = []p003Operation{
	{method: "post", path: "/v1/sessions", operationID: "createSession", status: "202"},
	{method: "get", path: "/v1/sessions/{session_id}", operationID: "getSession", status: "200"},
	{method: "delete", path: "/v1/sessions/{session_id}", operationID: "closeSession", status: "202"},
	{method: "post", path: "/v1/sessions/{session_id}/commands", operationID: "submitCommand", status: "202"},
	{method: "get", path: "/v1/commands/{command_id}", operationID: "getCommand", status: "200"},
	{method: "get", path: "/v1/commands/{command_id}/events", operationID: "getCommandEvents", status: "200"},
	{method: "post", path: "/v1/commands/{command_id}/cancel", operationID: "cancelCommand", status: "202"},
	{method: "post", path: "/v1/jobs", operationID: "createJob", status: "202"},
	{method: "get", path: "/v1/jobs/{job_id}", operationID: "getJob", status: "200"},
}

func p003Decode(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func p003Object(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want object", label, value)
	}
	return object
}

func p003At(t *testing.T, root any, pointer string) any {
	t.Helper()
	if !strings.HasPrefix(pointer, "#/") {
		t.Fatalf("unsupported local reference %q", pointer)
	}
	var current any = root
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("reference %q traverses a non-object at %q", pointer, part)
		}
		var exists bool
		current, exists = object[part]
		if !exists {
			t.Fatalf("reference %q has no member %q", pointer, part)
		}
	}
	return current
}

func p003Ref(t *testing.T, root any, object map[string]any) any {
	t.Helper()
	ref, ok := object["$ref"].(string)
	if !ok {
		t.Fatalf("object has no $ref: %#v", object)
	}
	if strings.HasPrefix(ref, "#/") {
		return p003At(t, root, ref)
	}
	t.Fatalf("expected a local OpenAPI reference, got %q", ref)
	return nil
}

func p003OpenAPIDoc(t *testing.T) map[string]any {
	t.Helper()
	data, err := p003ContractFiles.ReadFile("openapi/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	return p003Object(t, p003Decode(t, data), "OpenAPI document")
}

func p003CompileSchemas(t *testing.T, document map[string]any) *jsonschema.Compiler {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(p003OpenAPIURL, document); err != nil {
		t.Fatalf("register OpenAPI document as JSON Schema resource: %v", err)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source file")
	}
	domainSchemaDir := path.Join(path.Dir(sourceFile), "..", "domain", "schemas", "v1")
	for _, filename := range []string{"states.schema.json", "execution-target.schema.json", "source.schema.json", "session.schema.json", "command.schema.json", "command-event.schema.json", "error.schema.json"} {
		data, err := os.ReadFile(filepath.Join(domainSchemaDir, filename))
		if err != nil {
			t.Fatalf("read shared P002 schema %s: %v", filename, err)
		}
		schema := p003Object(t, p003Decode(t, data), filename)
		resourcePath := "src/internal/domain/schemas/v1/" + filename
		resourceURL, err := url.Parse(p003OpenAPIURL)
		if err != nil {
			t.Fatal(err)
		}
		resourceURL.Path = "/" + resourcePath
		if err := compiler.AddResource(resourceURL.String(), schema); err != nil {
			t.Fatalf("register shared P002 schema %s at %s: %v", filename, resourceURL, err)
		}
		if id, _ := schema["$id"].(string); id != "" {
			if err := compiler.AddResource(id, schema); err != nil {
				t.Fatalf("register shared P002 schema ID %s: %v", id, err)
			}
		}
	}
	return compiler
}

func p003CompileRef(t *testing.T, compiler *jsonschema.Compiler, ref string) *jsonschema.Schema {
	t.Helper()
	if strings.HasPrefix(ref, "#/") {
		ref = p003OpenAPIURL + ref
	} else {
		base, err := url.Parse(p003OpenAPIURL)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := base.Parse(ref)
		if err != nil {
			t.Fatalf("resolve schema reference %q: %v", ref, err)
		}
		ref = resolved.String()
	}
	schema, err := compiler.Compile(ref)
	if err != nil {
		t.Fatalf("compile schema reference %q: %v", ref, err)
	}
	return schema
}

func p003OperationAt(t *testing.T, document map[string]any, method, pathName string) map[string]any {
	t.Helper()
	paths := p003Object(t, document["paths"], "paths")
	pathItem := p003Object(t, paths[pathName], "path "+pathName)
	return p003Object(t, pathItem[method], method+" "+pathName)
}

func p003LoadFixtures(t *testing.T, fixturePath string) []p003Fixture {
	t.Helper()
	data, err := p003ContractFiles.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []p003Fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode %s: %v", fixturePath, err)
	}
	return fixtures
}

func p003RequestSchemaRef(t *testing.T, document map[string]any, operation map[string]any) (string, bool) {
	t.Helper()
	requestBodyValue, exists := operation["requestBody"]
	if !exists {
		return "", false
	}
	requestBody := p003Object(t, p003Ref(t, document, p003Object(t, requestBodyValue, "requestBody")), "request body component")
	content := p003Object(t, requestBody["content"], "request body content")
	jsonContent := p003Object(t, content["application/json"], "application/json request body")
	schema := p003Object(t, jsonContent["schema"], "request JSON schema")
	ref, ok := schema["$ref"].(string)
	if !ok {
		t.Fatalf("request schema for %v has no $ref", operation["operationId"])
	}
	return ref, true
}

func p003ResponseSchemaRef(t *testing.T, document map[string]any, operation map[string]any, status string, contentType string) string {
	t.Helper()
	responses := p003Object(t, operation["responses"], "operation responses")
	response := p003Object(t, p003Ref(t, document, p003Object(t, responses[status], status+" response")), status+" response component")
	content := p003Object(t, response["content"], "response content")
	media := p003Object(t, content[contentType], contentType+" response")
	schema := p003Object(t, media["schema"], "response JSON schema")
	ref, ok := schema["$ref"].(string)
	if !ok {
		t.Fatalf("response schema for status %s has no $ref", status)
	}
	return ref
}

func TestP003OpenAPIRoutesAndSecurity(t *testing.T) {
	document := p003OpenAPIDoc(t)
	if document["openapi"] != "3.1.0" {
		t.Fatalf("OpenAPI version = %v, want 3.1.0", document["openapi"])
	}
	servers := document["servers"].([]any)
	if len(servers) != 1 || p003Object(t, servers[0], "direct server")["url"] != "https://129.151.232.40:8443" {
		t.Fatalf("direct server URL must be the selected public HTTPS endpoint: %#v", servers)
	}
	security := p003Object(t, p003Object(t, document["components"], "components")["securitySchemes"], "security schemes")
	if p003Object(t, security["mutualTLS"], "mutualTLS security scheme")["type"] != "mutualTLS" {
		t.Fatal("direct HTTPS security scheme must be mutualTLS")
	}
	globalSecurity := document["security"].([]any)
	if len(globalSecurity) != 1 || p003Object(t, globalSecurity[0], "global security")["mutualTLS"] == nil {
		t.Fatalf("direct HTTPS must require mutual TLS: %#v", globalSecurity)
	}
	local := p003Object(t, document["x-runner-local-unix-socket"], "local Unix socket extension")
	if local["transport"] != "HTTP over AF_UNIX" || local["account"] != "tomasz.walczuk" || local["same_paths_and_json_contract"] != true || local["authentication"] != "Owner-only socket and the connected process account; no HTTP client certificate is used." {
		t.Fatalf("local ingress must document owner-only Unix identity separately from mTLS: %#v", local)
	}
	localController := p003Object(t, local["controller"], "local controller")
	if localController["controller_type"] != "local_user" || localController["controller_id"] != "tomasz.walczuk" || local["mutation_acceptance_scope"] != "local_intent" {
		t.Fatalf("local socket identity or acceptance scope mismatch: %#v", local)
	}
	if got := local["allowed_execution_target_kinds"].([]any); !reflect.DeepEqual(got, []any{"local", "remote"}) {
		t.Fatalf("local target kinds = %#v, want local and remote", got)
	}

	paths := p003Object(t, document["paths"], "paths")
	if len(paths) != 8 {
		t.Fatalf("OpenAPI defines %d paths, want the 8 v1 resource paths", len(paths))
	}
	var actual []string
	for pathName, value := range paths {
		pathItem := p003Object(t, value, pathName)
		for method, operationValue := range pathItem {
			if method == "parameters" || strings.HasPrefix(method, "x-") {
				continue
			}
			operation := p003Object(t, operationValue, method+" "+pathName)
			actual = append(actual, method+" "+pathName+"="+fmt.Sprint(operation["operationId"]))
		}
	}
	var expected []string
	for _, route := range p003ExpectedOperations {
		operation := p003OperationAt(t, document, route.method, route.path)
		expected = append(expected, route.method+" "+route.path+"="+route.operationID)
		if operation["operationId"] != route.operationID {
			t.Errorf("%s %s operationId = %v, want %s", route.method, route.path, operation["operationId"], route.operationID)
		}
		if _, overridden := operation["security"]; overridden {
			t.Errorf("%s %s overrides the global mandatory mutualTLS requirement", route.method, route.path)
		}
		responses := p003Object(t, operation["responses"], "responses")
		if _, ok := responses[route.status]; !ok {
			t.Errorf("%s %s is missing required %s response", route.method, route.path, route.status)
		}
		if _, ok := responses["403"]; route.method != "post" && route.method != "delete" && !ok {
			t.Errorf("read route %s %s must document controller refusal", route.method, route.path)
		}
		if route.method == "post" || route.method == "delete" {
			parameters := operation["parameters"].([]any)
			hasIdempotency := false
			for _, value := range parameters {
				parameter := p003Object(t, value, "mutation parameter")
				if parameter["$ref"] == "#/components/parameters/IdempotencyKey" {
					hasIdempotency = true
				}
			}
			if !hasIdempotency {
				t.Errorf("mutation %s %s must require Idempotency-Key", route.method, route.path)
			}
		}
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("route set mismatch:\n got %v\nwant %v", actual, expected)
	}

	createSession := p003OperationAt(t, document, "post", "/v1/sessions")
	createJob := p003OperationAt(t, document, "post", "/v1/jobs")
	for _, operation := range []map[string]any{createSession, createJob} {
		if got := operation["x-runner-direct-target-kinds"].([]any); !reflect.DeepEqual(got, []any{"remote"}) {
			t.Errorf("direct create target rule = %#v, want remote only", got)
		}
	}
	submitCommand := p003OperationAt(t, document, "post", "/v1/sessions/{session_id}/commands")
	requestRef, _ := p003RequestSchemaRef(t, document, submitCommand)
	requestSchema := p003Object(t, p003At(t, document, requestRef), "submit request schema")
	properties := p003Object(t, requestSchema["properties"], "submit request properties")
	if _, hasTarget := properties["execution_target"]; hasTarget || requestSchema["additionalProperties"] != false {
		t.Fatal("submit command schema must reject any target override")
	}

	events := p003OperationAt(t, document, "get", "/v1/commands/{command_id}/events")
	eventResponses := p003Object(t, events["responses"], "event responses")
	if _, ok := eventResponses["410"]; !ok {
		t.Fatal("event route must document 410 event_history_unavailable")
	}
	limits := p003Object(t, document["x-runner-limits"], "Runner API limits")
	wantRequestLimit := json.Number(fmt.Sprint(domain.MaxSerializedRequestBytes))
	wantScriptLimit := json.Number(fmt.Sprint(domain.MaxScriptUTF8Bytes))
	if limits["max_serialized_request_bytes"] != wantRequestLimit || limits["max_script_utf8_bytes"] != wantScriptLimit || limits["enforced_before_acceptance"] != true {
		t.Fatalf("API limits mismatch: %#v", limits)
	}
}

func TestP003ContractFixtures(t *testing.T) {
	document := p003OpenAPIDoc(t)
	compiler := p003CompileSchemas(t, document)

	for _, fixture := range p003LoadFixtures(t, "testdata/p003/valid/contract-cases.json") {
		t.Run("valid/"+fixture.Case, func(t *testing.T) {
			operation := p003OperationAt(t, document, fixture.Method, fixture.Path)
			if fixture.Request != nil {
				requestRef, ok := p003RequestSchemaRef(t, document, operation)
				if !ok {
					t.Fatal("fixture has request body but route does not")
				}
				request := p003Decode(t, fixture.Request)
				if err := p003CompileRef(t, compiler, requestRef).Validate(request); err != nil {
					t.Fatalf("valid request rejected: %v", err)
				}
				if err := p003ValidateRequestLimits(request); err != nil {
					t.Fatalf("valid request exceeds a declared wire limit: %v", err)
				}
			}
			if fixture.Status < 200 {
				t.Fatal("fixture has no expected response status")
			}
			responses := p003Object(t, operation["responses"], "responses")
			if _, ok := responses[fmt.Sprint(fixture.Status)]; !ok {
				t.Fatalf("route has no expected %d response", fixture.Status)
			}
			responseRef := p003ResponseSchemaRef(t, document, operation, fmt.Sprint(fixture.Status), "application/json")
			response := p003Decode(t, fixture.Response)
			if err := p003CompileRef(t, compiler, responseRef).Validate(response); err != nil {
				t.Fatalf("valid response rejected: %v", err)
			}
			if fixture.ExpectedScope != "" {
				responseObject := p003Object(t, response, "acceptance response")
				scope := responseObject["acceptance_scope"]
				wantScope := fixture.ExpectedScope
				if fixture.Ingress == "local_unix_socket" && wantScope != "local_intent" {
					t.Fatal("local Unix socket must report local_intent")
				}
				if fixture.Ingress == "direct_https" && wantScope != "target_authority" {
					t.Fatal("direct HTTPS must report target_authority")
				}
				if scope != wantScope {
					t.Fatalf("acceptance_scope = %v, want %s", scope, wantScope)
				}
				operationID := operation["operationId"]
				resourceID := responseObject["resource_id"]
				var specificID any
				switch operationID {
				case "createSession", "closeSession":
					specificID = responseObject["session_id"]
				case "submitCommand", "cancelCommand":
					specificID = responseObject["command_id"]
				case "createJob":
					specificID = responseObject["job_id"]
				}
				if specificID != nil && specificID != resourceID {
					t.Fatalf("resource_id %v differs from operation resource ID %v", resourceID, specificID)
				}
				if fixture.Ingress == "local_unix_socket" && fixture.TargetKind == "remote" {
					known := p003Object(t, responseObject["known_state"], "known_state")
					for _, authorityField := range []string{"session_state", "command_state", "job_phase"} {
						if _, exists := known[authorityField]; exists {
							t.Fatalf("queued remote local intent invents target-authoritative %s", authorityField)
						}
					}
					if known["delivery_state"] != "recorded" {
						t.Fatalf("queued remote local intent delivery_state = %v, want recorded", known["delivery_state"])
					}
				}
			}
			if fixture.TargetKind != "" {
				responseObject := p003Object(t, response, "response")
				targetValue := responseObject["execution_target"]
				if targetValue == nil {
					if resource, ok := responseObject["resource"]; ok {
						targetValue = p003Object(t, resource, "read resource")["execution_target"]
					}
				}
				target := p003Object(t, targetValue, "execution_target")
				if target["kind"] != fixture.TargetKind {
					t.Fatalf("response target = %v, want %s", target["kind"], fixture.TargetKind)
				}
				if fixture.Ingress == "direct_https" && target["kind"] != "remote" {
					t.Fatal("direct HTTPS fixture used a non-remote target")
				}
			}
			if responseObject, ok := response.(map[string]any); ok && responseObject["view"] == "local_intent" {
				resource := p003Object(t, responseObject["resource"], "local intent resource")
				for _, forbidden := range []string{"authority", "session_state", "command_state", "phase", "teardown_state", "exit_code", "final_event_sequence"} {
					if _, exists := resource[forbidden]; exists {
						t.Fatalf("local intent read fabricates target-authoritative %s", forbidden)
					}
				}
				if resource["delivery_state"] != "not_delivered" {
					t.Fatalf("local intent read delivery_state = %v, want not_delivered", resource["delivery_state"])
				}
			}
		})
	}

	for _, fixture := range p003LoadFixtures(t, "testdata/p003/invalid/contract-cases.json") {
		t.Run("invalid/"+fixture.Case, func(t *testing.T) {
			operation := p003OperationAt(t, document, fixture.Method, fixture.Path)
			requestRef, ok := p003RequestSchemaRef(t, document, operation)
			if !ok {
				t.Fatal("invalid fixture has no request body schema")
			}
			request := p003Decode(t, fixture.Request)
			if err := p003ValidateRequestLimits(request); err != nil {
				t.Fatalf("invalid fixture must pass the declared wire-size limits: %v", err)
			}
			validationErr := p003CompileRef(t, compiler, requestRef).Validate(request)
			switch fixture.Violation {
			case "request_schema":
				if validationErr == nil {
					t.Fatal("invalid request passed its schema")
				}
			case "ingress_target":
				if validationErr != nil {
					t.Fatalf("target-rule fixture must pass the generic request schema: %v", validationErr)
				}
				allowed := operation["x-runner-direct-target-kinds"].([]any)
				if fixture.Ingress != "direct_https" || containsP003(allowed, fixture.TargetKind) {
					t.Fatalf("direct ingress rule did not reject target %q", fixture.TargetKind)
				}
			case "request_limit":
				if validationErr != nil {
					t.Fatalf("wire-size fixture must pass its request schema: %v", validationErr)
				}
				if err := p003ValidateRequestLimits(request); err == nil {
					t.Fatal("oversized request passed the declared request limits")
				}
			default:
				t.Fatalf("unknown fixture violation %q", fixture.Violation)
			}
		})
	}
}

func p003ValidateRequestLimits(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("serialize request: %w", err)
	}
	if err := domain.ValidateSerializedRequest(data); err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if script, exists := object["script"]; exists {
		text, ok := script.(string)
		if ok {
			return domain.ValidateScriptUTF8(text)
		}
	}
	return nil
}

func TestP003WireLimitBoundaries(t *testing.T) {
	if err := p003ValidateRequestLimits(map[string]any{"script": strings.Repeat("x", domain.MaxScriptUTF8Bytes)}); err != nil {
		t.Fatalf("script at 131072-byte limit rejected: %v", err)
	}
	if err := p003ValidateRequestLimits(map[string]any{"script": strings.Repeat("x", domain.MaxScriptUTF8Bytes+1)}); err == nil {
		t.Fatal("script one byte above the UTF-8 byte limit was accepted")
	}
	if err := p003ValidateRequestLimits(map[string]any{"script": strings.Repeat("🧪", domain.MaxScriptUTF8Bytes/4)}); err != nil {
		t.Fatalf("four-byte UTF-8 script at exact byte limit rejected: %v", err)
	}
	if err := p003ValidateRequestLimits(map[string]any{"script": strings.Repeat("🧪", domain.MaxScriptUTF8Bytes/4+1)}); err == nil {
		t.Fatal("four-byte UTF-8 script above the byte limit was accepted")
	}
	bodyAtLimit := map[string]any{"environment": "linux-dev", "execution_target": map[string]any{"kind": "remote", "profile": "linux-host"}, "policy": map[string]any{"opaque": ""}}
	emptyBody, err := json.Marshal(bodyAtLimit)
	if err != nil {
		t.Fatal(err)
	}
	bodyAtLimit["policy"].(map[string]any)["opaque"] = strings.Repeat("x", domain.MaxSerializedRequestBytes-len(emptyBody))
	if err := p003ValidateRequestLimits(bodyAtLimit); err != nil {
		t.Fatalf("serialized request at exact 1 MiB limit rejected: %v", err)
	}
	bodyAtLimit["policy"].(map[string]any)["opaque"] = strings.Repeat("x", domain.MaxSerializedRequestBytes+1-len(emptyBody))
	if err := p003ValidateRequestLimits(bodyAtLimit); err == nil {
		t.Fatal("serialized request above the 1 MiB limit was accepted")
	}
}

func containsP003(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestP003EventHistoryUnavailableUsesSharedErrorEnvelope(t *testing.T) {
	document := p003OpenAPIDoc(t)
	compiler := p003CompileSchemas(t, document)
	operation := p003OperationAt(t, document, "get", "/v1/commands/{command_id}/events")
	ref := p003ResponseSchemaRef(t, document, operation, "410", "application/json")
	sharedError := p003CompileRef(t, compiler, ref)
	data, err := os.ReadFile(filepath.Join("..", "domain", "schemas", "v1", "fixtures", "valid", "error--event-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	value := p003Decode(t, data)
	if err := sharedError.Validate(value); err != nil {
		t.Fatalf("event-history response fixture rejected: %v", err)
	}
	errorObject := p003Object(t, value, "event-history error")
	if errorObject["code"] != "event_history_unavailable" || p003Object(t, errorObject["details"], "error details")["output_complete"] != false {
		t.Fatalf("event-history response must report unavailable output: %#v", value)
	}
}

func TestP003NDJSONEventSchemaIsTheP002CommandEvent(t *testing.T) {
	document := p003OpenAPIDoc(t)
	compiler := p003CompileSchemas(t, document)
	eventResponse := p003Object(t, p003Ref(t, document, p003Object(t, p003Object(t, p003OperationAt(t, document, "get", "/v1/commands/{command_id}/events")["responses"], "event responses")["200"], "200 response")), "events response")
	content := p003Object(t, eventResponse["content"], "events content")
	ndjson := p003Object(t, content["application/x-ndjson"], "NDJSON media type")
	ref := p003Object(t, ndjson["schema"], "NDJSON event schema")["$ref"].(string)
	eventSchema := p003CompileRef(t, compiler, ref)
	_, sourceFile, _, _ := runtime.Caller(0)
	eventPath := path.Join(path.Dir(sourceFile), "..", "domain", "schemas", "v1", "fixtures", "valid", "command-event--stdout.json")
	data, err := os.ReadFile(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := eventSchema.Validate(p003Decode(t, data)); err != nil {
		t.Fatalf("P002 event fixture rejected by the API stream schema: %v", err)
	}
}
