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
	"fmt"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"
)

// capturedEvent is one recorded event with the structured fields the
// events.k8s.io/v1 API persists.
type capturedEvent struct {
	eventtype string
	reason    string
	action    string
	note      string
}

// capturingEventRecorder is a test double for the controller-runtime
// recorder.EventRecorder (client-go tools/events) that keeps the structured
// fields of every emitted event. The client-go FakeRecorder flattens events
// into a "type reason note" string and drops the action, so it cannot guard
// the events.k8s.io/v1 contract that action must be non-empty (the API
// server rejects Events with an empty action with a 422).
type capturingEventRecorder struct {
	mu     sync.Mutex
	events []capturedEvent
}

var _ recorder.EventRecorder = (*capturingEventRecorder)(nil)

func (c *capturingEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	c.record(eventtype, reason, action, note, args...)
}

func (c *capturingEventRecorder) AnnotatedEventf(regarding runtime.Object, related runtime.Object, _ map[string]string, eventtype, reason, action, note string, args ...interface{}) {
	c.record(eventtype, reason, action, note, args...)
}

func (c *capturingEventRecorder) record(eventtype, reason, action, note string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, capturedEvent{
		eventtype: eventtype,
		reason:    reason,
		action:    action,
		note:      fmt.Sprintf(note, args...),
	})
}

// drain returns every recorded event and resets the buffer.
func (c *capturingEventRecorder) drain() []capturedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	got := c.events
	c.events = nil
	return got
}

// assertEventAction finds the event with the given reason among the recorded
// events and asserts its type and action. An empty action is a hard failure:
// the events.k8s.io/v1 API server rejects such Events with a 422, so the
// operator's signaling would be silently lost.
func assertEventAction(t *testing.T, got []capturedEvent, eventtype, reason, action string) {
	t.Helper()
	for _, e := range got {
		if e.reason != reason {
			continue
		}
		if e.eventtype != eventtype {
			t.Errorf("event %s: expected type %q, got %q", reason, eventtype, e.eventtype)
		}
		if e.action != action {
			t.Errorf("event %s: expected action %q, got %q", reason, action, e.action)
		}
		if e.action == "" {
			t.Errorf("event %s: action must not be empty (the events.k8s.io/v1 API server rejects empty actions with 422)", reason)
		}
		return
	}
	t.Errorf("expected a %q event, got %v", reason, got)
}

// newEventActionTestScheme builds the scheme shared by the event-action
// subtests (CRD + core + apps + kafka).
func newEventActionTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)
	return scheme
}

// TestRecordedEventsCarryNonEmptyAction guards the events.k8s.io/v1 action
// contract at every Recorder.Eventf call site: the API server rejects Events
// with an empty action (422), so each site must pass a short CamelCase action
// describing what the code does there. The expected action is asserted
// exactly, so a regression back to "" (or an accidental semantic change)
// fails the suite.
func TestRecordedEventsCarryNonEmptyAction(t *testing.T) {
	ctx := context.Background()
	logger := logr.Logger{}

	t.Run("spec validation skips (clientauth and replicas)", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)

		t.Run("ClientAuthRequiresSecureHTTP and ReconciliationSkipped", func(t *testing.T) {
			recorder := &capturingEventRecorder{}
			inst := newTestInstance()
			inst.Spec.ClientAuth = true
			inst.Spec.SecureHTTP = false

			client := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(inst).
				WithStatusSubresource(inst).
				Build()
			r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := recorder.drain()
			assertEventAction(t, got, corev1.EventTypeWarning, "ClientAuthRequiresSecureHTTP", "Validate")
			assertEventAction(t, got, corev1.EventTypeWarning, "ReconciliationSkipped", "Validate")
		})

		t.Run("ReplicasRequireUserTLSSecret and ReconciliationSkipped", func(t *testing.T) {
			recorder := &capturingEventRecorder{}
			inst := newTestInstance()
			inst.Spec.SecureHTTP = true
			inst.Spec.TLSSecretName = ""
			inst.Spec.Replicas = ptr.To(int32(3))

			client := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(inst).
				WithStatusSubresource(inst).
				Build()
			r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := recorder.drain()
			assertEventAction(t, got, corev1.EventTypeWarning, "ReplicasRequireUserTLSSecret", "Validate")
			assertEventAction(t, got, corev1.EventTypeWarning, "ReconciliationSkipped", "Validate")
		})
	})

	t.Run("InvalidTLSSecret", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)
		recorder := &capturingEventRecorder{}
		// newTestInstance sets SecureHTTP=false and TLSSecretName; enabling
		// securehttp without seeding the secret makes the user TLS secret
		// validation fail (the secret does not exist in the fake client).
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true

		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst).
			WithStatusSubresource(inst).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEventAction(t, recorder.drain(), corev1.EventTypeWarning, "InvalidTLSSecret", "Validate")
	})

	t.Run("ServiceAdopted", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)
		recorder := &capturingEventRecorder{}
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

		// The desired Service without a controller owner, with a diverged
		// spec so the adoption is followed by a real update.
		emptyClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		svc, err := (&StrimziSchemaRegistryReconciler{Client: emptyClient, Scheme: scheme}).createService(inst, logger)
		if err != nil {
			t.Fatalf("failed to build the desired service: %v", err)
		}
		svc.OwnerReferences = nil
		svc.Spec.Ports[0].Port = 9999

		// A Deployment in its desired shape (stable selector, current spec
		// hash, current bootstrap env) so reconciliation reaches the Service
		// block instead of returning after the Deployment update.
		hash, err := computeSpecHash(inst)
		if err != nil {
			t.Fatalf("failed to compute spec hash: %v", err)
		}
		bootstrap := newTestKafkaCR("kafka-cluster", inst.Namespace).Status.Listeners[0].BootstrapServers
		dep := &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        inst.Name + deploySuffix,
				Namespace:   inst.Namespace,
				Annotations: map[string]string{keyPrefix + "/specHash": hash},
			},
			Spec: apps.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1",
							Env: []corev1.EnvVar{{Name: bootstrapServersEnv, Value: bootstrap}}},
					}},
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep, svc).
			WithStatusSubresource(inst).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

		_, err = r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEventAction(t, recorder.drain(), corev1.EventTypeWarning, "ServiceAdopted", "Adopt")
	})

	t.Run("ConfigKeyOverridden", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)
		recorder := &capturingEventRecorder{}
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "PLAINTEXT"
		inst.Spec.Listener = "tls"
		inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
		inst.Spec.Configuration = map[string]string{
			"schema.registry.group.id": "custom-group",
			"heap.opts":                "-Xmx1g",
		}
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
		}

		// A Deployment carrying a stale spec hash so the reserved keys are
		// reported (one event per overridden key).
		dep := &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        inst.Name + deploySuffix,
				Namespace:   inst.Namespace,
				Annotations: map[string]string{keyPrefix + "/specHash": "stale-hash"},
			},
			Spec: apps.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
				},
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

		_, _, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got := recorder.drain()
		if len(got) != 2 {
			t.Fatalf("expected exactly 2 ConfigKeyOverridden events (one per reserved key), got %d: %v", len(got), got)
		}
		for _, key := range []string{"heap.opts", "schema.registry.group.id"} {
			found := false
			for _, e := range got {
				if e.reason == "ConfigKeyOverridden" && e.note == fmt.Sprintf("configuration key %q is operator-managed and was ignored", key) {
					assertEventAction(t, []capturedEvent{e}, corev1.EventTypeWarning, "ConfigKeyOverridden", "OverrideConfig")
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a ConfigKeyOverridden event naming %q, events: %v", key, got)
			}
		}
	})

	t.Run("TLSCertificateRenewed", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)
		recorder := &capturingEventRecorder{}

		ca, err := testutil.GenerateClusterCACert("test-ca-rotation")
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
		deployment := &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Name + deploySuffix, Namespace: inst.Namespace},
			Spec: apps.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "confluent-sr"}}},
				},
			},
		}
		// A -tls secret recorded against an older CA version forces the
		// CA-change renewal.
		tlsSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + tlsSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: "2000",
				Annotations:     map[string]string{CAVersionKey: "999"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls-keystore.jks":  []byte("stale-keystore-bytes"),
				"keystore_password": []byte("stale"),
				"key_password":      []byte("stale"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret, caKeySecret, deployment, tlsSecret).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

		if _, _, err := r.handleTLSCertExpiry(ctx, inst, logger, cluster); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEventAction(t, recorder.drain(), corev1.EventTypeNormal, "TLSCertificateRenewed", "RenewCertificate")
	})

	t.Run("TLSSecretRecreated", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)
		recorder := &capturingEventRecorder{}

		ca, err := testutil.GenerateClusterCACert("test-ca-rotation")
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
		// The Deployment exists but the operator-managed -tls secret was
		// deleted: recreateMissingTLSSecret must rebuild it and announce the
		// recreation.
		deployment := &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Name + deploySuffix, Namespace: inst.Namespace},
			Spec: apps.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "confluent-sr"}}},
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret, caKeySecret, deployment).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

		if _, err := r.recreateMissingTLSSecret(ctx, inst, logger, cluster); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEventAction(t, recorder.drain(), corev1.EventTypeNormal, "TLSSecretRecreated", "RecreateSecret")
	})

	t.Run("DeploymentRecreated", func(t *testing.T) {
		scheme := newEventActionTestScheme(t)
		recorder := &capturingEventRecorder{}
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "PLAINTEXT"
		inst.Spec.Listener = "tls"
		inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
		}

		// A pre-fix Deployment whose selector contains mutable labels: the
		// selector is immutable, so updateExistingDeployment must delete and
		// re-create it, emitting the DeploymentRecreated event.
		legacySelector := map[string]string{
			"app":                          inst.Name,
			"strimzi-schema-registry":      inst.Name,
			"app.kubernetes.io/instance":   inst.Name,
			"app.kubernetes.io/name":       "strimzischemaregistry",
			"app.kubernetes.io/managed-by": "strimzi-registry-operator",
			"app.kubernetes.io/part-of":    inst.Name,
			"app.kubernetes.io/version":    "7.6.1",
			"strimzi.io/cluster":           "kafka-cluster",
		}
		dep := &apps.Deployment{
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
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

		updated, _, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if updated == nil || updated.Spec.Selector == nil {
			t.Fatal("expected the re-created Deployment to be returned")
		}
		assertEventAction(t, recorder.drain(), corev1.EventTypeNormal, "DeploymentRecreated", "RecreateDeployment")
	})
}
