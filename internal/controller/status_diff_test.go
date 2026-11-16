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
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	eventsfake "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// countStatusUpdates returns interceptor.Funcs that count Status().Update
// calls, so tests can assert the reconciler only writes the CR status when it
// actually changed. Pass it to the fake client builder via WithInterceptorFuncs
// and read the counter after the operation.
func countStatusUpdates(updates *int) interceptor.Funcs {
	return interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if subResourceName == "status" {
				*updates++
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
}

// statusCountFixture bundles everything a status-update-counting test needs:
// a reconciler backed by a fake client whose Status().Update calls are
// counted, the reconciled CR, and count() which returns (and resets) the
// number of status updates performed since the last call.
type statusCountFixture struct {
	r     *StrimziSchemaRegistryReconciler
	inst  *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry
	count func() int
}

// newSteadyStateFixture builds a fake environment whose CR, Kafka cluster,
// Deployment, and Service are all in their desired shape, so a Reconcile has
// nothing to change: the exact steady state in which no status write may
// happen. The Deployment reports zero ready replicas, so the CR status is
// NotReady once the first reconcile has written it.
func newSteadyStateFixture(t *testing.T) statusCountFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	inst := newTestInstance()
	inst.UID = "cr-uid"
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}},
		},
	}

	// A Deployment in its desired shape (operator-complete, built by
	// buildDeploymentSpec) so reconciliation has no spec change to apply.
	bootstrap := newTestKafkaCR("kafka-cluster", inst.Namespace).Status.Listeners[0].BootstrapServers
	dep := newOperatorDeployment(t, scheme, inst, bootstrap, "")

	// The desired Service, already owned by the CR (createService sets the
	// controller reference), so reconciliation has no adoption or update to
	// perform.
	emptyClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	r0 := &StrimziSchemaRegistryReconciler{Client: emptyClient, Scheme: scheme}
	svc, err := r0.createService(inst, logr.Logger{})
	if err != nil {
		t.Fatalf("failed to build the desired service: %v", err)
	}

	var updates int
	count := func() int {
		n := updates
		updates = 0
		return n
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep, svc).
		WithStatusSubresource(inst, &apps.Deployment{}).
		WithInterceptorFuncs(countStatusUpdates(&updates)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme, Recorder: eventsfake.NewFakeRecorder(16)}
	return statusCountFixture{r: r, inst: inst, count: count}
}

// reconcileFixture runs one Reconcile pass over the fixture's CR.
func reconcileFixture(t *testing.T, f statusCountFixture) (reconcile.Result, error) {
	t.Helper()
	return f.r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: f.inst.Name, Namespace: f.inst.Namespace},
	})
}

// TestReconcileStatusUpdateCounting verifies that the reconciler only writes
// the CR status when it actually changed. The status snapshot must be compared
// like-for-like (pointer against pointer): comparing a *Status against a
// Status value is always unequal, which made every reconcile perform a
// redundant Status().Update. That bumps resourceVersion, which re-triggers
// reconciliation forever because the controller watches its own CR via For().
func TestReconcileStatusUpdateCounting(t *testing.T) {
	t.Run("steady-state reconcile performs no status updates", func(t *testing.T) {
		f := newSteadyStateFixture(t)

		// First reconcile: the status is written for the first time
		// (empty -> NotReady), which is a genuine change.
		if _, err := reconcileFixture(t, f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := f.count(); n != 1 {
			t.Fatalf("expected exactly 1 status update on the first reconcile, got %d", n)
		}

		// Second reconcile: nothing changed, so no status write may happen.
		if _, err := reconcileFixture(t, f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := f.count(); n != 0 {
			t.Errorf("expected 0 status updates on a steady-state reconcile, got %d", n)
		}
	})

	t.Run("repeated reconcile after a reconcile error performs no redundant status update", func(t *testing.T) {
		scheme := runtime.NewScheme()
		_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)
		_ = apps.AddToScheme(scheme)
		_ = kafka.AddToScheme(scheme)

		// A CR without the required strimzi.io/cluster label fails
		// reconciliation; the failure is written to the status. A repeated
		// reconcile fails with the identical error, so the status is
		// unchanged and must not be written again.
		inst := newTestInstance()

		var updates int
		count := func() int {
			n := updates
			updates = 0
			return n
		}
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst).
			WithStatusSubresource(inst).
			WithInterceptorFuncs(countStatusUpdates(&updates)).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme, Recorder: eventsfake.NewFakeRecorder(16)}
		f := statusCountFixture{r: r, inst: inst, count: count}

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}}
		if _, err := r.Reconcile(context.Background(), req); err == nil {
			t.Fatal("expected an error for the missing strimzi.io/cluster label")
		}
		if n := f.count(); n != 1 {
			t.Fatalf("expected exactly 1 status update when the error is first written, got %d", n)
		}

		if _, err := r.Reconcile(context.Background(), req); err == nil {
			t.Fatal("expected the repeated reconcile to fail with the same error")
		}
		if n := f.count(); n != 0 {
			t.Errorf("expected 0 status updates on the repeated failed reconcile, got %d", n)
		}

		// The error state must still be visible on the CR.
		updated := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}, updated); err != nil {
			t.Fatalf("failed to get the updated CR: %v", err)
		}
		if updated.Status.Status != "ReconcileError" {
			t.Errorf("expected status.status ReconcileError, got %q", updated.Status.Status)
		}
	})

	t.Run("genuine status change still triggers exactly one status update", func(t *testing.T) {
		f := newSteadyStateFixture(t)

		// Reach steady state: the first reconcile writes the status, the
		// second changes nothing.
		if _, err := reconcileFixture(t, f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := f.count(); n != 1 {
			t.Fatalf("expected exactly 1 status update on the first reconcile, got %d", n)
		}
		if _, err := reconcileFixture(t, f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := f.count(); n != 0 {
			t.Fatalf("expected 0 status updates on a steady-state reconcile, got %d", n)
		}

		// Make the Deployment ready: a genuine status change (NotReady -> Ok)
		// that must be written exactly once. The test's own Deployment status
		// write goes through the same counting interceptor, so drain the
		// counter before the reconcile under test.
		dep := &apps.Deployment{}
		if err := f.r.Get(context.Background(), types.NamespacedName{Name: f.inst.Name + deploySuffix, Namespace: f.inst.Namespace}, dep); err != nil {
			t.Fatalf("failed to get the Deployment: %v", err)
		}
		dep.Status.Replicas = 1
		dep.Status.ReadyReplicas = 1
		if err := f.r.Status().Update(context.Background(), dep); err != nil {
			t.Fatalf("failed to update the Deployment status: %v", err)
		}
		f.count()

		if _, err := reconcileFixture(t, f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := f.count(); n != 1 {
			t.Errorf("expected exactly 1 status update for a genuine status change, got %d", n)
		}

		// The new state must be visible on the CR.
		updated := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := f.r.Get(context.Background(), types.NamespacedName{Name: f.inst.Name, Namespace: f.inst.Namespace}, updated); err != nil {
			t.Fatalf("failed to get the updated CR: %v", err)
		}
		if updated.Status.Status != "Ok" {
			t.Errorf("expected status.status Ok, got %q", updated.Status.Status)
		}
	})
}
