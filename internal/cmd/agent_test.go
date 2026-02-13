package cmd

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/otterscale/otterscale-agent/internal/config"
)

type fakeTunnelSession struct {
	startErr error
	waitErr  error
	waitFn   func() error
}

func (f *fakeTunnelSession) Start(context.Context) error {
	return f.startErr
}

func (f *fakeTunnelSession) Wait() error {
	if f.waitFn != nil {
		return f.waitFn()
	}
	return f.waitErr
}

func (f *fakeTunnelSession) Close() error {
	return nil
}

func TestIsAuthFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "auth failed", err: errors.New("Authentication failed"), want: true},
		{name: "unable authenticate", err: errors.New("ssh: unable to authenticate"), want: true},
		{name: "unauthorized", err: errors.New("401 unauthorized"), want: true},
		{name: "other error", err: errors.New("connection reset by peer"), want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isAuthFailure(tc.err)
			if got != tc.want {
				t.Fatalf("isAuthFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRunTunnelSessionManagerWithDepsReregistersOnAuthFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerCalls := 0
	register := func(context.Context, *config.Config) (registrationResult, error) {
		registerCalls++
		return registrationResult{
			AgentID:     "agent-1",
			Auth:        "agent-1:token",
			Fingerprint: "fingerprint",
			Endpoint:    fmt.Sprintf("127.0.0.%d:16598", registerCalls),
		}, nil
	}

	newClientCalls := 0
	newClient := func(*config.Config, int, registrationResult) (tunnelSession, error) {
		newClientCalls++
		if newClientCalls == 1 {
			return &fakeTunnelSession{
				waitErr: errors.New("ssh: handshake failed: unable to authenticate"),
			}, nil
		}
		return &fakeTunnelSession{
			waitFn: func() error {
				cancel()
				return nil
			},
		}, nil
	}

	err := runTunnelSessionManagerWithDeps(ctx, nil, 18080, "http://127.0.0.1:8300", register, newClient)
	if err != nil {
		t.Fatalf("runTunnelSessionManagerWithDeps returned error: %v", err)
	}

	if registerCalls < 2 {
		t.Fatalf("expected re-register after auth failure, got %d register calls", registerCalls)
	}
	if newClientCalls < 2 {
		t.Fatalf("expected tunnel client recreation after auth failure, got %d client creations", newClientCalls)
	}
}

func TestNextRetryDelay(t *testing.T) {
	t.Parallel()

	if got := nextRetryDelay(0); got != sessionRetryBaseDelay {
		t.Fatalf("nextRetryDelay(0) = %s, want %s", got, sessionRetryBaseDelay)
	}

	if got := nextRetryDelay(time.Second); got != 2*time.Second {
		t.Fatalf("nextRetryDelay(1s) = %s, want 2s", got)
	}

	if got := nextRetryDelay(sessionRetryMaxDelay); got != sessionRetryMaxDelay {
		t.Fatalf("nextRetryDelay(max) = %s, want %s", got, sessionRetryMaxDelay)
	}
}

