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
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestHandleSASLSecretRotationAnnotationRollout verifies that the SASL
// rotation handler drives the truststore rollout from the jksVersion
// pod-template annotation, both after a cluster CA rotation and when the
// secret is already up-to-date but the recorded annotation is stale (the
// state left behind by a failed Deployment update).
func TestHandleSASLSecretRotationAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-sasl-annotation")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	auth := kafkaAuth{Protocol: "SASL_SSL", Mode: authSASLSSL, NeedsTruststore: true, NeedsSASL: true}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	newClusterCASecret := func(resourceVersion string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            cluster + clusterCASuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: resourceVersion,
			},
			Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
		}
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name,
			Namespace:       inst.Namespace,
			ResourceVersion: "2000",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"sasl.jaas.config": []byte("jaas")},
	}
	// An up-to-date truststore secret: its CA marker matches the cluster CA
	// version, so the rollout decision must come from the annotation.
	newJKSSecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + jksSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: "3000",
				Annotations:     map[string]string{CAVersionKey: "1000"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"truststore.jks": []byte("stub")},
		}
	}
	newClient := func(caVersion string, depAnnotations map[string]string) *StrimziSchemaRegistryReconciler {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newClusterCASecret(caVersion), userSecret.DeepCopy(), newJKSSecret(),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, depAnnotations)).
			Build()
		return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	}

	t.Run("patches the deployment when the jksVersion annotation is missing", func(t *testing.T) {
		c := newClient("1000", map[string]string{saslUserVersionKey: "2000"})
		result, err := c.handleSASLSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rollout patch")
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the jksVersion annotation %q, got %q", "3000", got)
		}
	})

	t.Run("does not patch when the annotations match the secret versions", func(t *testing.T) {
		c := newClient("1000", map[string]string{jksVersionKey: "3000", saslUserVersionKey: "2000"})
		result, err := c.handleSASLSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue when nothing changed, got %v", result.RequeueAfter)
		}
	})

	t.Run("patches after the cluster CA is rotated", func(t *testing.T) {
		c := newClient("1001", map[string]string{saslUserVersionKey: "2000"})
		result, err := c.handleSASLSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rotation rollout")
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got == "" || got == "3000" {
			t.Errorf("expected a fresh jksVersion annotation after the rotation, got %q", got)
		}
		rebuilt := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + jksSecretSuffix, Namespace: inst.Namespace}, rebuilt); err != nil {
			t.Fatalf("failed to get the rebuilt -jks secret: %v", err)
		}
		if rebuilt.Annotations[CAVersionKey] != "1001" {
			t.Errorf("expected the rebuilt secret to record the rotated CA version, got %q", rebuilt.Annotations[CAVersionKey])
		}
	})

	t.Run("does not spuriously roll on the following reconcile", func(t *testing.T) {
		c := newClient("1000", map[string]string{saslUserVersionKey: "2000"})
		ctx := context.Background()
		if _, err := c.handleSASLSecretRotation(ctx, inst, logr.Logger{}, cluster, auth); err != nil {
			t.Fatalf("unexpected error on the first reconcile: %v", err)
		}
		result, err := c.handleSASLSecretRotation(ctx, inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error on the second reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue once the annotations match, got %v", result.RequeueAfter)
		}
	})
}

// TestHandleClientAuthTruststoreRotationAnnotationRollout verifies that the
// client-auth truststore handler drives the Deployment rollout from the
// jksVersion pod-template annotation: it patches when the secret is newly
// created or the recorded annotation is stale, and does not roll spuriously
// once the annotation matches the up-to-date secret.
func TestHandleClientAuthTruststoreRotationAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-clientauth-annotation")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.ClientAuth = true
	inst.Spec.SecureHTTP = true
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	clusterCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	// An up-to-date truststore secret: its CA marker matches the cluster CA
	// version, so ensureClientAuthTruststoreSecret reports created=false.
	newJKSSecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + jksSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: "3000",
				Annotations:     map[string]string{CAVersionKey: "1000"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"truststore.jks": []byte("stub")},
		}
	}
	newClient := func(withJKS bool, depAnnotations map[string]string) *StrimziSchemaRegistryReconciler {
		builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, clusterCASecret.DeepCopy())
		if withJKS {
			builder = builder.WithObjects(newJKSSecret())
		}
		c := builder.WithObjects(newAnnotationRolloutDeployment(depName.Name, depName.Namespace, depAnnotations)).Build()
		return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	}

	t.Run("patches the deployment when the truststore secret is newly created", func(t *testing.T) {
		c := newClient(false, nil)
		result, err := c.handleClientAuthTruststoreRotation(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rollout patch")
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got == "" {
			t.Error("expected a non-empty jksVersion annotation after the rollout")
		}
	})

	t.Run("does not patch when the annotation matches the up-to-date secret", func(t *testing.T) {
		c := newClient(true, map[string]string{jksVersionKey: "3000"})
		result, err := c.handleClientAuthTruststoreRotation(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue when nothing changed, got %v", result.RequeueAfter)
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the annotation to stay %q, got %q", "3000", got)
		}
	})

	t.Run("patches when the recorded annotation is stale", func(t *testing.T) {
		// The -jks secret is up-to-date, but the Deployment still records the
		// previous version — the state left behind by a failed Deployment
		// update. The annotation check must retry the rollout.
		c := newClient(true, map[string]string{jksVersionKey: "2999"})
		result, err := c.handleClientAuthTruststoreRotation(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the recovery patch")
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the jksVersion annotation %q, got %q", "3000", got)
		}
	})
}

// TestHandleSASLSecretRotation_ClusterCAGetError covers the U-8 error branch:
// a cluster CA secret Get failure in SASL_SSL must be returned to the caller
// instead of being treated as a rotation trigger, and no truststore rebuild
// may be attempted (the -jks secret stays untouched).
func TestHandleSASLSecretRotation_ClusterCAGetError(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	auth := kafkaAuth{Protocol: "SASL_SSL", Mode: authSASLSSL, NeedsTruststore: true, NeedsSASL: true}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name,
			Namespace:       inst.Namespace,
			ResourceVersion: "2000",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"sasl.jaas.config": []byte("jaas")},
	}
	jksSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name + jksSecretSuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "3000",
			Annotations:     map[string]string{CAVersionKey: "1000"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"truststore.jks": []byte("stub")},
	}
	injected := errors.New("injected cluster CA secret get failure")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, userSecret.DeepCopy(), jksSecret.DeepCopy(),
			newAnnotationRolloutDeployment(depName.Name, depName.Namespace, nil)).
		WithInterceptorFuncs(failGets(&corev1.Secret{}, cluster+clusterCASuffix, injected)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	result, err := r.handleSASLSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
	if err == nil {
		t.Fatal("expected the cluster CA secret Get failure to be returned")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected the injected error, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on the error path, got %v", result.RequeueAfter)
	}

	// No truststore rebuild may be attempted: the -jks secret is untouched.
	untouched := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + jksSecretSuffix, Namespace: inst.Namespace}, untouched); err != nil {
		t.Fatalf("failed to get the -jks secret: %v", err)
	}
	if untouched.ResourceVersion != "3000" {
		t.Errorf("expected the -jks secret to be untouched, got resourceVersion %s", untouched.ResourceVersion)
	}
}
