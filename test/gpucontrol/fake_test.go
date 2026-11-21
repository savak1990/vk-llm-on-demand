package gpucontrol

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeResp struct {
	code int
	body string
}

type fakeCall struct {
	authHeader string
	body       string
	reqBody    string
}

type fake struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	scripts map[string][]fakeResp
	calls   map[string][]fakeCall
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, scripts: map[string][]fakeResp{}, calls: map[string][]fakeCall{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)

	// The script only accepts https:// endpoints, so curl must trust the fake's cert.
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})
	if err := os.WriteFile(caFile, ca, 0o600); err != nil {
		t.Fatalf("writing fake CA: %v", err)
	}
	t.Setenv("CURL_CA_BUNDLE", caFile)
	return f
}

func (f *fake) script(route string, resps ...fakeResp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[route] = append(f.scripts[route], resps...)
}

func (f *fake) callLog(route string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls[route]...)
}

func (f *fake) URL() string { return f.srv.URL }

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	reqBody, _ := io.ReadAll(r.Body)
	route := r.Method + " " + r.URL.Path
	f.mu.Lock()
	queue := f.scripts[route]
	var resp fakeResp
	if len(queue) == 0 {
		f.mu.Unlock()
		f.t.Errorf("fake: unscripted call %s", route)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	resp = queue[0]
	if len(queue) > 1 {
		f.scripts[route] = queue[1:]
	}
	f.calls[route] = append(f.calls[route], fakeCall{authHeader: r.Header.Get("Authorization"), body: resp.body, reqBody: string(reqBody)})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.code)
	_, _ = w.Write([]byte(resp.body))
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return string(b)
}
