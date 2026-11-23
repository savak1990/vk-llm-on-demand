package deploydev

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// needsList accepts GitHub Actions' two spellings of `needs`: a bare job name
// for one dependency, or a sequence for several.
type needsList []string

func (n *needsList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*n = needsList{node.Value}
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return err
		}
		*n = needsList(list)
	default:
		return fmt.Errorf("needs: unexpected yaml node kind %v", node.Kind)
	}
	return nil
}

type deployWorkflow struct {
	Jobs map[string]struct {
		Needs          needsList         `yaml:"needs"`
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
	if !slices.Equal(job.Needs, needsList{"version", "publish"}) {
		t.Errorf("needs = %v, want [version publish]", job.Needs)
	}
	if job.Environment != "gpu-control" {
		t.Errorf("environment = %q, want gpu-control (the only Environment vk-llm-ci-role trusts)", job.Environment)
	}
	if job.TimeoutMinutes != 15 {
		t.Errorf("timeout-minutes = %d, want 15", job.TimeoutMinutes)
	}
	if job.Permissions["contents"] != "read" || job.Permissions["id-token"] != "write" || len(job.Permissions) != 2 {
		t.Errorf("permissions = %v, want {contents: read, id-token: write}", job.Permissions)
	}
	var helm, run string
	var env map[string]string
	for _, s := range job.Steps {
		if strings.HasPrefix(s.Uses, "azure/setup-helm@") {
			helm, _ = s.With["version"].(string)
		}
		if strings.Contains(s.Run, "scripts/deploy-dev.sh") {
			run = s.Run
			env = s.Env
		}
	}
	if helm != "v4.3.0" {
		t.Errorf("setup-helm version = %q, want v4.3.0 (--rollback-on-failure)", helm)
	}
	if env["PROJECT_NAME"] == "" {
		t.Error("deploy step env lacks PROJECT_NAME")
	}
	for _, want := range []string{`"$status" -eq 2`, "::warning::", "exit \"$status\""} {
		if !strings.Contains(run, want) {
			t.Errorf("deploy step run lacks %q", want)
		}
	}
	for _, line := range strings.Split(run, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "make ") {
			t.Errorf("deploy step must call the script directly: make turns every failure into exit 2")
		}
	}
}
