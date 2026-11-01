// internal/provider/fake/fake.go
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

type Fake struct {
	mu        sync.Mutex
	clock     clock.Clock
	offers    []provider.Offer
	instances map[provider.InstanceID]provider.Instance
	statusSeq map[provider.InstanceID][]provider.InstanceStatus
	nextID    int64
	balance   provider.Money
	latency   map[string]time.Duration
	failNext  map[string]struct{ n int; err error }
	failDestroyN int
	calls     map[string]int
}

func New(c clock.Clock) *Fake {
	return &Fake{
		clock: c, instances: map[provider.InstanceID]provider.Instance{},
		statusSeq: map[provider.InstanceID][]provider.InstanceStatus{},
		latency:   map[string]time.Duration{}, failNext: map[string]struct{ n int; err error }{},
		calls: map[string]int{}, nextID: 1,
	}
}

func NewSeeded(c clock.Clock) *Fake {
	f := New(c)
	f.SetOffers(provider.Offer{ID: 1, MachineID: 1, GPUName: "L40S", Hourly: 500000})
	f.SetBalance(100_000_000)
	return f
}

func (f *Fake) SetOffers(o ...provider.Offer)    { f.mu.Lock(); defer f.mu.Unlock(); f.offers = o }
func (f *Fake) SetBalance(m provider.Money)       { f.mu.Lock(); defer f.mu.Unlock(); f.balance = m }
func (f *Fake) SetLatency(op string, d time.Duration) {
	f.mu.Lock(); defer f.mu.Unlock(); f.latency[op] = d
}
func (f *Fake) FailNext(op string, n int, err error) {
	f.mu.Lock(); defer f.mu.Unlock(); f.failNext[op] = struct{ n int; err error }{n, err}
}
func (f *Fake) FailDestroy(n int) { f.mu.Lock(); defer f.mu.Unlock(); f.failDestroyN = n }
func (f *Fake) SetStatusSequence(id provider.InstanceID, s ...provider.InstanceStatus) {
	f.mu.Lock(); defer f.mu.Unlock(); f.statusSeq[id] = s
}
func (f *Fake) AddInstance(i provider.Instance) {
	f.mu.Lock(); defer f.mu.Unlock(); f.instances[i.ID] = i
}
func (f *Fake) Calls(op string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[op] }

func (f *Fake) wait(ctx context.Context, op string) error {
	f.mu.Lock()
	d := f.latency[op]
	f.mu.Unlock()
	if d == 0 {
		return nil
	}
	t := f.clock.NewTicker(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Fake) takeFailure(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn, ok := f.failNext[op]
	if !ok || fn.n <= 0 {
		return nil
	}
	fn.n--
	f.failNext[op] = fn
	return fn.err
}

func (f *Fake) SearchOffers(ctx context.Context, filter provider.OfferFilter) ([]provider.Offer, error) {
	f.mu.Lock()
	f.calls["search"]++
	offers := append([]provider.Offer{}, f.offers...)
	f.mu.Unlock()
	if err := f.wait(ctx, "search"); err != nil {
		return nil, err
	}
	if err := f.takeFailure("search"); err != nil {
		return nil, err
	}
	return provider.Select(offers, filter), nil
}

func (f *Fake) CreateInstance(ctx context.Context, r provider.CreateRequest) (provider.InstanceID, error) {
	f.mu.Lock()
	f.calls["create"]++
	for _, inst := range f.instances {
		if inst.Label == r.Label {
			id := inst.ID
			f.mu.Unlock()
			if err := f.takeFailure("create"); err != nil {
				return 0, err
			}
			return id, nil
		}
	}
	id := provider.InstanceID(f.nextID)
	f.nextID++
	f.instances[id] = provider.Instance{ID: id, Label: r.Label, Status: provider.StatusOther, RawStatus: ""}
	f.mu.Unlock()
	if err := f.wait(ctx, "create"); err != nil {
		return 0, err
	}
	if err := f.takeFailure("create"); err != nil {
		return 0, err
	}
	return id, nil
}

func (f *Fake) GetInstance(ctx context.Context, id provider.InstanceID) (provider.Instance, error) {
	f.mu.Lock()
	f.calls["get"]++
	inst, ok := f.instances[id]
	seq := f.statusSeq[id]
	if ok && len(seq) > 0 {
		inst.Status = seq[0]
		f.statusSeq[id] = seq[1:]
		f.instances[id] = inst
	} else if ok && inst.Status == provider.StatusOther {
		inst.Status = provider.StatusRunning
		inst.SSHHost, inst.SSHPort = "fake", 22
		f.instances[id] = inst
	}
	f.mu.Unlock()
	if err := f.wait(ctx, "get"); err != nil {
		return provider.Instance{}, err
	}
	if !ok {
		return provider.Instance{}, fmt.Errorf("instance %d: %w", id, provider.ErrNotFound)
	}
	if err := f.takeFailure("get"); err != nil {
		return provider.Instance{}, err
	}
	return inst, nil
}

func (f *Fake) ListInstances(ctx context.Context, labelPrefix string) ([]provider.Instance, error) {
	f.mu.Lock()
	f.calls["list"]++
	var out []provider.Instance
	for _, inst := range f.instances {
		if labelPrefix == "" || (len(inst.Label) >= len(labelPrefix) && inst.Label[:len(labelPrefix)] == labelPrefix) {
			out = append(out, inst)
		}
	}
	f.mu.Unlock()
	if err := f.wait(ctx, "list"); err != nil {
		return nil, err
	}
	if err := f.takeFailure("list"); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *Fake) DestroyInstance(ctx context.Context, id provider.InstanceID) error {
	f.mu.Lock()
	f.calls["destroy"]++
	if f.failDestroyN > 0 {
		f.failDestroyN--
		f.mu.Unlock()
		return &provider.Error{Op: "destroy", Class: provider.Retryable, Err: fmt.Errorf("fake destroy failure")}
	}
	delete(f.instances, id)
	f.mu.Unlock()
	if err := f.wait(ctx, "destroy"); err != nil {
		return err
	}
	return f.takeFailure("destroy")
}

func (f *Fake) Balance(ctx context.Context) (provider.Money, error) {
	f.mu.Lock()
	f.calls["balance"]++
	b := f.balance
	f.mu.Unlock()
	if err := f.wait(ctx, "balance"); err != nil {
		return 0, err
	}
	if err := f.takeFailure("balance"); err != nil {
		return 0, err
	}
	return b, nil
}

var _ provider.Provider = (*Fake)(nil)
