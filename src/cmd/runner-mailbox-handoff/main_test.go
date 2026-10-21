package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMailboxHandoffCLIRequiresExplicitPauseAndCanonicalOperation(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"prepare"}, {"prepare", "--owner", "owner"}, {"prepare", "--publishers-paused"}, {"stop", "--operation-file", "/not-an-operation"}, {"activate", "--operation-file", "/not-an-operation"}, {"restore", "--operation-file", "/not-an-operation"}, {"status"}} {
		var out, err bytes.Buffer
		if code := run(context.Background(), args, &out, &err); code != 2 || out.Len() != 0 || err.Len() == 0 {
			t.Fatalf("args=%v code=%d out=%q err=%q", args, code, out.String(), err.String())
		}
	}
}

func TestMailboxHandoffCLIVersionIdentifiesBuildWithoutOperations(t *testing.T) {
	var out, err bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &out, &err); code != 0 || !strings.Contains(out.String(), "build_revision=") || err.Len() != 0 {
		t.Fatalf("version code=%d out=%q err=%q", code, out.String(), err.String())
	}
}

func TestMailboxHandoffCLIHelpHasNoOperationalSideEffects(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		var out, err bytes.Buffer
		if code := run(context.Background(), []string{arg}, &out, &err); code != 0 || out.Len() == 0 || err.Len() != 0 {
			t.Fatalf("help code=%d out=%q err=%q", code, out.String(), err.String())
		}
	}
}
