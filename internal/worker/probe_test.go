package worker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// redirectTransport sends every request to addr regardless of the
// request's own host:port, mimicking how the real tunnel always lands on
// the worker's fixed 127.0.0.1:8080 no matter what the caller dials.
func redirectTransport(addr string) http.RoundTripper {
	return &http.Transport{
		DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
			return net.Dial(network, addr)
		},
	}
}

func newProbeClient(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	return Client{Transport: redirectTransport(addr), Key: "test-key", Alias: "qwen3.6-35b-a3b"}
}

func TestProbe(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    Readiness
		wantErr bool
	}{
		{
			name: "503 health is Loading",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(503)
				}
			},
			want: Loading,
		},
		{
			name: "200 health, 200 models with alias is Ready",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(200)
				case "/v1/models":
					_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.6-35b-a3b"}]}`))
				}
			},
			want: Ready,
		},
		{
			name: "200 health, 200 models without alias is Loading plus error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(200)
				case "/v1/models":
					_, _ = w.Write([]byte(`{"data":[{"id":"other-model"}]}`))
				}
			},
			want:    Loading,
			wantErr: true,
		},
		{
			name: "200 health, 401 models is an error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(200)
				case "/v1/models":
					w.WriteHeader(401)
				}
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newProbeClient(t, tc.handler)
			got, err := c.Probe(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Probe() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Probe() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProbeConnectionRefused(t *testing.T) {
	// A real server that's been closed reproduces the local
	// connection-refused shape a stub Docker container also produces.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	c := Client{Transport: redirectTransport(addr), Key: "k", Alias: "a"}

	got, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil (refused is not an error, just Unreachable)", err)
	}
	if got != Unreachable {
		t.Errorf("Probe() = %v, want Unreachable", got)
	}
}

// fakeSSHRefusalTransport simulates the real SSH tunnel's refusal shape:
// an *ssh.OpenChannelError when nothing listens on the forwarded port.
type fakeSSHRefusalTransport struct{}

func (fakeSSHRefusalTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &ssh.OpenChannelError{Reason: ssh.ConnectionFailed, Message: "connect failed"}
}

func TestProbeSSHRefused(t *testing.T) {
	c := Client{Transport: fakeSSHRefusalTransport{}, Key: "k", Alias: "a"}
	got, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if got != Unreachable {
		t.Errorf("Probe() = %v, want Unreachable for an ssh.OpenChannelError", got)
	}
}
