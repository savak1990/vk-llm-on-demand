package controller

import (
	"errors"
	"strconv"
	"strings"
)

// errStale means a mutate closure's own guard no longer held against
// the document Transition actually loaded; the loop drops the event
// and re-reads rather than retrying blindly.
var errStale = errors.New("controller: stale guard")

// parseLabel splits "vkllm:<installationId>:<gen>". ok is false for a
// foreign prefix, a wrong field count, or a non-numeric generation.
func parseLabel(label string) (installationID string, gen int64, ok bool) {
	parts := strings.Split(label, ":")
	if len(parts) != 3 || parts[0] != "vkllm" {
		return "", 0, false
	}
	g, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[1], g, true
}

// mayDestroy is true only for a label under our own installation id
// whose generation is no newer than the one just read; a foreign or
// unparsable label is never a destroy target.
func mayDestroy(label, installationID string, docGeneration int64) bool {
	id, gen, ok := parseLabel(label)
	if !ok || id != installationID {
		return false
	}
	return gen <= docGeneration
}
