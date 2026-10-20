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

package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newTestInstance creates a minimal StrimziSchemaRegistry for testing.
func newTestInstance() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
	return &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sr",
			Namespace: "default",
		},
		Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
			Listener:           "tls",
			SecurityProtocol:   "SSL",
			CompatibilityLevel: "backward",
			SecureHTTP:         false,
			HeapOpts:           "-Xms512M -Xmx512M",
			TLSSecretName:      "test-tls-secret",
			Template:           corev1.PodTemplateSpec{},
		},
	}
}

func TestComputeSpecHash(t *testing.T) {
	t.Run("same spec produces same hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 != hash2 {
			t.Errorf("expected same hash for identical specs: got %q and %q", hash1, hash2)
		}
		if hash1 == "" {
			t.Error("hash should not be empty")
		}
	})

	t.Run("different CompatibilityLevel produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.CompatibilityLevel = "forward"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different CompatibilityLevel: both %q", hash1)
		}
	})

	t.Run("different SecureHTTP produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.SecureHTTP = true

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different SecureHTTP: both %q", hash1)
		}
	})

	t.Run("different HeapOpts produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.HeapOpts = "-Xms1G -Xmx1G"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different HeapOpts: both %q", hash1)
		}
	})

	t.Run("different Listener produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.Listener = "plaintext"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different Listener: both %q", hash1)
		}
	})

	t.Run("different SecurityProtocol produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.SecurityProtocol = "PLAINTEXT"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different SecurityProtocol: both %q", hash1)
		}
	})

	t.Run("different TLSSecretName produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.TLSSecretName = "other-tls-secret"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different TLSSecretName: both %q", hash1)
		}
	})

	// B5 regression test: Template changes must affect the hash
	t.Run("different Template produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:7.6.5"},
				},
			},
		}
		inst2 := newTestInstance()
		inst2.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:7.7.0"},
				},
			},
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)

		if err1 != nil {
			t.Fatalf("unexpected error computing hash1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error computing hash2: %v", err2)
		}
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different container images: both %q", hash1)
		}
	})

	t.Run("different Template resources produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:7.6.5"},
				},
			},
		}
		inst2 := newTestInstance()
		inst2.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:7.6.5",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: mustParseQuantity("500m"),
							},
						},
					},
				},
			},
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)

		if err1 != nil {
			t.Fatalf("unexpected error computing hash1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error computing hash2: %v", err2)
		}
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different resource requirements: both %q", hash1)
		}
	})

	// T4: Template env var changes must affect the hash
	t.Run("different Template env vars produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:7.6.5",
						Env: []corev1.EnvVar{
							{Name: "CUSTOM_ENV", Value: "value1"},
						},
					},
				},
			},
		}
		inst2 := newTestInstance()
		inst2.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:7.6.5",
						Env: []corev1.EnvVar{
							{Name: "CUSTOM_ENV", Value: "value2"},
						},
					},
				},
			},
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)

		if err1 != nil {
			t.Fatalf("unexpected error computing hash1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error computing hash2: %v", err2)
		}
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different container env vars: both %q", hash1)
		}
	})
}

func TestMustParseQuantity(t *testing.T) {
	// Valid quantities
	validCases := []struct {
		name  string
		input string
	}{
		{"512Mi", "512Mi"},
		{"1Gi", "1Gi"},
		{"256M", "256M"},
		{"100m", "100m"},
		{"500Mi", "500Mi"},
		{"2G", "2G"},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			q := mustParseQuantity(tc.input)
			if q.String() == "" {
				t.Errorf("expected non-empty quantity string for %q", tc.input)
			}
		})
	}

	// Invalid quantities should panic
	invalidCases := []string{
		"invalid",
		"abc",
		"",
	}

	for _, tc := range invalidCases {
		t.Run("panics on "+tc, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for invalid quantity %q, but no panic occurred", tc)
				}
			}()
			mustParseQuantity(tc)
		})
	}
}

// TestGetStrimziClusterName covers T3: verify that getStrimziClusterName
// handles nil labels, missing label, empty label, and valid label correctly.
func TestGetStrimziClusterName(t *testing.T) {
	t.Run("nil labels returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = nil

		name, err := getStrimziClusterName(inst)
		if err == nil {
			t.Error("expected error for nil labels, got nil")
		}
		if name != "" {
			t.Errorf("expected empty name for nil labels, got %q", name)
		}
	})

	t.Run("missing strimzi.io/cluster label returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"other-label": "some-value",
		}

		name, err := getStrimziClusterName(inst)
		if err == nil {
			t.Error("expected error for missing strimzi.io/cluster label, got nil")
		}
		if name != "" {
			t.Errorf("expected empty name for missing label, got %q", name)
		}
	})

	t.Run("empty strimzi.io/cluster label value returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "",
		}

		name, err := getStrimziClusterName(inst)
		if err == nil {
			t.Error("expected error for empty strimzi.io/cluster label, got nil")
		}
		if name != "" {
			t.Errorf("expected empty name for empty label value, got %q", name)
		}
	})

	t.Run("valid label returns cluster name", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "my-kafka-cluster",
		}

		name, err := getStrimziClusterName(inst)
		if err != nil {
			t.Fatalf("unexpected error for valid label: %v", err)
		}
		if name != "my-kafka-cluster" {
			t.Errorf("expected cluster name 'my-kafka-cluster', got %q", name)
		}
	})

	t.Run("valid label among other labels returns cluster name", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"app.kubernetes.io/name": "my-app",
			"strimzi.io/cluster":     "prod-kafka",
			"other-label":            "other",
		}

		name, err := getStrimziClusterName(inst)
		if err != nil {
			t.Fatalf("unexpected error for valid label: %v", err)
		}
		if name != "prod-kafka" {
			t.Errorf("expected cluster name 'prod-kafka', got %q", name)
		}
	})
}

// TestRenewTLSSecret_NoOp covers T1: verify that renewTLSSecret is a no-op
// when SecureHTTP is false or a custom TLSSecretName is provided. Also tests
// that a missing strimzi.io/cluster label causes an error.
func TestRenewTLSSecret_NoOp(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &StrimziSchemaRegistryReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	t.Run("SecureHTTP=false is no-op", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.TLSSecretName = ""
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "test-cluster",
		}

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err != nil {
			t.Errorf("expected no error when SecureHTTP=false, got: %v", err)
		}
	})

	t.Run("TLSSecretName set is no-op", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "my-custom-tls"
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "test-cluster",
		}

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err != nil {
			t.Errorf("expected no error when TLSSecretName is set, got: %v", err)
		}
	})

	t.Run("missing strimzi.io/cluster label returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = ""
		inst.Labels = nil

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err == nil {
			t.Error("expected error when strimzi.io/cluster label is missing, got nil")
		}
	})
}

// TestBuildProbesInitialDelay verifies that readiness and liveness probes no
// longer carry an InitialDelaySeconds value (the startup probe already gates
// them), while the startup probe keeps its 30s initial delay.
func TestBuildProbesInitialDelay(t *testing.T) {
	t.Run("HTTP probes have no initial delay and startup keeps 30s", func(t *testing.T) {
		readiness, liveness, startup := buildProbes(8081, corev1.URISchemeHTTP)
		if readiness.InitialDelaySeconds != 0 {
			t.Errorf("expected readiness InitialDelaySeconds=0, got %d", readiness.InitialDelaySeconds)
		}
		if liveness.InitialDelaySeconds != 0 {
			t.Errorf("expected liveness InitialDelaySeconds=0, got %d", liveness.InitialDelaySeconds)
		}
		if startup.InitialDelaySeconds != 30 {
			t.Errorf("expected startup InitialDelaySeconds=30, got %d", startup.InitialDelaySeconds)
		}
	})

	t.Run("HTTPS probes have no initial delay and startup keeps 30s", func(t *testing.T) {
		readiness, liveness, startup := buildProbes(8085, corev1.URISchemeHTTPS)
		if readiness.InitialDelaySeconds != 0 {
			t.Errorf("expected readiness InitialDelaySeconds=0, got %d", readiness.InitialDelaySeconds)
		}
		if liveness.InitialDelaySeconds != 0 {
			t.Errorf("expected liveness InitialDelaySeconds=0, got %d", liveness.InitialDelaySeconds)
		}
		if startup.InitialDelaySeconds != 30 {
			t.Errorf("expected startup InitialDelaySeconds=30, got %d", startup.InitialDelaySeconds)
		}
	})
}

// TestBuildPodEnvStorageIdentity verifies the storage and identity env vars,
// including their empty/nil fallback defaults and the nil replication factor
// being omitted entirely.
func TestBuildPodEnvStorageIdentity(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("defaults when fields are empty or nil", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.KafkastoreTopic = ""
		inst.Spec.KafkastoreTopicReplicationFactor = nil
		inst.Spec.SchemaRegistryGroupID = ""
		inst.Spec.MasterEligibility = nil

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", "registry-schemas")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_GROUP_ID", "schema-registry")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_MASTER_ELIGIBILITY", "true")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR")
	})

	t.Run("explicit values are propagated", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.KafkastoreTopic = "custom-schemas"
		rf := int32(1)
		inst.Spec.KafkastoreTopicReplicationFactor = &rf
		inst.Spec.SchemaRegistryGroupID = "custom-group"
		masterEligibility := false
		inst.Spec.MasterEligibility = &masterEligibility

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", "custom-schemas")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR", "1")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_GROUP_ID", "custom-group")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_MASTER_ELIGIBILITY", "false")
	})
}

// TestComputeSpecHashStorageIdentity verifies that the four new spec fields
// participate in the spec hash so changes trigger a deployment rollout.
func TestComputeSpecHashStorageIdentity(t *testing.T) {
	t.Run("different KafkastoreTopic produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.KafkastoreTopic = "custom-schemas"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different KafkastoreTopic: both %q", hash1)
		}
	})

	t.Run("different KafkastoreTopicReplicationFactor produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		rf := int32(1)
		inst2.Spec.KafkastoreTopicReplicationFactor = &rf

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different KafkastoreTopicReplicationFactor: both %q", hash1)
		}
	})

	t.Run("different SchemaRegistryGroupID produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.SchemaRegistryGroupID = "custom-group"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different SchemaRegistryGroupID: both %q", hash1)
		}
	})

	t.Run("different MasterEligibility produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		masterEligibility := false
		inst2.Spec.MasterEligibility = &masterEligibility

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different MasterEligibility: both %q", hash1)
		}
	})
}

func TestConfluentPropertyToEnvName(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"kafkastore.timeout.ms", "SCHEMA_REGISTRY_KAFKASTORE_TIMEOUT_MS"},
		{"access.control.allow.origin", "SCHEMA_REGISTRY_ACCESS_CONTROL_ALLOW_ORIGIN"},
		{"authentication.realm", "SCHEMA_REGISTRY_AUTHENTICATION_REALM"},
		{"debug", "SCHEMA_REGISTRY_DEBUG"},
		{"foo-bar.baz", "SCHEMA_REGISTRY_FOO_BAR_BAZ"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := confluentPropertyToEnvName(tt.key); got != tt.want {
				t.Errorf("confluentPropertyToEnvName(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestBuildPodEnvConfigurationReserved(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("reserved keys do not override managed env vars", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.Configuration = map[string]string{
			"kafkastore.security.protocol":     "SSL",
			"listeners":                        "http://10.0.0.1:9999",
			"kafkastore.topic":                 "attacker-topic",
			"kafkastore.ssl.keystore.location": "/tmp/evil.jks",
			"access.control.allow.origin":      "*",
		}

		env, overridden := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", "PLAINTEXT")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_LISTENERS", "http://0.0.0.0:8081")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", "registry-schemas")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_ACCESS_CONTROL_ALLOW_ORIGIN", "*")

		wantOverridden := []string{
			"kafkastore.security.protocol",
			"kafkastore.ssl.keystore.location",
			"kafkastore.topic",
			"listeners",
		}
		if len(overridden) != len(wantOverridden) {
			t.Fatalf("expected %d overridden keys, got %d: %v", len(wantOverridden), len(overridden), overridden)
		}
		for i := range wantOverridden {
			if overridden[i] != wantOverridden[i] {
				t.Errorf("overridden[%d] = %q, want %q", i, overridden[i], wantOverridden[i])
			}
		}
	})

	t.Run("non-reserved key colliding with managed env is skipped", func(t *testing.T) {
		inst := newTestInstance()
		rf := int32(1)
		inst.Spec.KafkastoreTopicReplicationFactor = &rf
		inst.Spec.Configuration = map[string]string{
			"kafkastore.topic.replication.factor": "3",
		}

		env, overridden := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR", "1")
		if len(overridden) != 1 || overridden[0] != "kafkastore.topic.replication.factor" {
			t.Errorf("expected overridden to contain the replication-factor collision, got %v", overridden)
		}
	})
}

func TestComputeSpecHashConfigurationDeterministic(t *testing.T) {
	t.Run("same configuration with different insertion order produces same hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Configuration = map[string]string{
			"access.control.allow.origin": "*",
			"authentication.realm":        "Basic",
			"debug":                       "true",
		}
		inst2 := newTestInstance()
		inst2.Spec.Configuration = map[string]string{
			"debug":                       "true",
			"access.control.allow.origin": "*",
			"authentication.realm":        "Basic",
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)
		if err1 != nil || err2 != nil {
			t.Fatalf("unexpected error: %v %v", err1, err2)
		}
		if hash1 != hash2 {
			t.Errorf("expected same hash for same configuration with different insertion order: %q vs %q", hash1, hash2)
		}
	})

	t.Run("different configuration produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Configuration = map[string]string{"debug": "true"}
		inst2 := newTestInstance()
		inst2.Spec.Configuration = map[string]string{"debug": "false"}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different configuration values: both %q", hash1)
		}
	})
}

// assertQuantity fails the test when the resource quantity does not equal want.
func assertQuantity(t *testing.T, got resource.Quantity, want string) {
	t.Helper()
	if got.Cmp(mustParseQuantity(want)) != 0 {
		t.Errorf("expected quantity %s, got %s", want, got.String())
	}
}

// TestBuildDeploymentSpecResourceDefaults verifies that resource requests and
// limits are defaulted per-field, rather than only when both are nil.
func TestBuildDeploymentSpecResourceDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	t.Run("only limits set gets default requests", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:7.6.5",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    mustParseQuantity("2000m"),
								corev1.ResourceMemory: mustParseQuantity("2Gi"),
							},
						},
					},
				},
			},
		}

		dep, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", kafkaAuth{}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res := dep.Spec.Template.Spec.Containers[0].Resources
		assertQuantity(t, res.Requests[corev1.ResourceCPU], "250m")
		assertQuantity(t, res.Requests[corev1.ResourceMemory], "512Mi")
		assertQuantity(t, res.Limits[corev1.ResourceCPU], "2000m")
		assertQuantity(t, res.Limits[corev1.ResourceMemory], "2Gi")
	})

	t.Run("only requests set gets default limits", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:7.6.5",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    mustParseQuantity("300m"),
								corev1.ResourceMemory: mustParseQuantity("600Mi"),
							},
						},
					},
				},
			},
		}

		dep, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", kafkaAuth{}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res := dep.Spec.Template.Spec.Containers[0].Resources
		assertQuantity(t, res.Requests[corev1.ResourceCPU], "300m")
		assertQuantity(t, res.Requests[corev1.ResourceMemory], "600Mi")
		assertQuantity(t, res.Limits[corev1.ResourceCPU], "1000m")
		assertQuantity(t, res.Limits[corev1.ResourceMemory], "1Gi")
	})

	t.Run("neither requests nor limits get full defaults", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:7.6.5"},
				},
			},
		}

		dep, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", kafkaAuth{}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res := dep.Spec.Template.Spec.Containers[0].Resources
		assertQuantity(t, res.Requests[corev1.ResourceCPU], "250m")
		assertQuantity(t, res.Requests[corev1.ResourceMemory], "512Mi")
		assertQuantity(t, res.Limits[corev1.ResourceCPU], "1000m")
		assertQuantity(t, res.Limits[corev1.ResourceMemory], "1Gi")
	})
}

func TestCreateServicePassthrough(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	inst := newTestInstance()
	inst.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}
	inst.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
		Annotations: map[string]string{"prometheus.io/scrape": "true"},
		Labels:      map[string]string{"monitoring.example.com/team": "schemas"},
	}

	svc, err := reconciler.createService(inst, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error creating service: %v", err)
	}

	if svc.Annotations["type"] != "http" {
		t.Errorf("expected operator-managed type annotation 'http', got %q", svc.Annotations["type"])
	}
	if svc.Annotations["prometheus.io/scrape"] != "true" {
		t.Errorf("expected passthrough annotation to be applied, got %v", svc.Annotations)
	}
	if svc.Labels["strimzi.io/cluster"] != "kafka-cluster" {
		t.Errorf("expected CR labels to be preserved, got %v", svc.Labels)
	}
	if svc.Labels["monitoring.example.com/team"] != "schemas" {
		t.Errorf("expected passthrough label to be applied, got %v", svc.Labels)
	}
}

func TestComputeSpecHashServiceNetworkPolicy(t *testing.T) {
	t.Run("different service annotations produce different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
			Annotations: map[string]string{"prometheus.io/scrape": "true"},
		}
		inst2 := newTestInstance()
		inst2.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
			Annotations: map[string]string{"prometheus.io/scrape": "false"},
		}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different service annotations: both %q", hash1)
		}
	})

	t.Run("different service labels produce different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Service = &strimziregistryoperatorv1alpha1.Service{Labels: map[string]string{"team": "a"}}
		inst2 := newTestInstance()
		inst2.Spec.Service = &strimziregistryoperatorv1alpha1.Service{Labels: map[string]string{"team": "b"}}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different service labels: both %q", hash1)
		}
	})

	t.Run("networkpolicy enabled produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.NetworkPolicy = &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes when networkpolicy is enabled: both %q", hash1)
		}
	})
}

func TestBuildNetworkPolicy(t *testing.T) {
	inst := newTestInstance()
	inst.Spec.SecureHTTP = false

	np, err := buildNetworkPolicy(inst, "kafka-cluster", "kafka-cluster-kafka-bootstrap.kafka.svc:9093")
	if err != nil {
		t.Fatalf("unexpected error building networkpolicy: %v", err)
	}

	if np.Name != "test-sr-network-policy" {
		t.Errorf("expected name test-sr-network-policy, got %q", np.Name)
	}
	if np.Spec.PodSelector.MatchLabels["app"] != "test-sr" {
		t.Errorf("expected pod selector app=test-sr, got %v", np.Spec.PodSelector.MatchLabels)
	}
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("expected Ingress and Egress policy types, got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].Ports) != 1 {
		t.Fatalf("expected a single ingress rule with one port, got %v", np.Spec.Ingress)
	}
	if np.Spec.Ingress[0].Ports[0].Port.IntVal != 8081 {
		t.Errorf("expected ingress on SR listener port 8081, got %d", np.Spec.Ingress[0].Ports[0].Port.IntVal)
	}
	if len(np.Spec.Egress) != 1 || len(np.Spec.Egress[0].Ports) != 1 {
		t.Fatalf("expected a single egress rule with one port, got %v", np.Spec.Egress)
	}
	if np.Spec.Egress[0].Ports[0].Port.IntVal != 9093 {
		t.Errorf("expected egress to Kafka bootstrap port 9093, got %d", np.Spec.Egress[0].Ports[0].Port.IntVal)
	}
	if len(np.Spec.Egress[0].To) != 1 {
		t.Fatalf("expected one egress peer, got %v", np.Spec.Egress[0].To)
	}
	if np.Spec.Egress[0].To[0].PodSelector.MatchLabels["strimzi.io/cluster"] != "kafka-cluster" {
		t.Errorf("expected egress peer to target strimzi.io/cluster=kafka-cluster, got %v",
			np.Spec.Egress[0].To[0].PodSelector.MatchLabels)
	}
}

func TestPortFromBootstrapServer(t *testing.T) {
	t.Run("valid host port", func(t *testing.T) {
		port, err := portFromBootstrapServer("kafka-bootstrap.kafka.svc:9093")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 9093 {
			t.Errorf("expected 9093, got %d", port)
		}
	})

	t.Run("missing port returns error", func(t *testing.T) {
		if _, err := portFromBootstrapServer("kafka-bootstrap.kafka.svc"); err == nil {
			t.Error("expected error for missing port")
		}
	})

	t.Run("non-numeric port returns error", func(t *testing.T) {
		if _, err := portFromBootstrapServer("kafka-bootstrap.kafka.svc:notaport"); err == nil {
			t.Error("expected error for non-numeric port")
		}
	})
}

func TestMigrateFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)

	t.Run("legacy removed and current added in one pass", func(t *testing.T) {
		inst := newTestInstance()
		inst.Finalizers = []string{legacyFinalizer}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		if err := reconciler.migrateFinalizer(context.Background(), inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr", Namespace: "default"}, got); err != nil {
			t.Fatalf("unexpected get error: %v", err)
		}
		if containsFinalizer(got.Finalizers, legacyFinalizer) {
			t.Errorf("legacy finalizer should be removed, got %v", got.Finalizers)
		}
		if !containsFinalizer(got.Finalizers, finalizer) {
			t.Errorf("current finalizer should be added, got %v", got.Finalizers)
		}
	})

	t.Run("already migrated is a no-op", func(t *testing.T) {
		inst := newTestInstance()
		inst.Finalizers = []string{finalizer}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		if err := reconciler.migrateFinalizer(context.Background(), inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr", Namespace: "default"}, got); err != nil {
			t.Fatalf("unexpected get error: %v", err)
		}
		if len(got.Finalizers) != 1 || got.Finalizers[0] != finalizer {
			t.Errorf("finalizers should be unchanged after idempotent migration, got %v", got.Finalizers)
		}
	})

	t.Run("fresh object gets current finalizer", func(t *testing.T) {
		inst := newTestInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		if err := reconciler.migrateFinalizer(context.Background(), inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr", Namespace: "default"}, got); err != nil {
			t.Fatalf("unexpected get error: %v", err)
		}
		if !containsFinalizer(got.Finalizers, finalizer) {
			t.Errorf("expected current finalizer to be added, got %v", got.Finalizers)
		}
	})
}

func containsFinalizer(finalizers []string, target string) bool {
	for _, f := range finalizers {
		if f == target {
			return true
		}
	}
	return false
}
