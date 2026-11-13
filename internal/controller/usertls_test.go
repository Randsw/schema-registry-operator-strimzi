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
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

	// B11 pre-filters: Secret events are cluster-wide, so irrelevant events
	// must exit before any CR List call. These subtests count List calls with
	// a fake-client interceptor.
	srTLSSuffixed := newTestInstance()
	srTLSSuffixed.Name = "sr-tls-suffixed"
	srTLSSuffixed.Spec.TLSSecretName = "schema-registry-tls"

	// operatorOwnedSecret returns a secret carrying a controller owner
	// reference to a StrimziSchemaRegistry, as the operator's own output
	// secrets (<name>-jks, <name>-tls, derived <tlssecretname>-jks) do.
	operatorOwnedSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "strimziregistryoperator.randsw.code/v1alpha1",
					Kind:       "StrimziSchemaRegistry",
					Name:       "sr-name",
					UID:        "test-uid",
					Controller: ptr.To(true),
				}},
			},
		}
	}

	newCountingReconciler := func(t *testing.T, listCalls *int, objs ...crclient.Object) *StrimziSchemaRegistryReconciler {
		t.Helper()
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(objs...).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, cl crclient.WithWatch, list crclient.ObjectList, opts ...crclient.ListOption) error {
					*listCalls++
					return cl.List(ctx, list, opts...)
				},
			}).
			Build()
		return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	}

	t.Run("operator-owned jks secret is skipped before any List call", func(t *testing.T) {
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srUserTLS, srOther, srLabeled)
		if reqs := r.mapSecretToRequests(ctx, operatorOwnedSecret("sr-name-jks")); len(reqs) != 0 {
			t.Errorf("expected no requests for an operator-owned -jks secret, got %v", reqs)
		}
		if listCalls != 0 {
			t.Errorf("expected zero List calls for an operator-owned -jks secret, got %d", listCalls)
		}
	})

	t.Run("operator-owned managed TLS secret is skipped before any List call", func(t *testing.T) {
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srUserTLS, srOther, srLabeled)
		if reqs := r.mapSecretToRequests(ctx, operatorOwnedSecret("sr-name-tls")); len(reqs) != 0 {
			t.Errorf("expected no requests for an operator-owned -tls secret, got %v", reqs)
		}
		if listCalls != 0 {
			t.Errorf("expected zero List calls for an operator-owned -tls secret, got %d", listCalls)
		}
	})

	t.Run("derived user TLS jks secret is skipped before any List call", func(t *testing.T) {
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srUserTLS, srOther, srLabeled)
		if reqs := r.mapSecretToRequests(ctx, operatorOwnedSecret("user-tls-secret-jks")); len(reqs) != 0 {
			t.Errorf("expected no requests for the derived -jks secret, got %v", reqs)
		}
		if listCalls != 0 {
			t.Errorf("expected zero List calls for the derived -jks secret, got %d", listCalls)
		}
	})

	t.Run("user TLS secret ending with -tls is still matched", func(t *testing.T) {
		// The README documents user TLS secrets named e.g.
		// schema-registry-tls; the owner-based skip must not swallow them.
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srTLSSuffixed)
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "schema-registry-tls", Namespace: "default"}}
		expectRequests(t, r.mapSecretToRequests(ctx, secret), "sr-tls-suffixed")
		if listCalls != 1 {
			t.Errorf("expected exactly 1 List call (the name path), got %d", listCalls)
		}
	})

	t.Run("cluster CA secret skips the name-path List", func(t *testing.T) {
		// A CA cert secret can only match via the label path, so the
		// namespace-wide spec.tlssecretname List must not run for it.
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srLabeled)
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "kafka-cluster-cluster-ca-cert",
			Namespace: "default",
			Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
		}}
		expectRequests(t, r.mapSecretToRequests(ctx, secret), "sr-name", "sr-labeled")
		if listCalls != 1 {
			t.Errorf("expected exactly 1 List call (the label path) for a CA secret, got %d", listCalls)
		}
	})
}

// realPEMSecretData generates a real certificate and key with the shared
// testutil helpers so the PEM → JKS conversion under test actually runs the
// openssl/keytool pipeline.
func realPEMSecretData(t *testing.T) map[string][]byte {
	t.Helper()
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}
	return map[string][]byte{
		"tls.crt": []byte(uc.UserCertPEM),
		"tls.key": []byte(uc.UserKeyPEM),
	}
}

// TestValidateUserTLSSecret covers the shape-dispatching validation wrapper:
// standard kubernetes.io/tls (PEM) secrets, pre-built keystores following
// spec.keystoretype, and the derived-secret name collision guard.
func TestValidateUserTLSSecret(t *testing.T) {
	newInstance := func(tlsSecretName string) *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = tlsSecretName
		return inst
	}

	tests := []struct {
		name         string
		instance     *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry
		secret       *corev1.Secret
		wantInvalid  bool
		wantMentions []string
	}{
		{
			name:         "nil secret (not found) is invalid",
			instance:     newInstance("user-tls"),
			secret:       nil,
			wantInvalid:  true,
			wantMentions: []string{"user-tls", "not found"},
		},
		{
			name:     "valid PEM secret (with optional ca.crt) passes",
			instance: newInstance("user-tls"),
			secret: &corev1.Secret{Data: map[string][]byte{
				"tls.crt": []byte("pem-cert"),
				"tls.key": []byte("pem-key"),
				"ca.crt":  []byte("ca-pem"),
			}},
		},
		{
			name:         "PEM secret with empty tls.key is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("pem-cert"), "tls.key": {}}},
			wantInvalid:  true,
			wantMentions: []string{"tls.key"},
		},
		{
			name:         "PEM secret with empty tls.crt is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": {}, "tls.key": []byte("pem-key")}},
			wantInvalid:  true,
			wantMentions: []string{"tls.crt"},
		},
		{
			name:         "secret with only tls.crt falls back to the keystore contract",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("pem-cert")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.jks"},
		},
		{
			name: "derived secret name colliding with the operator-managed -jks secret is invalid",
			// tlssecretname == CR name makes <tlssecretname>-jks equal the
			// operator-managed KafkaStore secret <name>-jks.
			instance:     newInstance("test-sr"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("pem-cert"), "tls.key": []byte("pem-key")}},
			wantInvalid:  true,
			wantMentions: []string{"test-sr-jks", "collides"},
		},
		{
			name:     "JKS keystore contract still passes",
			instance: newInstance("user-tls"),
			secret:   &corev1.Secret{Data: validUserTLSSecretData()},
		},
		{
			name:         "PKCS12 keystore contract is validated per keystoretype",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: validUserTLSSecretData()},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.p12"},
		},
	}

	// The PKCS12 case needs spec.keystoretype set on its instance.
	for i := range tests {
		if tests[i].name == "PKCS12 keystore contract is validated per keystoretype" {
			tests[i].instance.Spec.KeystoreType = keystoreTypePKCS12
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validateUserTLSSecret(tt.instance, tt.secret)
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
		})
	}
}

// TestDerivedSecretNameCollision unit-tests the collision guard for the
// derived <tlssecretname>-jks secret name.
func TestDerivedSecretNameCollision(t *testing.T) {
	t.Run("tlssecretname equal to the CR name collides with <name>-jks", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.TLSSecretName = inst.Name
		if msg := derivedSecretNameCollision(inst); msg == "" {
			t.Error("expected a collision message, got empty")
		}
	})

	t.Run("distinct tlssecretname does not collide", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.TLSSecretName = "user-tls"
		if msg := derivedSecretNameCollision(inst); msg != "" {
			t.Errorf("expected no collision, got %q", msg)
		}
	})
}

// TestEnsureDerivedJKSSecret covers the derived-secret lifecycle with a fake
// client: created when missing, idempotent when up-to-date, and regenerated
// only when the user secret's resourceVersion changes.
func TestEnsureDerivedJKSSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	newInstance := func() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-tls"
		return inst
	}
	derivedName := types.NamespacedName{Name: "user-tls-jks", Namespace: "default"}

	t.Run("creates the derived secret with the JKS contract", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       realPEMSecretData(t),
		}

		if err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, userSecret, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		derived := &corev1.Secret{}
		if err := client.Get(context.Background(), derivedName, derived); err != nil {
			t.Fatalf("derived secret not created: %v", err)
		}
		for _, key := range []string{"tls-keystore.jks", "keystore_password", "key_password"} {
			if len(derived.Data[key]) == 0 {
				t.Errorf("derived secret missing non-empty %q", key)
			}
		}
		if derived.Annotations[userSecretVersionKey] != "100" {
			t.Errorf("expected user-secret-resourceVersion annotation %q, got %q", "100", derived.Annotations[userSecretVersionKey])
		}
		if len(derived.OwnerReferences) == 0 {
			t.Error("expected the derived secret to be owned by the CR")
		}
	})

	t.Run("is idempotent when up-to-date", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       realPEMSecretData(t),
		}
		if err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, userSecret, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		before := &corev1.Secret{}
		if err := client.Get(context.Background(), derivedName, before); err != nil {
			t.Fatalf("derived secret not created: %v", err)
		}

		if err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, userSecret, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error on second ensure: %v", err)
		}
		after := &corev1.Secret{}
		if err := client.Get(context.Background(), derivedName, after); err != nil {
			t.Fatalf("derived secret disappeared: %v", err)
		}
		if before.ResourceVersion != after.ResourceVersion {
			t.Errorf("expected the derived secret to be untouched when up-to-date, RV %q -> %q", before.ResourceVersion, after.ResourceVersion)
		}
	})

	t.Run("regenerates when the user secret resourceVersion changes", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       realPEMSecretData(t),
		}
		if err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, userSecret, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		before := &corev1.Secret{}
		if err := client.Get(context.Background(), derivedName, before); err != nil {
			t.Fatalf("derived secret not created: %v", err)
		}

		rotated := userSecret.DeepCopy()
		rotated.ResourceVersion = "200"
		if err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, rotated, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error after rotation: %v", err)
		}
		after := &corev1.Secret{}
		if err := client.Get(context.Background(), derivedName, after); err != nil {
			t.Fatalf("derived secret disappeared: %v", err)
		}
		if after.Annotations[userSecretVersionKey] != "200" {
			t.Errorf("expected the annotation to track the new user secret version %q, got %q", "200", after.Annotations[userSecretVersionKey])
		}
		if after.ResourceVersion == before.ResourceVersion {
			t.Error("expected the derived secret to be regenerated (resourceVersion bump)")
		}
	})
}

// newRestAPITLSDeployment builds a Deployment whose rest-api-tls volume and
// keystore/key password env refs point at the given secret, mirroring what
// buildDeploymentSpec renders for a user TLS secret.
func newRestAPITLSDeployment(mountSecretName string) *apps.Deployment {
	secretRef := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: mountSecretName},
			Key:                  key,
		}}
	}
	return &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sr-deploy", Namespace: "default"},
		Spec: apps.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "test",
						Image: "image:tag",
						Env: []corev1.EnvVar{
							{Name: sslKeystorePasswordEnv, ValueFrom: secretRef("keystore_password")},
							{Name: sslKeyPasswordEnv, ValueFrom: secretRef("key_password")},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: restAPITLSVolumeName,
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: mountSecretName},
						},
					}},
				},
			},
		},
	}
}

// TestHandleUserTLSSecretRotationPEM covers the rotation handler for
// kubernetes.io/tls user secrets: the derived secret is created and
// regenerated on user-secret change, the deployment rolls once via the
// tlsVersion annotation (which tracks the USER secret version) and then
// stays stable, and a stale mount is repointed at the derived secret.
func TestHandleUserTLSSecretRotationPEM(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	newInstance := func() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-tls"
		return inst
	}
	derivedName := types.NamespacedName{Name: "user-tls-jks", Namespace: "default"}
	depName := types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}

	t.Run("creates the derived secret and rolls once, then stays stable", func(t *testing.T) {
		inst := newInstance()
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       realPEMSecretData(t),
		}
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, userSecret, newRestAPITLSDeployment("user-tls")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		result, err := reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != time.Second {
			t.Errorf("expected requeue after 1s, got %v", result.RequeueAfter)
		}

		derived := &corev1.Secret{}
		if err := client.Get(ctx, derivedName, derived); err != nil {
			t.Fatalf("derived secret not created: %v", err)
		}
		if derived.Annotations[userSecretVersionKey] != "100" {
			t.Errorf("expected derived annotation %q, got %q", "100", derived.Annotations[userSecretVersionKey])
		}
		dep := &apps.Deployment{}
		if err := client.Get(ctx, depName, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "100" {
			t.Errorf("expected tlsVersion annotation %q (USER secret version), got %q", "100", got)
		}

		// Second reconcile: stable, no requeue, derived secret untouched.
		derivedBefore := derived.DeepCopy()
		result, err = reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error on second reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue once stable, got %v", result.RequeueAfter)
		}
		derivedAfter := &corev1.Secret{}
		if err := client.Get(ctx, derivedName, derivedAfter); err != nil {
			t.Fatalf("derived secret disappeared: %v", err)
		}
		if derivedBefore.ResourceVersion != derivedAfter.ResourceVersion {
			t.Error("expected the derived secret to be untouched once stable")
		}
	})

	t.Run("regenerates the derived secret when the user secret rotates", func(t *testing.T) {
		inst := newInstance()
		// The user secret was rotated (cert-manager reissue) to version 200;
		// the derived secret and the deployment still record version 100.
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "200"},
			Data:       realPEMSecretData(t),
		}
		staleDerived := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "user-tls-jks",
				Namespace:   "default",
				Annotations: map[string]string{userSecretVersionKey: "100"},
			},
			Data: map[string][]byte{
				"tls-keystore.jks":  []byte("stale-keystore"),
				"keystore_password": []byte("stale-password"),
				"key_password":      []byte("stale-password"),
			},
		}
		dep := newRestAPITLSDeployment("user-tls-jks")
		dep.Spec.Template.Annotations = map[string]string{tlsVersionKey: "100"}
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, userSecret, staleDerived, dep).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		result, err := reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error after rotation: %v", err)
		}
		if result.RequeueAfter != time.Second {
			t.Errorf("expected requeue after 1s, got %v", result.RequeueAfter)
		}
		derived := &corev1.Secret{}
		if err := client.Get(ctx, derivedName, derived); err != nil {
			t.Fatalf("derived secret disappeared: %v", err)
		}
		if derived.Annotations[userSecretVersionKey] != "200" {
			t.Errorf("expected derived annotation %q after regeneration, got %q", "200", derived.Annotations[userSecretVersionKey])
		}
		if string(derived.Data["tls-keystore.jks"]) == "stale-keystore" {
			t.Error("expected the derived keystore to be regenerated from the rotated user secret")
		}
		deployment := &apps.Deployment{}
		if err := client.Get(ctx, depName, deployment); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := deployment.Spec.Template.Annotations[tlsVersionKey]; got != "200" {
			t.Errorf("expected tlsVersion annotation %q, got %q", "200", got)
		}
	})

	t.Run("repoints stale mounts at the derived secret on a shape swap", func(t *testing.T) {
		// The deployment was built when the user secret held a pre-built JKS
		// keystore (mounted directly); the user then replaced its content
		// with a kubernetes.io/tls shape. The rotation must repoint the
		// volume and env refs at the derived secret in the same update that
		// rolls the pods.
		inst := newInstance()
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "300"},
			Data:       realPEMSecretData(t),
		}
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, userSecret, newRestAPITLSDeployment("user-tls")).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		if _, err := reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		dep := &apps.Deployment{}
		if err := client.Get(ctx, depName, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := restAPITLSVolumeSecretName(dep); got != "user-tls-jks" {
			t.Errorf("expected the rest-api-tls volume to mount the derived secret, got %q", got)
		}
		for _, envName := range []string{sslKeystorePasswordEnv, sslKeyPasswordEnv} {
			found := false
			for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
				if e.Name == envName {
					found = true
					if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil || e.ValueFrom.SecretKeyRef.Name != "user-tls-jks" {
						t.Errorf("expected %s to reference the derived secret, got %+v", envName, e.ValueFrom)
					}
				}
			}
			if !found {
				t.Errorf("expected env var %s to be present", envName)
			}
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got != "300" {
			t.Errorf("expected tlsVersion annotation %q, got %q", "300", got)
		}
	})
}
