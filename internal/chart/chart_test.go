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

// Package chart guards the Helm chart layout contracts that protect
// production data. The tests read the chart sources directly so a layout
// regression (for example moving the CRD back under templates/) fails the
// unit suite instead of surfacing as data loss on `helm uninstall`.
package chart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

const crdFileName = "strimziregistryoperator.randsw.code_strimzischemaregistries.yaml"

// TestCRDInstalledFromCrdsDirectory guards the CRD uninstall-safety contract:
// the CRD must live in the chart's crds/ directory, which Helm installs
// before templates and never upgrades or deletes. A CRD under templates/ is
// an ordinary release resource: `helm uninstall` would delete it and, with
// it, every StrimziSchemaRegistry custom resource cluster-wide.
func TestCRDInstalledFromCrdsDirectory(t *testing.T) {
	root := repoRoot(t)
	chartDir := filepath.Join(root, "helm-chart", "ssr-operator")

	crdPath := filepath.Join(chartDir, "crds", crdFileName)
	data, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("CRD must be installed from the chart's crds/ directory; %s is missing: %v", crdPath, err)
	}

	var crd struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := sigsyaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("failed to parse %s: %v", crdPath, err)
	}
	if crd.Kind != "CustomResourceDefinition" || crd.APIVersion != "apiextensions.k8s.io/v1" {
		t.Errorf("%s is not a CustomResourceDefinition: kind=%q apiVersion=%q", crdPath, crd.Kind, crd.APIVersion)
	}
	if crd.Metadata.Name != "strimzischemaregistries.strimziregistryoperator.randsw.code" {
		t.Errorf("unexpected CRD name %q in %s", crd.Metadata.Name, crdPath)
	}

	// No CRD may remain under templates/: files there are release resources
	// and would be deleted on uninstall.
	matches, err := filepath.Glob(filepath.Join(chartDir, "templates", "crds", "*.yaml"))
	if err != nil {
		t.Fatalf("failed to scan templates/crds: %v", err)
	}
	if len(matches) > 0 {
		t.Errorf("CRD file(s) found under templates/crds (release resources, deleted on uninstall): %v", matches)
	}
}

// TestValuesImageTagNotMutableLatest guards the reproducibility contract:
// the default operator image tag must not be the mutable "latest", which
// would make a chart install (or a pod replacement) run an operator binary
// mismatched with the chart and its CRD. The template falls back to
// .Chart.AppVersion when the tag is unset; an explicit override in the
// user's values stays allowed, only the chart default is pinned.
func TestValuesImageTagNotMutableLatest(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "helm-chart", "ssr-operator", "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}

	var values struct {
		Image struct {
			Repository string `yaml:"repository"`
			Tag        string `yaml:"tag"`
		} `yaml:"image"`
	}
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}

	if strings.EqualFold(strings.TrimSpace(values.Image.Tag), "latest") {
		t.Error("values.yaml image.tag defaults to the mutable \"latest\" tag; remove the default so the template falls back to .Chart.AppVersion")
	}
}

// TestChartAppVersionIsPinned guards the image-pinning contract end to end:
// the chart must carry a concrete appVersion (the operator image tag the
// chart installs by default) and it must not be a mutable tag.
func TestChartAppVersionIsPinned(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "helm-chart", "ssr-operator", "Chart.yaml"))
	if err != nil {
		t.Fatalf("failed to read Chart.yaml: %v", err)
	}

	var chart struct {
		AppVersion string `yaml:"appVersion"`
	}
	if err := sigsyaml.Unmarshal(data, &chart); err != nil {
		t.Fatalf("failed to parse Chart.yaml: %v", err)
	}

	if strings.TrimSpace(chart.AppVersion) == "" {
		t.Fatal("Chart.yaml appVersion is empty; the default operator image tag would be empty")
	}
	if strings.EqualFold(strings.TrimSpace(chart.AppVersion), "latest") {
		t.Error("Chart.yaml appVersion is the mutable \"latest\"; pin it to the released operator version")
	}
}

// chartDir returns the absolute path of the ssr-operator Helm chart.
func chartDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "helm-chart", "ssr-operator")
}

// helmAvailable reports whether the helm binary is on PATH. The rendering
// tests exercise real `helm template` output but skip (instead of fail) in
// environments without helm: the source-contract tests already pin the
// chart sources, and CI renders the chart in the dedicated helm workflow.
func helmAvailable(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("helm")
	return err == nil
}

// renderChart renders the chart with `helm template` and the given --set
// flags, returning the rendered multi-document YAML.
func renderChart(t *testing.T, sets ...string) []byte {
	t.Helper()
	args := append([]string{"template", "ssr-operator", chartDir(t)}, sets...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %v failed: %v\n%s", sets, err, out)
	}
	return out
}

// renderedDocs splits a rendered multi-document YAML stream (as produced by
// `helm template`) into one generic map per Kubernetes object.
func renderedDocs(t *testing.T, rendered []byte) []map[string]any {
	t.Helper()
	var docs []map[string]any
	var current []string
	flush := func() {
		doc := strings.TrimSpace(strings.Join(current, "\n"))
		current = nil
		if doc == "" {
			return
		}
		var obj map[string]any
		if err := sigsyaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("failed to parse rendered document: %v\n%s", err, doc)
		}
		if len(obj) > 0 {
			docs = append(docs, obj)
		}
	}
	for _, line := range strings.Split(string(rendered), "\n") {
		if strings.TrimSpace(line) == "---" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return docs
}

// findRendered returns the rendered object with the given kind whose
// metadata.name contains nameSuffix, failing the test if absent.
func findRendered(t *testing.T, docs []map[string]any, kind, nameSuffix string) map[string]any {
	t.Helper()
	for _, doc := range docs {
		if doc["kind"] != kind {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if strings.Contains(name, nameSuffix) {
			return doc
		}
	}
	t.Fatalf("no rendered %s with name containing %q; rendered objects:\n%s", kind, nameSuffix, renderedSummary(docs))
	return nil
}

// renderedSummary lists the kind/name of every rendered object for failure
// messages.
func renderedSummary(docs []map[string]any) string {
	var sb strings.Builder
	for _, doc := range docs {
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		fmt.Fprintf(&sb, "- %s/%s\n", doc["kind"], name)
	}
	return sb.String()
}

// grantsEvents reports whether one of the decoded RBAC rules grants create
// and patch on core ("") events.
func grantsEvents(rules []any) bool {
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		groups, _ := rule["apiGroups"].([]any)
		resources, _ := rule["resources"].([]any)
		verbs, _ := rule["verbs"].([]any)
		if !anyListContains(groups, "") || !anyListContains(resources, "events") {
			continue
		}
		if anyListContains(verbs, "create") && anyListContains(verbs, "patch") {
			return true
		}
	}
	return false
}

// anyListContains reports whether a decoded YAML list ([]any) contains the
// given string value.
func anyListContains(list []any, want string) bool {
	for _, v := range list {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// TestValuesManagerRoleGrantsEvents guards the events RBAC contract at the
// values level: the manager role must grant create/patch on core events.
// The operator emits events (Recorder.Eventf) in the watched CR's
// namespace, which in the default cluster-scoped mode can be any namespace;
// without this rule every event outside the release namespace fails with
// RBAC Forbidden and the operator's signaling is silently lost.
func TestValuesManagerRoleGrantsEvents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}

	// sigs.k8s.io/yaml decodes with encoding/json semantics (yaml struct
	// tags are ignored), so the fields are tagged with json names; the
	// underscored "cluster_roles" key cannot be matched case-insensitively.
	var values struct {
		ClusterRoles []struct {
			Name  string `json:"name"`
			Rules []struct {
				APIGroups []string `json:"apiGroups"`
				Resources []string `json:"resources"`
				Verbs     []string `json:"verbs"`
			} `json:"rules"`
		} `json:"cluster_roles"`
	}
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}

	var manager struct {
		Rules []struct {
			APIGroups []string `json:"apiGroups"`
			Resources []string `json:"resources"`
			Verbs     []string `json:"verbs"`
		} `json:"rules"`
	}
	found := false
	for _, role := range values.ClusterRoles {
		if role.Name == "manager-role" {
			manager.Rules = role.Rules
			found = true
			break
		}
	}
	if !found {
		t.Fatal("values.yaml cluster_roles has no manager-role entry")
	}

	for _, rule := range manager.Rules {
		if slices.Contains(rule.APIGroups, "") &&
			slices.Contains(rule.Resources, "events") &&
			slices.Contains(rule.Verbs, "create") &&
			slices.Contains(rule.Verbs, "patch") {
			return // contract satisfied
		}
	}
	t.Errorf("values.yaml manager-role rules grant no create/patch on core events; rules: %+v", manager.Rules)
}

// TestRenderedManagerRoleGrantsEvents verifies the events rule survives
// rendering in BOTH renderings of templates/clusterrole.yaml: the default
// cluster-scoped ClusterRole and the namespaced Role rendered when
// rbac.namespaceScoped=true.
func TestRenderedManagerRoleGrantsEvents(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
		kind string
	}{
		{name: "default cluster-scoped ClusterRole", sets: nil, kind: "ClusterRole"},
		{name: "namespaced Role (rbac.namespaceScoped=true)", sets: []string{"--set", "rbac.namespaceScoped=true"}, kind: "Role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderedDocs(t, renderChart(t, tc.sets...))
			role := findRendered(t, docs, tc.kind, "-manager-role")
			rules, _ := role["rules"].([]any)
			if !grantsEvents(rules) {
				t.Errorf("rendered %s manager role does not grant create/patch on core events; rules: %#v", tc.kind, rules)
			}
		})
	}
}
