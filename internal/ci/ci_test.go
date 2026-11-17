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
// The chart version bump and the release commit assets must stay in place so
// the appVersion change is actually published.
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

	// The chart version bump must stay: every stable release that moves
	// appVersion must move the chart version too, or the helm-chart workflow
	// (which publishes a new chart package only when Chart.yaml version
	// changes) never publishes the updated default image tag.
	if !strings.Contains(exec.Cmd, "pybump bump --file helm-chart/ssr-operator/Chart.yaml --level patch") {
		t.Error(".releaserc.yaml exec prepare step must keep the chart version bump (pybump bump --level patch)")
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

// releasercExecCmd returns the code of the @semantic-release/exec prepare
// step's cmd (full-line script comments stripped), failing the test if the
// step is missing.
func releasercExecCmd(t *testing.T) string {
	t.Helper()
	rc := loadReleaserc(t)
	for i := range rc.Prepare {
		if rc.Prepare[i].Path != "@semantic-release/exec" {
			continue
		}
		var code []string
		for _, line := range strings.Split(rc.Prepare[i].Cmd, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			code = append(code, line)
		}
		return strings.Join(code, "\n")
	}
	t.Fatal(".releaserc.yaml prepare has no @semantic-release/exec step")
	return ""
}

// TestReleasercBumpsChartVersionOnEveryStableRelease guards the chart
// publication contract: the helm-chart workflow publishes a new chart
// package only when Chart.yaml version changes, so a stable (main) release
// that moves appVersion must always bump the chart version too — otherwise
// chart-releaser (skip_existing) skips the publication and Helm users stay
// on the old chart and its old default operator image. The bump must not be
// gated on tag resolution: develop prerelease tags become ancestors of main
// through merges, and the former git describe --tags / git diff gate let a
// merged prerelease tag shadow the last stable tag and suppress the bump on
// appVersion-only releases.
func TestReleasercBumpsChartVersionOnEveryStableRelease(t *testing.T) {
	cmd := releasercExecCmd(t)

	if !strings.Contains(cmd, "pybump bump --file helm-chart/ssr-operator/Chart.yaml --level patch") {
		t.Error(".releaserc.yaml exec prepare step must bump the chart version (pybump bump --level patch) so every appVersion change publishes a new chart package")
	}
	if strings.Contains(cmd, "git describe") {
		t.Error(".releaserc.yaml exec prepare step must not resolve the version-bump decision via git describe; develop prerelease tags merged into main shadow the last stable tag and suppressed the bump on appVersion-only releases")
	}
	if strings.Contains(cmd, "git diff --name-only") {
		t.Error(".releaserc.yaml exec prepare step must not gate the chart version bump on a git diff; an appVersion-only release must still bump the chart version so the new default image tag is published")
	}
}

// TestReleasercSkipsChartVersionBumpOnPrereleases guards the prerelease
// guard: develop prerelease versions (e.g. 1.19.1-develop.1) never publish a
// chart (the helm-chart workflow releases from main only), so the
// version-bump logic must branch on the release version's prerelease suffix
// and exclude prereleases from the stable-release bump path.
func TestReleasercSkipsChartVersionBumpOnPrereleases(t *testing.T) {
	cmd := releasercExecCmd(t)

	if !strings.Contains(cmd, "case \"${nextRelease.version}\" in") {
		t.Error(".releaserc.yaml exec prepare step must branch the version bump on the release version (case \"${nextRelease.version}\" in) so prereleases are excluded from the stable-release bump path")
	}
	if !strings.Contains(cmd, "*-*)") {
		t.Error(".releaserc.yaml exec prepare step must detect prerelease versions by their dash suffix (*-*) so develop prereleases never take the stable-release bump path")
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

// metadataActionStepBlock returns the configuration text of the
// docker/metadata-action step in the given workflow (full-line YAML
// comments stripped) so tests can assert on the whole step configuration,
// not only the fields the parsed structs expose.
func metadataActionStepBlock(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatalf("failed to read workflow %s: %v", name, err)
	}
	lines := strings.Split(string(data), "\n")
	usesIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "uses:") && strings.Contains(line, "docker/metadata-action") {
			usesIdx = i
			break
		}
	}
	if usesIdx < 0 {
		t.Fatalf("workflow %s has no docker/metadata-action step", name)
	}
	// Walk back to the start of the step (its "- name:" list item).
	start := usesIdx
	for i := usesIdx; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "- ") {
			start = i
			break
		}
	}
	// The step ends at the next non-empty line indented no deeper than the
	// list-item dash (the next step or job key).
	dashIndent := len(lines[start]) - len(strings.TrimLeft(lines[start], " "))
	var block []string
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if i > start {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if indent := len(line) - len(strings.TrimLeft(line, " ")); indent <= dashIndent {
				break
			}
		}
		// Strip full-line comments so assertions cover the step's actual
		// configuration rather than its documentation.
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		block = append(block, line)
	}
	return strings.Join(block, "\n")
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
// "latest" tag, and only for non-prerelease semver tags. latest=auto lets
// docker/metadata-action derive latest from the tag, so stable tags
// (1.19.0) publish :latest while prerelease tags cut from develop
// (1.19.1-develop.1, matched by the '*.*.*' trigger) do not — latest=true
// would force the mutable tag on every prerelease build and overwrite
// ghcr.io/randsw/strimzi-schema-registry-operator:latest with unreleased
// code.
func TestBuildWorkflowKeepsLatestTag(t *testing.T) {
	wf := loadWorkflowSteps(t, "build.yaml")
	if flavor := metadataFlavor(t, wf, "Deploy"); flavor != "latest=auto" {
		t.Errorf("build.yaml Deploy job metadata flavor = %q, want latest=auto; latest must be derived from the tag so prerelease builds do not overwrite it", flavor)
	}

	// latest=true must not appear anywhere in the metadata-action step: the
	// flavor is the only place the mutable tag is configured there, and
	// forcing it would republish :latest on prerelease builds too.
	if block := metadataActionStepBlock(t, "build.yaml"); strings.Contains(block, "latest=true") {
		t.Error("build.yaml metadata-action step must not contain latest=true; it forces the mutable latest tag on prerelease builds (use latest=auto)")
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

// TestDockerfileRuntimeStageIsDistrolessWithoutSubprocessTools guards the
// image contract: the manager binary is static (CGO_ENABLED=0) and
// certProcessor encodes the JKS/PKCS12 keystores in pure Go, so the runtime
// stage needs no package manager, JRE, or openssl. Pinning the distroless
// base here makes a regression (re-adding apt-get/default-jre/openssl
// install lines, or switching back to a full distro base to run subprocess
// tooling) fail CI instead of silently reintroducing the JVM startup
// overhead and the OOM risk the pure-Go keystore refactor eliminated.
func TestDockerfileRuntimeStageIsDistrolessWithoutSubprocessTools(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile"))
	if err != nil {
		t.Fatalf("failed to read Dockerfile: %v", err)
	}

	// The last FROM instruction is the runtime stage (the builder stage
	// cross-compiles on the build platform); it must be a distroless base.
	lastFrom := ""
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "FROM ") {
			lastFrom = trimmed
		}
	}
	if !strings.Contains(lastFrom, "distroless/") {
		t.Errorf("Dockerfile runtime stage %q must use a distroless base image (gcr.io/distroless/...): the manager is a static CGO-free binary and certProcessor encodes keystores in pure Go, so no package manager, JRE, or openssl is needed at runtime", lastFrom)
	}

	// No instruction line may (re)install subprocess tooling. Comment lines
	// are skipped so the stage comments can still explain why the tools are
	// gone; any executable instruction (RUN lines and their continuations,
	// COPY, ...) carrying one of these tokens is a regression.
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, banned := range []string{"apt-get", "default-jre", "openssl"} {
			if strings.Contains(trimmed, banned) {
				t.Errorf("Dockerfile instruction line %q contains %q; the operator image must stay subprocess-free (pure-Go keystore encoding on a distroless runtime)", trimmed, banned)
			}
		}
	}
}

// workflowTriggers is the subset of the GitHub Actions workflow schema
// needed to inspect the push/pull_request path filters. The top-level "on"
// key is resolved to the boolean true by the YAML 1.1 parser underneath
// sigs.k8s.io/yaml, so the decoded document carries it under the "true"
// key (with an "on" fallback should the resolver change).
type workflowTriggers struct {
	Push struct {
		Paths []string `json:"paths"`
	} `json:"push"`
	PullRequest struct {
		Paths []string `json:"paths"`
	} `json:"pull_request"`
}

func loadWorkflowTriggers(t *testing.T, name string) workflowTriggers {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatalf("failed to read workflow %s: %v", name, err)
	}
	// The document is decoded key-by-key: the other top-level keys (name,
	// jobs, ...) have unrelated shapes, so only the trigger document is
	// unmarshaled into the struct.
	var raw map[string]json.RawMessage
	if err := sigsyaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to parse workflow %s: %v", name, err)
	}
	doc, ok := raw["true"]
	if !ok {
		doc, ok = raw["on"]
	}
	if !ok {
		t.Fatalf("workflow %s has no push/pull_request triggers (on: key not found)", name)
	}
	var triggers workflowTriggers
	if err := json.Unmarshal(doc, &triggers); err != nil {
		t.Fatalf("failed to parse workflow %s triggers: %v", name, err)
	}
	return triggers
}

// TestTestReleasePathFiltersCoverConfigReleasercAndMakefile guards the
// trigger contract: config/** carries the CRD/RBAC manifests asserted by
// internal/chart/chart_test.go, .releaserc.yaml the release automation
// asserted by internal/ci/ci_test.go, and the Makefile the build/test
// entry points. A change touching only those files must trigger this
// workflow on pushes and pull requests instead of bypassing CI entirely.
func TestTestReleasePathFiltersCoverConfigReleasercAndMakefile(t *testing.T) {
	triggers := loadWorkflowTriggers(t, "test-release.yaml")

	for _, filter := range []struct {
		event string
		paths []string
	}{
		{"push", triggers.Push.Paths},
		{"pull_request", triggers.PullRequest.Paths},
	} {
		if len(filter.paths) == 0 {
			t.Fatalf("test-release.yaml %s trigger has no paths filter", filter.event)
		}
		for _, entry := range []string{"config/**", ".releaserc.yaml", "Makefile"} {
			found := false
			for _, p := range filter.paths {
				if p == entry {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("test-release.yaml %s paths filter is missing %q; changes to that file alone would bypass CI", filter.event, entry)
			}
		}
	}
}

// TestDeployAndSemanticReleaseJobsCarryExplicitGates guards the explicit
// job gates: Deploy and Semantic-Release previously relied on the implicit
// needs-skip cascade when a semantic-release commit ("chore(release):
// x.y.z") was pushed, which is fragile — the gates must be spelled out in
// the job if expressions (mirroring the Lint/e2e_gate style) so a release
// commit push can never re-publish a branch image or re-run a release.
func TestDeployAndSemanticReleaseJobsCarryExplicitGates(t *testing.T) {
	wf := loadWorkflow(t, "test-release.yaml")

	for _, name := range []string{"Deploy", "Semantic-Release"} {
		job, ok := wf.Jobs[name]
		if !ok {
			t.Fatalf("%s job missing from test-release.yaml", name)
		}
		needsTest := false
		for _, n := range job.Needs {
			if n == "Test" {
				needsTest = true
			}
		}
		if !needsTest {
			t.Errorf("%s job must need Test so needs.Test.result is a valid gate", name)
		}
		if !strings.Contains(job.If, "needs.Test.result == 'success'") {
			t.Errorf("%s job if expression must require a successful Test (needs.Test.result == 'success')", name)
		}
		if !strings.Contains(job.If, "!startsWith(github.event.head_commit.message, 'chore(release):')") {
			t.Errorf("%s job if expression must skip semantic-release commits (!startsWith(github.event.head_commit.message, 'chore(release):'))", name)
		}
	}
}

// TestE2EDiagnosticsReferenceCurrentOperatorDeploymentName guards the
// diagnostics collection: the chart rename derives resource names from the
// fullname template, so the e2e release "ssr-operator" deploys the operator
// as "ssr-operator" and the pre-rename strimzi-schema-registry-operator
// deployment no longer exists. A stale kubectl logs reference would
// silently collect nothing (the step tolerates failure with || true).
func TestE2EDiagnosticsReferenceCurrentOperatorDeploymentName(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "test-release.yaml"))
	if err != nil {
		t.Fatalf("failed to read workflow test-release.yaml: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "deployment/strimzi-schema-registry-operator") {
		t.Error("test-release.yaml still references deployment/strimzi-schema-registry-operator; the chart rename derives resource names from fullname (ssr-operator for the e2e release), so the diagnostics kubectl logs would silently collect nothing")
	}
	if !strings.Contains(content, "deployment/ssr-operator") {
		t.Error("test-release.yaml diagnostics must log the operator deployment via its fullname-derived name (deployment/ssr-operator for the e2e release name)")
	}
}
