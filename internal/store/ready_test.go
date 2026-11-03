package store_test

import (
	"errors"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/store/fake"
)

func TestReadyCheck(t *testing.T) {
	f := fake.New("4f2c1a9b0e77")
	if err := store.Ready(f)(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	wantErr := errors.New("down")
	f.FailNext(1, wantErr)
	if err := store.Ready(f)(); !errors.Is(err, wantErr) {
		t.Fatalf("Ready: want %v, got %v", wantErr, err)
	}
}
