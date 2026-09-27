package dispatcher

import (
	"context"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/store"
)

type p105CredentialLoader struct{ calls int }

func (l *p105CredentialLoader) Load() (string, error) {
	l.calls++
	return "test-only-credential-handle", nil
}

type p105RemoteDialer struct{ calls int }

func (d *p105RemoteDialer) Dial(_ context.Context, credentialHandle string, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	d.calls++
	if credentialHandle != "test-only-credential-handle" {
		return sshbridge.ReplyFrame{}, ErrRemoteDriverConfiguration
	}
	return p069AcceptedReply(frame), nil
}

type p105InstrumentedRemoteCaller struct {
	credentials *p105CredentialLoader
	dialer      *p105RemoteDialer
}

func (c *p105InstrumentedRemoteCaller) Call(ctx context.Context, frame sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	credentialHandle, err := c.credentials.Load()
	if err != nil {
		return sshbridge.ReplyFrame{}, err
	}
	return c.dialer.Dial(ctx, credentialHandle, frame)
}

func TestP105I06RouterAloneLoadsCredentialsAndDialsRemote(t *testing.T) {
	authority := p068Authority(t)
	remoteIntent := p068SubmitIntent(t, "intent-p105-remote", "session-p105-remote", "command-p105-remote", domain.TargetKindRemote, "printf remote")
	localIntent := p068SubmitIntent(t, "intent-p105-local", "session-p105-local", "command-p105-local", domain.TargetKindLocal, "printf local")
	for _, intent := range []store.LocalIntentCreate{remoteIntent, localIntent} {
		if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
			t.Fatal(err)
		}
	}

	credentials := &p105CredentialLoader{}
	dialer := &p105RemoteDialer{}
	remoteDriver, err := NewRemoteDriver(authority, &p105InstrumentedRemoteCaller{credentials: credentials, dialer: dialer}, "router-p105", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	remoteRecord, _, err := remoteDriver.DispatchIntent(context.Background(), remoteIntent.IntentID)
	if err != nil || remoteRecord.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("remote Router dispatch record=%+v err=%v", remoteRecord, err)
	}
	if credentials.calls != 1 || dialer.calls != 1 {
		t.Fatalf("remote Router dependency calls: credential_loads=%d remote_dials=%d, want 1 each", credentials.calls, dialer.calls)
	}

	localDriver, err := NewLocalDriver(authority, &p068FakeAcceptor{operation: "submit_command"}, "router-p105", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	localRecord, _, err := localDriver.DispatchIntent(context.Background(), localIntent.IntentID)
	if err != nil || localRecord.DeliveryState != store.LocalIntentAccepted {
		t.Fatalf("local Router dispatch record=%+v err=%v", localRecord, err)
	}
	if credentials.calls != 1 || dialer.calls != 1 {
		t.Fatalf("local Router dispatch touched remote dependencies: credential_loads=%d remote_dials=%d", credentials.calls, dialer.calls)
	}
}

var _ RemoteCaller = (*p105InstrumentedRemoteCaller)(nil)
