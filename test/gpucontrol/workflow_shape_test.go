package gpucontrol

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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
				MonthlyUSD struct {
					Type     string `yaml:"type"`
					Required bool   `yaml:"required"`
				} `yaml:"monthly_usd"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Environment    string            `yaml:"environment"`
		Permissions    map[string]string `yaml:"permissions"`
		Steps          []struct {
			Uses string         `yaml:"uses"`
			With map[string]any `yaml:"with"`
			Run  string         `yaml:"run"`
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
	if want := []string{"start", "status", "stop", "budget"}; !slices.Equal(action.Options, want) {
		t.Errorf("action options = %v, want %v", action.Options, want)
	}
	if m := doc.On.WorkflowDispatch.Inputs.MonthlyUSD; m.Type != "string" || m.Required {
		t.Errorf("monthly_usd input = %+v, want an optional string", m)
	}

	if len(doc.Jobs) != 1 {
		t.Fatalf("job count = %d, want 1", len(doc.Jobs))
	}
	for _, job := range doc.Jobs {
		if job.TimeoutMinutes != 25 {
			t.Errorf("timeout-minutes = %d, want 25", job.TimeoutMinutes)
		}
		if job.Environment != "gpu-control" {
			t.Errorf("environment = %q, want gpu-control", job.Environment)
		}
		if job.Permissions["contents"] != "read" || job.Permissions["id-token"] != "write" || len(job.Permissions) != 2 {
			t.Errorf("job permissions = %v, want {contents: read, id-token: write}", job.Permissions)
		}

		var usesSteps []string
		checkoutSHA := regexp.MustCompile(`^actions/checkout@[0-9a-f]{40}$`)
		awsCredsSHA := regexp.MustCompile(`^aws-actions/configure-aws-credentials@[0-9a-f]{40}$`)
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			usesSteps = append(usesSteps, step.Uses)
			switch {
			case checkoutSHA.MatchString(step.Uses):
				if pc, ok := step.With["persist-credentials"]; !ok || pc != false {
					t.Errorf("checkout step persist-credentials = %v, want false", pc)
				}
			case awsCredsSHA.MatchString(step.Uses):
				if step.With["role-to-assume"] != "${{ vars.AWS_ROLE_ARN }}" {
					t.Errorf("configure-aws-credentials role-to-assume = %v, want ${{ vars.AWS_ROLE_ARN }}", step.With["role-to-assume"])
				}
				if step.With["aws-region"] != "eu-west-1" {
					t.Errorf("configure-aws-credentials aws-region = %v, want eu-west-1", step.With["aws-region"])
				}
			default:
				t.Errorf("uses = %q, want actions/checkout@<sha> or aws-actions/configure-aws-credentials@<sha>", step.Uses)
			}
		}
		if len(usesSteps) != 2 {
			t.Errorf("uses: steps = %v, want exactly 2 (checkout, configure-aws-credentials)", usesSteps)
		}

		var ssmSteps []string
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "--with-decryption") && strings.Contains(step.Run, "/vk-hetzner-lab/persistent/llm/management_tokens") {
				ssmSteps = append(ssmSteps, step.Run)
			}
		}
		if len(ssmSteps) != 1 {
			t.Fatalf("SSM-reading run: steps = %d, want exactly 1", len(ssmSteps))
		}
		run := ssmSteps[0]
		maskIdx := strings.Index(run, "::add-mask::")
		envIdx := strings.Index(run, "GITHUB_ENV")
		if maskIdx < 0 {
			t.Errorf("SSM step run missing ::add-mask::")
		}
		if envIdx < 0 {
			t.Errorf("SSM step run missing GITHUB_ENV")
		}
		if maskIdx >= 0 && envIdx >= 0 && maskIdx >= envIdx {
			t.Errorf("::add-mask:: (index %d) must come before GITHUB_ENV (index %d)", maskIdx, envIdx)
		}
		// A second line would be written to GITHUB_ENV unmasked, so the step
		// must refuse a multi-line value before it masks or exports anything.
		guardIdx := strings.Index(run, "more than one line")
		if guardIdx < 0 || (maskIdx >= 0 && guardIdx >= maskIdx) {
			t.Errorf("SSM step must reject a multi-line value before ::add-mask:: (guard index %d, mask index %d)", guardIdx, maskIdx)
		}
	}
}
