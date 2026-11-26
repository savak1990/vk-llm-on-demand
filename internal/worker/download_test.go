package worker

import (
	"reflect"
	"testing"
	"time"
)

func TestParseProgress(t *testing.T) {
	out := []byte("VKLLM_STAGE start 1000\nVKLLM_STAGE download_begin 2000\nVKLLM_STAGE download_end 9000\nVKLLM_BYTES 12000000000\n")
	p, err := ParseProgress(out)
	if err != nil {
		t.Fatalf("ParseProgress: %v", err)
	}
	if p.Bytes != 12_000_000_000 {
		t.Fatalf("Bytes = %d", p.Bytes)
	}
	if !reflect.DeepEqual(p.Stages, []string{"download_begin", "download_end", "start"}) {
		t.Fatalf("Stages = %q", p.Stages)
	}
}

func TestParseProgressMissingBytes(t *testing.T) {
	for _, out := range []string{"", "VKLLM_BYTES \n", "VKLLM_BYTES x\n"} {
		p, err := ParseProgress([]byte(out))
		if err != nil || p.Bytes != 0 {
			t.Fatalf("%q: Bytes %d err %v, want 0 and nil", out, p.Bytes, err)
		}
	}
}

func TestParseProgressKeepsTimes(t *testing.T) {
	out := []byte("VKLLM_STAGE download_begin 1764151000000\nVKLLM_STAGE download_end 1764151285500\nVKLLM_BYTES 42\n")
	p, err := ParseProgress(out)
	if err != nil {
		t.Fatalf("ParseProgress: %v", err)
	}
	if got := p.Times["download_end"].Sub(p.Times["download_begin"]); got != 285500*time.Millisecond {
		t.Fatalf("download_end - download_begin = %v, want 285.5s", got)
	}
}
