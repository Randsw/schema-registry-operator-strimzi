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
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	monitoring "github.com/randsw/schema-registry-operator-strimzi/metrics"
)

// newErrorPathScheme registers every scheme the reconcile flow touches, so a
// fake client can hold the CR, the Kafka CR, the Deployment, the Service and
// the NetworkPolicy.
func newErrorPathScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)
	return scheme
}

// newErrorPathInstance returns a PLAINTEXT CR whose only live dependency is
// the Kafka CR status, so an injected client failure is the sole reason a
// reconcile can fail.
func newErrorPathInstance() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
	inst := newTestInstance()
	inst.UID = "cr-uid"
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Spec.TLSSecretName = ""
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}},
		},
	}
	return inst
}

// failGets injects a generic (non-NotFound) error into Get calls for the
// given object type and name, so the reconcile error branches (distinct from
// the missing-dependency branches) can be exercised.
func failGets(on client.Object, name string, injected error) interceptor.Funcs {
	want := reflect.TypeOf(on)
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if reflect.TypeOf(obj) == want && key.Name == name {
				return injected
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
}

// failStatusUpdates injects an error into every Status().Update call.
func failStatusUpdates(injected error) interceptor.Funcs {
	return interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if subResourceName == "status" {
				return injected
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
}

// failCreates injects an error into Create calls for the given object type.
func failCreates(on client.Object, injected error) interceptor.Funcs {
	want := reflect.TypeOf(on)
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if reflect.TypeOf(obj) == want {
				return injected
			}
			return c.Create(ctx, obj, opts...)
		},
	}
}

// reconcileErrorsDelta runs fn and returns how far the reconcile-error
// counter advanced during it, so tests can assert the metric increments
// without depending on other tests' totals.
func reconcileErrorsDelta(fn func()) float64 {
	before := testutil.ToFloat64(monitoring.StrimziSchemaRegistryReconcileErrorsTotal)
	fn()
	return testutil.ToFloat64(monitoring.StrimziSchemaRegistryReconcileErrorsTotal) - before
}

// assertReconcileErrorStatus asserts the CR carries the ReconcileError state:
// status.status=ReconcileError plus a Ready=False condition with the
// ReconcileError reason carrying the injected error message.
func assertReconcileErrorStatus(t *testing.T, c client.Client,
	inst *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, wantMsg string) {
	t.Helper()
	updated := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}, updated); err != nil {
		t.Fatalf("failed to get the updated CR: %v", err)
	}
	if updated.Status.Status != "ReconcileError" {
		t.Errorf("expected status.status ReconcileError, got %q", updated.Status.Status)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatal("expected a Ready condition on the CR status")
	}
	if cond.Status != metav1.ConditionFalse || cond.Reason != "ReconcileError" {
		t.Errorf("expected Ready=False/ReconcileError, got %s/%s", cond.Status, cond.Reason)
	}
	if !strings.Contains(cond.Message, wantMsg) {
		t.Errorf("expected the Ready condition message to carry %q, got %q", wantMsg, cond.Message)
	}
}

// U-2: a Deployment Get failure (a generic API error, not a missing
// dependency) must surface as a returned error, a ReconcileError CR status
// carrying the message, and a reconcile-error metric increment — not as a
// silent hot loop.
func TestReconcileDeploymentGetError(t *testing.T) {
	scheme := newErrorPathScheme()
	inst := newErrorPathInstance()
	injected := errors.New("injected deployment get failure")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace)).
		WithStatusSubresource(inst).
		WithInterceptorFuncs(failGets(&apps.Deployment{}, inst.Name+deploySuffix, injected)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	var err error
	var delta float64
	delta = reconcileErrorsDelta(func() {
		_, err = r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
		})
	})
	if err == nil {
		t.Fatal("expected the Deployment Get failure to be returned")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected the injected error, got %v", err)
	}
	if delta != 1 {
		t.Errorf("expected the reconcile-error counter to advance by 1, got %v", delta)
	}
	assertReconcileErrorStatus(t, c, inst, "injected deployment get failure")
}

// U-3: a failing Status().Update must propagate the error and increment the
// reconcile-error metric.
func TestReconcileStatusUpdateError(t *testing.T) {
	scheme := newErrorPathScheme()
	inst := newErrorPathInstance()
	kafkaCR := newTestKafkaCR("kafka-cluster", inst.Namespace)
	dep := newOperatorDeployment(t, scheme, inst, kafkaCR.Status.Listeners[0].BootstrapServers, "")
	injected := errors.New("injected status update failure")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, kafkaCR, dep).
		WithStatusSubresource(inst, &apps.Deployment{}).
		WithInterceptorFuncs(failStatusUpdates(injected)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	var err error
	var delta float64
	delta = reconcileErrorsDelta(func() {
		_, err = r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
		})
	})
	if err == nil {
		t.Fatal("expected the Status().Update failure to be returned")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected the injected error, got %v", err)
	}
	if delta != 1 {
		t.Errorf("expected the reconcile-error counter to advance by 1, got %v", delta)
	}
}

// U-3: when the ReconcileError status write itself fails, the ORIGINAL
// reconcile error must still be the one returned — the status write failure
// is logged, never swapped in.
func TestReconcileErrorStatusWriteFailureDoesNotMaskOriginalError(t *testing.T) {
	scheme := newErrorPathScheme()
	inst := newErrorPathInstance()
	depErr := errors.New("injected deployment get failure")
	statusErr := errors.New("injected status update failure")

	// One interceptor failing both the Deployment Get and every status write.
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace)).
		WithStatusSubresource(inst).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*apps.Deployment); ok && key.Name == inst.Name+deploySuffix {
					return depErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName == "status" {
					return statusErr
				}
				return c.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
	})
	if err == nil {
		t.Fatal("expected the Deployment Get failure to be returned")
	}
	if !errors.Is(err, depErr) {
		t.Fatalf("expected the original Deployment Get error to be returned, got %v", err)
	}
	if errors.Is(err, statusErr) {
		t.Fatalf("the status write failure must not mask the original error, got %v", err)
	}
}

// U-4: a Service Get failure after a successful Deployment reconcile must
// surface as a ReconcileError without writing a partial Service.
func TestReconcileServiceGetError(t *testing.T) {
	scheme := newErrorPathScheme()
	inst := newErrorPathInstance()
	kafkaCR := newTestKafkaCR("kafka-cluster", inst.Namespace)
	dep := newOperatorDeployment(t, scheme, inst, kafkaCR.Status.Listeners[0].BootstrapServers, "")
	injected := errors.New("injected service get failure")

	// The interceptor fails only the reconcile's Service Get (the test's own
	// verification Get passes through) and counts Service Creates, so the
	// test can assert no partial Service was written.
	serviceGets, serviceCreates := 0, 0
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, kafkaCR, dep).
		WithStatusSubresource(inst, &apps.Deployment{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Service); ok && key.Name == inst.Name {
					serviceGets++
					if serviceGets == 1 {
						return injected
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.Service); ok {
					serviceCreates++
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	var err error
	var delta float64
	delta = reconcileErrorsDelta(func() {
		_, err = r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
		})
	})
	if err == nil {
		t.Fatal("expected the Service Get failure to be returned")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected the injected error, got %v", err)
	}
	if delta != 1 {
		t.Errorf("expected the reconcile-error counter to advance by 1, got %v", delta)
	}
	assertReconcileErrorStatus(t, c, inst, "injected service get failure")

	if serviceCreates != 0 {
		t.Errorf("expected no Service Create on the failed reconcile, got %d", serviceCreates)
	}
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected no Service to exist after the failed reconcile, got %v", err)
	}
}

// U-5: a NetworkPolicy Create failure on the first reconcile (policy enabled)
// must surface as a ReconcileError, increment the metric, and emit no events
// — the failure path has nothing to announce beyond the CR status.
func TestReconcileNetworkPolicyCreateError(t *testing.T) {
	scheme := newErrorPathScheme()
	inst := newErrorPathInstance()
	inst.Spec.NetworkPolicy = &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true}
	kafkaCR := newTestKafkaCR("kafka-cluster", inst.Namespace)
	dep := newOperatorDeployment(t, scheme, inst, kafkaCR.Status.Listeners[0].BootstrapServers, "")

	emptyClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	svc, err := (&StrimziSchemaRegistryReconciler{Client: emptyClient, Scheme: scheme}).
		createService(inst, logr.Logger{})
	if err != nil {
		t.Fatalf("failed to build the desired service: %v", err)
	}

	injected := errors.New("injected networkpolicy create failure")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, kafkaCR, dep, svc).
		WithStatusSubresource(inst, &apps.Deployment{}).
		WithInterceptorFuncs(failCreates(&networkingv1.NetworkPolicy{}, injected)).
		Build()
	rec := &capturingEventRecorder{}
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme, Recorder: rec}

	var reconcileErr error
	var delta float64
	delta = reconcileErrorsDelta(func() {
		_, reconcileErr = r.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
		})
	})
	if reconcileErr == nil {
		t.Fatal("expected the NetworkPolicy Create failure to be returned")
	}
	if !errors.Is(reconcileErr, injected) {
		t.Fatalf("expected the injected error, got %v", reconcileErr)
	}
	if delta != 1 {
		t.Errorf("expected the reconcile-error counter to advance by 1, got %v", delta)
	}
	assertReconcileErrorStatus(t, c, inst, "injected networkpolicy create failure")

	if events := rec.drain(); len(events) != 0 {
		t.Errorf("expected no events on the NetworkPolicy create failure path, got %v", events)
	}
}
