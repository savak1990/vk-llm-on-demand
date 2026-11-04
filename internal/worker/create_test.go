package worker

import (
	"testing"
	"time"
)

func validParams() CreateParams {
	return CreateParams{
		Image:       "vastai/server-cuda:latest",
		DiskGB:      60,
		Label:       "vkllm:inst-1:1",
		ModelURL:    "https://huggingface.co/example/model.gguf",
		ModelSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"[:64],
		Alias:       "qwen3.6-35b-a3b",
		Ctx:         32768,
		Parallel:    1,
		DeadMan:     2400 * time.Second,
		WorkerKey:   "x",
	}
}

func TestBuildCreate(t *testing.T) {
	req, err := BuildCreate(validParams())
	if err != nil {
		t.Fatalf("BuildCreate() error = %v, want nil", err)
	}
	if req.RunType != "ssh" {
		t.Errorf("RunType = %q, want ssh", req.RunType)
	}
	if req.DiskGB != 60 {
		t.Errorf("DiskGB = %d, want 60", req.DiskGB)
	}
	if req.Image != "vastai/server-cuda:latest" {
		t.Errorf("Image = %q", req.Image)
	}
	if req.Label != "vkllm:inst-1:1" {
		t.Errorf("Label = %q", req.Label)
	}
	if req.Onstart != Render() {
		t.Errorf("Onstart does not equal Render() output")
	}
	if len(req.Env) != 8 {
		t.Errorf("len(Env) = %d, want 8", len(req.Env))
	}
	for k := range req.Env {
		if len(k) >= 2 && k[:2] == "-p" {
			t.Errorf("Env has a port-mapping key %q, want none", k)
		}
	}
	wantKeys := []string{"MODEL_URL", "MODEL_SHA256", "MODEL_ALIAS", "CTX", "PARALLEL", "WORKER_API_KEY", "DEADMAN_SECONDS", "REASONING_FORMAT"}
	for _, k := range wantKeys {
		if _, ok := req.Env[k]; !ok {
			t.Errorf("Env missing key %q", k)
		}
	}
	if req.Env["REASONING_FORMAT"] != "deepseek" {
		t.Errorf("REASONING_FORMAT = %q, want deepseek", req.Env["REASONING_FORMAT"])
	}
}

func TestBuildCreateValidation(t *testing.T) {
	cases := map[string]func(*CreateParams){
		"short sha256":  func(p *CreateParams) { p.ModelSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abc" }, // 63 chars
		"bad url":       func(p *CreateParams) { p.ModelURL = "http://example.com/model.gguf" },
		"empty alias":   func(p *CreateParams) { p.Alias = "" },
		"zero ctx":      func(p *CreateParams) { p.Ctx = 0 },
		"zero parallel": func(p *CreateParams) { p.Parallel = 0 },
		"short deadman": func(p *CreateParams) { p.DeadMan = 59 * time.Second },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mutate(&p)
			if _, err := BuildCreate(p); err == nil {
				t.Errorf("BuildCreate() error = nil, want an error")
			}
		})
	}
}
