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

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestHandleMTLSSecretRotationSelfHeal verifies that a manually deleted -jks
// secret is recreated (and the deployment rolled) when the Deployment already
// exists: without this the next pod restart fails to mount the secret.
func TestHandleMTLSSecretRotationSelfHeal(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	user, err := testutil.GenerateTestUserCert(ca, "user-password")
	if err != nil {
		t.Fatalf("failed to generate user certificate: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	auth := kafkaAuth{Protocol: "SSL", Mode: authMTLS, NeedsTruststore: true, NeedsKeystore: true}

	clusterCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name,
			Namespace:       inst.Namespace,
			ResourceVersion: "2000",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt":        []byte(ca.CACertPEM),
			"user.crt":      []byte(user.UserCertPEM),
			"user.key":      []byte(user.UserKeyPEM),
			"user.p12":      user.PKCS12Data,
			"user.password": []byte(user.Password),
		},
	}
	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name + deploySuffix, Namespace: inst.Namespace},
		Spec: apps.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "confluent-sr"}}},
			},
		},
	}

	t.Run("recreates a deleted -jks secret and rolls the deployment", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, clusterCASecret.DeepCopy(), userSecret.DeepCopy(), deployment.DeepCopy()).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		result, err := reconciler.handleMTLSSecretRotation(ctx, inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after recreating the secret")
		}

		recreated := &corev1.Secret{}
		if err := client.Get(ctx, types.NamespacedName{Name: inst.Name + jksSecretSuffix, Namespace: inst.Namespace}, recreated); err != nil {
			t.Fatalf("expected the -jks secret to be recreated: %v", err)
		}
		if len(recreated.Data["truststore.jks"]) == 0 || len(recreated.Data["keystore.jks"]) == 0 {
			t.Error("expected the recreated secret to contain keystore and truststore material")
		}
		if got := recreated.Annotations[CAVersionKey]; got != clusterCASecret.ResourceVersion {
			t.Errorf("expected CAVersionKey annotation %q, got %q", clusterCASecret.ResourceVersion, got)
		}
		if got := recreated.Annotations[userVersionKey]; got != userSecret.ResourceVersion {
			t.Errorf("expected userVersionKey annotation %q, got %q", userSecret.ResourceVersion, got)
		}

		dep := &apps.Deployment{}
		if err := client.Get(ctx, types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[keyPrefix+"/jksVersion"]; got == "" {
			t.Error("expected the jksVersion pod-template annotation to trigger a rollout")
		}
	})

	t.Run("stable once the secret is recreated", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, clusterCASecret.DeepCopy(), userSecret.DeepCopy(), deployment.DeepCopy()).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		// First reconcile heals the missing secret.
		if _, err := reconciler.handleMTLSSecretRotation(ctx, inst, logr.Logger{}, cluster, auth); err != nil {
			t.Fatalf("unexpected error on healing reconcile: %v", err)
		}

		// Second reconcile: the recreated secret's annotations match the user
		// and cluster CA secret versions, so nothing changes.
		result, err := reconciler.handleMTLSSecretRotation(ctx, inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error on second reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue once stable, got %v", result.RequeueAfter)
		}
	})
}

// TestHandleTLSCertExpirySelfHeal verifies that a manually deleted
// operator-managed -tls secret is recreated (and the deployment rolled) when
// the Deployment already exists, and that nothing happens before the
// Deployment exists (createDeployment bootstraps the secret then).
func TestHandleTLSCertExpirySelfHeal(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	// PLAINTEXT keeps the scenario free of -jks material; the expiry handler
	// is independent of the Kafka auth mode.
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}

	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCASuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	caKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCAKeySuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.key": []byte(ca.CAKeyPEM)},
	}
	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name + deploySuffix, Namespace: inst.Namespace},
		Spec: apps.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "confluent-sr"}}},
			},
		},
	}

	t.Run("recreates a deleted -tls secret and rolls the deployment", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy(), deployment.DeepCopy()).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		result, err := reconciler.handleTLSCertExpiry(ctx, inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after recreating the secret")
		}

		recreated := &corev1.Secret{}
		if err := client.Get(ctx, types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, recreated); err != nil {
			t.Fatalf("expected the -tls secret to be recreated: %v", err)
		}
		if len(recreated.Data["tls-keystore.jks"]) == 0 || len(recreated.Data["keystore_password"]) == 0 {
			t.Error("expected the recreated secret to contain keystore material")
		}

		dep := &apps.Deployment{}
		if err := client.Get(ctx, types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}, dep); err != nil {
			t.Fatalf("failed to get deployment: %v", err)
		}
		if got := dep.Spec.Template.Annotations[tlsVersionKey]; got == "" {
			t.Error("expected the tlsVersion pod-template annotation to trigger a rollout")
		}
	})

	t.Run("stable once the secret is recreated", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy(), deployment.DeepCopy()).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		// First reconcile heals the missing secret.
		if _, err := reconciler.handleTLSCertExpiry(ctx, inst, logr.Logger{}, cluster); err != nil {
			t.Fatalf("unexpected error on healing reconcile: %v", err)
		}

		// Second reconcile: the recreated certificate is fresh, so it is not
		// inside the renewal window and nothing changes.
		result, err := reconciler.handleTLSCertExpiry(ctx, inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error on second reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue once stable, got %v", result.RequeueAfter)
		}
	})

	t.Run("no-op when the Deployment does not exist yet", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy()).
			Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
		ctx := context.Background()

		result, err := reconciler.handleTLSCertExpiry(ctx, inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue before the Deployment exists, got %v", result.RequeueAfter)
		}
		err = client.Get(ctx, types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, &corev1.Secret{})
		if !apierrors.IsNotFound(err) {
			t.Errorf("expected no -tls secret to be created before the Deployment exists, got %v", err)
		}
	})
}
