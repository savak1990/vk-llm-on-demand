package worker

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ProgressCommand prints the stage log and the bytes written under /models;
// du counts allocated blocks, so a sparse aria2c file reports real progress.
const ProgressCommand = `cat /var/log/vkllm-stages.log 2>/dev/null; echo "VKLLM_BYTES $(du -cB1 /models/model.gguf /models/model.gguf.[0-9]* 2>/dev/null | tail -1 | cut -f1)"`

// Progress is one reading of the model download on a worker.
type Progress struct {
	Bytes  int64
	Stages []string
	Times  map[string]time.Time
}

// ParseProgress reads ProgressCommand's output; a missing or malformed
// byte count reads as zero so a half-started worker is not an error.
func ParseProgress(out []byte) (Progress, error) {
	stages, err := ParseStages(bytes.NewReader(out))
	if err != nil {
		return Progress{}, err
	}
	p := Progress{Times: stages}
	for name := range stages {
		p.Stages = append(p.Stages, name)
	}
	slices.Sort(p.Stages)
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "VKLLM_BYTES "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				p.Bytes = n
			}
		}
	}
	return p, nil
}
