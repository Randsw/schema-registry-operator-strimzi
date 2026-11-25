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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestChartAppVersionMatchesDefaultImageTag guards the image-tag
// consistency contract end to end: the operator image the chart installs
// by default must be the released operator version carried in appVersion.
// values.yaml may pin an explicit image.tag — then it must equal appVersion;
// when it is unset the Deployment template must fall back to
// .Chart.AppVersion, so a stale appVersion directly changes the deployed
// operator binary (release automation keeps appVersion in sync with the
// released operator version, see .releaserc.yaml).
func TestChartAppVersionMatchesDefaultImageTag(t *testing.T) {
	root := repoRoot(t)

	chartData, err := os.ReadFile(filepath.Join(root, "helm-chart", "ssr-operator", "Chart.yaml"))
	if err != nil {
		t.Fatalf("failed to read Chart.yaml: %v", err)
	}
	var chart struct {
		AppVersion string `yaml:"appVersion"`
	}
	if err := sigsyaml.Unmarshal(chartData, &chart); err != nil {
		t.Fatalf("failed to parse Chart.yaml: %v", err)
	}
	appVersion := strings.TrimSpace(chart.AppVersion)
	if appVersion == "" {
		t.Fatal("Chart.yaml appVersion is empty; the default operator image tag would be empty")
	}
	// A concrete version (semver, prerelease allowed) — never a mutable tag.
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(appVersion) {
		t.Errorf("Chart.yaml appVersion = %q, want a concrete semver (X.Y.Z[-prerelease]) matching the released operator version", appVersion)
	}

	valuesData, err := os.ReadFile(filepath.Join(root, "helm-chart", "ssr-operator", "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}
	var values struct {
		Image struct {
			Tag string `yaml:"tag"`
		} `yaml:"image"`
	}
	if err := sigsyaml.Unmarshal(valuesData, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}
	if tag := strings.TrimSpace(values.Image.Tag); tag != "" {
		if tag != appVersion {
			t.Errorf("values.yaml image.tag = %q does not match Chart.yaml appVersion = %q; the chart would install an operator binary mismatched with its appVersion", tag, appVersion)
		}
		return
	}

	// tag unset: the Deployment template must fall back to .Chart.AppVersion.
	depData, err := os.ReadFile(filepath.Join(root, "helm-chart", "ssr-operator", "templates", "deployment.yaml"))
	if err != nil {
		t.Fatalf("failed to read templates/deployment.yaml: %v", err)
	}
	if !strings.Contains(string(depData), "{{ .Values.image.tag | default .Chart.AppVersion }}") {
		t.Error("templates/deployment.yaml must default the image tag to .Chart.AppVersion so the chart installs the operator version carried in appVersion")
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

// renderChart renders the chart with `helm template` under the canonical
// "ssr-operator" release name in the default namespace, with the given --set
// flags, returning the rendered multi-document YAML.
func renderChart(t *testing.T, sets ...string) []byte {
	t.Helper()
	return renderChartAs(t, "ssr-operator", "", sets...)
}

// renderChartAs renders the chart with `helm template` under an explicit
// release name and, when non-empty, an explicit --namespace, with the given
// --set flags. The release name and namespace drive ssr-operator.fullname and
// .Release.Namespace, which the naming tests vary to prove resource names are
// not pinned to a single install.
func renderChartAs(t *testing.T, release, namespace string, sets ...string) []byte {
	t.Helper()
	args := []string{"template", release, chartDir(t)}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	args = append(args, sets...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %v failed: %v\n%s", args, err, out)
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
// and patch on events for BOTH the core ("") and the events.k8s.io API
// groups. The operator records events.k8s.io/v1 Events (mgr.GetEventRecorder)
// alongside core events, and the API server rejects the new-style Events with
// an empty action, so both groups must be granted exactly as the generated
// config/rbac/role.yaml does (controller-gen emits them as a single rule).
func grantsEvents(rules []any) bool {
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		groups, _ := rule["apiGroups"].([]any)
		resources, _ := rule["resources"].([]any)
		verbs, _ := rule["verbs"].([]any)
		if !anyListContains(groups, "") || !anyListContains(groups, "events.k8s.io") || !anyListContains(resources, "events") {
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

// containerArgs returns the first container's args from a rendered Deployment.
func containerArgs(t *testing.T, dep map[string]any) []string {
	t.Helper()
	spec, _ := dep["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("rendered Deployment has no containers")
	}
	container, _ := containers[0].(map[string]any)
	raw, _ := container["args"].([]any)
	args := make([]string, 0, len(raw))
	for _, a := range raw {
		s, _ := a.(string)
		args = append(args, s)
	}
	return args
}

// hasTmpEmptyDir reports whether the rendered pod spec backs /tmp with an
// emptyDir volume mounted into one of its containers. With
// readOnlyRootFilesystem the container has no other writable path.
func hasTmpEmptyDir(podSpec map[string]any) bool {
	volumes, _ := podSpec["volumes"].([]any)
	containers, _ := podSpec["containers"].([]any)
	for _, v := range volumes {
		vol, _ := v.(map[string]any)
		if _, ok := vol["emptyDir"]; !ok {
			continue
		}
		name, _ := vol["name"].(string)
		if name == "" {
			continue
		}
		for _, c := range containers {
			container, _ := c.(map[string]any)
			mounts, _ := container["volumeMounts"].([]any)
			for _, m := range mounts {
				mount, _ := m.(map[string]any)
				if mount["name"] == name && mount["mountPath"] == "/tmp" {
					return true
				}
			}
		}
	}
	return false
}

// TestValuesManagerRoleGrantsEvents guards the events RBAC contract at the
// values level: the manager role must grant create/patch on events for both
// the core and the events.k8s.io API groups. The operator emits events
// (Recorder.Eventf) in the watched CR's namespace, which in the default
// cluster-scoped mode can be any namespace; without the core rule every
// event outside the release namespace fails with RBAC Forbidden, and without
// the events.k8s.io rule the events.k8s.io/v1 Events the manager recorder
// emits fail the same way — the operator's signaling is silently lost. The
// rule must mirror config/rbac/role.yaml, where controller-gen grants both
// groups in a single rule (TestChartManagerRoleMatchesGeneratedRBAC enforces
// that parity for the rendered chart).
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
			slices.Contains(rule.APIGroups, "events.k8s.io") &&
			slices.Contains(rule.Resources, "events") &&
			slices.Contains(rule.Verbs, "create") &&
			slices.Contains(rule.Verbs, "patch") {
			return // contract satisfied
		}
	}
	t.Errorf("values.yaml manager-role rules grant no create/patch on events for both the core and events.k8s.io groups; rules: %+v", manager.Rules)
}

// TestRenderedManagerRoleGrantsEvents verifies the events rule (core and
// events.k8s.io groups, create/patch) survives rendering in BOTH renderings
// of templates/clusterrole.yaml: the default cluster-scoped ClusterRole and
// the namespaced Role rendered when rbac.namespaceScoped=true.
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

// TestAutoscalingValuesRemoved guards the dead-values contract: the chart
// has no HPA template, so autoscaling values are dead — setting
// autoscaling.enabled=true would silently drop spec.replicas from the
// Deployment (the API default of 1 applies) and nothing would ever
// autoscale the operator. The values block and the deployment guard must
// stay removed; replicaCount covers manual scaling of the leader-elected,
// lightweight operator.
func TestAutoscalingValuesRemoved(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}
	var values map[string]any
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}
	if _, ok := values["autoscaling"]; ok {
		t.Error("values.yaml still defines autoscaling; the chart has no HPA template, so the values are dead and autoscaling.enabled=true would silently drop spec.replicas")
	}

	depSrc, err := os.ReadFile(filepath.Join(chartDir(t), "templates", "deployment.yaml"))
	if err != nil {
		t.Fatalf("failed to read templates/deployment.yaml: %v", err)
	}
	src := string(depSrc)
	if strings.Contains(src, "autoscaling") {
		t.Error("templates/deployment.yaml still references autoscaling; remove the guard so spec.replicas is always rendered")
	}
	if !strings.Contains(src, "replicas: {{ .Values.replicaCount }}") {
		t.Error("templates/deployment.yaml must render spec.replicas unconditionally from .Values.replicaCount")
	}
}

// TestRenderedDeploymentAlwaysSetsReplicas verifies the guard removal end
// to end: spec.replicas is rendered from replicaCount in every rendering
// of the Deployment instead of being silently dropped.
func TestRenderedDeploymentAlwaysSetsReplicas(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
	}{
		{name: "default values", sets: nil},
		{name: "rbac.namespaceScoped=true", sets: []string{"--set", "rbac.namespaceScoped=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderedDocs(t, renderChart(t, tc.sets...))
			dep := findRendered(t, docs, "Deployment", "")
			spec, _ := dep["spec"].(map[string]any)
			replicas, ok := spec["replicas"].(float64)
			if !ok {
				t.Fatal("rendered Deployment has no numeric spec.replicas; the autoscaling guard must stay removed so replicas always render from replicaCount")
			}
			if replicas != 1 {
				t.Errorf("rendered Deployment spec.replicas = %v, want 1 (the replicaCount default)", replicas)
			}
		})
	}
}

// TestValuesPodSecurityStandardsRestricted guards the PSS restricted
// hardening at the values level: on clusters enforcing the restricted Pod
// Security Standard the operator Deployment is rejected unless the pod
// runs as non-root with the RuntimeDefault seccomp profile and the
// container drops all Linux capabilities and keeps privilege escalation
// disabled.
func TestValuesPodSecurityStandardsRestricted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}

	var values struct {
		Image struct {
			SecurityContext struct {
				AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation"`
				ReadOnlyRootFilesystem   *bool `json:"readOnlyRootFilesystem"`
				RunAsNonRoot             *bool `json:"runAsNonRoot"`
				Capabilities             struct {
					Drop []string `json:"drop"`
				} `json:"capabilities"`
			} `json:"securityContext"`
		} `json:"image"`
		PodSecurityContext struct {
			RunAsNonRoot   *bool `json:"runAsNonRoot"`
			SeccompProfile struct {
				Type string `json:"type"`
			} `json:"seccompProfile"`
		} `json:"podSecurityContext"`
		Metrics struct {
			Secure *bool `json:"secure"`
		} `json:"metrics"`
	}
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}

	if values.Image.SecurityContext.AllowPrivilegeEscalation == nil || *values.Image.SecurityContext.AllowPrivilegeEscalation {
		t.Error("image.securityContext.allowPrivilegeEscalation must be false for the PSS restricted standard")
	}
	if !slices.Contains(values.Image.SecurityContext.Capabilities.Drop, "ALL") {
		t.Errorf("image.securityContext.capabilities.drop = %v, want it to include ALL for the PSS restricted standard; the operator needs no Linux capabilities", values.Image.SecurityContext.Capabilities.Drop)
	}
	if values.Image.SecurityContext.ReadOnlyRootFilesystem == nil || !*values.Image.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("image.securityContext.readOnlyRootFilesystem must be true for the PSS restricted standard; the operator writes only to the /tmp emptyDir")
	}
	if values.Image.SecurityContext.RunAsNonRoot == nil || !*values.Image.SecurityContext.RunAsNonRoot {
		t.Error("image.securityContext.runAsNonRoot must be true so the container stays non-root even if the pod-level setting is overridden")
	}
	if values.Metrics.Secure == nil || !*values.Metrics.Secure {
		t.Error("metrics.secure must default to true so the operator metrics endpoint requires authentication and authorization; plain HTTP requires an explicit metrics.secure=false opt-out")
	}
	if values.PodSecurityContext.RunAsNonRoot == nil || !*values.PodSecurityContext.RunAsNonRoot {
		t.Error("podSecurityContext.runAsNonRoot must be true for the PSS restricted standard")
	}
	if values.PodSecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Errorf("podSecurityContext.seccompProfile.type = %q, want RuntimeDefault for the PSS restricted standard", values.PodSecurityContext.SeccompProfile.Type)
	}
}

// TestRenderedDeploymentPodSecurityRestricted verifies the PSS restricted
// hardening survives rendering: the pod spec carries
// seccompProfile.type=RuntimeDefault and the operator container drops all
// Linux capabilities, in both RBAC renderings of the chart.
func TestRenderedDeploymentPodSecurityRestricted(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
	}{
		{name: "default values", sets: nil},
		{name: "rbac.namespaceScoped=true", sets: []string{"--set", "rbac.namespaceScoped=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderedDocs(t, renderChart(t, tc.sets...))
			dep := findRendered(t, docs, "Deployment", "")
			spec, _ := dep["spec"].(map[string]any)
			template, _ := spec["template"].(map[string]any)
			podSpec, _ := template["spec"].(map[string]any)

			podSC, _ := podSpec["securityContext"].(map[string]any)
			seccomp, _ := podSC["seccompProfile"].(map[string]any)
			if seccomp["type"] != "RuntimeDefault" {
				t.Errorf("rendered pod securityContext.seccompProfile.type = %v, want RuntimeDefault", seccomp["type"])
			}

			containers, _ := podSpec["containers"].([]any)
			if len(containers) == 0 {
				t.Fatal("rendered Deployment has no containers")
			}
			container, _ := containers[0].(map[string]any)
			containerSC, _ := container["securityContext"].(map[string]any)
			caps, _ := containerSC["capabilities"].(map[string]any)
			drop, _ := caps["drop"].([]any)
			if !anyListContains(drop, "ALL") {
				t.Errorf("rendered container securityContext.capabilities.drop = %v, want it to include ALL", drop)
			}
			if ro, ok := containerSC["readOnlyRootFilesystem"].(bool); !ok || !ro {
				t.Errorf("rendered container readOnlyRootFilesystem = %v, want true", containerSC["readOnlyRootFilesystem"])
			}
			if nonRoot, ok := containerSC["runAsNonRoot"].(bool); !ok || !nonRoot {
				t.Errorf("rendered container runAsNonRoot = %v, want true", containerSC["runAsNonRoot"])
			}
			if !hasTmpEmptyDir(podSpec) {
				t.Error("rendered pod must mount a writable emptyDir at /tmp; with readOnlyRootFilesystem the container has no other writable path")
			}
		})
	}
}

// TestRenderedDeploymentMetricsSecureDefaultAndOptOut locks the
// secure-by-default metrics contract: the chart passes --metrics-secure=true
// unless the operator explicitly opts out, and the documented opt-out renders
// plain HTTP metrics.
func TestRenderedDeploymentMetricsSecureDefaultAndOptOut(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
		want string
	}{
		{name: "secure by default", sets: nil, want: "--metrics-secure=true"},
		{name: "documented opt-out", sets: []string{"--set", "metrics.secure=false"}, want: "--metrics-secure=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderedDocs(t, renderChart(t, tc.sets...))
			dep := findRendered(t, docs, "Deployment", "")
			args := containerArgs(t, dep)
			if !slices.Contains(args, tc.want) {
				t.Errorf("rendered container args = %v, want them to contain %q", args, tc.want)
			}
		})
	}
}

// TestManagerConfigConfigMapRemoved guards the dead-resource contract: the
// Deployment never mounted or referenced the manager-config ConfigMap (its
// args are hardcoded flags), so the template shipped a dead resource on
// every install. The template must stay deleted and no template may
// reference the ConfigMap again.
func TestManagerConfigConfigMapRemoved(t *testing.T) {
	cmPath := filepath.Join(chartDir(t), "templates", "cm.yaml")
	if _, err := os.Stat(cmPath); err == nil {
		t.Errorf("%s still exists; the Deployment never mounts the manager-config ConfigMap (args are hardcoded flags), so the dead template must stay deleted", cmPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("failed to stat %s: %v", cmPath, err)
	}

	matches, err := filepath.Glob(filepath.Join(chartDir(t), "templates", "*"))
	if err != nil {
		t.Fatalf("failed to scan chart templates: %v", err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read %s: %v", path, err)
		}
		if strings.Contains(string(data), "manager-config") {
			t.Errorf("%s references the removed manager-config ConfigMap", path)
		}
	}
}

// TestRenderedChartHasNoManagerConfigConfigMap verifies the removal end to
// end: the chart renders no ConfigMap at all (the manager-config ConfigMap
// was its only one), in both RBAC renderings.
func TestRenderedChartHasNoManagerConfigConfigMap(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
	}{
		{name: "default values", sets: nil},
		{name: "rbac.namespaceScoped=true", sets: []string{"--set", "rbac.namespaceScoped=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderedDocs(t, renderChart(t, tc.sets...))
			for _, doc := range docs {
				if doc["kind"] == "ConfigMap" {
					t.Errorf("chart still renders a ConfigMap; the dead manager-config ConfigMap must stay removed:\n%s", renderedSummary(docs))
				}
			}
		})
	}
}

// TestChartCRDMatchesGeneratedCRD guards the CRD-sync contract: the chart's
// crds/ copy must be byte-identical to the generated CRD in
// config/crd/bases. A drifted copy ships a chart whose CRD schema disagrees
// with the operator binary (new spec fields silently dropped or validation
// mismatched). `make manifests` syncs the copy via helm-crd-sync; this test
// fails the unit suite if the copy is ever left stale.
func TestChartCRDMatchesGeneratedCRD(t *testing.T) {
	root := repoRoot(t)
	genPath := filepath.Join(root, "config", "crd", "bases", crdFileName)
	chartPath := filepath.Join(root, "helm-chart", "ssr-operator", "crds", crdFileName)

	gen, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("failed to read generated CRD %s: %v", genPath, err)
	}
	chartCRD, err := os.ReadFile(chartPath)
	if err != nil {
		t.Fatalf("failed to read chart CRD %s: %v", chartPath, err)
	}
	if !bytes.Equal(gen, chartCRD) {
		t.Errorf("chart CRD %s differs from the generated CRD %s; run 'make helm-crd-sync' to refresh the chart copy", chartPath, genPath)
	}
}

// TestMakefileSyncsChartCRD guards the CRD-sync contract at the build
// level: the Makefile must provide a helm-crd-sync target that copies the
// generated CRD into the chart's crds/ directory, and the manifests target
// (which regenerates config/crd/bases) must invoke it so a regeneration
// always syncs the chart copy.
func TestMakefileSyncsChartCRD(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("failed to read Makefile: %v", err)
	}
	src := string(data)

	if !strings.Contains(src, "helm-crd-sync:") {
		t.Error("Makefile must define a helm-crd-sync target that syncs the chart CRD copy")
	}
	if !strings.Contains(src, "cp config/crd/bases/$(CRD_FILE) helm-chart/ssr-operator/crds/$(CRD_FILE)") {
		t.Error("Makefile helm-crd-sync target must copy config/crd/bases into helm-chart/ssr-operator/crds")
	}

	// Extract the manifests recipe (target line plus its tab-indented
	// recipe lines) and require the sync invocation in it.
	var recipe []string
	inManifests := false
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "manifests: controller-gen") {
			inManifests = true
			recipe = append(recipe, line)
			continue
		}
		if inManifests {
			if strings.HasPrefix(line, "\t") {
				recipe = append(recipe, line)
				continue
			}
			break
		}
	}
	if len(recipe) == 0 {
		t.Fatal("Makefile manifests target not found")
	}
	if !strings.Contains(strings.Join(recipe, "\n"), "helm-crd-sync") {
		t.Error("Makefile manifests target must invoke helm-crd-sync after regenerating the CRD so the chart copy always matches")
	}
}

// rbacRuleKey renders a decoded RBAC rule as a canonical,
// order-insensitive string so rules can be compared as sets.
func rbacRuleKey(t *testing.T, rule any) string {
	t.Helper()
	m, ok := rule.(map[string]any)
	if !ok {
		t.Fatalf("unexpected RBAC rule shape: %#v", rule)
	}
	join := func(field string) string {
		list, _ := m[field].([]any)
		parts := make([]string, 0, len(list))
		for _, v := range list {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		slices.Sort(parts)
		return strings.Join(parts, ",")
	}
	return fmt.Sprintf("apiGroups=[%s] nonResourceURLs=[%s] resources=[%s] verbs=[%s]",
		join("apiGroups"), join("nonResourceURLs"), join("resources"), join("verbs"))
}

// TestChartManagerRoleMatchesGeneratedRBAC guards the RBAC parity contract:
// the chart's rendered manager ClusterRole (default values) must mirror
// config/rbac/role.yaml — the generated role is what controller-gen produces
// from the kubebuilder markers, and the chart must never silently drift from
// it (the events rule was missing from the chart exactly this way). Rules
// are compared as order-insensitive sets in both directions. The chart's
// manager role currently mirrors the generated role exactly; if a
// legitimate intentional difference ever appears (for example a chart-only
// rule), document it here with a comment and an explicit allowlist instead
// of weakening the comparison silently.
func TestChartManagerRoleMatchesGeneratedRBAC(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}

	// Generated role (source of truth).
	genData, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatalf("failed to read config/rbac/role.yaml: %v", err)
	}
	var generated struct {
		Rules []any `yaml:"rules"`
	}
	if err := sigsyaml.Unmarshal(genData, &generated); err != nil {
		t.Fatalf("failed to parse config/rbac/role.yaml: %v", err)
	}
	if len(generated.Rules) == 0 {
		t.Fatal("config/rbac/role.yaml has no rules")
	}

	// Chart manager ClusterRole (default values).
	docs := renderedDocs(t, renderChart(t))
	role := findRendered(t, docs, "ClusterRole", "-manager-role")
	rules, _ := role["rules"].([]any)

	chartRules := make(map[string]bool, len(rules))
	for _, r := range rules {
		chartRules[rbacRuleKey(t, r)] = true
	}
	genRules := make(map[string]bool, len(generated.Rules))
	for _, r := range generated.Rules {
		genRules[rbacRuleKey(t, r)] = true
	}

	for key := range genRules {
		if !chartRules[key] {
			t.Errorf("chart manager role is missing a rule granted by config/rbac/role.yaml: %s", key)
		}
	}
	for key := range chartRules {
		if !genRules[key] {
			t.Errorf("chart manager role grants a rule config/rbac/role.yaml does not (keep the chart in sync with the generated role, or document the intentional difference here): %s", key)
		}
	}
}

// TestGeneratedRBACOmitsUnusedPatchVerbs guards the least-privilege contract
// (audit M-1): the controller never patches Deployments, Services, Secrets or
// NetworkPolicies (it creates, updates or deletes them), so the generated
// manager role must not grant the patch verb on those resources. An unused
// patch grant would let a compromised operator mutate pod specs or credentials
// field-by-field instead of only replacing whole objects it already owns.
func TestGeneratedRBACOmitsUnusedPatchVerbs(t *testing.T) {
	genData, err := os.ReadFile(filepath.Join(repoRoot(t), "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatalf("failed to read config/rbac/role.yaml: %v", err)
	}
	var generated struct {
		Rules []struct {
			APIGroups []string `yaml:"apiGroups"`
			Resources []string `yaml:"resources"`
			Verbs     []string `yaml:"verbs"`
		} `yaml:"rules"`
	}
	if err := sigsyaml.Unmarshal(genData, &generated); err != nil {
		t.Fatalf("failed to parse config/rbac/role.yaml: %v", err)
	}

	guarded := map[string]bool{
		"deployments":     true,
		"services":        true,
		"secrets":         true,
		"networkpolicies": true,
	}
	for _, rule := range generated.Rules {
		for _, resource := range rule.Resources {
			if !guarded[resource] {
				continue
			}
			if slices.Contains(rule.Verbs, "patch") {
				t.Errorf("generated manager role grants patch on %s (apiGroups %v), but the controller only creates/updates/deletes it; drop the verb from the kubebuilder marker", resource, rule.APIGroups)
			}
		}
	}
}

// metaName returns the metadata.name of a rendered object.
func metaName(t *testing.T, doc map[string]any) string {
	t.Helper()
	meta, _ := doc["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	return name
}

// renderedNames returns the metadata.name of every rendered object of the
// given kind.
func renderedNames(t *testing.T, docs []map[string]any, kind string) []string {
	t.Helper()
	var names []string
	for _, doc := range docs {
		if doc["kind"] == kind {
			names = append(names, metaName(t, doc))
		}
	}
	return names
}

// TestValuesNamingOverridesEmpty guards the multi-install naming contract at
// the values level: nameOverride and serviceAccount.name must stay empty so
// resource names derive from ssr-operator.fullname (release-name based) and
// the ServiceAccount falls back to it. The old hardcoded
// "strimzi-schema-registry-operator" overrides pinned identical names across
// releases and broke multi-namespace installs.
func TestValuesNamingOverridesEmpty(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}
	var values struct {
		NameOverride     string `json:"nameOverride"`
		FullnameOverride string `json:"fullnameOverride"`
		ServiceAccount   struct {
			Name string `json:"name"`
		} `json:"serviceAccount"`
	}
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}
	if strings.TrimSpace(values.NameOverride) != "" {
		t.Errorf("values.yaml nameOverride = %q, want empty so resource names derive from the release name and vary per install", values.NameOverride)
	}
	if strings.TrimSpace(values.ServiceAccount.Name) != "" {
		t.Errorf("values.yaml serviceAccount.name = %q, want empty so _helpers.tpl falls back to ssr-operator.fullname", values.ServiceAccount.Name)
	}
}

// TestRenderedNamesDeriveFromFullname guards the multi-install naming
// contract end to end: the Deployment, the metrics Service and the
// ServiceAccount must be named from ssr-operator.fullname — which varies with
// the Helm release name — instead of the chart-wide ssr-operator.name. With
// the old nameOverride-pinned names, two installs of the chart in different
// namespaces produced identically named namespaced resources and the second
// install failed with Helm ownership/already-exists errors.
func TestRenderedNamesDeriveFromFullname(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name       string
		release    string
		wantPrefix string
	}{
		// A release named after the chart collapses to the release name.
		{name: "release named after the chart", release: "ssr-operator", wantPrefix: "ssr-operator"},
		// Any other release name is suffixed with the chart name.
		{name: "arbitrary release name", release: "registry-two", wantPrefix: "registry-two-ssr-operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := renderedDocs(t, renderChartAs(t, tc.release, ""))
			dep := findRendered(t, docs, "Deployment", "")
			if got := metaName(t, dep); got != tc.wantPrefix {
				t.Errorf("rendered Deployment name = %q, want the fullname %q", got, tc.wantPrefix)
			}
			svc := findRendered(t, docs, "Service", "")
			if got := metaName(t, svc); got != tc.wantPrefix+"-metrics-service" {
				t.Errorf("rendered Service name = %q, want the fullname-derived %q", got, tc.wantPrefix+"-metrics-service")
			}
			sa := findRendered(t, docs, "ServiceAccount", "")
			if got := metaName(t, sa); got != tc.wantPrefix {
				t.Errorf("rendered ServiceAccount name = %q, want the fullname %q", got, tc.wantPrefix)
			}
		})
	}
}

// assertBindingsReferenceRenderedRoles fails the test when a rendered
// RoleBinding/ClusterRoleBinding references a role that the same rendering
// does not contain (wrong kind or wrong name): role templates and their
// binding templates must keep their names internally consistent.
func assertBindingsReferenceRenderedRoles(t *testing.T, docs []map[string]any) {
	t.Helper()
	roles := make(map[string]bool) // "Kind/Name" of every rendered role
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind == "Role" || kind == "ClusterRole" {
			roles[kind+"/"+metaName(t, doc)] = true
		}
	}
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind != "RoleBinding" && kind != "ClusterRoleBinding" {
			continue
		}
		roleRef, _ := doc["roleRef"].(map[string]any)
		refKind, _ := roleRef["kind"].(string)
		refName, _ := roleRef["name"].(string)
		if !roles[refKind+"/"+refName] {
			t.Errorf("rendered %s %q references %s %q, which the same rendering does not contain:\n%s", kind, metaName(t, doc), refKind, refName, renderedSummary(docs))
		}
	}
}

// TestRenderedClusterScopedRBACNamesNeverCollide guards the multi-namespace
// install contract: ClusterRole and ClusterRoleBinding objects live outside
// any namespace, so their names must embed the release namespace — two
// installs of the chart in different namespaces (the primary use case of
// rbac.namespaceScoped=true) must render disjoint cluster-scoped RBAC names,
// and every binding must reference the role rendered by the same release.
func TestRenderedClusterScopedRBACNamesNeverCollide(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
	}{
		{name: "default cluster-scoped mode", sets: nil},
		{name: "rbac.namespaceScoped=true", sets: []string{"--set", "rbac.namespaceScoped=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Same release name in two namespaces: only the embedded
			// namespace can keep the cluster-scoped names apart.
			docsA := renderedDocs(t, renderChartAs(t, "ssr-operator", "team-a", tc.sets...))
			docsB := renderedDocs(t, renderChartAs(t, "ssr-operator", "team-b", tc.sets...))
			for _, kind := range []string{"ClusterRole", "ClusterRoleBinding"} {
				namesA := renderedNames(t, docsA, kind)
				namesB := renderedNames(t, docsB, kind)
				if len(namesA) == 0 {
					t.Fatalf("no %s rendered in namespace team-a:\n%s", kind, renderedSummary(docsA))
				}
				for _, n := range namesA {
					if !strings.Contains(n, "team-a") {
						t.Errorf("cluster-scoped %s name %q does not embed the release namespace", kind, n)
					}
					if slices.Contains(namesB, n) {
						t.Errorf("two installs in different namespaces both render %s %q; cluster-scoped names must be namespace-qualified", kind, n)
					}
				}
			}

			// Different release names in different namespaces must stay
			// disjoint as well.
			docsC := renderedDocs(t, renderChartAs(t, "registry-two", "team-c", tc.sets...))
			for _, kind := range []string{"ClusterRole", "ClusterRoleBinding"} {
				for _, n := range renderedNames(t, docsC, kind) {
					if slices.Contains(renderedNames(t, docsA, kind), n) {
						t.Errorf("releases with different names both render %s %q", kind, n)
					}
				}
			}

			// Role and binding names must stay internally consistent.
			assertBindingsReferenceRenderedRoles(t, docsA)
		})
	}
}

// TestRenderedServiceAccountNameFallsBackToFullname guards the
// serviceAccount.name contract end to end: the default (empty) name must fall
// back to ssr-operator.fullname so the ServiceAccount name varies per release
// like every other namespaced resource, while an explicit override stays
// honored — including in the RBAC binding subjects, which must keep
// referencing the ServiceAccount the Deployment actually runs as.
func TestRenderedServiceAccountNameFallsBackToFullname(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}

	// Default values: serviceAccount.name is empty and falls back to fullname.
	docs := renderedDocs(t, renderChart(t))
	sa := findRendered(t, docs, "ServiceAccount", "")
	if got := metaName(t, sa); got != "ssr-operator" {
		t.Errorf("rendered ServiceAccount name = %q, want the fullname %q when serviceAccount.name is empty", got, "ssr-operator")
	}

	// An explicit override is honored by the ServiceAccount and by every
	// binding subject.
	docs = renderedDocs(t, renderChart(t, "--set", "serviceAccount.name=custom-sa"))
	sa = findRendered(t, docs, "ServiceAccount", "")
	if got := metaName(t, sa); got != "custom-sa" {
		t.Errorf("rendered ServiceAccount name = %q, want the explicit override %q", got, "custom-sa")
	}
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind != "RoleBinding" && kind != "ClusterRoleBinding" {
			continue
		}
		subjects, _ := doc["subjects"].([]any)
		if len(subjects) == 0 {
			t.Fatalf("rendered %s has no subjects", kind)
		}
		subject, _ := subjects[0].(map[string]any)
		if got, _ := subject["name"].(string); got != "custom-sa" {
			t.Errorf("rendered %s subject name = %q, want the overridden ServiceAccount %q", kind, got, "custom-sa")
		}
	}
}

// bindingReferencesRole reports whether a rendered RoleBinding or
// ClusterRoleBinding references a role whose name contains nameSuffix.
func bindingReferencesRole(t *testing.T, docs []map[string]any, nameSuffix string) bool {
	t.Helper()
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind != "RoleBinding" && kind != "ClusterRoleBinding" {
			continue
		}
		roleRef, _ := doc["roleRef"].(map[string]any)
		refName, _ := roleRef["name"].(string)
		if strings.Contains(refName, nameSuffix) {
			return true
		}
	}
	return false
}

// valuesOverrideFile writes a copy of the chart's default values with the
// given mutation applied and returns its path. --set cannot flip a single
// entry of the cluster_roles/roles lists: Helm replaces the whole list,
// dropping the other entries (and with them the very binding flag under
// test), so the mutation must go through a values file.
func valuesOverrideFile(t *testing.T, mutate func(values map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}
	var values map[string]any
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}
	mutate(values)
	out, err := sigsyaml.Marshal(values)
	if err != nil {
		t.Fatalf("failed to re-marshal values: %v", err)
	}
	path := filepath.Join(t.TempDir(), "values-override.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("failed to write values override: %v", err)
	}
	return path
}

// disableListEntry flips enabled=false on the first entry of the named
// values list (cluster_roles or roles) while keeping every other field —
// in particular the entry's binding flag — and every other entry intact.
func disableListEntry(t *testing.T, values map[string]any, key string) {
	t.Helper()
	entries, ok := values[key].([]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("values.yaml %s is not a non-empty list", key)
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("values.yaml %s[0] is not a mapping", key)
	}
	entry["enabled"] = false
}

// TestRenderedBindingsGateOnRoleEnabled guards the binding-gating contract:
// a RoleBinding/ClusterRoleBinding must render only when BOTH the role's
// enabled flag and the binding flag are true. A binding rendered while its
// role is disabled references a role that was never created and is rejected
// by the API server, breaking the whole install.
func TestRenderedBindingsGateOnRoleEnabled(t *testing.T) {
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	for _, tc := range []struct {
		name string
		sets []string
	}{
		{name: "default cluster-scoped mode", sets: nil},
		{name: "rbac.namespaceScoped=true", sets: []string{"--set", "rbac.namespaceScoped=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Control: with the default values every enabled role with a
			// binding flag renders its binding, and roles with binding=false
			// (metrics-reader) render none.
			docs := renderedDocs(t, renderChartAs(t, "ssr-operator", "", tc.sets...))
			if !bindingReferencesRole(t, docs, "manager-role") {
				t.Errorf("enabled manager role with cluster_role_binding=true renders no binding:\n%s", renderedSummary(docs))
			}
			if !bindingReferencesRole(t, docs, "leader-election-role") {
				t.Errorf("enabled leader-election role with role_binding=true renders no binding:\n%s", renderedSummary(docs))
			}
			if bindingReferencesRole(t, docs, "metrics-reader") {
				t.Errorf("metrics-reader with cluster_role_binding=false renders a binding:\n%s", renderedSummary(docs))
			}

			// A cluster_roles entry disabled while its binding flag stays
			// true: neither the role nor a binding referencing it may render.
			override := valuesOverrideFile(t, func(values map[string]any) {
				disableListEntry(t, values, "cluster_roles")
			})
			docs = renderedDocs(t, renderChartAs(t, "ssr-operator", "", append(slices.Clone(tc.sets), "-f", override)...))
			if bindingReferencesRole(t, docs, "manager-role") {
				t.Errorf("disabled manager role still has a binding referencing it:\n%s", renderedSummary(docs))
			}

			// A roles entry disabled while its binding flag stays true.
			override = valuesOverrideFile(t, func(values map[string]any) {
				disableListEntry(t, values, "roles")
			})
			docs = renderedDocs(t, renderChartAs(t, "ssr-operator", "", append(slices.Clone(tc.sets), "-f", override)...))
			if bindingReferencesRole(t, docs, "leader-election-role") {
				t.Errorf("disabled leader-election role still has a binding referencing it:\n%s", renderedSummary(docs))
			}
		})
	}
}

// TestValuesMemoryDefaultsCoverReconciliationHeadroom guards the memory
// contract: certificate generation (self-signed REST API certificates and
// pure-Go JKS/PKCS12 keystore encoding) is bursty work on top of the Go
// runtime, and the old 128Mi limit left no headroom for those spikes — the
// operator pod gets OOMKilled (exit 137) mid-reconciliation. The defaults
// must stay at a 512Mi limit / 256Mi request (the plan's 256Mi–512Mi range),
// and the Deployment must render them into the operator container.
func TestValuesMemoryDefaultsCoverReconciliationHeadroom(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatalf("failed to read values.yaml: %v", err)
	}
	var values struct {
		Image struct {
			Resources struct {
				Limits   map[string]string `json:"limits"`
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"image"`
	}
	if err := sigsyaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("failed to parse values.yaml: %v", err)
	}
	if got := values.Image.Resources.Limits["memory"]; got != "512Mi" {
		t.Errorf("values.yaml image.resources.limits.memory = %q, want 512Mi: a smaller limit leaves no headroom for the bursty certificate-generation workload and OOMKills the pod mid-reconciliation", got)
	}
	if got := values.Image.Resources.Requests["memory"]; got != "256Mi" {
		t.Errorf("values.yaml image.resources.requests.memory = %q, want 256Mi", got)
	}

	// The defaults must survive rendering into the operator container.
	if !helmAvailable(t) {
		t.Skip("helm not installed; skipping helm template rendering test")
	}
	docs := renderedDocs(t, renderChart(t))
	dep := findRendered(t, docs, "Deployment", "")
	spec, _ := dep["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("rendered Deployment has no containers")
	}
	container, _ := containers[0].(map[string]any)
	resources, _ := container["resources"].(map[string]any)
	limits, _ := resources["limits"].(map[string]any)
	requests, _ := resources["requests"].(map[string]any)
	if got, _ := limits["memory"].(string); got != "512Mi" {
		t.Errorf("rendered Deployment container resources.limits.memory = %v, want 512Mi (the values.yaml default)", limits["memory"])
	}
	if got, _ := requests["memory"].(string); got != "256Mi" {
		t.Errorf("rendered Deployment container resources.requests.memory = %v, want 256Mi (the values.yaml default)", requests["memory"])
	}
}
