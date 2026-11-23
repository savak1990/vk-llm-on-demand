package modelsize

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPReadsLinkedSizeWithoutFollowingRedirect(t *testing.T) {
	var cdnHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/m", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Linked-Size", "29200000000")
		w.Header().Set("Location", "/cdn")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, _ *http.Request) { cdnHits.Add(1) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n, err := NewHTTP(srv.Client()).Bytes(context.Background(), srv.URL+"/m")
	if err != nil || n != 29_200_000_000 {
		t.Fatalf("Bytes = %d, %v; want 29200000000", n, err)
	}
	if cdnHits.Load() != 0 {
		t.Fatalf("redirect followed %d times, want 0", cdnHits.Load())
	}
}

func TestHTTPFallsBackToContentLengthAfterRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/m", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/cdn", http.StatusFound) })
	mux.HandleFunc("/cdn", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("Content-Length", "555") })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	n, err := NewHTTP(srv.Client()).Bytes(context.Background(), srv.URL+"/m")
	if err != nil || n != 555 {
		t.Fatalf("Bytes = %d, %v; want 555", n, err)
	}
}

func TestHTTPNoSizeIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	if n, err := NewHTTP(srv.Client()).Bytes(context.Background(), srv.URL+"/m"); err == nil {
		t.Fatalf("Bytes = %d, nil; want an error", n)
	}
}

func TestHTTPCachesSuccessOnly(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Linked-Size", "123")
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	r := NewHTTP(srv.Client())
	ctx := context.Background()

	if _, err := r.Bytes(ctx, srv.URL+"/m"); err == nil {
		t.Fatal("404: want an error")
	}
	status.Store(http.StatusFound)
	if n, err := r.Bytes(ctx, srv.URL+"/m"); err != nil || n != 123 {
		t.Fatalf("after recovery: %d, %v; want 123 (a failure must not be cached)", n, err)
	}
	status.Store(http.StatusInternalServerError)
	if n, err := r.Bytes(ctx, srv.URL+"/m"); err != nil || n != 123 {
		t.Fatalf("cached: %d, %v; want 123 from the cache", n, err)
	}
}

func TestFixed(t *testing.T) {
	if n, err := Fixed(7).Bytes(context.Background(), "any"); n != 7 || err != nil {
		t.Fatalf("Fixed(7) = %d, %v; want 7, nil", n, err)
	}
	if n, err := Fixed(0).Bytes(context.Background(), "any"); err == nil {
		t.Fatalf("Fixed(0) = %d, nil; want error", n)
	}
	if n, err := Fixed(-1).Bytes(context.Background(), "any"); err == nil {
		t.Fatalf("Fixed(-1) = %d, nil; want error", n)
	}
}
