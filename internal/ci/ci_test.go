/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package ci guards the GitHub Actions gating contracts that control when
// operator images are published. The tests parse the workflow sources
// directly so a gating regression (for example a skipped optional E2E job
// silently skipping the image-publishing job) fails the unit suite instead
// of surfacing as a missing image on a feature branch.
package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// repoRoot locates the repository root relative to this file so the tests
// work regardless of the working directory go test is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("failed to resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repository root (no go.mod found)")
		}
		dir = parent
	}
}

// stringSlice accepts either a YAML string or a list of strings for the
// job "needs" field (GitHub Actions allows both spellings).
type stringSlice []string

func (s *stringSlice) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// workflow is the subset of the GitHub Actions workflow schema the tests
// inspect.
type workflow struct {
	Jobs map[string]struct {
		If    string      `yaml:"if"`
		Needs stringSlice `yaml:"needs"`
	} `yaml:"jobs"`
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatalf("failed to read workflow %s: %v", name, err)
	}
	var wf workflow
	if err := sigsyaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("failed to parse workflow %s: %v", name, err)
	}
	return wf
}

// TestDeployJobRunsWhenOptionalE2EIsSkipped guards the branch-publishing
// contract: on feature/fix branch pushes the E2E job is intentionally
// skipped (e2e_gate), and the Deploy job — which needs E2E — must still run.
// Without an explicit needs.E2E.result == 'skipped' allowance in the job's
// if expression, GitHub Actions skips a dependent job when a needed job is
// skipped, so the branch image was never published. A failed E2E must still
// block publication.
func TestDeployJobRunsWhenOptionalE2EIsSkipped(t *testing.T) {
	wf := loadWorkflow(t, "test-release.yaml")

	deploy, ok := wf.Jobs["Deploy"]
	if !ok {
		t.Fatal("Deploy job missing from test-release.yaml")
	}

	needsE2E := false
	for _, n := range deploy.Needs {
		if n == "E2E" {
			needsE2E = true
		}
	}
	if !needsE2E {
		t.Error("Deploy job must still need the E2E job so a failed E2E blocks publication")
	}

	if !strings.Contains(deploy.If, "needs.E2E.result == 'skipped'") {
		t.Error("Deploy job if expression must allow a skipped E2E (needs.E2E.result == 'skipped'); otherwise a skipped optional E2E skips the image publication too")
	}
	if !strings.Contains(deploy.If, "needs.E2E.result == 'success'") {
		t.Error("Deploy job if expression must require a successful E2E when E2E runs")
	}
}

// TestSemanticReleaseKeepsStrictE2EGate guards the release gate: the
// Semantic-Release job runs only on main/develop pushes where E2E always
// runs, so its if expression must NOT gain the skipped-E2E allowance — the
// release must stay blocked unless E2E actually passed.
func TestSemanticReleaseKeepsStrictE2EGate(t *testing.T) {
	wf := loadWorkflow(t, "test-release.yaml")

	release, ok := wf.Jobs["Semantic-Release"]
	if !ok {
		t.Fatal("Semantic-Release job missing from test-release.yaml")
	}

	if strings.Contains(release.If, "needs.E2E.result == 'skipped'") {
		t.Error("Semantic-Release job must not allow a skipped E2E; the release gate must stay strict")
	}
}
