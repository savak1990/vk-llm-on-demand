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

// Fixed returns one size for every URL.
type Fixed int64

func (f Fixed) Bytes(context.Context, string) (int64, error) { return int64(f), nil }

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

	resp, err := head(ctx, h.noRedirect, url)
	if err != nil {
		return 0, err
	}
	n = parseSize(resp.Header.Get("X-Linked-Size"))
	if n <= 0 {
		if resp, err = head(ctx, h.follow, url); err != nil {
			return 0, err
		}
		if n = parseSize(resp.Header.Get("Content-Length")); n <= 0 {
			n = resp.ContentLength
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

func head(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("modelsize: HEAD returned %s", resp.Status)
	}
	return resp, nil
}

func parseSize(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
