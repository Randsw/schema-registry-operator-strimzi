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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/pavlo-v-chernykh/keystore-go/v4"
	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// validUserTLSSecretData returns secret data satisfying the PRESENCE part of
// the user-provided TLS secret contract: a JKS keystore plus both passwords.
// The keystore bytes are deliberately not decodable; tests that exercise the
// material validation (H-1) must use validPrebuiltJKSSecretData instead.
func validUserTLSSecretData() map[string][]byte {
	return map[string][]byte{
		"tls-keystore.jks":  []byte("keystore-bytes"),
		"keystore_password": []byte("store-password"),
		"key_password":      []byte("key-password"),
	}
}

// validPrebuiltJKSSecretData builds a real, decodable JKS keystore secret. The
// store and key passwords are the same, matching what the conversion pipeline
// produces and what the Schema Registry container expects.
func validPrebuiltJKSSecretData(t *testing.T) map[string][]byte {
	t.Helper()
	pemData, _ := realPEMSecretDataWithCA(t)
	password := "store-password"
	keystoreBytes, _, err := certprocessor.NewCertProcessor(logr.Logger{}).ConvertPEMToJKS(
		context.Background(),
		string(pemData["tls.crt"]),
		string(pemData["tls.key"]),
		string(pemData["ca.crt"]),
		password,
	)
	if err != nil {
		t.Fatalf("failed to build a test JKS keystore: %v", err)
	}
	return map[string][]byte{
		"tls-keystore.jks":  keystoreBytes,
		"keystore_password": []byte(password),
		"key_password":      []byte(password),
	}
}

// validPrebuiltPKCS12SecretData builds a real, decodable PKCS12 keystore
// secret using the shared test-certificate generator.
func validPrebuiltPKCS12SecretData(t *testing.T) map[string][]byte {
	t.Helper()
	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate a test CA: %v", err)
	}
	userCert, err := testutil.GenerateTestUserCert(ca, "p12-password")
	if err != nil {
		t.Fatalf("failed to generate a test user certificate: %v", err)
	}
	return map[string][]byte{
		"tls-keystore.p12":  userCert.PKCS12Data,
		"keystore_password": []byte(userCert.Password),
		"key_password":      []byte(userCert.Password),
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
			name:       "valid JKS secret passes",
			secretName: "user-tls",
			secret:     &corev1.Secret{Data: validPrebuiltJKSSecretData(t)},
		},
		{
			name:         "garbage JKS material with the right password is invalid (T-4)",
			secretName:   "user-tls",
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": []byte("not-a-keystore"), "keystore_password": []byte("store-password"), "key_password": []byte("store-password")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.jks"},
		},
		{
			name:       "JKS with a wrong key_password is invalid (T-4)",
			secretName: "user-tls",
			secret: func() *corev1.Secret {
				data := validPrebuiltJKSSecretData(t)
				data["key_password"] = []byte("wrong-key-password")
				return &corev1.Secret{Data: data}
			}(),
			wantInvalid:  true,
			wantMentions: []string{"key_password"},
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
			secret:       &corev1.Secret{Data: validPrebuiltPKCS12SecretData(t)},
		},
		{
			name:         "PKCS12: garbage p12 material is invalid (T-4)",
			secretName:   "user-tls",
			keystoreType: keystoreTypePKCS12,
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.p12": []byte("not-a-p12"), "keystore_password": []byte("store-password"), "key_password": []byte("store-password")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.p12"},
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
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
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
	newInstance := func() *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
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
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)

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
					APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
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

	// Operator-owned output secrets resolve straight back to their owning CR
	// via the controller owner reference — without any List call. Their
	// Create/Update events are filtered upstream by secretWatchPredicate, so
	// this mapping only runs for Delete events (and tests), where the
	// recreation of a manually deleted secret must be triggered.
	t.Run("operator-owned jks secret maps to its owner CR without any List call", func(t *testing.T) {
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srUserTLS, srOther, srLabeled)
		expectRequests(t, r.mapSecretToRequests(ctx, operatorOwnedSecret("sr-name-jks")), "sr-name")
		if listCalls != 0 {
			t.Errorf("expected zero List calls for an operator-owned -jks secret, got %d", listCalls)
		}
	})

	t.Run("operator-owned managed TLS secret maps to its owner CR without any List call", func(t *testing.T) {
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srUserTLS, srOther, srLabeled)
		expectRequests(t, r.mapSecretToRequests(ctx, operatorOwnedSecret("sr-name-tls")), "sr-name")
		if listCalls != 0 {
			t.Errorf("expected zero List calls for an operator-owned -tls secret, got %d", listCalls)
		}
	})

	t.Run("derived user TLS jks secret maps to its owner CR without any List call", func(t *testing.T) {
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srUserTLS, srOther, srLabeled)
		expectRequests(t, r.mapSecretToRequests(ctx, operatorOwnedSecret("user-tls-secret-jks")), "sr-name")
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

	t.Run("labeled cluster CA key secret enqueues the CRs of the cluster", func(t *testing.T) {
		// Strimzi creates and rotates the CA key and cert secrets
		// independently; a <cluster>-cluster-ca event must converge the CRs
		// of the cluster (CA-key convergence).
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "kafka-cluster-cluster-ca",
			Namespace: "default",
			Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
		}}
		expectRequests(t, reconciler.mapSecretToRequests(ctx, secret), "sr-name", "sr-labeled")
	})

	t.Run("cluster CA key secret skips the name-path List", func(t *testing.T) {
		// Like the CA cert secret, the CA key secret can only match via the
		// label path, so the namespace-wide spec.tlssecretname List must not
		// run for it.
		listCalls := 0
		r := newCountingReconciler(t, &listCalls, srName, srLabeled)
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "kafka-cluster-cluster-ca",
			Namespace: "default",
			Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
		}}
		expectRequests(t, r.mapSecretToRequests(ctx, secret), "sr-name", "sr-labeled")
		if listCalls != 1 {
			t.Errorf("expected exactly 1 List call (the label path) for a CA key secret, got %d", listCalls)
		}
	})

	t.Run("unlabeled cluster CA key secret enqueues nothing", func(t *testing.T) {
		// Without the Strimzi label the CA key secret cannot match the label
		// path, and the name path is skipped for CA secrets: no CR can
		// reference a CA secret via spec.tlssecretname.
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: "default"}}
		expectRequests(t, reconciler.mapSecretToRequests(ctx, secret))
	})
}

// TestSecretWatchPredicate covers the Secret watch predicate paired with
// mapSecretToRequests: Create/Update/Generic events of operator-controlled
// output secrets are suppressed (loop prevention), their Delete events pass
// through so a manually deleted secret is recreated, and secrets the
// operator does not control flow for every event type.
func TestSecretWatchPredicate(t *testing.T) {
	operatorOwned := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
					Kind:       "StrimziSchemaRegistry",
					Name:       "sr-name",
					UID:        "test-uid",
					Controller: ptr.To(true),
				}},
			},
		}
	}
	userSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "user-tls-secret", Namespace: "default"}}
	p := secretWatchPredicate()

	t.Run("operator-owned secret Create/Update/Generic events are suppressed", func(t *testing.T) {
		secret := operatorOwned("sr-name-jks")
		if p.Create(event.CreateEvent{Object: secret}) {
			t.Error("expected the Create event of an operator-owned secret to be suppressed")
		}
		if p.Update(event.UpdateEvent{ObjectOld: secret, ObjectNew: secret}) {
			t.Error("expected the Update event of an operator-owned secret to be suppressed")
		}
		if p.Generic(event.GenericEvent{Object: secret}) {
			t.Error("expected the Generic event of an operator-owned secret to be suppressed")
		}
	})

	t.Run("operator-owned secret Delete event passes through", func(t *testing.T) {
		if !p.Delete(event.DeleteEvent{Object: operatorOwned("sr-name-jks")}) {
			t.Error("expected the Delete event of an operator-owned secret to pass through")
		}
	})

	t.Run("uncontrolled secrets flow for every event type", func(t *testing.T) {
		if !p.Create(event.CreateEvent{Object: userSecret}) {
			t.Error("expected the Create event of an uncontrolled secret to pass through")
		}
		if !p.Update(event.UpdateEvent{ObjectOld: userSecret, ObjectNew: userSecret}) {
			t.Error("expected the Update event of an uncontrolled secret to pass through")
		}
		if !p.Delete(event.DeleteEvent{Object: userSecret}) {
			t.Error("expected the Delete event of an uncontrolled secret to pass through")
		}
		if !p.Generic(event.GenericEvent{Object: userSecret}) {
			t.Error("expected the Generic event of an uncontrolled secret to pass through")
		}
	})

	t.Run("secrets controlled by another kind are not suppressed", func(t *testing.T) {
		// The suppression must key on the StrimziSchemaRegistry controller
		// kind; a secret owned by e.g. a Deployment keeps flowing.
		foreign := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foreign-secret",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "some-deploy",
					UID:        "other-uid",
					Controller: ptr.To(true),
				}},
			},
		}
		if !p.Create(event.CreateEvent{Object: foreign}) {
			t.Error("expected the Create event of a foreign-controlled secret to pass through")
		}
		if !p.Update(event.UpdateEvent{ObjectOld: foreign, ObjectNew: foreign}) {
			t.Error("expected the Update event of a foreign-controlled secret to pass through")
		}
		if !p.Generic(event.GenericEvent{Object: foreign}) {
			t.Error("expected the Generic event of a foreign-controlled secret to pass through")
		}
	})

	t.Run("released secret Update event passes through", func(t *testing.T) {
		// Old object controlled, new object released: the event must flow so
		// the now-uncontrolled secret keeps being matched by name/label.
		if !p.Update(event.UpdateEvent{ObjectOld: operatorOwned("sr-name-jks"), ObjectNew: userSecret}) {
			t.Error("expected the Update event of a released secret to pass through")
		}
	})
}

// TestSecretEventPipelineBounded guards the pairing of secretWatchPredicate
// with mapSecretToRequests: simulating the watch pipeline (predicate → map)
// over the operator's own Create event plus a burst of self-triggered Update
// events must enqueue nothing (no reconcile storm), while a single Delete
// event enqueues the owning CR exactly once.
func TestSecretEventPipelineBounded(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)

	sr := newTestInstance()
	sr.Name = "sr-name"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sr).Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	ctx := context.Background()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sr-name-jks",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
				Kind:       "StrimziSchemaRegistry",
				Name:       "sr-name",
				UID:        "test-uid",
				Controller: ptr.To(true),
			}},
		},
	}

	p := secretWatchPredicate()

	// Operator-issued writes: one Create followed by a burst of Updates —
	// the pipeline must enqueue nothing at all.
	enqueued := 0
	if p.Create(event.CreateEvent{Object: secret}) {
		enqueued += len(r.mapSecretToRequests(ctx, secret))
	}
	for i := 0; i < 10; i++ {
		if p.Update(event.UpdateEvent{ObjectOld: secret, ObjectNew: secret}) {
			enqueued += len(r.mapSecretToRequests(ctx, secret))
		}
	}
	if enqueued != 0 {
		t.Errorf("expected 0 enqueued requests from operator-issued Create/Update events, got %d", enqueued)
	}

	// Manual deletion: exactly one request for the owning CR.
	if !p.Delete(event.DeleteEvent{Object: secret}) {
		t.Fatal("expected the Delete event of an operator-owned secret to pass through")
	}
	requests := r.mapSecretToRequests(ctx, secret)
	if len(requests) != 1 {
		t.Fatalf("expected exactly 1 request for the owning CR, got %d: %v", len(requests), requests)
	}
	if requests[0].Name != "sr-name" || requests[0].Namespace != "default" {
		t.Errorf("expected a request for default/sr-name, got %v", requests[0])
	}
}

// TestCASecretEventPipelineBounded guards the CA-key convergence mapping end
// to end: a <cluster>-cluster-ca secret event (Create/Update/Generic —
// Strimzi owns that secret, so every event type flows) enqueues the CRs of
// the cluster exactly once per event, while the operator's own output-secret
// writes issued during the resulting reconciles stay suppressed by
// secretWatchPredicate — the pipeline stays bounded (no reconcile storm) when
// the CA key secret is created after the CA cert secret.
func TestCASecretEventPipelineBounded(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)

	sr := newTestInstance()
	sr.Name = "sr-name"
	sr.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sr).Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	ctx := context.Background()

	p := secretWatchPredicate()
	caKeySecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "kafka-cluster-cluster-ca",
		Namespace: "default",
		Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
	}}

	// Every CA key secret event flows — Strimzi owns the secret, the
	// operator never writes it — and each enqueues the CR exactly once.
	enqueued := 0
	if !p.Create(event.CreateEvent{Object: caKeySecret}) {
		t.Fatal("expected the Create event of a Strimzi CA key secret to pass through")
	}
	enqueued += len(r.mapSecretToRequests(ctx, caKeySecret))
	for i := 0; i < 10; i++ {
		if !p.Update(event.UpdateEvent{ObjectOld: caKeySecret, ObjectNew: caKeySecret}) {
			t.Fatal("expected the Update event of a Strimzi CA key secret to pass through")
		}
		enqueued += len(r.mapSecretToRequests(ctx, caKeySecret))
	}
	if !p.Generic(event.GenericEvent{Object: caKeySecret}) {
		t.Fatal("expected the Generic event of a Strimzi CA key secret to pass through")
	}
	enqueued += len(r.mapSecretToRequests(ctx, caKeySecret))
	if enqueued != 12 {
		t.Fatalf("expected 12 enqueued requests (exactly 1 per CA key secret event), got %d", enqueued)
	}

	// The reconciles triggered by those events write the operator's own
	// output secrets; their Create/Update events must stay suppressed so the
	// pipeline converges instead of looping (1.7 loop prevention).
	for _, name := range []string{"sr-name-jks", "sr-name-tls"} {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
					Kind:       "StrimziSchemaRegistry",
					Name:       "sr-name",
					UID:        "test-uid",
					Controller: ptr.To(true),
				}},
			},
		}
		if p.Create(event.CreateEvent{Object: secret}) {
			t.Errorf("expected the Create event of the operator-owned %s to be suppressed", name)
		}
		if p.Update(event.UpdateEvent{ObjectOld: secret, ObjectNew: secret}) {
			t.Errorf("expected the Update event of the operator-owned %s to be suppressed", name)
		}
	}
}

// realPEMSecretData generates a real certificate and key with the shared
// testutil helpers so the PEM → JKS conversion under test actually runs the
// pure-Go conversion pipeline.
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

// realPEMSecretDataWithCA is realPEMSecretData plus the issuing CA under the
// optional ca.crt key — the full shape cert-manager writes for a
// kubernetes.io/tls secret. It also returns the generated CA so tests can
// assert on the threaded chain.
func realPEMSecretDataWithCA(t *testing.T) (map[string][]byte, *testutil.TestCA) {
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
		"ca.crt":  []byte(ca.CACertPEM),
	}, ca
}

// jksCertificates decodes JKS keystore bytes in Go with the keystore-go
// library and returns the certificates the store holds — for the derived
// keystore that is the private key entry's full certificate chain.
func jksCertificates(t *testing.T, keystoreBytes []byte, password string) []*x509.Certificate {
	t.Helper()
	store := keystore.New(keystore.WithOrderedAliases())
	if err := store.Load(bytes.NewReader(keystoreBytes), []byte(password)); err != nil {
		t.Fatalf("failed to decode the derived keystore: %v", err)
	}
	var certs []*x509.Certificate
	for _, alias := range store.Aliases() {
		var chain []keystore.Certificate
		if store.IsPrivateKeyEntry(alias) {
			entryChain, err := store.GetPrivateKeyEntryCertificateChain(alias)
			if err != nil {
				t.Fatalf("failed to read the certificate chain of the keystore entry %q: %v", alias, err)
			}
			chain = entryChain
		} else if store.IsTrustedCertificateEntry(alias) {
			entry, err := store.GetTrustedCertificateEntry(alias)
			if err != nil {
				t.Fatalf("failed to read the keystore entry %q: %v", alias, err)
			}
			chain = []keystore.Certificate{entry.Certificate}
		} else {
			continue
		}
		for _, c := range chain {
			cert, err := x509.ParseCertificate(c.Content)
			if err != nil {
				t.Fatalf("failed to parse a certificate from the derived keystore: %v", err)
			}
			certs = append(certs, cert)
		}
	}
	return certs
}

// TestBuildDerivedJKSSecretThreadCACert covers the ca.crt threading: the user
// TLS secret's optional ca.crt must reach the JKS conversion so the derived
// keystore carries the issuing chain; without ca.crt the derived keystore is
// unchanged (leaf only); and an unparseable ca.crt fails with an actionable
// error naming the offending key.
func TestBuildDerivedJKSSecretThreadCACert(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	newInstance := func() *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-tls"
		return inst
	}
	newUserSecret := func(data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       data,
		}
	}

	t.Run("with ca.crt the derived keystore carries the issuing chain", func(t *testing.T) {
		inst := newInstance()
		data, ca := realPEMSecretDataWithCA(t)
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		derived, err := reconciler.buildDerivedJKSSecret(context.Background(), inst, newUserSecret(data), logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		certs := jksCertificates(t, derived.Data["tls-keystore.jks"], string(derived.Data["keystore_password"]))
		if len(certs) != 2 {
			t.Fatalf("expected the derived keystore to hold the leaf and the CA chain, got %d certs", len(certs))
		}
		var leaf *x509.Certificate
		foundCA := false
		for _, cert := range certs {
			if bytes.Equal(cert.Raw, ca.CACert.Raw) {
				foundCA = true
			} else {
				leaf = cert
			}
		}
		if !foundCA {
			t.Fatal("expected the provided ca.crt certificate to be part of the derived keystore chain")
		}
		if leaf == nil {
			t.Fatal("expected the leaf certificate in the derived keystore")
		}
		// The threaded chain must make the leaf validate against the CA the
		// user provided.
		roots := x509.NewCertPool()
		roots.AddCert(ca.CACert)
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
			t.Errorf("expected the leaf certificate to validate against the threaded CA, got: %v", err)
		}
	})

	t.Run("without ca.crt the derived keystore holds only the leaf", func(t *testing.T) {
		inst := newInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		derived, err := reconciler.buildDerivedJKSSecret(context.Background(), inst, newUserSecret(realPEMSecretData(t)), logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		certs := jksCertificates(t, derived.Data["tls-keystore.jks"], string(derived.Data["keystore_password"]))
		if len(certs) != 1 {
			t.Fatalf("expected the derived keystore to hold only the leaf certificate, got %d certs", len(certs))
		}
	})

	t.Run("invalid ca.crt fails with an actionable error", func(t *testing.T) {
		inst := newInstance()
		data := realPEMSecretData(t)
		data["ca.crt"] = []byte("not a valid PEM certificate")
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		_, err := reconciler.buildDerivedJKSSecret(context.Background(), inst, newUserSecret(data), logr.Logger{})
		if err == nil {
			t.Fatal("expected an error for an unparseable ca.crt, got nil")
		}
		if !strings.Contains(err.Error(), "ca.crt") {
			t.Errorf("expected the error to name the offending key ca.crt, got: %v", err)
		}
	})
}

// sec1ECPrivateKeyPEM generates an ECDSA P-256 key and encodes it in the
// traditional SEC1 "EC PRIVATE KEY" PEM format used for EC keys — one of the
// private key formats the PEM material validation must accept.
func sec1ECPrivateKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatalf("failed to generate EC key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal EC key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// TestHasPEMTLSSecretKeys covers the TLS-secret detection used by the user
// TLS secret validation: a secret carrying any of the standard
// kubernetes.io/tls keys — including a partially populated one — and a
// secret typed kubernetes.io/tls are recognized as PEM TLS secrets so they
// get precise error paths, while unrelated secrets are not.
func TestHasPEMTLSSecretKeys(t *testing.T) {
	tests := []struct {
		name   string
		secret *corev1.Secret
		want   bool
	}{
		{name: "nil secret", secret: nil, want: false},
		{
			name:   "both tls.crt and tls.key",
			secret: &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("c"), "tls.key": []byte("k")}},
			want:   true,
		},
		{
			name:   "partial: tls.crt only",
			secret: &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("c")}},
			want:   true,
		},
		{
			name:   "partial: tls.key only",
			secret: &corev1.Secret{Data: map[string][]byte{"tls.key": []byte("k")}},
			want:   true,
		},
		{
			name:   "empty-valued tls keys still mark the shape",
			secret: &corev1.Secret{Data: map[string][]byte{"tls.crt": {}, "tls.key": {}}},
			want:   true,
		},
		{
			name:   "kubernetes.io/tls type without keys",
			secret: &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{}},
			want:   true,
		},
		{
			name:   "kubernetes.io/tls type with unrelated keys only",
			secret: &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{"unrelated": []byte("x")}},
			want:   true,
		},
		{
			name:   "opaque secret with keystore keys only",
			secret: &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": []byte("jks"), "keystore_password": []byte("pw")}},
			want:   false,
		},
		{
			name:   "opaque empty secret",
			secret: &corev1.Secret{Data: map[string][]byte{}},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasPEMTLSSecretKeys(tt.secret); got != tt.want {
				t.Errorf("hasPEMTLSSecretKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidateUserTLSSecret covers the shape-dispatching validation wrapper:
// standard kubernetes.io/tls (PEM) secrets, pre-built keystores following
// spec.keystoretype, and the derived-secret name collision guard. The PEM
// cases use real certificate/key material because the validation now parses
// the PEM content, and partial PEM secrets must surface the missing key
// instead of falling back to the keystore contract.
func TestValidateUserTLSSecret(t *testing.T) {
	newInstance := func(tlsSecretName string) *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = tlsSecretName
		return inst
	}

	// Real PEM material: the validation parses tls.crt/tls.key, so the
	// fixtures must be genuine certificate and key PEM.
	realPEM := realPEMSecretData(t)
	realCertPEM := realPEM["tls.crt"]
	realKeyPEM := realPEM["tls.key"]
	realPEMWithCA, _ := realPEMSecretDataWithCA(t)

	tests := []struct {
		name         string
		instance     *strimziregistryoperatorv1beta1.StrimziSchemaRegistry
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
			secret:   &corev1.Secret{Data: realPEMWithCA},
		},
		{
			name:     "valid PEM secret with a SEC1 EC private key passes",
			instance: newInstance("user-tls"),
			secret: &corev1.Secret{Data: map[string][]byte{
				"tls.crt": realCertPEM,
				"tls.key": sec1ECPrivateKeyPEM(t),
			}},
		},
		{
			name:         "PEM secret with empty tls.key is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": realCertPEM, "tls.key": {}}},
			wantInvalid:  true,
			wantMentions: []string{"tls.key"},
		},
		{
			name:         "PEM secret with empty tls.crt is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": {}, "tls.key": realKeyPEM}},
			wantInvalid:  true,
			wantMentions: []string{"tls.crt"},
		},
		{
			name:         "partial PEM secret with only tls.crt reports the missing tls.key",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": realCertPEM}},
			wantInvalid:  true,
			wantMentions: []string{"tls.key"},
		},
		{
			name:         "partial PEM secret with only tls.key reports the missing tls.crt",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.key": realKeyPEM}},
			wantInvalid:  true,
			wantMentions: []string{"tls.crt"},
		},
		{
			name:         "kubernetes.io/tls-typed secret without keys is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Type: corev1.SecretTypeTLS, Data: map[string][]byte{}},
			wantInvalid:  true,
			wantMentions: []string{"tls.crt", "tls.key"},
		},
		{
			name:         "PEM secret with unparseable tls.crt is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": []byte("not a valid PEM certificate"), "tls.key": realKeyPEM}},
			wantInvalid:  true,
			wantMentions: []string{"tls.crt"},
		},
		{
			name:         "PEM secret with unparseable tls.key is invalid",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": realCertPEM, "tls.key": []byte("not a valid PEM private key")}},
			wantInvalid:  true,
			wantMentions: []string{"tls.key"},
		},
		{
			name: "tlssecretname equal to the CR name no longer collides (per-registry derived name)",
			// The derived secret is named <name>-<tlssecretname>-jks, so
			// tlssecretname == CR name cannot collide with the
			// operator-managed <name>-jks / <name>-tls secrets.
			instance: newInstance("test-sr"),
			secret:   &corev1.Secret{Data: map[string][]byte{"tls.crt": realCertPEM, "tls.key": realKeyPEM}},
		},
		{
			name: "derived secret name exceeding the 253-character limit is invalid",
			instance: func() *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
				inst := newInstance("user-tls")
				inst.Name = strings.Repeat("a", 241)
				return inst
			}(),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls.crt": realCertPEM, "tls.key": realKeyPEM}},
			wantInvalid:  true,
			wantMentions: []string{"253"},
		},
		{
			name:     "JKS keystore contract still passes",
			instance: newInstance("user-tls"),
			secret:   &corev1.Secret{Data: validPrebuiltJKSSecretData(t)},
		},
		{
			name:         "garbage JKS keystore material is invalid (T-4)",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: map[string][]byte{"tls-keystore.jks": []byte("not-a-keystore"), "keystore_password": []byte("store-password"), "key_password": []byte("store-password")}},
			wantInvalid:  true,
			wantMentions: []string{"tls-keystore.jks"},
		},
		{
			name:         "PKCS12 keystore contract is validated per keystoretype",
			instance:     newInstance("user-tls"),
			secret:       &corev1.Secret{Data: validPrebuiltJKSSecretData(t)},
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

// TestDerivedSecretNameValid unit-tests the object-name limit guard for the
// per-registry derived <name>-<tlssecretname>-jks secret name.
func TestDerivedSecretNameValid(t *testing.T) {
	t.Run("a normal name is valid", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.TLSSecretName = "user-tls"
		if msg := derivedSecretNameValid(inst); msg != "" {
			t.Errorf("expected no validation message, got %q", msg)
		}
	})

	t.Run("a name exceeding 253 characters is rejected", func(t *testing.T) {
		inst := newTestInstance()
		inst.Name = strings.Repeat("a", 241)
		inst.Spec.TLSSecretName = "user-tls"
		if msg := derivedSecretNameValid(inst); msg == "" {
			t.Error("expected a validation message, got empty")
		}
	})
}

// TestEnsureDerivedJKSSecret covers the derived-secret lifecycle with a fake
// client: created when missing, idempotent when up-to-date, and regenerated
// only when the user secret's resourceVersion changes.
func TestEnsureDerivedJKSSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	newInstance := func() *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-tls"
		return inst
	}
	// The derived secret is named per registry: <name>-<tlssecretname>-jks.
	derivedName := types.NamespacedName{Name: "test-sr-user-tls-jks", Namespace: "default"}

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

	t.Run("refuses to adopt a secret controlled by another registry", func(t *testing.T) {
		inst := newInstance()
		other := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sr-user-tls-jks",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
					Kind:       "StrimziSchemaRegistry",
					Name:       "other-sr",
					UID:        "other-uid",
					Controller: ptr.To(true),
				}},
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, other).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       realPEMSecretData(t),
		}

		err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, userSecret, logr.Logger{})
		if err == nil {
			t.Fatal("expected an error for a derived secret controlled by another registry")
		}
		if !strings.Contains(err.Error(), "other-sr") {
			t.Errorf("expected the error to name the controlling registry, got %q", err.Error())
		}
	})

	t.Run("refuses to adopt an unowned secret", func(t *testing.T) {
		inst := newInstance()
		unowned := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr-user-tls-jks", Namespace: "default"},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, unowned).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
			Data:       realPEMSecretData(t),
		}

		err := reconciler.ensureDerivedJKSSecret(context.Background(), inst, userSecret, logr.Logger{})
		if err == nil {
			t.Fatal("expected an error for an unowned derived secret")
		}
		if !strings.Contains(err.Error(), "not controlled") {
			t.Errorf("expected the error to explain the ownership problem, got %q", err.Error())
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
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	newInstance := func() *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-tls"
		return inst
	}
	// The derived secret is named per registry: <name>-<tlssecretname>-jks.
	derivedName := types.NamespacedName{Name: "test-sr-user-tls-jks", Namespace: "default"}
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
				Name:      "test-sr-user-tls-jks",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
					Kind:       "StrimziSchemaRegistry",
					Name:       "test-sr",
					UID:        "test-uid",
					Controller: ptr.To(true),
				}},
				Annotations: map[string]string{userSecretVersionKey: "100"},
			},
			Data: map[string][]byte{
				"tls-keystore.jks":  []byte("stale-keystore"),
				"keystore_password": []byte("stale-password"),
				"key_password":      []byte("stale-password"),
			},
		}
		dep := newRestAPITLSDeployment("test-sr-user-tls-jks")
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
		if got := restAPITLSVolumeSecretName(dep); got != "test-sr-user-tls-jks" {
			t.Errorf("expected the rest-api-tls volume to mount the derived secret, got %q", got)
		}
		for _, envName := range []string{sslKeystorePasswordEnv, sslKeyPasswordEnv} {
			found := false
			for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
				if e.Name == envName {
					found = true
					if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil || e.ValueFrom.SecretKeyRef.Name != "test-sr-user-tls-jks" {
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

// TestHandleUserTLSSecretRotationPEM_DerivedSecretError covers the U-10 error
// branch: when the derived JKS secret regeneration fails (here: the derived
// secret exists but is controlled by another registry, which
// ensureDerivedJKSSecret refuses to touch), the error must be propagated and
// the deployment must not be rolled.
func TestHandleUserTLSSecretRotationPEM_DerivedSecretError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = "user-tls"
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
		Data:       realPEMSecretData(t),
	}
	// The derived secret exists but is owned by a DIFFERENT registry:
	// ensureDerivedJKSSecret must refuse to touch it.
	foreignDerived := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sr-user-tls-jks",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
				Kind:       "StrimziSchemaRegistry",
				Name:       "another-registry",
				UID:        "other-uid",
				Controller: ptr.To(true),
			}},
		},
		Data: map[string][]byte{"tls-keystore.jks": []byte("foreign")},
	}
	dep := newRestAPITLSDeployment("user-tls")
	dep.Spec.Template.Annotations = map[string]string{tlsVersionKey: "50"}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, userSecret, foreignDerived, dep).
		Build()
	reconciler := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	result, err := reconciler.handleUserTLSSecretRotation(context.Background(), inst, logr.Logger{})
	if err == nil {
		t.Fatal("expected the derived-secret ownership conflict to be returned")
	}
	if !strings.Contains(err.Error(), "test-sr-user-tls-jks") {
		t.Errorf("expected the error to name the derived secret, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on the error path, got %v", result.RequeueAfter)
	}

	// The deployment must not be rolled: the tlsVersion annotation still
	// records the old user secret version.
	untouched := &apps.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, untouched); err != nil {
		t.Fatalf("failed to get the deployment: %v", err)
	}
	if got := untouched.Spec.Template.Annotations[tlsVersionKey]; got != "50" {
		t.Errorf("expected the tlsVersion annotation to stay %q (no rollout), got %q", "50", got)
	}
}

// TestHandleUserTLSSecretRotationPEM_LegacyMountMigration covers the U-17
// upgrade-migration branch: the deployment's tlsVersion annotation already
// matches the user secret version, but the rest-api-tls volume still mounts
// the legacy derived-secret name. The mounts must be repointed and the pods
// rolled exactly once, then the state must be stable.
func TestHandleUserTLSSecretRotationPEM_LegacyMountMigration(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = "user-tls"
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: "default", ResourceVersion: "100"},
		Data:       realPEMSecretData(t),
	}
	// The derived secret is up to date (annotation matches the user secret
	// version) but the deployment mounts the LEGACY derived-secret name.
	derived := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sr-user-tls-jks",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "strimziregistryoperator.randsw.code/v1beta1",
				Kind:       "StrimziSchemaRegistry",
				Name:       "test-sr",
				UID:        "test-uid",
				Controller: ptr.To(true),
			}},
			Annotations: map[string]string{userSecretVersionKey: "100"},
		},
		Data: map[string][]byte{
			"tls-keystore.jks":  []byte("keystore"),
			"keystore_password": []byte("password"),
			"key_password":      []byte("password"),
		},
	}
	dep := newRestAPITLSDeployment("legacy-derived-name")
	dep.Spec.Template.Annotations = map[string]string{tlsVersionKey: "100"}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, userSecret, derived, dep).
		Build()
	reconciler := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	ctx := context.Background()

	result, err := reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Errorf("expected requeue after 1s for the migration rollout, got %v", result.RequeueAfter)
	}

	migrated := &apps.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, migrated); err != nil {
		t.Fatalf("failed to get the deployment: %v", err)
	}
	if got := restAPITLSVolumeSecretName(migrated); got != "test-sr-user-tls-jks" {
		t.Errorf("expected the rest-api-tls volume to be repointed at the derived secret, got %q", got)
	}
	for _, envName := range []string{sslKeystorePasswordEnv, sslKeyPasswordEnv} {
		for _, e := range migrated.Spec.Template.Spec.Containers[0].Env {
			if e.Name == envName && (e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil || e.ValueFrom.SecretKeyRef.Name != "test-sr-user-tls-jks") {
				t.Errorf("expected %s to reference the derived secret, got %+v", envName, e.ValueFrom)
			}
		}
	}
	if got := migrated.Spec.Template.Annotations[tlsVersionKey]; got != "100" {
		t.Errorf("expected the tlsVersion annotation to stay %q (the user secret did not rotate), got %q", "100", got)
	}

	// The next reconcile must be stable: no spurious second rollout.
	result, err = reconciler.handleUserTLSSecretRotation(ctx, inst, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error on the second reconcile: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue once the migration is complete, got %v", result.RequeueAfter)
	}
}
