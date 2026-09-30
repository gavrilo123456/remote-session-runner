package mailboxclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestP104FileOnlyClientPublishesRequestReadsFrozenEventsAndWritesExactAck(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	for _, directory := range []string{"", "inbox", "outbox", "events", "acks"} {
		path := root
		if directory != "" {
			path = filepath.Join(root, directory)
		}
		if err := os.Mkdir(path, directoryMode); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"request_id":"req-p104-client","operation":"get_command","command_id":"cmd-p104-client"}`)
	if err := client.WriteRequest("req-p104-client", request); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".json", ".ready"} {
		info, err := os.Stat(filepath.Join(root, "inbox", "req-p104-client"+suffix))
		if err != nil || info.Mode().Perm() != fileMode {
			t.Fatalf("request pair %s info=%v err=%v", suffix, info, err)
		}
	}
	if marker, err := os.ReadFile(filepath.Join(root, "inbox", "req-p104-client.ready")); err != nil || len(marker) != 0 {
		t.Fatalf("ready marker=%q err=%v", marker, err)
	}

	cursor := int64(2)
	responseBytes := []byte(`{"request_id":"req-p104-client","operation":"get_command","request_state":"complete","response_revision":3,"command_id":"cmd-p104-client","available_event_sequence":2,"output_complete":false,"events_file":"events/cmd-p104-client.ndjson"}`)
	if err := os.WriteFile(filepath.Join(root, "outbox", "req-p104-client.json"), responseBytes, fileMode); err != nil {
		t.Fatal(err)
	}
	response, err := client.WaitResponse(context.Background(), "req-p104-client")
	if err != nil || response.ResponseRevision != 3 || response.OutputComplete == nil || *response.OutputComplete {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	eventPath := filepath.Join(root, "events", "cmd-p104-client.ndjson")
	events := "{\"command_id\":\"cmd-p104-client\",\"sequence\":1,\"type\":\"stdout\",\"encoding\":\"utf8\",\"text\":\"prefix\\n\",\"byte_count\":7}\n" +
		"{\"command_id\":\"cmd-p104-client\",\"sequence\":2,\"type\":\"command_started\"}\n" +
		"{\"command_id\":\"cmd-p104-client\",\"sequence\":3,\"type\":\"stdout\",\"encoding\":\"utf8\",\"text\":\"tail\\n\",\"byte_count\":5}\n"
	if err := os.WriteFile(eventPath, []byte(events), fileMode); err != nil {
		t.Fatal(err)
	}
	read, err := client.ReadEventsThroughCursor(response)
	if err != nil || len(read) != 2 || read[0].Sequence != 1 || read[1].Sequence != 2 {
		t.Fatalf("events=%+v err=%v, want only sequences through cursor 2", read, err)
	}
	if err := client.WriteAcknowledgment("req-p104-client", response); err != nil {
		t.Fatal(err)
	}
	ackBytes, err := os.ReadFile(filepath.Join(root, "acks", "req-p104-client.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ack Acknowledgment
	if err := json.Unmarshal(ackBytes, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.RequestID != "req-p104-client" || ack.ResponseRevision != 3 || ack.AvailableEventSequence == nil || *ack.AvailableEventSequence != cursor {
		t.Fatalf("ACK=%+v, want exact revision 3 and cursor %d", ack, cursor)
	}
}

func TestP104FileOnlyClientRejectsMismatchedIDsAndUnsafeEventReferences(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	for _, directory := range []string{"", "inbox", "events"} {
		path := root
		if directory != "" {
			path = filepath.Join(root, directory)
		}
		if err := os.Mkdir(path, directoryMode); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.WriteRequest("req-p104-a", []byte(`{"request_id":"req-p104-b"}`)); err == nil {
		t.Fatal("request ID mismatch was accepted")
	}
	zero := int64(0)
	empty, err := client.ReadEventsThroughCursor(Response{RequestID: "req-p104-a", CommandID: "cmd-p104-a", AvailableEventSequence: &zero})
	if err != nil || len(empty) != 0 {
		t.Fatalf("zero event cursor=%v err=%v, want an empty event prefix", empty, err)
	}
	cursor := int64(1)
	for _, response := range []Response{
		{RequestID: "req-p104-a", CommandID: "cmd-p104-a", AvailableEventSequence: &cursor, EventsFile: "events/../escape.ndjson"},
		{RequestID: "req-p104-a", CommandID: "cmd-p104-a", AvailableEventSequence: &cursor, EventsFile: "events/cmd-p104-b.ndjson"},
	} {
		if _, err := client.ReadEventsThroughCursor(response); err == nil {
			t.Errorf("unsafe event reference accepted: %+v", response)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := client.WaitResponse(ctx, "req-p104-missing"); err == nil {
		t.Fatal("missing response wait unexpectedly succeeded")
	}
}

func TestP155ClientDecodesSelectionAndTerminalRunFields(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mailbox")
	for _, directory := range []string{"", "inbox", "outbox", "events", "acks"} {
		path := root
		if directory != "" {
			path = filepath.Join(root, directory)
		}
		if err := os.Mkdir(path, directoryMode); err != nil {
			t.Fatal(err)
		}
	}
	client, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	responseBytes := []byte(`{"inbox_id":"analytics","request_id":"req-p155-client","operation":"run","request_state":"complete","response_revision":4,"job_id":"job-p155-client","job_phase":"complete","session_id":"sess-p155-client","command_id":"cmd-p155-client","delivery_state":"accepted","command_state":"succeeded","exit_code":0,"stdout":"P155_OK\n","final_event_sequence":4,"available_event_sequence":4,"output_complete":true,"output_truncated":false,"events_file":"events/cmd-p155-client.ndjson","teardown_outcome":"closed","execution_selection_source":"request_override","resolved_environment":"linux-dev","resolved_execution_target":{"kind":"remote","profile":"linux-host"}}`)
	if err := os.WriteFile(filepath.Join(root, "outbox", "req-p155-client.json"), responseBytes, fileMode); err != nil {
		t.Fatal(err)
	}
	response, err := client.WaitResponse(context.Background(), "req-p155-client")
	if err != nil {
		t.Fatal(err)
	}
	if response.InboxID != "analytics" || response.JobID != "job-p155-client" || response.JobPhase != "complete" ||
		response.DeliveryState != "accepted" || response.CommandState != "succeeded" || response.ExitCode == nil || *response.ExitCode != 0 ||
		response.OutputComplete == nil || !*response.OutputComplete || response.OutputTruncated == nil || *response.OutputTruncated ||
		response.ExecutionSelectionSource != "request_override" || response.ResolvedEnvironment != "linux-dev" ||
		response.ResolvedExecutionTarget == nil || response.ResolvedExecutionTarget.Kind != "remote" || response.ResolvedExecutionTarget.Profile != "linux-host" ||
		response.TeardownOutcome != "closed" || response.FinalEventSequence == nil || *response.FinalEventSequence != 4 || response.Stdout != "P155_OK\n" {
		t.Fatalf("decoded response=%+v", response)
	}
}
