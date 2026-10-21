package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func TestMailboxIngressDefaultsRemainEnabled(t *testing.T) {
	fixtures := map[string][]byte{
		"v1": []byte(macConfigFixture),
		"v2": []byte(macV2ConfigFixture),
	}
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		document := simpleRuntimeDocument(t, simpleRuntimeMacBindings(t, lane))
		fixtures["v3/"+string(lane)] = mailboxHandoffYAML(t, document)
	}
	for name, contents := range fixtures {
		t.Run(name, func(t *testing.T) {
			loaded, err := parse(contents)
			if err != nil {
				t.Fatal(err)
			}
			settings, ok := loaded.MacSettings()
			if !ok || !settings.MailboxIngressEnabled || settings.MailboxHandoffReceipt != "" {
				t.Fatalf("legacy ingress defaults changed: %+v", settings)
			}
			if _, ok := loaded.Mailbox("default"); !ok {
				t.Fatal("default inbox disappeared without an explicit disable")
			}
		})
	}
}

func TestMailboxIngressDisableRequiresExplicitFalseAndEmptyRegistry(t *testing.T) {
	for _, version := range []string{"v2", "v3/dev", "v3/qas", "v3/prd"} {
		t.Run(version, func(t *testing.T) {
			newDocument := func() fileDocument {
				if version == "v2" {
					var document fileDocument
					if err := yaml.Unmarshal([]byte(macV2ConfigFixture), &document); err != nil {
						t.Fatal(err)
					}
					// The fixture's explicit deadline equals the default. Omit
					// its parse-only duration value when marshaling this mutation.
					document.Mac.ReconciliationDeadline = nil
					return document
				}
				return simpleRuntimeDocument(t, simpleRuntimeMacBindings(t, ReleaseLane(strings.TrimPrefix(version, "v3/"))))
			}
			baseline, err := parse(mailboxHandoffYAML(t, newDocument()))
			if err != nil {
				t.Fatal(err)
			}
			disabled := newDocument()
			value := false
			disabled.Mac.MailboxIngressEnabled = &value
			disabled.Mailboxes = map[string]mailboxDocument{}
			loaded, err := parse(mailboxHandoffYAML(t, disabled))
			if err != nil {
				t.Fatalf("explicit empty disabled ingress rejected: %v", err)
			}
			settings, _ := loaded.MacSettings()
			if settings.MailboxIngressEnabled || len(loaded.MailboxNames()) != 0 ||
				!reflect.DeepEqual(loaded.ExecutionContextNames(), baseline.ExecutionContextNames()) ||
				!reflect.DeepEqual(loaded.RemoteHostNames(), baseline.RemoteHostNames()) ||
				!reflect.DeepEqual(loaded.EnvironmentNames(), baseline.EnvironmentNames()) ||
				!reflect.DeepEqual(loaded.DirectEndpointNames(), baseline.DirectEndpointNames()) {
				t.Fatal("disabling ingress changed unrelated execution or route configuration")
			}
			for name, mutate := range map[string]func(*fileDocument){
				"omitted flag with empty registry": func(document *fileDocument) { document.Mailboxes = nil },
				"enabled with empty registry": func(document *fileDocument) {
					value := true
					document.Mac.MailboxIngressEnabled = &value
					document.Mailboxes = nil
				},
				"disabled with existing registry": func(document *fileDocument) {
					value := false
					document.Mac.MailboxIngressEnabled = &value
				},
			} {
				t.Run(name, func(t *testing.T) {
					document := newDocument()
					mutate(&document)
					if _, err := parse(mailboxHandoffYAML(t, document)); err == nil {
						t.Fatal("ambiguous ingress configuration accepted")
					}
				})
			}
		})
	}
}

func TestMailboxHandoffQASExternalRootsRequireOwnCanonicalReceipt(t *testing.T) {
	document := qasMailboxHandoffDocument(t)
	contents := mailboxHandoffYAML(t, document)
	loaded, err := parse(contents)
	if err != nil {
		t.Fatalf("lexically valid QAS receipt reference rejected: %v", err)
	}
	settings, _ := loaded.MacSettings()
	if !settings.MailboxIngressEnabled || settings.MailboxHandoffReceipt != document.Mac.MailboxHandoffReceipt {
		t.Fatalf("handoff settings were not exposed: %+v", settings)
	}
	if mailbox, ok := loaded.Mailbox("prd-default"); !ok || mailbox.Root != filepath.Join(MacServiceRoot, "mailbox") {
		t.Fatalf("preserved PRD default was not exposed under its distinct ID: %+v", mailbox)
	}
	if mailbox, _ := loaded.Mailbox("default"); mailbox.Root != filepath.Join(MacServiceRoot+"-qas", "mailbox") {
		t.Fatal("QAS default moved outside its lane")
	}
	// No receipt file is created. Lexical parsing cannot stand in for the
	// runtime's exact receipt/ownership validation or cause filesystem access.
	for name, path := range map[string]string{
		"missing":                     "",
		"relative":                    "staging/mailbox-handoffs/" + strings.Repeat("a", 32) + ".json",
		"peer dev":                    filepath.Join(MacServiceRoot+"-dev", "staging/mailbox-handoffs", strings.Repeat("a", 32)+".json"),
		"peer prd":                    filepath.Join(MacServiceRoot, "staging/mailbox-handoffs", strings.Repeat("a", 32)+".json"),
		"wrong selected subdirectory": filepath.Join(MacServiceRoot+"-qas", "staging/other", strings.Repeat("a", 32)+".json"),
		"nested":                      filepath.Join(MacServiceRoot+"-qas", "staging/mailbox-handoffs/nested", strings.Repeat("a", 32)+".json"),
		"traversal":                   MacServiceRoot + "-qas/staging/other/../mailbox-handoffs/" + strings.Repeat("a", 32) + ".json",
		"uppercase identity":          filepath.Join(MacServiceRoot+"-qas", "staging/mailbox-handoffs", strings.Repeat("A", 32)+".json"),
		"short identity":              filepath.Join(MacServiceRoot+"-qas", "staging/mailbox-handoffs", strings.Repeat("a", 31)+".json"),
		"wrong extension":             filepath.Join(MacServiceRoot+"-qas", "staging/mailbox-handoffs", strings.Repeat("a", 32)+".yaml"),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := qasMailboxHandoffDocument(t)
			candidate.Mac.MailboxHandoffReceipt = path
			if _, err := parse(mailboxHandoffYAML(t, candidate)); err == nil {
				t.Fatal("external root accepted without the exact QAS receipt reference")
			}
		})
	}
}

func TestMailboxHandoffDoesNotRelaxOtherRootOrLaneGuards(t *testing.T) {
	for name, mutate := range map[string]func(*fileDocument){
		"external default": func(document *fileDocument) {
			mailbox := document.Mailboxes["default"]
			mailbox.Root = "/Users/tomasz.walczuk/projects/example/runner/mailbox-"
			document.Mailboxes["default"] = mailbox
		},
		"unapproved internal root": func(document *fileDocument) {
			mailbox := document.Mailboxes["prd-default"]
			mailbox.Root = filepath.Join(document.Mac.ServiceRoot, "unapproved")
			document.Mailboxes["prd-default"] = mailbox
		},
		"duplicate root": func(document *fileDocument) { document.Mailboxes["duplicate"] = document.Mailboxes["prd-default"] },
		"nested root": func(document *fileDocument) {
			mailbox := document.Mailboxes["prd-default"]
			mailbox.Root += "/nested"
			document.Mailboxes["nested"] = mailbox
		},
		"normalized alias": func(document *fileDocument) {
			mailbox := document.Mailboxes["prd-default"]
			mailbox.Root = strings.ToLower(mailbox.Root)
			document.Mailboxes["alias"] = mailbox
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := qasMailboxHandoffDocument(t)
			mutate(&document)
			if _, err := parse(mailboxHandoffYAML(t, document)); err == nil {
				t.Fatal("handoff reference bypassed an unrelated mailbox guard")
			}
		})
	}
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLanePRD} {
		t.Run(string(lane), func(t *testing.T) {
			document := simpleRuntimeDocument(t, simpleRuntimeMacBindings(t, lane))
			document.Mac.MailboxHandoffReceipt = filepath.Join(document.Mac.ServiceRoot, "staging/mailbox-handoffs", strings.Repeat("b", 32)+".json")
			if _, err := parse(mailboxHandoffYAML(t, document)); err == nil {
				t.Fatal("non-QAS lane accepted a handoff receipt")
			}
		})
	}
}

func TestMailboxHandoffLegacyAndIngressFieldTypesStayRestricted(t *testing.T) {
	for _, flag := range []string{"mailbox_ingress_enabled: false", "mailbox_ingress_enabled: true", "mailbox_handoff_receipt: /tmp/receipt.json"} {
		contents := strings.Replace(macConfigFixture, "mac:\n", "mac:\n  "+flag+"\n", 1)
		if _, err := parse([]byte(contents)); err == nil {
			t.Fatalf("V1 accepted new mailbox setting %q", flag)
		}
	}
	v2 := strings.Replace(macV2ConfigFixture, "mac:\n", "mac:\n  mailbox_handoff_receipt: /tmp/receipt.json\n", 1)
	if _, err := parse([]byte(v2)); err == nil {
		t.Fatal("V2 accepted a handoff receipt")
	}
	for _, value := range []string{`"false"`, "0", "[]"} {
		v2 := strings.Replace(macV2ConfigFixture, "mac:\n", "mac:\n  mailbox_ingress_enabled: "+value+"\n", 1)
		if _, err := parse([]byte(v2)); err == nil {
			t.Fatalf("non-boolean ingress flag %s accepted", value)
		}
	}
}

func TestMailboxHandoffRuntimeSchemaExposesOnlyMacSettings(t *testing.T) {
	contents, err := os.ReadFile("schemas/v3/runtime.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Type    string `json:"type"`
				Default *bool  `json:"default"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatal(err)
	}
	flag := schema.Defs["mac"].Properties["mailbox_ingress_enabled"]
	if flag.Type != "boolean" || flag.Default == nil || !*flag.Default || schema.Defs["mac"].Properties["mailbox_handoff_receipt"].Type != "string" {
		t.Fatal("Mac schema does not declare the mailbox ingress and receipt fields")
	}
	for _, key := range []string{"mailbox_ingress_enabled", "mailbox_handoff_receipt"} {
		if _, exists := schema.Defs["linux"].Properties[key]; exists {
			t.Fatalf("Mac-only setting %q leaked into Linux schema", key)
		}
	}
}

func TestMailboxHandoffConfigSourceHashBindsExactLoadedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mac.yaml")
	contents := []byte(macV2ConfigFixture)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(contents))
	if loaded.SourceSHA256() != want {
		t.Fatal("configuration hash did not bind the exact validated bytes")
	}
	contents = append(contents, []byte("\n# new byte snapshot with unchanged policies\n")...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SourceSHA256() != want || reloaded.SourceSHA256() == want ||
		reloaded.SourceSHA256() != fmt.Sprintf("%x", sha256.Sum256(contents)) {
		t.Fatal("configuration source digest followed a later file or ignored changed bytes")
	}
}

func TestMailboxHandoffSchemaEmptyRegistryRequiresExplicitDisable(t *testing.T) {
	contents, err := os.ReadFile("schemas/v3/runtime.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	// Exercise the published Mac host-variant branch itself, independently of
	// unrelated endpoint and certificate fixtures required by the full schema.
	fragment := map[string]any{
		"$schema": document["$schema"],
		"allOf":   []any{document["oneOf"].([]any)[0]},
	}
	compiler := jsonschema.NewCompiler()
	const resource = "https://remote-session-runner.invalid/tests/mailbox-host-variant.json"
	if err := compiler.AddResource(resource, fragment); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, json string
		valid      bool
	}{
		{"omitted ingress with default", `{"mac":{},"mailboxes":{"default":{}}}`, true},
		{"omitted ingress without mailboxes", `{"mac":{}}`, false},
		{"omitted ingress with empty mailboxes", `{"mac":{},"mailboxes":{}}`, false},
		{"disabled with omitted mailboxes", `{"mac":{"mailbox_ingress_enabled":false}}`, true},
		{"disabled with empty mailboxes", `{"mac":{"mailbox_ingress_enabled":false},"mailboxes":{}}`, true},
		{"disabled with existing mailbox", `{"mac":{"mailbox_ingress_enabled":false},"mailboxes":{"default":{}}}`, false},
		{"enabled without mailboxes", `{"mac":{"mailbox_ingress_enabled":true}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var candidate any
			if err := json.Unmarshal([]byte(test.json), &candidate); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(candidate); (err == nil) != test.valid {
				t.Fatalf("schema result=%v, want accepted=%t", err, test.valid)
			}
		})
	}
}

func mailboxHandoffYAML(t *testing.T, document fileDocument) []byte {
	t.Helper()
	contents, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func qasMailboxHandoffDocument(t *testing.T) fileDocument {
	t.Helper()
	document := simpleRuntimeDocument(t, simpleRuntimeMacBindings(t, ReleaseLaneQAS))
	document.Mac.MailboxHandoffReceipt = filepath.Join(document.Mac.ServiceRoot, "staging/mailbox-handoffs", strings.Repeat("a", 32)+".json")
	mailbox := document.Mailboxes["default"]
	mailbox.Root = filepath.Join(MacServiceRoot, "mailbox")
	document.Mailboxes["prd-default"] = mailbox
	return document
}
