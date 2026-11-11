package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type fakeResponse struct {
	code    int
	body    string
	headers map[string]string
}

type fakeCall struct {
	method, path string
	headers      http.Header
	body         string
}

type fakeServer struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	scripted map[string][]fakeResponse
	calls    map[string][]fakeCall
}

func newFake(t *testing.T) *fakeServer {
	f := &fakeServer{t: t, scripted: map[string][]fakeResponse{}, calls: map[string][]fakeCall{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	route := r.Method + " " + r.URL.Path
	f.calls[route] = append(f.calls[route], fakeCall{method: r.Method, path: r.URL.Path, headers: r.Header.Clone(), body: string(body)})
	queue := f.scripted[route]
	var resp fakeResponse
	switch {
	case len(queue) > 1:
		resp = queue[0]
		f.scripted[route] = queue[1:]
	case len(queue) == 1:
		resp = queue[0]
	default:
		resp = fakeResponse{code: 404, body: `{"error":{"code":"not_found"}}`}
	}
	f.mu.Unlock()

	for k, v := range resp.headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(resp.code)
	_, _ = w.Write([]byte(resp.body))
}

func (f *fakeServer) script(route string, responses ...fakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripted[route] = responses
}

func (f *fakeServer) callsFor(route string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall{}, f.calls[route]...)
}

func (f *fakeServer) callCount(route string) int { return len(f.callsFor(route)) }

func (f *fakeServer) URL() string { return f.srv.URL }

func TestFakeServerScriptsPerRoute(t *testing.T) {
	f := newFake(t)
	f.script("GET /x", fakeResponse{code: 200, body: "a"}, fakeResponse{code: 200, body: "b"})
	for _, want := range []string{"a", "b", "b"} {
		resp, err := http.Get(f.URL() + "/x")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if f.callCount("GET /x") != 3 {
		t.Errorf("callCount = %d, want 3", f.callCount("GET /x"))
	}
}
