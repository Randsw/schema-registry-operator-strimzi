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
	"testing"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
