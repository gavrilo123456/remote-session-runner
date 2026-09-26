package localapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP063CreateReadSessionLocalIntentForBothTargets(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		body   string
		target string
		source string
	}{
		{name: "local", body: `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"}}`, target: "local", source: "empty"},
		{name: "queued remote", body: `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`, target: "remote", source: "empty"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, authority, db, client := p063Server(t)
			request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions", strings.NewReader(fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Idempotency-Key", "p063-"+fixture.name)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				data, _ := io.ReadAll(response.Body)
				t.Fatalf("create status = %d, body = %s", response.StatusCode, data)
			}
			var accepted struct {
				ResourceID      string `json:"resource_id"`
				SessionID       string `json:"session_id"`
				IntentID        string `json:"intent_id"`
				AcceptanceScope string `json:"acceptance_scope"`
				ExecutionTarget struct {
					Kind string `json:"kind"`
				} `json:"execution_target"`
				KnownState struct {
					DeliveryState string `json:"delivery_state"`
				} `json:"known_state"`
			}
			if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
				t.Fatal(err)
			}
			if accepted.ResourceID == "" || accepted.ResourceID != accepted.SessionID || accepted.IntentID == "" || accepted.AcceptanceScope != "local_intent" || accepted.ExecutionTarget.Kind != fixture.target || accepted.KnownState.DeliveryState != "recorded" {
				t.Fatalf("acceptance = %+v", accepted)
			}
			record, err := authority.GetLocalIntentByResource(context.Background(), "create_session", accepted.SessionID, p063Owner(t))
			if err != nil {
				t.Fatal(err)
			}
			if string(record.IntentID) == "" || record.DeliveryState != store.LocalIntentRecorded || string(record.Source.Mode()) != fixture.source {
				t.Fatalf("durable record = %+v", record)
			}
			getResponse, err := client.Get("http://local/v1/sessions/" + accepted.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer getResponse.Body.Close()
			if getResponse.StatusCode != http.StatusOK {
				data, _ := io.ReadAll(getResponse.Body)
				t.Fatalf("read status = %d, body = %s", getResponse.StatusCode, data)
			}
			var read struct {
				View     string `json:"view"`
				IsStale  bool   `json:"is_stale"`
				Resource struct {
					SessionID     string `json:"session_id"`
					DeliveryState string `json:"delivery_state"`
					Source        struct {
						Mode string `json:"mode"`
					} `json:"source"`
					Target struct {
						Kind string `json:"kind"`
					} `json:"execution_target"`
				} `json:"resource"`
			}
			if err := json.NewDecoder(getResponse.Body).Decode(&read); err != nil {
				t.Fatal(err)
			}
			if read.View != "local_intent" || read.IsStale || read.Resource.SessionID != accepted.SessionID || read.Resource.DeliveryState != "recorded" || read.Resource.Source.Mode != fixture.source || read.Resource.Target.Kind != fixture.target {
				t.Fatalf("read = %+v", read)
			}
			var authorityRows int
			if err := db.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&authorityRows); err != nil {
				t.Fatal(err)
			}
			if authorityRows != 0 {
				t.Fatalf("local API created %d authoritative sessions", authorityRows)
			}
		})
	}
}

func TestP063OversizeCreateIsRejectedBeforeIntentInsert(t *testing.T) {
	server, _, db, client := p063Server(t)
	padding := strings.Repeat("x", int(domain.MaxSerializedRequestBytes))
	body := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"policy":{"padding":"` + padding + `"}}`
	request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "p063-oversize")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("oversize status = %d, body = %s", response.StatusCode, data)
	}
	var intents int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 0 {
		t.Fatalf("oversize request inserted %d local intents", intents)
	}
	_ = server
}

func TestP063IdempotencyAndRequestValidation(t *testing.T) {
	_, _, db, client := p063Server(t)
	body := `{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`
	create := func(key, requestBody string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, "http://local/v1/sessions", strings.NewReader(requestBody))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Idempotency-Key", key)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := create("p063-idempotent", body)
	firstBytes, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first status = %d, body = %s", first.StatusCode, firstBytes)
	}
	var firstValue struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(firstBytes, &firstValue); err != nil {
		t.Fatal(err)
	}
	retry := create("p063-idempotent", body)
	retryBytes, _ := io.ReadAll(retry.Body)
	retry.Body.Close()
	if retry.StatusCode != http.StatusAccepted {
		t.Fatalf("retry status = %d, body = %s", retry.StatusCode, retryBytes)
	}
	var retryValue struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(retryBytes, &retryValue); err != nil || retryValue.SessionID != firstValue.SessionID {
		t.Fatalf("retry response = %s", retryBytes)
	}
	conflict := create("p063-idempotent", `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}`)
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		data, _ := io.ReadAll(conflict.Body)
		t.Fatalf("changed idempotency status = %d, body = %s", conflict.StatusCode, data)
	}
	invalid := create("p063-invalid", `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"local_worktree","path":"/tmp/work","portable":false}}`)
	defer invalid.Body.Close()
	if invalid.StatusCode != http.StatusUnprocessableEntity {
		data, _ := io.ReadAll(invalid.Body)
		t.Fatalf("remote local-worktree status = %d, body = %s", invalid.StatusCode, data)
	}
	var intents int
	if err := db.QueryRow("SELECT COUNT(*) FROM local_intents").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 1 {
		t.Fatalf("validation/conflict changed intent count = %d, want 1", intents)
	}
}

func TestP063OwnerOnlySocketAndNoSSHHandlerImport(t *testing.T) {
	server, _, _, _ := p063Server(t)
	info, err := os.Stat(server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(server.SocketPath()))
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("socket parent mode = %04o, must exclude group/other", got)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve localapi package path")
	}
	entries, err := os.ReadDir(filepath.Dir(sourceFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(filepath.Dir(sourceFile), entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			if strings.Contains(strings.ToLower(imported.Path.Value), "ssh") {
				t.Fatalf("localapi handler imports remote SSH package %s", imported.Path.Value)
			}
		}
	}
}

func p063Server(t *testing.T) (*Server, *store.AuthorityStore, *sql.DB, *http.Client) {
	t.Helper()
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "local.db")
	db, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "rsr-p063-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := NewServer(ServerOptions{Authority: authority, Owner: p063Owner(t), SocketPath: filepath.Join(runDir, "local-api.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	go func() { _ = server.Serve() }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", server.SocketPath())
	}}
	return server, authority, db, &http.Client{Transport: transport}
}

func p063Owner(t *testing.T) domain.ControllerIdentity {
	t.Helper()
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
