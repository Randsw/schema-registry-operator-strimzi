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
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// validUserTLSSecretData returns secret data satisfying the user-provided TLS
// secret contract: a JKS keystore plus both passwords.
func validUserTLSSecretData() map[string][]byte {
	return map[string][]byte{
		"tls-keystore.jks":  []byte("keystore-bytes"),
		"keystore_password": []byte("store-password"),
		"key_password":      []byte("key-password"),
	}
}

// TestUserTLSSecretValidationError is a table-driven test for the pure
// validation helper: a missing secret, a missing each required key, empty
// values, and the valid case — for both the JKS (default) and PKCS12
// keystore contracts.
func TestUserTLSSecretValidationError(t *testing.T) {
	tests := []struct {
		name          string
		secretName    string
		secret        *corev1.Secret
		keystoreType  string
		wantInvalid   bool
		wantMentions  []string // substrings the message must contain
		wantForbidden []string // substrings the message must not contain
	}{
		{
			name:         "nil secret (not found) is invalid",
			secretName:   "user-tls",
			secret:       nil,
			wantInvalid:  true,
			wantMentions: []string{"user-tls", "not found"},
		},
		{
			name:         "missing keystore key",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{"keystore_password": []byte("pw"), "key_password": []byte("pw")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.jks"},
		},
		{
			name:         "missing keystore_password key",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": []byte("jks"), "key_password": []byte("pw")}},
			wantInvalid:  true,
			wantMentions: []string{"keystore_password"},
		},
		{
			name:         "missing key_password key",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": []byte("jks"), "keystore_password": []byte("pw")}},
			wantInvalid:  true,
			wantMentions: []string{"key_password"},
		},
		{
			name:         "empty keystore value",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": {}, "keystore_password": []byte("pw"), "key_password": []byte("pw")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.jks"},
		},
		{
			name:         "empty password values report every offending key",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": []byte("jks"), "keystore_password": {}, "key_password": {}}},
			wantInvalid:  true,
			wantMentions: []string{"keystore_password", "key_password"},
		},
		{
			name:         "all keys missing reports all of them",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.jks", "keystore_password", "key_password"},
		},
		{
			name:       "valid secret passes",
			secretName: "user-tls",
			secret:     &corev1.Secret{Data: validUserTLSSecretData()},
		},
		// PKCS12 keystore contract (spec.keystoretype: PKCS12).
		{
			name:         "PKCS12: missing p12 keystore key",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret:       &corev1.Secret{Data: map[string][]byte{"keystore_password": []byte("pw"), "key_password": []byte("pw")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.p12"},
		},
		{
			name:         "PKCS12: empty p12 keystore value",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.p12": {}, "keystore_password": []byte("pw"), "key_password": []byte("pw")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.p12"},
		},
		{
			name:         "PKCS12: missing passwords",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.p12": []byte("p12")}},
			wantInvalid:  true,
			wantMentions: []string{"keystore_password", "key_password"},
		},
		{
			name:         "PKCS12: jks-only secret is invalid",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret:       &corev1.Secret{Data: validUserTLSSecretData()},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.p12"},
		},
		{
			name:         "PKCS12: valid p12 secret passes",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret: &corev1.Secret{Data: map[string][]byte{
				"tls-keystore.p12":  []byte("p12-bytes"),
				"keystore_password": []byte("store-password"),
				"key_password":      []byte("key-password"),
			}},
		},
		{
			name:         "PKCS12: nil secret is invalid",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret:       nil,
			wantInvalid:  true,
			wantMentions: []string{"user-tls", "not found"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := userTLSSecretValidationError(tt.secretName, tt.secret, tt.keystoreType)
			if tt.wantInvalid && msg == "" {
				t.Fatalf("expected a validation error message, got empty")
			}
			if !tt.wantInvalid && msg != "" {
				t.Fatalf("expected no validation error message, got %q", msg)
			}
			for _, want := range tt.wantMentions {
				if !strings.Contains(msg, want) {
					t.Errorf("expected message %q to contain %q", msg, want)
				}
			}
			for _, forbidden := range tt.wantForbidden {
				if strings.Contains(msg, forbidden) {
					t.Errorf("expected message %q to not contain %q", msg, forbidden)
				}
			}
		})
	}
}

// TestHandleUserTLSSecretRotation covers the rotation handler with a fake
// client: it rolls the deployment once when the recorded tlsVersion annotation
// differs from the current secret resourceVersion, and is stable (no further
// updates) on subsequent reconciles.
func TestHandleUserTLSSecretRotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	newDeployment := func(tlsVersion string) *apps.Deployment {
		dep := &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr-deploy", Namespace: "default"},
			Spec: apps.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "image:tag"}}},
				},
			},
		}
		if tlsVersion != "" {
			dep.Spec.Template.Annotations = map[string]string{tlsVersionKey: tlsVersion}
		}
		return dep
	}
	newUserSecret := func(resourceVersion string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: resourceVersion},
			Data:       validUserTLSSecretData(),
		}
	}
	newInstance := func() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-tls"
		return inst
	}

	t.Run("rolls once when the annotation is stale, then stays stable", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newUserSecret("100"), newDeployment("99")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		// First reconcile: the recorded version (99) differs from the secret
		// version (100) → the deployment must be rolled.
		result, err := reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != time.Second {
			t.Errorf("expected requeue after 1s, got %v", result.RequeueAfter)
		}
		dep := &apps.Deployment{}
		if err := client.Get(ctx, types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "100" {
			t.Errorf("expected tlsVersion annotation %q after rollout, got %q", "100", got)
		}

		// Second reconcile: the annotation now matches → stable, no requeue.
		result, err = reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error on second reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue once stable, got %v", result.RequeueAfter)
		}
		dep = &apps.Deployment{}
		if err := client.Get(ctx, types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "100" {
			t.Errorf("expected tlsVersion annotation to stay %q, got %q", "100", got)
		}
	})

	t.Run("rolls when the annotation is absent", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newUserSecret("100"), newDeployment("")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, err := reconciler.handleUserTLSSecretRotation(context.Background(), inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != time.Second {
			t.Errorf("expected requeue after 1s, got %v", result.RequeueAfter)
		}
		dep := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "100" {
			t.Errorf("expected tlsVersion annotation %q, got %q", "100", got)
		}
	})

	t.Run("no-op when securehttp is disabled", func(t *testing.T) {
		inst := newInstance()
		inst.Spec.SecureHTTP = false
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newUserSecret("100"), newDeployment("99")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, err := reconciler.handleUserTLSSecretRotation(context.Background(), inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue, got %v", result.RequeueAfter)
		}
		dep := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "99" {
			t.Errorf("expected the annotation to be untouched, got %q", got)
		}
	})

	t.Run("no-op when tlssecretname is empty (operator-managed TLS)", func(t *testing.T) {
		inst := newInstance()
		inst.Spec.TLSSecretName = ""
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newDeployment("99")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, err := reconciler.handleUserTLSSecretRotation(context.Background(), inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue, got %v", result.RequeueAfter)
		}
	})

	t.Run("no-op when the deployment does not exist yet", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newUserSecret("100")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, err := reconciler.handleUserTLSSecretRotation(context.Background(), inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue, got %v", result.RequeueAfter)
		}
	})

	t.Run("no-op when the user TLS secret does not exist", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newDeployment("99")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, err := reconciler.handleUserTLSSecretRotation(context.Background(), inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue, got %v", result.RequeueAfter)
		}
		dep := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "99" {
			t.Errorf("expected the annotation to be untouched, got %q", got)
		}
	})
}

// TestMapSecretToRequests covers the Secret watch mapper: the label fast-path
// for Strimzi secrets is unchanged, user-provided TLS secrets are matched by
// spec.tlssecretname, and a CR matching both paths is enqueued exactly once.
func TestMapSecretToRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)

	// sr-name: matches the label path (secret named after the CR) and, via its
	// own spec.tlssecretname, the name path too — must be enqueued once.
	srName := newTestInstance()
	srName.Name = "sr-name"
	srName.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}
	srName.Spec.TLSSecretName = "sr-name"

	// sr-user-tls: references a user TLS secret by name, no Strimzi label.
	srUserTLS := newTestInstance()
	srUserTLS.Name = "sr-user-tls"
	srUserTLS.Spec.TLSSecretName = "user-tls-secret"

	// sr-other: references a different TLS secret.
	srOther := newTestInstance()
	srOther.Name = "sr-other"
	srOther.Spec.TLSSecretName = "other-secret"

	// sr-labeled: Strimzi-labeled CR that does not reference any TLS secret.
	srLabeled := newTestInstance()
	srLabeled.Name = "sr-labeled"
	srLabeled.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(srName, srUserTLS, srOther, srLabeled).
		Build()
	reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
	ctx := context.Background()

	expectRequests := func(t *testing.T, requests []reconcile.Request, wantNames ...string) {
		t.Helper()
		if len(requests) != len(wantNames) {
			t.Fatalf("expected %d request(s) %v, got %d: %v", len(wantNames), wantNames, len(requests), requests)
		}
		got := make(map[string]bool, len(requests))
		for _, req := range requests {
			if req.Namespace != "default" {
				t.Errorf("expected requests in namespace default, got %q", req.Namespace)
			}
			got[req.Name] = true
		}
		for _, name := range wantNames {
			if !got[name] {
				t.Errorf("expected a request for %q, got %v", name, requests)
			}
		}
	}

	t.Run("unlabeled user TLS secret enqueues the referencing CR", func(t *testing.T) {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "user-tls-secret", Namespace: "default"}}
		expectRequests(t, reconciler.mapSecretToRequests(ctx, secret), "sr-user-tls")
	})

	t.Run("unlabeled unrelated secret enqueues nothing", func(t *testing.T) {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default"}}
		expectRequests(t, reconciler.mapSecretToRequests(ctx, secret))
	})

	t.Run("unlabeled secret matching another CR's tlssecretname is not enqueued", func(t *testing.T) {
		// other-secret belongs to sr-other; a change to user-tls-secret must
		// not enqueue it.
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "user-tls-secret", Namespace: "default"}}
		requests := reconciler.mapSecretToRequests(ctx, secret)
		for _, req := range requests {
			if req.Name == "sr-other" {
				t.Errorf("did not expect a request for sr-other, got %v", requests)
			}
		}
	})

	t.Run("labeled cluster CA secret keeps the label fast-path", func(t *testing.T) {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "kafka-cluster-cluster-ca-cert",
			Namespace: "default",
			Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
		}}
		// All labeled CRs of the cluster are enqueued for CA events.
		expectRequests(t, reconciler.mapSecretToRequests(ctx, secret), "sr-name", "sr-labeled")
	})

	t.Run("CR matching both label and name paths is enqueued once", func(t *testing.T) {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "sr-name",
			Namespace: "default",
			Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
		}}
		requests := reconciler.mapSecretToRequests(ctx, secret)
		count := 0
		for _, req := range requests {
			if req.Name == "sr-name" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly one request for sr-name, got %d: %v", count, requests)
		}
	})

	t.Run("labeled user TLS secret is enqueued once via dedupe", func(t *testing.T) {
		// A user TLS secret that also carries the Strimzi label and is named
		// after the CR matches both the label path (name == CR name) and the
		// name path (spec.tlssecretname == secret name) — exactly one request.
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "sr-name",
			Namespace: "default",
			Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
		}}
		expectRequests(t, reconciler.mapSecretToRequests(ctx, secret), "sr-name")
	})
}
