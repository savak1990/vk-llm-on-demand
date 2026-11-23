package deploydev

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type deployWorkflow struct {
	Jobs map[string]struct {
		Needs          []string          `yaml:"needs"`
		Environment    string            `yaml:"environment"`
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Permissions    map[string]string `yaml:"permissions"`
		Steps          []struct {
			Uses string            `yaml:"uses"`
			With map[string]any    `yaml:"with"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestDeployDevJobShape(t *testing.T) {
	wd, _ := os.Getwd()
	raw, err := os.ReadFile(filepath.Join(wd, "..", "..", ".github", "workflows", "deploy.yml"))
	if err != nil {
		t.Fatalf("reading deploy.yml: %v", err)
	}
	var doc deployWorkflow
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing deploy.yml: %v", err)
	}
	job, ok := doc.Jobs["deploy-dev"]
	if !ok {
		t.Fatal("deploy.yml has no deploy-dev job")
	}
	if !slices.Equal(job.Needs, []string{"version", "publish"}) {
		t.Errorf("needs = %v, want [version publish]", job.Needs)
	}
	if job.Environment != "gpu-control" {
		t.Errorf("environment = %q, want gpu-control (the only Environment vk-llm-ci-role trusts)", job.Environment)
	}
	if job.Permissions["contents"] != "read" || job.Permissions["id-token"] != "write" || len(job.Permissions) != 2 {
		t.Errorf("permissions = %v, want {contents: read, id-token: write}", job.Permissions)
	}
	var helm, run string
	for _, s := range job.Steps {
		if strings.HasPrefix(s.Uses, "azure/setup-helm@") {
			helm, _ = s.With["version"].(string)
		}
		if strings.Contains(s.Run, "scripts/deploy-dev.sh") {
			run = s.Run
		}
	}
	if helm != "v4.3.0" {
		t.Errorf("setup-helm version = %q, want v4.3.0 (--rollback-on-failure)", helm)
	}
	for _, want := range []string{"PROJECT_NAME", `"$status" -eq 2`, "::warning::", "exit \"$status\""} {
		if !strings.Contains(run, want) {
			t.Errorf("deploy step run lacks %q", want)
		}
	}
	if strings.Contains(run, "make ") {
		t.Errorf("deploy step must call the script directly: make turns every failure into exit 2")
	}
}
