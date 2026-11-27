// internal/provider/fake/fake.go
package fake

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
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
	failNext  map[string]struct {
		n   int
		err error
	}
	failDestroyN int
	failAttachN  int
	calls        map[string]int

	volumeOffers []provider.VolumeOffer
	volumes      map[provider.VolumeID]provider.Volume
	nextVolumeID int64
	lastCreate   provider.CreateRequest
}

func New(c clock.Clock) *Fake {
	return &Fake{
		clock: c, instances: map[provider.InstanceID]provider.Instance{},
		statusSeq: map[provider.InstanceID][]provider.InstanceStatus{},
		latency:   map[string]time.Duration{}, failNext: map[string]struct {
			n   int
			err error
		}{},
		calls: map[string]int{}, nextID: 1,
		volumes: map[provider.VolumeID]provider.Volume{}, nextVolumeID: 1000,
	}
}

func NewSeeded(c clock.Clock) *Fake {
	f := New(c)
	f.SetOffers(provider.Offer{ID: 1, MachineID: 1, GPUName: "L40S", Hourly: 500000})
	f.SetBalance(100_000_000)
	return f
}

// SetOffers stores o. Tests set only the fields they assert on, so a zero
// speed or disk gets a value that passes every search filter.
func (f *Fake) SetOffers(o ...provider.Offer) {
	o = append([]provider.Offer(nil), o...)
	for i := range o {
		if o[i].InetDownMbps == 0 {
			o[i].InetDownMbps = 1000
		}
		if o[i].DiskSpaceGB == 0 {
			o[i].DiskSpaceGB = 500
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offers = o
}
func (f *Fake) SetBalance(m provider.Money) { f.mu.Lock(); defer f.mu.Unlock(); f.balance = m }
func (f *Fake) SetLatency(op string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latency[op] = d
}
func (f *Fake) FailNext(op string, n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[op] = struct {
		n   int
		err error
	}{n, err}
}
func (f *Fake) FailDestroy(n int) { f.mu.Lock(); defer f.mu.Unlock(); f.failDestroyN = n }

// FailVolumeAttach refuses the next n creates that carry a volume, before
// any volume or instance is made, as the provider rejects bad volume_info.
func (f *Fake) FailVolumeAttach(n int) { f.mu.Lock(); defer f.mu.Unlock(); f.failAttachN = n }
func (f *Fake) SetStatusSequence(id provider.InstanceID, s ...provider.InstanceStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusSeq[id] = s
}
func (f *Fake) AddInstance(i provider.Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[i.ID] = i
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
	return f.takeFailureLocked(op)
}

// takeFailureLocked is takeFailure for a caller that already holds f.mu.
func (f *Fake) takeFailureLocked(op string) error {
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
	return provider.Filter(offers, filter), nil
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
	f.lastCreate = r
	machine := f.offerMachine(r.OfferID)
	id := provider.InstanceID(f.nextID)
	f.nextID++
	f.instances[id] = provider.Instance{ID: id, Label: r.Label, MachineID: machine, Status: provider.StatusOther}
	if v := r.Volume; v != nil {
		if f.failAttachN > 0 {
			f.failAttachN--
			delete(f.instances, id)
			f.mu.Unlock()
			return 0, &provider.Error{Op: "create", Class: provider.Terminal, Status: 400, Err: fmt.Errorf("fake: volume_info refused")}
		}
		vid := provider.VolumeID(v.ID)
		if v.CreateNew {
			vid = provider.VolumeID(f.nextVolumeID)
			f.nextVolumeID++
			f.volumes[vid] = provider.Volume{ID: vid, Label: v.Name, MachineID: machine, SizeGB: float64(v.SizeGB)}
		}
		vol, ok := f.volumes[vid]
		if !ok || vol.MachineID != machine || len(vol.Instances) > 0 {
			delete(f.instances, id)
			f.mu.Unlock()
			return 0, &provider.Error{Op: "create", Class: provider.Terminal, Status: 400, Err: fmt.Errorf("fake: volume %d cannot attach", vid)}
		}
		vol.Instances, vol.Status = []provider.InstanceID{id}, "in-use"
		f.volumes[vid] = vol
	}
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
	if _, ok := f.instances[id]; !ok {
		f.mu.Unlock()
		return &provider.Error{Op: "destroy", Class: provider.Terminal, Err: fmt.Errorf("instance %d: %w", id, provider.ErrNotFound)}
	}
	delete(f.instances, id)
	for vid, vol := range f.volumes {
		if slices.Contains(vol.Instances, id) {
			vol.Instances = slices.DeleteFunc(vol.Instances, func(x provider.InstanceID) bool { return x == id })
			vol.Status = "created"
			f.volumes[vid] = vol
		}
	}
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

func (f *Fake) SetVolumeOffers(o ...provider.VolumeOffer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumeOffers = append([]provider.VolumeOffer{}, o...)
}

func (f *Fake) AddVolume(v provider.Volume) provider.VolumeID {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v.ID == 0 {
		v.ID = provider.VolumeID(f.nextVolumeID)
		f.nextVolumeID++
	}
	f.volumes[v.ID] = v
	return v.ID
}

func (f *Fake) Volumes() []provider.Volume {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]provider.Volume, 0, len(f.volumes))
	for _, v := range f.volumes {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b provider.Volume) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (f *Fake) LastCreate() provider.CreateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCreate
}

func (f *Fake) offerMachine(offerID int64) int64 {
	for _, o := range f.offers {
		if o.ID == offerID {
			return o.MachineID
		}
	}
	return 0
}

func (f *Fake) SearchVolumeOffers(ctx context.Context, machineIDs []int64, minSizeGB int) ([]provider.VolumeOffer, error) {
	f.mu.Lock()
	f.calls["search_volumes"]++
	var out []provider.VolumeOffer
	for _, o := range f.volumeOffers {
		if slices.Contains(machineIDs, o.MachineID) && o.DiskSpaceGB >= float64(minSizeGB) {
			out = append(out, o)
		}
	}
	f.mu.Unlock()
	if err := f.wait(ctx, "search_volumes"); err != nil {
		return nil, err
	}
	if err := f.takeFailure("search_volumes"); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *Fake) ListVolumes(ctx context.Context, labelPrefix string) ([]provider.Volume, error) {
	f.mu.Lock()
	f.calls["list_volumes"]++
	var out []provider.Volume
	for _, v := range f.volumes {
		if strings.HasPrefix(v.Label, labelPrefix) {
			v.Instances = append([]provider.InstanceID{}, v.Instances...)
			out = append(out, v)
		}
	}
	f.mu.Unlock()
	if err := f.wait(ctx, "list_volumes"); err != nil {
		return nil, err
	}
	if err := f.takeFailure("list_volumes"); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *Fake) DeleteVolume(ctx context.Context, id provider.VolumeID) error {
	f.mu.Lock()
	f.calls["delete_volume"]++
	if err := f.takeFailureLocked("delete_volume"); err != nil {
		f.mu.Unlock()
		return err
	}
	v, ok := f.volumes[id]
	switch {
	case !ok:
		f.mu.Unlock()
		return &provider.Error{Op: "delete_volume", Class: provider.Terminal, Status: 404, Err: fmt.Errorf("volume %d: %w", id, provider.ErrNotFound)}
	case len(v.Instances) > 0:
		f.mu.Unlock()
		return &provider.Error{Op: "delete_volume", Class: provider.Terminal, Status: 409, Err: fmt.Errorf("volume %d: %w", id, provider.ErrVolumeInUse)}
	}
	delete(f.volumes, id)
	f.mu.Unlock()
	return f.wait(ctx, "delete_volume")
}

var _ provider.Provider = (*Fake)(nil)
