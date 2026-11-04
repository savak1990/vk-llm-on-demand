package gateway

import (
	"net/http"
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

type firstTokenCall struct {
	gen int64
	d   time.Duration
}

// fakeController is the test double for Controller; every call is
// recorded so tests can assert exact call counts and arguments.
type fakeController struct {
	mu sync.Mutex

	admitErr      error
	admitRT       http.RoundTripper
	admitCalls    []int64
	endCalls      []int64
	firstToken    []firstTokenCall
	upstreamErr   []int64
	status        controller.Snapshot
	panicOnStatus bool
}

func (f *fakeController) Admit(gen int64) (http.RoundTripper, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admitCalls = append(f.admitCalls, gen)
	if f.admitErr != nil {
		return nil, f.admitErr
	}
	return f.admitRT, nil
}

func (f *fakeController) RequestEnd(gen int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endCalls = append(f.endCalls, gen)
}

func (f *fakeController) ObserveFirstToken(gen int64, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.firstToken = append(f.firstToken, firstTokenCall{gen, d})
}

func (f *fakeController) ReportUpstreamFailure(gen int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upstreamErr = append(f.upstreamErr, gen)
}

func (f *fakeController) Status() controller.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicOnStatus {
		panic("fakeController.Status: should never be called by handleModels")
	}
	return f.status
}

func (f *fakeController) snapshot() (admitCalls, endCalls []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64{}, f.admitCalls...), append([]int64{}, f.endCalls...)
}

// fakeRoundTripper lets a test script the upstream's response or error
// per call without a real network hop.
type fakeRoundTripper func(*http.Request) (*http.Response, error)

func (f fakeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
