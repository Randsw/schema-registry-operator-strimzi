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
