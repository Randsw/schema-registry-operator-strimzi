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

package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// T-11: the kustomize and Helm deployment paths must not silently diverge on
// security-relevant fields. The chart and config/ are edited independently;
// a hardening change landing in only one of them leaves half the installs
// outside the PSS restricted profile. The source test below is dependency-free
// and always runs; the render test compares real `kubectl kustomize` and
// `helm template` output and skips only when those binaries are absent.

// deploymentSecurity is the normalized subset both deploy paths must agree on.
type deploymentSecurity struct {
	podRunAsNonRoot                   bool
	podSeccompType                    string
	containerAllowPrivilegeEscalation bool
	containerReadOnlyRootFilesystem   bool
	containerRunAsNonRoot             bool
	capabilitiesDrop                  []string
	metricsSecure                     bool
	tmpEmptyDir                       bool
}

// chartValuesSecurity is the subset of values.yaml the chart renders into the
// Deployment. Pointers distinguish "unset" from an explicit false.
type chartValuesSecurity struct {
	PodSecurityContext struct {
		RunAsNonRoot   *bool `yaml:"runAsNonRoot"`
		SeccompProfile struct {
			Type string `yaml:"type"`
		} `yaml:"seccompProfile"`
	} `yaml:"podSecurityContext"`
	Image struct {
		SecurityContext struct {
			AllowPrivilegeEscalation *bool `yaml:"allowPrivilegeEscalation"`
			ReadOnlyRootFilesystem   *bool `yaml:"readOnlyRootFilesystem"`
			RunAsNonRoot             *bool `yaml:"runAsNonRoot"`
			Capabilities             struct {
				Drop []string `yaml:"drop"`
			} `yaml:"capabilities"`
		} `yaml:"securityContext"`
	} `yaml:"image"`
	Metrics struct {
		Secure *bool `yaml:"secure"`
	} `yaml:"metrics"`
}

// findDeploymentDoc returns the first Deployment document from a rendered
// stream (kustomize, helm or the raw manager manifest).
func findDeploymentDoc(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	for _, doc := range docs {
		if doc["kind"] == "Deployment" {
			return doc
		}
	}
	t.Fatalf("no Deployment in the rendered manifests:\n%s", renderedSummary(docs))
	return nil
}

// securityFromDeployment normalizes the security-relevant fields of a
// Deployment object map.
func securityFromDeployment(t *testing.T, dep map[string]any) deploymentSecurity {
	t.Helper()
	spec, _ := dep["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	if podSpec == nil {
		t.Fatal("Deployment has no pod spec")
	}
	podSC, _ := podSpec["securityContext"].(map[string]any)
	seccomp, _ := podSC["seccompProfile"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("Deployment has no containers")
	}
	container, _ := containers[0].(map[string]any)
	containerSC, _ := container["securityContext"].(map[string]any)
	caps, _ := containerSC["capabilities"].(map[string]any)
	dropRaw, _ := caps["drop"].([]any)

	drop := make([]string, 0, len(dropRaw))
	for _, d := range dropRaw {
		if s, ok := d.(string); ok {
			drop = append(drop, s)
		}
	}
	slices.Sort(drop)

	return deploymentSecurity{
		podRunAsNonRoot:                   boolValue(podSC["runAsNonRoot"]),
		podSeccompType:                    stringValue(seccomp["type"]),
		containerAllowPrivilegeEscalation: boolValue(containerSC["allowPrivilegeEscalation"]),
		containerReadOnlyRootFilesystem:   boolValue(containerSC["readOnlyRootFilesystem"]),
		containerRunAsNonRoot:             boolValue(containerSC["runAsNonRoot"]),
		capabilitiesDrop:                  drop,
		// --metrics-secure defaults to true in cmd/main.go, so an absent flag
		// is secure; only an explicit false opts out.
		metricsSecure: metricsFlag(containerArgs(t, dep)) != "false",
		tmpEmptyDir:   hasTmpEmptyDir(podSpec),
	}
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

// metricsFlag returns the value of --metrics-secure when present ("true" or
// "false"), and "unset" otherwise.
func metricsFlag(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--metrics-secure=") {
			return strings.TrimPrefix(arg, "--metrics-secure=")
		}
	}
	return "unset"
}

// securityFromValues derives what the chart renders from values.yaml. /tmp is
// provisioned by the deployment template (not configurable), so it is read
// from the template source.
func securityFromValues(t *testing.T, valuesData, deploymentTemplate []byte) deploymentSecurity {
	t.Helper()
	var values chartValuesSecurity
	if err := sigsyaml.Unmarshal(valuesData, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}
	secure := values.Metrics.Secure != nil && *values.Metrics.Secure
	tpl := string(deploymentTemplate)
	return deploymentSecurity{
		podRunAsNonRoot:                   values.PodSecurityContext.RunAsNonRoot != nil && *values.PodSecurityContext.RunAsNonRoot,
		podSeccompType:                    values.PodSecurityContext.SeccompProfile.Type,
		containerAllowPrivilegeEscalation: values.Image.SecurityContext.AllowPrivilegeEscalation != nil && *values.Image.SecurityContext.AllowPrivilegeEscalation,
		containerReadOnlyRootFilesystem:   values.Image.SecurityContext.ReadOnlyRootFilesystem != nil && *values.Image.SecurityContext.ReadOnlyRootFilesystem,
		containerRunAsNonRoot:             values.Image.SecurityContext.RunAsNonRoot != nil && *values.Image.SecurityContext.RunAsNonRoot,
		capabilitiesDrop:                  append([]string(nil), values.Image.SecurityContext.Capabilities.Drop...),
		metricsSecure:                     secure,
		tmpEmptyDir:                       strings.Contains(tpl, "mountPath: /tmp") && strings.Contains(tpl, "emptyDir:"),
	}
}

// assertDeploySecurityParity fails with a field-named message when the two
// deploy paths disagree.
func assertDeploySecurityParity(t *testing.T, leftPath, rightPath string, left, right deploymentSecurity) {
	t.Helper()
	slices.Sort(left.capabilitiesDrop)
	slices.Sort(right.capabilitiesDrop)

	check := func(field string, want, got any) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s differs between %s (%v) and %s (%v)", field, leftPath, want, rightPath, got)
		}
	}
	check("pod securityContext.runAsNonRoot", left.podRunAsNonRoot, right.podRunAsNonRoot)
	check("pod securityContext.seccompProfile.type", left.podSeccompType, right.podSeccompType)
	check("container securityContext.allowPrivilegeEscalation", left.containerAllowPrivilegeEscalation, right.containerAllowPrivilegeEscalation)
	check("container securityContext.readOnlyRootFilesystem", left.containerReadOnlyRootFilesystem, right.containerReadOnlyRootFilesystem)
	check("container securityContext.runAsNonRoot", left.containerRunAsNonRoot, right.containerRunAsNonRoot)
	check("container securityContext.capabilities.drop", left.capabilitiesDrop, right.capabilitiesDrop)
	check("metrics secure", left.metricsSecure, right.metricsSecure)
	check("/tmp emptyDir", left.tmpEmptyDir, right.tmpEmptyDir)

	if !left.podRunAsNonRoot || left.podSeccompType != "RuntimeDefault" ||
		left.containerAllowPrivilegeEscalation || !left.containerReadOnlyRootFilesystem ||
		!left.containerRunAsNonRoot || !slices.Contains(left.capabilitiesDrop, "ALL") ||
		!left.metricsSecure || !left.tmpEmptyDir {
		t.Errorf("%s does not meet the PSS restricted + secure-metrics contract: %+v", leftPath, left)
	}
}

// TestDeployPathsSourceSecurityParity always runs: it compares config/manager
// with the chart sources, so divergence fails the unit suite even where helm
// or kubectl is unavailable.
func TestDeployPathsSourceSecurityParity(t *testing.T) {
	root := repoRoot(t)

	managerData, err := os.ReadFile(filepath.Join(root, "config", "manager", "manager.yaml"))
	if err != nil {
		t.Fatalf("failed to read config/manager/manager.yaml: %v", err)
	}
	managerDeployment := findDeploymentDoc(t, renderedDocs(t, managerData))
	managerSecurity := securityFromDeployment(t, managerDeployment)

	valuesData, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}
	templateData, err := os.ReadFile(filepath.Join(chartDir(t), "templates", "deployment.yaml"))
	if err != nil {
		t.Fatalf("failed to read templates/deployment.yaml: %v", err)
	}
	valuesSecurity := securityFromValues(t, valuesData, templateData)

	assertDeploySecurityParity(t, "config/manager/manager.yaml", "helm-chart/ssr-operator/values.yaml", managerSecurity, valuesSecurity)
}

// kustomizeAvailable reports whether a kubectl new enough to embed kustomize
// v4+ is on PATH. The repository's kustomization uses `patches` with
// path/target, which kustomize v3 and older reject, so an old kubectl (for
// example the v1.19 shipped by some local toolchains) cannot render
// config/default. The render test skips in that case and in environments
// without kubectl, matching the helm rendering tests; the source test above
// still guards the contract unconditionally.
func kustomizeAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("kubectl"); err != nil {
		return false
	}
	out, err := exec.Command("kubectl", "version", "--client").CombinedOutput()
	if err != nil {
		return false
	}
	minorMatch := regexp.MustCompile(`Minor:\s*"?(\d+)`).FindSubmatch(out)
	if len(minorMatch) < 2 {
		return false
	}
	minor, err := strconv.Atoi(string(minorMatch[1]))
	if err != nil {
		return false
	}
	return minor >= 21
}

// TestRenderedDeployPathsSecurityParity renders both deploy paths and diffs
// the security fields of their Deployments.
func TestRenderedDeployPathsSecurityParity(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	if !kustomizeAvailable(t) {
		t.Skip("kubectl missing or older than v1.21 (kustomize v4+ needed for `patches`); skipping kustomize rendering test")
	}

	out, err := exec.Command("kubectl", "kustomize", filepath.Join(repoRoot(t), "config", "default")).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl kustomize config/default failed: %v\n%s", err, out)
	}
	kustomizeSecurity := securityFromDeployment(t, findDeploymentDoc(t, renderedDocs(t, out)))

	helmDocs := renderedDocs(t, renderChart(t))
	helmSecurity := securityFromDeployment(t, findDeploymentDoc(t, helmDocs))

	assertDeploySecurityParity(t, "config/default (kustomize)", "helm-chart/ssr-operator (helm)", kustomizeSecurity, helmSecurity)
}
