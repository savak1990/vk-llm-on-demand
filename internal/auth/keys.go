package auth

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
)

type Surface int

const (
	Inference Surface = iota
	Management
)

var (
	ErrMissing      = errors.New("auth: missing")
	ErrMalformed    = errors.New("auth: malformed")
	ErrUnknown      = errors.New("auth: unknown")
	ErrWrongSurface = errors.New("auth: wrong surface")
)

type keySet map[[32]byte]string // sha256 digest -> keyID

type Keys struct {
	clientPath, mgmtPath string
	clock                clock.Clock
	log                  *slog.Logger

	sets atomic.Pointer[struct{ client, mgmt keySet }]

	clientMod, mgmtMod time.Time // mtimes at the last successful load, for Run's poll
}

// Load reads both key-list files once; Run polls for later changes. A
// value present in both lists is rejected (a key must have exactly one
// surface).
func Load(clientPath, mgmtPath string, c clock.Clock, log *slog.Logger) (*Keys, error) {
	k := &Keys{clientPath: clientPath, mgmtPath: mgmtPath, clock: c, log: log}
	client, mgmt, clientMod, mgmtMod, err := k.readBoth()
	if err != nil {
		return nil, err
	}
	k.sets.Store(&struct{ client, mgmt keySet }{client, mgmt})
	k.clientMod, k.mgmtMod = clientMod, mgmtMod
	return k, nil
}

func (k *Keys) readBoth() (client, mgmt keySet, clientMod, mgmtMod time.Time, err error) {
	client, clientMod, err = readKeySet(k.clientPath)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}
	mgmt, mgmtMod, err = readKeySet(k.mgmtPath)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}
	for digest := range client {
		if _, dup := mgmt[digest]; dup {
			return nil, nil, time.Time{}, time.Time{}, errors.New("auth: a key is present in both client-keys and management-tokens")
		}
	}
	return client, mgmt, clientMod, mgmtMod, nil
}

func readKeySet(path string) (keySet, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	set := keySet{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		digest := sha256.Sum256([]byte(line))
		keyID := line
		if idx := strings.Index(line, "sk-vkllm-"); idx == 0 && len(line) >= len("sk-vkllm-")+8 {
			keyID = line[len("sk-vkllm-") : len("sk-vkllm-")+8]
		}
		set[digest] = keyID
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return set, info.ModTime(), nil
}

// Run polls both files' mtimes every 10s and swaps in a successful
// reload atomically; a failed reload (including the both-lists
// conflict) keeps the current sets and logs keys_reload_failed.
func (k *Keys) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.clock.After(10 * time.Second):
		}
		ci, err1 := os.Stat(k.clientPath)
		mi, err2 := os.Stat(k.mgmtPath)
		if err1 != nil || err2 != nil {
			k.log.Warn("keys_reload_failed")
			continue
		}
		if !ci.ModTime().After(k.clientMod) && !mi.ModTime().After(k.mgmtMod) {
			continue
		}
		client, mgmt, clientMod, mgmtMod, err := k.readBoth()
		if err != nil {
			k.log.Warn("keys_reload_failed")
			continue
		}
		k.sets.Store(&struct{ client, mgmt keySet }{client, mgmt})
		k.clientMod, k.mgmtMod = clientMod, mgmtMod
	}
}

// Check compares the presented token's digest against every entry of
// both lists with subtle.ConstantTimeCompare, no early exit, so a
// caller can't learn which list matched fastest.
func (k *Keys) Check(authorization string, want Surface) (keyID string, err error) {
	if authorization == "" {
		return "", ErrMissing
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return "", ErrMalformed
	}
	token := strings.TrimPrefix(authorization, prefix)
	digest := sha256.Sum256([]byte(token))

	sets := k.sets.Load()
	clientID, clientOK := matchConstantTime(sets.client, digest)
	mgmtID, mgmtOK := matchConstantTime(sets.mgmt, digest)

	switch {
	case want == Inference && clientOK:
		return clientID, nil
	case want == Management && mgmtOK:
		return mgmtID, nil
	case clientOK || mgmtOK:
		return "", ErrWrongSurface
	default:
		return "", ErrUnknown
	}
}

func matchConstantTime(set keySet, digest [32]byte) (keyID string, ok bool) {
	for d, id := range set {
		if subtle.ConstantTimeCompare(d[:], digest[:]) == 1 {
			keyID, ok = id, true
		}
	}
	return keyID, ok
}

// Loaded reports whether both lists are non-empty.
func (k *Keys) Loaded() bool {
	sets := k.sets.Load()
	return len(sets.client) > 0 && len(sets.mgmt) > 0
}
