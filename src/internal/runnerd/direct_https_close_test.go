package runnerd

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestDirectHTTPSServerConcurrentCloseIsIdempotent(t *testing.T) {
	pki := newP106TestPKI(t)
	options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
	options.Handler = http.NotFoundHandler()
	server, err := NewDirectHTTPSServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()

	const closeCallers = 8
	var callers sync.WaitGroup
	closeErrors := make(chan error, closeCallers)
	for range closeCallers {
		callers.Add(1)
		go func() {
			defer callers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			closeErrors <- server.Close(ctx)
		}()
	}
	callers.Wait()
	close(closeErrors)
	for err := range closeErrors {
		if err != nil {
			t.Errorf("concurrent server close: %v", err)
		}
	}
	if err := <-serveResult; err != nil {
		t.Fatalf("serve after concurrent close: %v", err)
	}
}
