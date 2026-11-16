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
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestHandleTLSCertExpiryAnnotationRollout verifies that the TLS expiry
// handler drives the Deployment rollout from the tlsVersion pod-template
// annotation: it patches when the annotation is stale — including when no
// renewal is due, which is the state left behind by a failed Deployment
// update — and does not roll spuriously once the annotation matches.
func TestHandleTLSCertExpiryAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-tls-expiry-annotation")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}
	cp := certprocessor.NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "test-sr", "default")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	caKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCAKeySuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1001",
		},
		Data: map[string][]byte{"ca.key": []byte(ca.CAKeyPEM)},
	}
	newTLSSecret := func(resourceVersion, caVersion string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + tlsSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: resourceVersion,
				Annotations:     map[string]string{CAVersionKey: caVersion},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls-keystore.jks":  keystore,
				"keystore_password": []byte(password),
				"key_password":      []byte(password),
			},
		}
	}
	newClient := func(caVersion, tlsVersion string) *StrimziSchemaRegistryReconciler {
		depAnnotations := map[string]string{}
		if tlsVersion != "" {
			depAnnotations[tlsVersionKey] = tlsVersion
		}
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy(), newTLSSecret("2000", caVersion),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, depAnnotations)).
			Build()
		return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	}

	t.Run("does not patch when the annotation matches and the certificate is fresh", func(t *testing.T) {
		c := newClient("1000", "2000")
		result, recheck, err := c.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no immediate requeue when nothing changed, got %v", result.RequeueAfter)
		}
		if recheck != tlsCertRecheckInterval {
			t.Errorf("expected the standing recheck interval %v, got %v", tlsCertRecheckInterval, recheck)
		}
		if got := deploymentAnnotation(t, c.Client, depName, tlsVersionKey); got != "2000" {
			t.Errorf("expected the annotation to stay %q, got %q", "2000", got)
		}
	})

	t.Run("patches when the annotation is missing", func(t *testing.T) {
		// No renewal is due, but the Deployment does not record the secret
		// version at all — the shape of a deployment created before the
		// annotation-driven check existed, or whose marker was stripped. The
		// annotation check must record it and roll the pods once.
		c := newClient("1000", "")
		result, recheck, err := c.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the recovery patch")
		}
		if recheck != 0 {
			t.Errorf("expected no standing recheck alongside the immediate requeue, got %v", recheck)
		}
		if got := deploymentAnnotation(t, c.Client, depName, tlsVersionKey); got != "2000" {
			t.Errorf("expected the tlsVersion annotation %q, got %q", "2000", got)
		}
	})

	t.Run("patches after the cluster CA is rotated", func(t *testing.T) {
		// The -tls secret records the previous CA version, so the certificate
		// is renewed; the renewed secret's new ResourceVersion must be
		// recorded on the deployment to trigger the rollout.
		c := newClient("999", "2000")
		result, _, err := c.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the renewal rollout")
		}
		renewed := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, renewed); err != nil {
			t.Fatalf("failed to get the renewed -tls secret: %v", err)
		}
		if got := deploymentAnnotation(t, c.Client, depName, tlsVersionKey); got != renewed.ResourceVersion {
			t.Errorf("expected the tlsVersion annotation %q, got %q", renewed.ResourceVersion, got)
		}
	})
}

// TestRecreateMissingTLSSecretAnnotationRollout verifies that the missing
// -tls secret recreation rolls the Deployment through the tlsVersion
// pod-template annotation and stays a no-op before the Deployment exists.
func TestRecreateMissingTLSSecretAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-tls-recreate-annotation")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCASuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	caKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCAKeySuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.key": []byte(ca.CAKeyPEM)},
	}

	t.Run("patches the deployment after recreating the secret", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy(),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, nil)).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		result, err := r.recreateMissingTLSSecret(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rollout patch")
		}
		recreated := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, recreated); err != nil {
			t.Fatalf("expected the -tls secret to be recreated: %v", err)
		}
		if got := deploymentAnnotation(t, c, depName, tlsVersionKey); got != recreated.ResourceVersion {
			t.Errorf("expected the tlsVersion annotation %q, got %q", recreated.ResourceVersion, got)
		}
	})

	t.Run("does nothing when the Deployment does not exist", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy()).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		result, err := r.recreateMissingTLSSecret(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue before the Deployment exists, got %v", result.RequeueAfter)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, &corev1.Secret{}); err == nil {
			t.Error("expected no -tls secret to be created before the Deployment exists")
		}
	})
}
