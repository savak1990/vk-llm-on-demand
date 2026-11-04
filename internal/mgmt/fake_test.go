package mgmt

import (
	"context"
	"sync"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

type fakeController struct {
	mu                              sync.Mutex
	startCalls, stopCalls           int
	startSnap, stopSnap, statusSnap controller.Snapshot
	startErr, stopErr               error
	startBlock                      chan struct{} // when non-nil, Start blocks until closed
	stopBlock                       chan struct{} // when non-nil, Stop blocks until closed
	stopForce                       bool          // force passed on the most recent Stop call
}

func newFakeController() *fakeController { return &fakeController{} }

var _ Controller = (*fakeController)(nil)

func (f *fakeController) Start(_ context.Context) (controller.Snapshot, error) {
	f.mu.Lock()
	f.startCalls++
	block, snap, err := f.startBlock, f.startSnap, f.startErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return snap, err
}

func (f *fakeController) Stop(_ context.Context, force bool) (controller.Snapshot, error) {
	f.mu.Lock()
	f.stopCalls++
	f.stopForce = force
	block, snap, err := f.stopBlock, f.stopSnap, f.stopErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return snap, err
}

func (f *fakeController) Status() controller.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusSnap
}

func (f *fakeController) StopForce() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopForce
}

func (f *fakeController) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case "start":
		return f.startCalls
	case "stop":
		return f.stopCalls
	}
	return 0
}
