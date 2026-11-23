// Package modelsize finds the byte size of a model file from its URL.
package modelsize

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type Resolver interface {
	Bytes(ctx context.Context, url string) (int64, error)
}

type Fixed int64

func (f Fixed) Bytes(context.Context, string) (int64, error) {
	if f <= 0 {
		return 0, errors.New("modelsize: fixed size must be positive")
	}
	return int64(f), nil
}

type HTTP struct {
	noRedirect *http.Client
	follow     *http.Client

	mu    sync.Mutex
	cache map[string]int64
}

func NewHTTP(base *http.Client) *HTTP {
	nr := *base
	nr.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTP{noRedirect: &nr, follow: base, cache: map[string]int64{}}
}

// Bytes reads Hugging Face's X-Linked-Size from the first, unfollowed
// response; other hosts fall back to Content-Length after redirects.
func (h *HTTP) Bytes(ctx context.Context, url string) (int64, error) {
	h.mu.Lock()
	n, ok := h.cache[url]
	h.mu.Unlock()
	if ok {
		return n, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	hdr, _, err := head(ctx, h.noRedirect, url)
	if err != nil {
		return 0, err
	}
	n = parseSize(hdr.Get("X-Linked-Size"))
	if n <= 0 {
		if _, n, err = head(ctx, h.follow, url); err != nil {
			return 0, err
		}
	}
	if n <= 0 {
		return 0, errors.New("modelsize: response carries no size")
	}
	h.mu.Lock()
	h.cache[url] = n
	h.mu.Unlock()
	return n, nil
}

func head(ctx context.Context, c *http.Client, url string) (http.Header, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, 0, fmt.Errorf("modelsize: HEAD returned %s", resp.Status)
	}
	return resp.Header, resp.ContentLength, nil
}

func parseSize(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
