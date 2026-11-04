package worker

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const stageLinePrefix = "VKLLM_STAGE "

// ParseStages reads onstart's stage log (VKLLM_STAGE <name> <unix_ms>
// lines) and returns the timestamp of each named stage. Non-VKLLM_STAGE
// lines are ignored; a malformed VKLLM_STAGE line is an error.
func ParseStages(r io.Reader) (map[string]time.Time, error) {
	stages := make(map[string]time.Time)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, stageLinePrefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, stageLinePrefix))
		if len(fields) != 2 {
			return nil, fmt.Errorf("worker: malformed stage line %q", line)
		}
		ms, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("worker: malformed stage timestamp in %q: %w", line, err)
		}
		stages[fields[0]] = time.UnixMilli(ms)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return stages, nil
}
