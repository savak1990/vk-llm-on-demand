// internal/store/fake/fake_test.go
package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestFakeStaleVersion(t *testing.T) {
	f := New("4f2c1a9b0e77")
	doc, v, err := f.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	doc.Generation = 1
	if _, err := f.Save(context.Background(), doc, v); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	doc.Generation = 2
	if _, err := f.Save(context.Background(), doc, v); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale Save: want ErrConflict, got %v", err)
	}
}

func TestFakeInjectedFailure(t *testing.T) {
	f := New("4f2c1a9b0e77")
	wantErr := errors.New("boom")
	f.FailNext(2, wantErr)

	if _, _, err := f.Load(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("1st Load: want %v, got %v", wantErr, err)
	}
	if _, _, err := f.Load(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("2nd Load: want %v, got %v", wantErr, err)
	}
	if _, _, err := f.Load(context.Background()); err != nil {
		t.Fatalf("3rd Load: want nil, got %v", err)
	}

	f.ConflictNext(1)
	doc, v, _ := f.Load(context.Background())
	if _, err := f.Save(context.Background(), doc, v); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Save after ConflictNext(1): want ErrConflict, got %v", err)
	}
}

func TestFakeInstallationMismatch(t *testing.T) {
	f := New("bbbb")
	d, err := store.NewDocument("aaaa")
	if err != nil {
		t.Fatalf("NewDocument: %v", err)
	}
	f.Put(d)
	before := f.Raw()

	if _, _, err := f.Load(context.Background()); !errors.Is(err, store.ErrInstallation) {
		t.Fatalf("Load: want ErrInstallation, got %v", err)
	}
	if string(f.Raw()) != string(before) {
		t.Fatal("Load must not have written anything")
	}
	if err := store.Ready(f)(); !errors.Is(err, store.ErrInstallation) {
		t.Fatalf("Ready: want ErrInstallation, got %v", err)
	}
}
