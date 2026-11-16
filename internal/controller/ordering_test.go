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
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newOperatorDeployment builds an operator-complete Deployment fixture for
// updateExistingDeployment tests: the spec is produced by buildDeploymentSpec
// itself, so the fixture matches what the operator would have created
// (labels, annotations, env, resources, probes) and a steady-state reconcile
// reports no drift. hashOverride replaces the computed spec hash annotation
// (simulating a stale hash) when non-empty.
func newOperatorDeployment(t *testing.T, scheme *runtime.Scheme,
	inst *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, bootstrap, hashOverride string) *apps.Deployment {
	t.Helper()
	// Resolve the REST API TLS secret name the same way updateExistingDeployment
	// does, so the fixture matches the desired build exactly.
	tlsSecretName := ""
	if inst.Spec.SecureHTTP {
		if inst.Spec.TLSSecretName != "" {
			tlsSecretName = inst.Spec.TLSSecretName
		} else {
			tlsSecretName = inst.Name + tlsSecretSuffix
		}
	}
	r := &StrimziSchemaRegistryReconciler{Scheme: scheme}
	dep, _, err := r.buildDeploymentSpec(inst, bootstrap, inst.Labels[strimziClusterLabel], "", "", "", tlsSecretName,
		kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}, logr.Logger{})
	if err != nil {
		t.Fatalf("failed to build the operator deployment fixture: %v", err)
	}
	if hashOverride != "" {
		dep.Annotations[keyPrefix+"/specHash"] = hashOverride
		dep.Spec.Template.Annotations[keyPrefix+"/specHash"] = hashOverride
	}
	return dep
}

// TestReconcileAppliesSpecChangeBeforeRotationRollout covers the 1.8 ordering
// fix: Deployment spec changes are reconciled BEFORE the rotation handlers, so
// a pending rotation can no longer preempt (and requeue ahead of) a spec
// change. A reconcile that faces both a drifted spec and a pending rotation
// rollout applies the spec change in that same pass — the rotation rollout is
// unified with it instead of patching the stale spec first and rolling pods a
// second time.
func TestReconcileAppliesSpecChangeBeforeRotationRollout(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	ca, err := testutil.GenerateClusterCACert("test-ordering")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}
	cp := certprocessor.NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "test-sr", "default")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	cluster := "kafka-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	// PLAINTEXT keeps the scenario free of KafkaUser requirements; the TLS
	// rotation path is independent of the Kafka auth mode.
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
	}

	kafkaCR := newTestKafkaCR(cluster, inst.Namespace)
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
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        inst.Name + tlsSecretSuffix,
			Namespace:   inst.Namespace,
			Annotations: map[string]string{CAVersionKey: "1000"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  keystore,
			"keystore_password": []byte(password),
			"key_password":      []byte(password),
		},
	}

	// An operator-complete deployment carrying a stale spec hash (a spec
	// change is pending) and a stale tlsVersion marker (a rotation rollout is
	// pending too).
	bootstrap := kafkaCR.Status.Listeners[0].BootstrapServers
	dep := newOperatorDeployment(t, scheme, inst, bootstrap, "stale-hash")
	dep.Spec.Template.Annotations[tlsVersionKey] = "1"

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, kafkaCR, caCertSecret, caKeySecret, tlsSecret, dep).
		WithStatusSubresource(inst).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
	ctx := context.Background()
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	// First reconcile: the spec change must be applied — not preempted by the
	// pending rotation rollout.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}}); err != nil {
		t.Fatalf("unexpected error on the first reconcile: %v", err)
	}
	updated := &apps.Deployment{}
	if err := client.Get(ctx, depName, updated); err != nil {
		t.Fatalf("failed to get deployment after the first reconcile: %v", err)
	}
	currentHash, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("failed to compute the current spec hash: %v", err)
	}
	if got := updated.Annotations[keyPrefix+"/specHash"]; got != currentHash {
		t.Errorf("expected the spec change to be applied on the first reconcile (hash %q), got %q", currentHash, got)
	}
	// The rotation rollout is unified with the spec update: the tlsVersion
	// marker records the current -tls secret version in the same pass.
	storedTLS := &corev1.Secret{}
	if err := client.Get(ctx, types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, storedTLS); err != nil {
		t.Fatalf("failed to get the -tls secret: %v", err)
	}
	if got := updated.Spec.Template.Annotations[tlsVersionKey]; got != storedTLS.ResourceVersion {
		t.Errorf("expected the tlsVersion marker %q after the unified update, got %q", storedTLS.ResourceVersion, got)
	}

	// Second reconcile: steady state — no further deployment update.
	before := updated.ResourceVersion
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}}); err != nil {
		t.Fatalf("unexpected error on the second reconcile: %v", err)
	}
	stable := &apps.Deployment{}
	if err := client.Get(ctx, depName, stable); err != nil {
		t.Fatalf("failed to get deployment after the second reconcile: %v", err)
	}
	if stable.ResourceVersion != before {
		t.Error("expected no further deployment update once the spec and the rotation marker are reconciled")
	}
}
