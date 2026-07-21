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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
		inst.ObjectMeta.Labels = map[string]string{
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
		inst.ObjectMeta.Labels = map[string]string{
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
		inst.ObjectMeta.Labels = map[string]string{
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
		inst.ObjectMeta.Labels = map[string]string{
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
		inst.ObjectMeta.Labels = nil

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err == nil {
			t.Error("expected error when strimzi.io/cluster label is missing, got nil")
		}
	})
}
