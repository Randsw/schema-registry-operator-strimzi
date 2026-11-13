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
