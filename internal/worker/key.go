package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// WorkerKey derives the per-instance API key from the controller's worker
// seed, which is never itself stored or sent to the worker.
func WorkerKey(workerSeed []byte, installationID string, generation int64) string {
	mac := hmac.New(sha256.New, workerSeed)
	_, _ = fmt.Fprintf(mac, "worker:%s:%d", installationID, generation) // hash.Hash.Write never errors
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
