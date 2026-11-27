package main

import (
	"context"
	"strings"
	"testing"
)

func TestHeaders(t *testing.T) {
	f := newFake(t)
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"STOPPED"}`})
	c := newClient(Config{Endpoint: f.URL(), Token: "sk-vkllm-REPLACE_ME"})
	if _, _, err := c.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.callsFor("GET /api/v1/gpu/status")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if got := calls[0].headers.Get("Authorization"); got != "Bearer sk-vkllm-REPLACE_ME" {
		t.Errorf("Authorization = %q", got)
	}
	if ua := calls[0].headers.Get("User-Agent"); ua == "" {
		t.Error("User-Agent header is empty")
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		resp     fakeResponse
		wantCode int
	}{
		{"unauthorized", fakeResponse{code: 401, body: `{"error":{"code":"invalid_token"}}`}, 3},
		{"forbidden", fakeResponse{code: 403, body: `{"error":{"code":"permission_denied"}}`}, 3},
		{"unavailable", fakeResponse{code: 503, body: `{"error":{"code":"gpu_unavailable","reason":"budget_exhausted"}}`}, 1},
		{"server error", fakeResponse{code: 500, body: `{"error":{"code":"internal_error"}}`}, 1},
		{"bad request", fakeResponse{code: 400, body: `{"error":{"code":"force_not_enabled"}}`}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			f.script("GET /api/v1/gpu/status", c.resp)
			cl := newClient(Config{Endpoint: f.URL(), Token: "sk-vkllm-REPLACE_ME"})
			_, code, err := cl.status(context.Background())
			if err == nil {
				t.Fatal("want an error")
			}
			if code != c.wantCode {
				t.Errorf("exit code = %d, want %d", code, c.wantCode)
			}
			if c.name == "unavailable" && !strings.Contains(err.Error(), "budget_exhausted") {
				t.Errorf("error = %q, want it to contain the reason verbatim", err.Error())
			}
		})
	}
}

func TestNoRedirect(t *testing.T) {
	target := newFake(t)
	target.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"STOPPED"}`})
	redirector := newFake(t)
	redirector.script("GET /api/v1/gpu/status", fakeResponse{code: 302, headers: map[string]string{"Location": target.URL() + "/api/v1/gpu/status"}})
	cl := newClient(Config{Endpoint: redirector.URL(), Token: "sk-vkllm-REPLACE_ME"})
	_, code, err := cl.status(context.Background())
	if err == nil {
		t.Fatal("want an error on a 3xx response")
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if target.callCount("GET /api/v1/gpu/status") != 0 {
		t.Error("the redirect target received a request; it must not be followed")
	}
}

func TestStartAndStop(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"CREATING"}`})
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"STOPPING"}`})
	cl := newClient(Config{Endpoint: f.URL(), Token: "sk-vkllm-REPLACE_ME"})

	if _, _, err := cl.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.callCount("POST /api/v1/gpu/start"); got != 1 {
		t.Errorf("start calls = %d, want 1", got)
	}

	if _, _, err := cl.stop(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	calls := f.callsFor("POST /api/v1/gpu/stop")
	if len(calls) != 1 {
		t.Fatalf("stop calls = %d, want 1", len(calls))
	}
	if calls[0].body != `{"force":true}` {
		t.Errorf("stop body = %q, want force:true", calls[0].body)
	}

	if _, _, err := cl.stop(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if got := f.callsFor("POST /api/v1/gpu/stop")[1].body; got != "" {
		t.Errorf("stop body without force = %q, want empty", got)
	}
}
