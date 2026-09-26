package runnerd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type p048FakeRuntime struct {
	generation    string
	commandCalls  int
	cancelCalls   int
	stopCalls     int
	stopConfirmed bool
}

func (r *p048FakeRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *p048FakeRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (*p048FakeRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (r *p048FakeRuntime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.commandCalls++
	return execution.RuntimeCommandResult{Stdout: []byte("p048-output\n"), ExitCode: 0}, nil
}

func (r *p048FakeRuntime) CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	r.cancelCalls++
	return execution.RuntimeCommandStopResult{Confirmed: r.stopConfirmed}, nil
}

func (r *p048FakeRuntime) StopSession(context.Context, store.SessionRecord) (bool, error) {
	r.stopCalls++
	return r.stopConfirmed, nil
}

func TestP048PrivateCancelCloseRunGetJobAndReplay(t *testing.T) {
	runtimeAdapter := &p048FakeRuntime{generation: "p048-fake-generation", stopConfirmed: true}
	service, authority := newP046Service(t, runtimeAdapter)
	server, serveErr := p048StartServer(t, service)
	client := p046UnixClient(server.SocketPath())
	defer p048CloseServer(t, server, serveErr)

	createBody := []byte(`{"session_id":"p048-session","idempotency_key":"p048-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	create := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if create.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 create status = %d, body = %s", create.StatusCode, p046ReadBody(t, create))
	}
	_ = p046ReadBody(t, create)

	p048QueueCommand(t, authority, "p048-session", "p048-queued-command", "p048-queue-key")
	cancelBody := []byte(`{"idempotency_key":"p048-cancel","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
	cancel := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/commands/p048-queued-command/cancel", cancelBody)
	if cancel.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 cancel status = %d, body = %s", cancel.StatusCode, p046ReadBody(t, cancel))
	}
	var cancelled commandResponse
	p046DecodeJSON(t, cancel, &cancelled)
	if cancelled.CommandState != string(domain.CommandStateCancelled) || runtimeAdapter.cancelCalls != 0 {
		t.Fatalf("P048 cancel response = %+v, runtime cancel calls = %d", cancelled, runtimeAdapter.cancelCalls)
	}
	duplicateCancel := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/commands/p048-queued-command/cancel", cancelBody)
	if duplicateCancel.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 duplicate cancel status = %d, body = %s", duplicateCancel.StatusCode, p046ReadBody(t, duplicateCancel))
	}
	var duplicateCancelled commandResponse
	p046DecodeJSON(t, duplicateCancel, &duplicateCancelled)
	if !duplicateCancelled.Duplicate || duplicateCancelled.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("P048 duplicate cancel = %+v", duplicateCancelled)
	}

	closeBody := []byte(`{"idempotency_key":"p048-close","policy":"graceful","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
	closed := p046DoJSON(t, client, http.MethodDelete, "http://runnerd/internal/v1/sessions/p048-session", closeBody)
	if closed.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 close status = %d, body = %s", closed.StatusCode, p046ReadBody(t, closed))
	}
	var closedResource sessionResponse
	p046DecodeJSON(t, closed, &closedResource)
	if closedResource.SessionState != string(domain.SessionStateClosed) || runtimeAdapter.stopCalls != 1 {
		t.Fatalf("P048 close response = %+v, runtime stop calls = %d", closedResource, runtimeAdapter.stopCalls)
	}

	runBody := []byte(`{"job_id":"p048-job","session_id":"p048-job-session","command_id":"p048-job-command","idempotency_key":"p048-run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"},"script":"printf p048"}`)
	run := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/jobs", runBody)
	if run.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 run status = %d, body = %s", run.StatusCode, p046ReadBody(t, run))
	}
	var job jobResponse
	p046DecodeJSON(t, run, &job)
	if job.JobPhase != string(store.JobPhaseComplete) || job.CommandState == nil || *job.CommandState != string(domain.CommandStateSucceeded) || job.TeardownState != string(store.JobTeardownClosed) {
		t.Fatalf("P048 run response = %+v", job)
	}
	if runtimeAdapter.commandCalls != 1 || runtimeAdapter.stopCalls != 2 {
		t.Fatalf("P048 run runtime calls = command %d stop %d", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}

	read := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/jobs/p048-job?controller_type=direct_mtls&controller_id=tomasz.walczuk", nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("P048 get-job status = %d, body = %s", read.StatusCode, p046ReadBody(t, read))
	}
	var readJob jobResponse
	p046DecodeJSON(t, read, &readJob)
	if readJob.JobID != job.JobID || readJob.JobPhase != job.JobPhase || readJob.TeardownState != job.TeardownState {
		t.Fatalf("P048 get-job = %+v, run = %+v", readJob, job)
	}

	replay := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/jobs", runBody)
	if replay.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 run replay status = %d, body = %s", replay.StatusCode, p046ReadBody(t, replay))
	}
	_ = p046ReadBody(t, replay)
	if runtimeAdapter.commandCalls != 1 || runtimeAdapter.stopCalls != 2 {
		t.Fatalf("P048 run replay reran work: command %d stop %d", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}
}

func TestP048LinuxPrivateCancelCloseRunGetJobUsesUbuntuRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Linux host-process gate runs on Ubuntu")
	}
	fixture := testfixture.New(t)
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewLinuxProcessAdapter(hostruntime.LinuxRuntimeOptions{Account: hostruntime.LinuxHostAccount, WorkspaceRoot: workspaceRoot, ShellPath: "/usr/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAdapter, err := NewLinuxSessionRuntime(adapter)
	if err != nil {
		t.Fatal(err)
	}
	service, authority := newP046Service(t, runtimeAdapter)
	server, serveErr := p048StartServer(t, service)
	client := p046UnixClient(server.SocketPath())
	defer p048CloseServer(t, server, serveErr)

	createBody := []byte(`{"session_id":"p048-linux-session","idempotency_key":"p048-linux-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	create := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if create.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 Linux create status = %d, body = %s", create.StatusCode, p046ReadBody(t, create))
	}
	_ = p046ReadBody(t, create)
	session, err := authority.GetSession(context.Background(), "p048-linux-session")
	if err != nil {
		t.Fatal(err)
	}
	p048QueueCommandWithTimeout(t, authority, session, "p048-linux-queued-command", "p048-linux-queue-key")
	cancelBody := []byte(`{"idempotency_key":"p048-linux-cancel","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
	cancel := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/commands/p048-linux-queued-command/cancel", cancelBody)
	if cancel.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 Linux cancel status = %d, body = %s", cancel.StatusCode, p046ReadBody(t, cancel))
	}
	var cancelled commandResponse
	p046DecodeJSON(t, cancel, &cancelled)
	if cancelled.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("P048 Linux cancel response = %+v", cancelled)
	}

	closeBody := []byte(`{"idempotency_key":"p048-linux-close","policy":"graceful","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
	closed := p046DoJSON(t, client, http.MethodDelete, "http://runnerd/internal/v1/sessions/p048-linux-session", closeBody)
	if closed.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 Linux close status = %d, body = %s", closed.StatusCode, p046ReadBody(t, closed))
	}
	var closedResource sessionResponse
	p046DecodeJSON(t, closed, &closedResource)
	if closedResource.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("P048 Linux close response = %+v", closedResource)
	}

	runBody := []byte(`{"job_id":"p048-linux-job","session_id":"p048-linux-job-session","command_id":"p048-linux-job-command","idempotency_key":"p048-linux-run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"},"script":"printf p048-linux"}`)
	run := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/jobs", runBody)
	if run.StatusCode != http.StatusAccepted {
		t.Fatalf("P048 Linux run status = %d, body = %s", run.StatusCode, p046ReadBody(t, run))
	}
	var job jobResponse
	p046DecodeJSON(t, run, &job)
	if job.JobPhase != string(store.JobPhaseComplete) || job.CommandState == nil || *job.CommandState != string(domain.CommandStateSucceeded) || job.TeardownState != string(store.JobTeardownClosed) {
		t.Fatalf("P048 Linux run response = %+v", job)
	}
	read := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/jobs/p048-linux-job?controller_type=direct_mtls&controller_id=tomasz.walczuk", nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("P048 Linux get-job status = %d, body = %s", read.StatusCode, p046ReadBody(t, read))
	}
	var readJob jobResponse
	p046DecodeJSON(t, read, &readJob)
	if readJob.JobPhase != job.JobPhase || readJob.TeardownState != job.TeardownState {
		t.Fatalf("P048 Linux get-job = %+v, run = %+v", readJob, job)
	}
}

func p048StartServer(t *testing.T, service *execution.Service) (*PrivateServer, chan error) {
	t.Helper()
	socketPath := filepath.Join(p046SocketParent(t), "runnerd.sock")
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	return server, serveErr
}

func p048CloseServer(t *testing.T, server *PrivateServer, serveErr chan error) {
	t.Helper()
	if err := server.Close(context.Background()); err != nil {
		t.Error(err)
	}
	if err := <-serveErr; err != nil {
		t.Error(err)
	}
}

func p048QueueCommand(t *testing.T, authority *store.AuthorityStore, sessionID, commandID, key string) {
	t.Helper()
	session, err := authority.GetSession(context.Background(), domain.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	p048QueueCommandWithTimeout(t, authority, session, commandID, key)
}

func p048QueueCommandWithTimeout(t *testing.T, authority *store.AuthorityStore, session store.SessionRecord, commandID, key string) {
	t.Helper()
	controller := session.Controller
	script := "printf queued"
	raw := []byte(fmt.Sprintf(`{"operation":"submit_command","session_id":%q,"script":%q}`, session.SessionID, script))
	hash, err := domain.HashMutationRequestJSON("submit_command", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: domain.CommandID(commandID), SessionID: session.SessionID, RequestHash: hash,
		IdempotencyKey: key, IdempotencyRetention: time.Hour, Script: script, Timeout: session.Limits.CommandTimeout,
	})
	if err != nil || duplicate {
		t.Fatalf("P048 queue command duplicate=%v err=%v", duplicate, err)
	}
	_ = controller
}
