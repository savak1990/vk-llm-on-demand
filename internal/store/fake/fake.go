// Package fake is an in-memory store.Store for tests.
package fake

import (
	"context"
	"strconv"
	"sync"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

type Fake struct {
	mu        sync.Mutex
	encoded   []byte
	version   int
	failNext  int
	failErr   error
	conflicts int
}

func New(installationID string) *Fake {
	d, err := store.NewDocument(installationID)
	if err != nil {
		panic(err)
	}
	b, err := store.Encode(d)
	if err != nil {
		panic(err)
	}
	return &Fake{encoded: b, version: 1}
}

func (f *Fake) Load(_ context.Context) (store.Document, store.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext > 0 {
		f.failNext--
		return store.Document{}, "", f.failErr
	}
	d, err := store.Decode(f.encoded)
	if err != nil {
		return store.Document{}, "", err
	}
	return d, store.Version(strconv.Itoa(f.version)), nil
}

func (f *Fake) Save(_ context.Context, d store.Document, v store.Version) (store.Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext > 0 {
		f.failNext--
		return "", f.failErr
	}
	if f.conflicts > 0 {
		f.conflicts--
		return "", store.ErrConflict
	}
	if v != store.Version(strconv.Itoa(f.version)) {
		return "", store.ErrConflict
	}
	b, err := store.Encode(d)
	if err != nil {
		return "", err
	}
	f.encoded = b
	f.version++
	return store.Version(strconv.Itoa(f.version)), nil
}

func (f *Fake) FailNext(n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
	f.failErr = err
}

func (f *Fake) ConflictNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conflicts = n
}

func (f *Fake) Raw() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte{}, f.encoded...)
}

func (f *Fake) Put(d store.Document) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := store.Encode(d)
	if err != nil {
		panic(err)
	}
	f.encoded = b
	f.version++
}
