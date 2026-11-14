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

// releaserc is the subset of the semantic-release configuration the tests
// inspect.
type releaserc struct {
	Prepare []struct {
		Path   string   `yaml:"path"`
		Cmd    string   `yaml:"cmd"`
		Assets []string `yaml:"assets"`
	} `yaml:"prepare"`
}

func loadReleaserc(t *testing.T) releaserc {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".releaserc.yaml"))
	if err != nil {
		t.Fatalf("failed to read .releaserc.yaml: %v", err)
	}
	var rc releaserc
	if err := sigsyaml.Unmarshal(data, &rc); err != nil {
		t.Fatalf("failed to parse .releaserc.yaml: %v", err)
	}
	return rc
}

// TestReleasercSyncsChartAppVersion guards the appVersion-sync contract: the
// chart's default image tag falls back to Chart.yaml appVersion, so release
// automation must set appVersion to the released operator version on every
// release. Without it the chart ships an operator binary mismatched with the
// chart and its CRD (appVersion drifted to 1.17.0 while 1.17.1+ were out).
// The conditional chart version bump and the release commit assets must stay
// in place so the appVersion change is actually published.
func TestReleasercSyncsChartAppVersion(t *testing.T) {
	rc := loadReleaserc(t)

	var exec, git *struct {
		Path   string   `yaml:"path"`
		Cmd    string   `yaml:"cmd"`
		Assets []string `yaml:"assets"`
	}
	for i := range rc.Prepare {
		switch rc.Prepare[i].Path {
		case "@semantic-release/exec":
			exec = &rc.Prepare[i]
		case "@semantic-release/git":
			git = &rc.Prepare[i]
		}
	}
	if exec == nil {
		t.Fatal(".releaserc.yaml prepare has no @semantic-release/exec step")
	}
	if git == nil {
		t.Fatal(".releaserc.yaml prepare has no @semantic-release/git step")
	}

	// appVersion is set to the released operator version on every release
	// (semantic-release substitutes ${nextRelease.version} before exec).
	if !strings.Contains(exec.Cmd, "pybump set --file helm-chart/ssr-operator/Chart.yaml --app-version") {
		t.Error(".releaserc.yaml exec prepare step must set the chart appVersion (pybump set --app-version); otherwise the chart ships a stale operator image tag")
	}
	if !strings.Contains(exec.Cmd, "--set-version ${nextRelease.version}") {
		t.Error(".releaserc.yaml exec prepare step must set appVersion to ${nextRelease.version} (semantic-release substitution), not a hardcoded version")
	}

	// The conditional chart version bump must stay: the helm-chart workflow
	// publishes a new chart package only when Chart.yaml changes.
	if !strings.Contains(exec.Cmd, "pybump bump --file helm-chart/ssr-operator/Chart.yaml --level patch") {
		t.Error(".releaserc.yaml exec prepare step must keep the conditional chart version bump (pybump bump --level patch)")
	}

	// The appVersion change must be committed with the release.
	chartAsset := false
	for _, a := range git.Assets {
		if a == "helm-chart/ssr-operator/Chart.yaml" {
			chartAsset = true
		}
	}
	if !chartAsset {
		t.Error("@semantic-release/git assets must include helm-chart/ssr-operator/Chart.yaml so the appVersion sync is committed with the release")
	}
}

// workflowSteps is the subset of the GitHub Actions workflow schema needed
// to inspect job steps.
type workflowSteps struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
			With struct {
				Flavor string `yaml:"flavor"`
			} `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadWorkflowSteps(t *testing.T, name string) workflowSteps {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatalf("failed to read workflow %s: %v", name, err)
	}
	var wf workflowSteps
	if err := sigsyaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("failed to parse workflow %s: %v", name, err)
	}
	return wf
}

// metadataFlavor returns the docker/metadata-action flavor of the given
// job, failing the test if the job has no metadata step.
func metadataFlavor(t *testing.T, wf workflowSteps, job string) string {
	t.Helper()
	j, ok := wf.Jobs[job]
	if !ok {
		t.Fatalf("job %s missing from workflow", job)
	}
	for _, s := range j.Steps {
		if strings.Contains(s.Uses, "docker/metadata-action") {
			return s.With.Flavor
		}
	}
	t.Fatalf("job %s has no docker/metadata-action step", job)
	return ""
}

// TestDeployJobDoesNotOverwriteLatestTag guards the mutable-tag contract:
// the Deploy job of test-release.yaml runs on every feature/fix branch push,
// so it must not publish the mutable "latest" tag — otherwise any branch
// push overwrites ghcr.io/randsw/strimzi-schema-registry-operator:latest
// with unreviewed code. Branch tags (type=ref,event=branch) remain.
func TestDeployJobDoesNotOverwriteLatestTag(t *testing.T) {
	wf := loadWorkflowSteps(t, "test-release.yaml")
	if flavor := metadataFlavor(t, wf, "Deploy"); flavor != "latest=false" {
		t.Errorf("test-release.yaml Deploy job metadata flavor = %q, want latest=false; branch builds must not overwrite the mutable latest tag", flavor)
	}
}

// TestBuildWorkflowKeepsLatestTag guards the intended latest publisher: the
// tag-triggered build workflow is the only place that publishes the mutable
// "latest" tag (version-tag builds), so its metadata flavor must stay
// latest=true.
func TestBuildWorkflowKeepsLatestTag(t *testing.T) {
	wf := loadWorkflowSteps(t, "build.yaml")
	if flavor := metadataFlavor(t, wf, "Deploy"); flavor != "latest=true" {
		t.Errorf("build.yaml Deploy job metadata flavor = %q, want latest=true; the tag-triggered build is the intended latest publisher", flavor)
	}
}

// TestHelmChartWorkflowInstallsStrimziCRDsBeforeInstall guards the ct
// install contract: the chart install test runs on a bare kind cluster, and
// the operator watches kafka.strimzi.io Kafkas/KafkaUsers — without the
// Strimzi CRDs its informers can never sync, so ct install would pass while
// validating only template rendering (an idle controller with reflector
// errors). A Strimzi CRD install step (pinned version) must exist before
// the ct install step, gated by the same changed-helm-files condition.
func TestHelmChartWorkflowInstallsStrimziCRDsBeforeInstall(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "helm-chart.yaml"))
	if err != nil {
		t.Fatalf("failed to read workflow helm-chart.yaml: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := sigsyaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("failed to parse workflow helm-chart.yaml: %v", err)
	}
	job, ok := wf.Jobs["Lint-test"]
	if !ok {
		t.Fatal("Lint-test job missing from helm-chart.yaml")
	}

	ctInstallIdx, crdIdx := -1, -1
	var ctInstallIf string
	for i, s := range job.Steps {
		if strings.Contains(s.Run, "ct install") {
			ctInstallIdx = i
			ctInstallIf = s.If
		}
		if strings.Contains(s.Run, "helm show crds") &&
			strings.Contains(s.Run, "strimzi-kafka-operator") &&
			strings.Contains(s.Run, "kubectl apply") {
			crdIdx = i
		}
	}
	if ctInstallIdx < 0 {
		t.Fatal("helm-chart.yaml has no ct install step")
	}
	if crdIdx < 0 {
		t.Fatal("helm-chart.yaml has no Strimzi CRD install step (helm show crds oci://quay.io/strimzi-helm/strimzi-kafka-operator | kubectl apply); without the Strimzi CRDs the operator informers can never sync and ct install validates only template rendering")
	}
	if crdIdx > ctInstallIdx {
		t.Errorf("Strimzi CRD install step (index %d) must run before the ct install step (index %d)", crdIdx, ctInstallIdx)
	}

	// The CRDs must be pinned so the install test is reproducible.
	crdStep := job.Steps[crdIdx]
	if !strings.Contains(crdStep.Run, "--version") {
		t.Error("Strimzi CRD install step must pin the chart version (--version) so the install test is reproducible")
	}
	// Same gating as the install test: no CRD install (and no failure) when
	// the install test itself is skipped.
	if ctInstallIf != "" && !strings.Contains(crdStep.If, "helm_any_changed") {
		t.Error("Strimzi CRD install step must be gated by the same changed-helm-files condition as the ct install step")
	}
}

// TestDependabotDockerEntriesHaveDockerfiles guards the dependabot
// configuration: a docker ecosystem entry pointing at a directory without a
// Dockerfile can never produce updates and logs errors on every run (the
// helm-chart/ssr-operator entry was exactly that). Every docker entry must
// point at a directory containing a Dockerfile.
func TestDependabotDockerEntriesHaveDockerfiles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "dependabot.yml"))
	if err != nil {
		t.Fatalf("failed to read .github/dependabot.yml: %v", err)
	}
	// sigs.k8s.io/yaml decodes with encoding/json semantics, so the
	// hyphenated keys must be tagged with json names (yaml tags are not
	// matched for keys containing separators).
	var deps struct {
		Updates []struct {
			PackageEcosystem string `json:"package-ecosystem"`
			Directory        string `json:"directory"`
		} `json:"updates"`
	}
	if err := sigsyaml.Unmarshal(data, &deps); err != nil {
		t.Fatalf("failed to parse .github/dependabot.yml: %v", err)
	}

	dockerEntries := 0
	for _, u := range deps.Updates {
		if u.PackageEcosystem != "docker" {
			continue
		}
		dockerEntries++
		dir := filepath.Join(repoRoot(t), filepath.FromSlash(u.Directory))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("docker ecosystem entry directory %q cannot be read: %v", u.Directory, err)
		}
		hasDockerfile := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "Dockerfile") {
				hasDockerfile = true
				break
			}
		}
		if !hasDockerfile {
			t.Errorf("docker ecosystem entry points at %q which contains no Dockerfile; dependabot can never produce updates there and logs errors on every run", u.Directory)
		}
	}
	if dockerEntries == 0 {
		t.Error(".github/dependabot.yml has no docker ecosystem entry; the root Dockerfile must stay covered")
	}
}
