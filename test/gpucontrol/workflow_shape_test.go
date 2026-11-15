package gpucontrol

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowDoc struct {
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	On struct {
		WorkflowDispatch struct {
			Inputs struct {
				Action struct {
					Type    string   `yaml:"type"`
					Options []string `yaml:"options"`
					Default string   `yaml:"default"`
				} `yaml:"action"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Permissions    map[string]string `yaml:"permissions"`
		Steps          []struct {
			Uses string         `yaml:"uses"`
			With map[string]any `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestWorkflowShape(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "..", "..", ".github", "workflows", "gpu-control.yml"))
	if err != nil {
		t.Fatalf("reading gpu-control.yml: %v", err)
	}
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing gpu-control.yml: %v", err)
	}

	if len(doc.Permissions) != 0 {
		t.Errorf("top-level permissions = %v, want {}", doc.Permissions)
	}
	if doc.Concurrency.Group != "gpu-control" || doc.Concurrency.CancelInProgress {
		t.Errorf("concurrency = %+v, want group=gpu-control cancel-in-progress=false", doc.Concurrency)
	}

	action := doc.On.WorkflowDispatch.Inputs.Action
	if action.Type != "choice" || action.Default != "status" {
		t.Errorf("action input = %+v, want type=choice default=status", action)
	}
	if want := []string{"start", "status", "stop"}; !equalSlices(action.Options, want) {
		t.Errorf("action options = %v, want %v", action.Options, want)
	}

	if len(doc.Jobs) != 1 {
		t.Fatalf("job count = %d, want 1", len(doc.Jobs))
	}
	for _, job := range doc.Jobs {
		if job.TimeoutMinutes != 25 {
			t.Errorf("timeout-minutes = %d, want 25", job.TimeoutMinutes)
		}
		if job.Permissions["contents"] != "read" || len(job.Permissions) != 1 {
			t.Errorf("job permissions = %v, want {contents: read}", job.Permissions)
		}
		var usesSteps int
		checkoutSHA := regexp.MustCompile(`^actions/checkout@[0-9a-f]{40}$`)
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			usesSteps++
			if !checkoutSHA.MatchString(step.Uses) {
				t.Errorf("uses = %q, want actions/checkout@<40-hex-sha>", step.Uses)
			}
			if pc, ok := step.With["persist-credentials"]; !ok || pc != false {
				t.Errorf("checkout step persist-credentials = %v, want false", pc)
			}
		}
		if usesSteps != 1 {
			t.Errorf("uses: steps = %d, want exactly 1", usesSteps)
		}
	}
}
