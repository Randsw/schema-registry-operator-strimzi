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
	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// newSpecDriftFixture builds an operator-complete Deployment fixture (built by
// buildDeploymentSpec, so it matches the desired build exactly) together with
// the reconciler needed to exercise updateExistingDeployment.
func newSpecDriftFixture(t *testing.T, inst *strimziregistryoperatorv1beta1.StrimziSchemaRegistry) (*StrimziSchemaRegistryReconciler, *apps.Deployment) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	kafkaCR := newTestKafkaCR(inst.Labels[strimziClusterLabel], inst.Namespace)
	bootstrap := kafkaCR.Status.Listeners[0].BootstrapServers
	dep := newOperatorDeployment(t, scheme, inst, bootstrap, "")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, kafkaCR, dep).Build()
	return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}, dep
}

// TestUpdateExistingDeploymentPatchesSpecDrift covers the precise spec-drift
// patching: manual edits to operator-managed fields are reverted even when the
// CR spec hash is unchanged, the replica count survives a manual scale when
// the CR does not pin spec.replicas and is reverted when it does, and the
// rotation rollout markers survive a spec patch.
func TestUpdateExistingDeploymentPatchesSpecDrift(t *testing.T) {
	newInst := func() *strimziregistryoperatorv1beta1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "PLAINTEXT"
		inst.Spec.Listener = "tls"
		inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}},
			},
		}
		return inst
	}

	t.Run("manual image edit is reverted", func(t *testing.T) {
		inst := newInst()
		r, dep := newSpecDriftFixture(t, inst)
		dep.Spec.Template.Spec.Containers[0].Image = "evil.example.com/backdoored:latest"

		updated, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a manually edited image")
		}
		if got := updated.Spec.Template.Spec.Containers[0].Image; got != "confluentinc/cp-schema-registry:8.4.1" {
			t.Errorf("expected the manual image edit to be reverted, got %q", got)
		}
	})

	t.Run("manual env edit is reverted", func(t *testing.T) {
		inst := newInst()
		r, dep := newSpecDriftFixture(t, inst)
		// Drop an operator-managed env var from the live container.
		env := dep.Spec.Template.Spec.Containers[0].Env
		filtered := env[:0]
		for _, e := range env {
			if e.Name != "SCHEMA_REGISTRY_HEAP_OPTS" {
				filtered = append(filtered, e)
			}
		}
		dep.Spec.Template.Spec.Containers[0].Env = filtered

		updated, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a manually removed env var")
		}
		found := false
		for _, e := range updated.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "SCHEMA_REGISTRY_HEAP_OPTS" {
				found = true
			}
		}
		if !found {
			t.Error("expected the operator-managed env var to be restored")
		}
	})

	t.Run("manual resources edit is reverted", func(t *testing.T) {
		inst := newInst()
		r, dep := newSpecDriftFixture(t, inst)
		dep.Spec.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("999m"),
		}

		updated, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a manually edited resource limit")
		}
		if got := updated.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String(); got != "1" {
			t.Errorf("expected the operator-managed cpu limit to be restored, got %q", got)
		}
	})

	t.Run("replicas survive a manual scale when the CR does not pin them", func(t *testing.T) {
		inst := newInst()
		r, dep := newSpecDriftFixture(t, inst)
		dep.Spec.Replicas = ptr.To(int32(5))

		_, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if specChanged {
			t.Fatal("expected specChanged=false: an unpinned CR never desires a replica count")
		}
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 5 {
			t.Errorf("expected the manually scaled replicas to survive, got %v", dep.Spec.Replicas)
		}
	})

	t.Run("replicas are reverted when the CR pins them", func(t *testing.T) {
		inst := newInst()
		inst.Spec.Replicas = ptr.To(int32(2))
		r, dep := newSpecDriftFixture(t, inst)
		dep.Spec.Replicas = ptr.To(int32(5))

		updated, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true when the pinned replica count drifted")
		}
		if updated.Spec.Replicas == nil || *updated.Spec.Replicas != 2 {
			t.Errorf("expected the CR-pinned replicas (2) to be applied, got %v", updated.Spec.Replicas)
		}
	})

	t.Run("rollout markers survive a spec patch", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)
		_ = apps.AddToScheme(scheme)
		_ = kafka.AddToScheme(scheme)

		inst := newInst()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = ""
		kafkaCR := newTestKafkaCR("kafka-cluster", inst.Namespace)
		bootstrap := kafkaCR.Status.Listeners[0].BootstrapServers
		dep := newOperatorDeployment(t, scheme, inst, bootstrap, "stale-hash")
		// A marker recorded by a previous renewal, older than the current
		// secret version.
		dep.Spec.Template.Annotations[tlsVersionKey] = "old-marker"
		tlsSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + tlsSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: "2000",
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"tls-keystore.jks": []byte("stub")},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, kafkaCR, tlsSecret, dep).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		updated, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a stale spec hash")
		}
		if got := updated.Spec.Template.Annotations[tlsVersionKey]; got != "2000" {
			t.Errorf("expected the tlsVersion marker to survive the spec patch at the current secret version %q, got %q", "2000", got)
		}
	})
}

// TestRecreateDeploymentForLegacySelector_CreateError covers the U-9 error
// branch: when the replacement Deployment Create fails after the legacy
// Deployment was deleted, the error must be returned (surfacing as a
// ReconcileError on the CR through the Reconcile wrapper) and a subsequent
// successful reconcile must converge — the migration is retried, not lost.
func TestRecreateDeploymentForLegacySelector_CreateError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	inst := newTestInstance()
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
	}
	kafkaCR := newTestKafkaCR("kafka-cluster", inst.Namespace)

	legacySelector := map[string]string{
		"app":                       inst.Name,
		"strimzi-schema-registry":   inst.Name,
		"app.kubernetes.io/version": "7.6.1",
		"strimzi.io/cluster":        "kafka-cluster",
	}
	legacyDep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        inst.Name + deploySuffix,
			Namespace:   inst.Namespace,
			Annotations: map[string]string{keyPrefix + "/specHash": "stale-hash"},
		},
		Spec: apps.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: legacySelector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: legacySelector},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:7.6.1"}}},
			},
		},
	}

	ctx := context.Background()
	logger := logr.Logger{}
	injected := errors.New("injected deployment create failure")

	t.Run("create failure is returned after the delete", func(t *testing.T) {
		creates := 0
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, kafkaCR, legacyDep.DeepCopy()).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*apps.Deployment); ok {
						creates++
						return injected
					}
					return c.Create(ctx, obj, opts...)
				},
			}).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		_, _, err := r.updateExistingDeployment(inst, ctx, logger, legacyDep.DeepCopy())
		if err == nil {
			t.Fatal("expected the replacement Deployment Create failure to be returned")
		}
		if !errors.Is(err, injected) {
			t.Fatalf("expected the injected error, got %v", err)
		}
		if creates != 1 {
			t.Errorf("expected exactly 1 replacement Create attempt, got %d", creates)
		}
		// The legacy Deployment was deleted before the failed create.
		deleted := &apps.Deployment{}
		if err := c.Get(ctx, types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}, deleted); !apierrors.IsNotFound(err) {
			t.Errorf("expected the legacy Deployment to be deleted, got %v", err)
		}
	})

	t.Run("a subsequent successful reconcile converges", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, kafkaCR, legacyDep.DeepCopy()).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		// First pass fails (simulating the injected create failure above by
		// deleting the legacy Deployment underneath the reconciler is not
		// needed: run the failing variant through a one-shot interceptor).
		failing := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, kafkaCR, legacyDep.DeepCopy()).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*apps.Deployment); ok {
						return injected
					}
					return c.Create(ctx, obj, opts...)
				},
			}).
			Build()
		rFailing := &StrimziSchemaRegistryReconciler{Client: failing, Scheme: scheme}
		if _, _, err := rFailing.updateExistingDeployment(inst, ctx, logger, legacyDep.DeepCopy()); err == nil {
			t.Fatal("expected the first pass to fail")
		}

		// Recovery: a fresh client holding the legacy Deployment again (the
		// state a real cluster is left in when the create failed and the
		// operator retried) converges on the next reconcile.
		updated, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, legacyDep.DeepCopy())
		if err != nil {
			t.Fatalf("expected the retry to converge, got %v", err)
		}
		if specChanged {
			t.Error("expected specChanged=false after the successful migration")
		}
		if updated == nil || updated.Spec.Selector == nil {
			t.Fatal("expected the re-created Deployment to be returned")
		}
		want := selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)
		if !equality.Semantic.DeepEqual(updated.Spec.Selector.MatchLabels, want) {
			t.Errorf("expected the stable selector %v after recovery, got %v", want, updated.Spec.Selector.MatchLabels)
		}
	})
}
