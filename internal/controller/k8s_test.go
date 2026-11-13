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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	eventsfake "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	sigsyaml "sigs.k8s.io/yaml"
)

// newTestKafkaCR returns a Kafka CR whose status exposes a "tls" listener so
// getKafkaBootstrapServers resolves against the fake client.
func newTestKafkaCR(name, namespace string) *kafka.Kafka {
	return &kafka.Kafka{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: &kafka.KafkaStatus{
			Listeners: []kafka.ListenerStatus{
				{Name: "tls", BootstrapServers: name + "-kafka-bootstrap:9093"},
			},
		},
	}
}

// TestHTTPSMultiReplicaValidationError is a table-driven test for the
// unsupported-combination guard: multiple replicas combined with securehttp
// and no user-provided TLS secret must be rejected (the operator-generated
// certificate covers DNS names only, so inter-instance HTTPS forwarding
// fails TLS verification), while every other combination stays valid.
func TestHTTPSMultiReplicaValidationError(t *testing.T) {
	cases := []struct {
		name     string
		replicas *int32
		secure   bool
		tlsName  string
		wantErr  bool
	}{
		{name: "replicas=2 securehttp no user tls secret is rejected", replicas: ptr.To(int32(2)), secure: true, wantErr: true},
		{name: "replicas=3 securehttp no user tls secret is rejected", replicas: ptr.To(int32(3)), secure: true, wantErr: true},
		{name: "replicas=1 securehttp is valid", replicas: ptr.To(int32(1)), secure: true},
		{name: "unset replicas (default 1) securehttp is valid", secure: true},
		{name: "replicas=2 with user tls secret is valid", replicas: ptr.To(int32(2)), secure: true, tlsName: "user-tls"},
		{name: "replicas=2 plain http is valid", replicas: ptr.To(int32(2))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := newTestInstance()
			inst.Spec.SecureHTTP = tc.secure
			inst.Spec.TLSSecretName = tc.tlsName
			inst.Spec.Replicas = tc.replicas

			msg := httpsMultiReplicaValidationError(inst)
			if tc.wantErr && msg == "" {
				t.Error("expected a validation error, got none")
			}
			if !tc.wantErr && msg != "" {
				t.Errorf("expected no validation error, got %q", msg)
			}
		})
	}
}

// TestUpdateExistingDeploymentBootstrapDrift covers the live-input drift
// fix: the Kafka bootstrap address is rendered into the deployment env from
// the Kafka CR's status, which the CR spec hash cannot see. When the Kafka
// listener is rebuilt (different bootstrap service or port) the deployment
// must be updated even though the CR spec — and therefore the hash — is
// unchanged; otherwise the pods keep the stale bootstrap address forever.
func TestUpdateExistingDeploymentBootstrapDrift(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	inst := newTestInstance()
	// PLAINTEXT avoids any JKS/user-secret requirements; only the Kafka CR
	// with status listeners is needed.
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}},
		},
	}

	desiredHash, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("failed to compute spec hash: %v", err)
	}

	// The Kafka CR exposes the current bootstrap address; the deployment
	// under test may carry a stale one.
	kafkaCR := newTestKafkaCR("kafka-cluster", inst.Namespace)
	currentBootstrap := kafkaCR.Status.Listeners[0].BootstrapServers

	newDeployment := func(hash, bootstrapEnv string) *apps.Deployment {
		env := []corev1.EnvVar{}
		if bootstrapEnv != "" {
			env = append(env, corev1.EnvVar{Name: bootstrapServersEnv, Value: bootstrapEnv})
		}
		return &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        inst.Name + deploySuffix,
				Namespace:   inst.Namespace,
				Annotations: map[string]string{keyPrefix + "/specHash": hash},
			},
			Spec: apps.DeploymentSpec{
				// Operator-created deployments carry the stable identity
				// selector (F1); without it updateExistingDeployment would
				// treat this fixture as a legacy selector and migrate it.
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{
						{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1", Env: env},
					}},
				},
			},
		}
	}

	newReconciler := func(dep *apps.Deployment) *StrimziSchemaRegistryReconciler {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, kafkaCR, dep).
			Build()
		return &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
	}

	ctx := context.Background()
	logger := logr.Logger{}

	t.Run("stale bootstrap address triggers a deployment update", func(t *testing.T) {
		dep := newDeployment(desiredHash, "old-bootstrap:9092")
		r := newReconciler(dep)

		updated, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true when the bootstrap address drifted")
		}
		if got := deploymentBootstrapServers(updated); got != currentBootstrap {
			t.Errorf("expected the deployment env to carry the current bootstrap %q, got %q", currentBootstrap, got)
		}
	})

	t.Run("current bootstrap address stays in steady state", func(t *testing.T) {
		dep := newDeployment(desiredHash, currentBootstrap)
		r := newReconciler(dep)

		_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if specChanged {
			t.Fatal("expected specChanged=false when the bootstrap address matches")
		}
	})

	t.Run("drift update is idempotent", func(t *testing.T) {
		dep := newDeployment(desiredHash, "old-bootstrap:9092")
		r := newReconciler(dep)

		updated, _, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error on the drift update: %v", err)
		}
		_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, updated)
		if err != nil {
			t.Fatalf("unexpected error on the follow-up reconcile: %v", err)
		}
		if specChanged {
			t.Fatal("expected specChanged=false once the bootstrap address is reconciled")
		}
	})
}

// TestUpdateExistingDeploymentConfigOverriddenEventSpam covers B2: the
// ConfigKeyOverridden Warning events must be emitted only when the
// deployment spec actually changed, not on every reconcile. A CR with
// reserved spec.configuration keys is reconciled repeatedly against a
// Deployment whose spec hash is current: steady-state reconciles must
// produce zero events, while a real spec change must produce exactly one
// event per overridden key.
func TestUpdateExistingDeploymentConfigOverriddenEventSpam(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	recorder := eventsfake.NewFakeRecorder(64)
	events := recorder.Events

	inst := newTestInstance()
	// PLAINTEXT avoids any JKS/user-secret requirements; only the Kafka CR
	// with status listeners is needed.
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Configuration = map[string]string{
		// Reserved keys: both are operator-managed and must be reported
		// as overridden (sorted order: heap.opts before schema.registry.group.id?
		// sorted: "heap.opts" < "schema.registry.group.id").
		"schema.registry.group.id": "custom-group",
		"heap.opts":                "-Xmx1g",
	}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}},
		},
	}

	desiredHash, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("failed to compute spec hash: %v", err)
	}

	newDeployment := func(hash string) *apps.Deployment {
		return &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        inst.Name + deploySuffix,
				Namespace:   inst.Namespace,
				Annotations: map[string]string{keyPrefix + "/specHash": hash},
			},
			Spec: apps.DeploymentSpec{
				// Operator-created deployments carry the stable identity
				// selector (F1); without it updateExistingDeployment would
				// treat this fixture as a legacy selector and migrate it.
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
				},
			},
		}
	}

	newReconciler := func(dep *apps.Deployment) (*StrimziSchemaRegistryReconciler, *apps.Deployment) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep).
			Build()
		stored := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, stored); err != nil {
			t.Fatalf("failed to get the seeded deployment: %v", err)
		}
		return &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}, stored
	}

	drainEvents := func() []string {
		var got []string
		for {
			select {
			case e := <-events:
				got = append(got, e)
			default:
				return got
			}
		}
	}

	ctx := context.Background()
	logger := logr.Logger{}

	t.Run("zero events when the spec hash is unchanged", func(t *testing.T) {
		r, dep := newReconciler(newDeployment(desiredHash))
		// Several steady-state reconciles: this is exactly the spam pattern
		// B2 removes — every reconcile rebuilds the desired spec and used to
		// emit one Warning per reserved key.
		for i := 0; i < 3; i++ {
			_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if specChanged {
				t.Fatal("expected specChanged=false for an unchanged spec hash")
			}
		}
		if got := drainEvents(); len(got) != 0 {
			t.Errorf("expected zero events for steady-state reconciles, got %d: %v", len(got), got)
		}
	})

	t.Run("exactly one event per overridden key when the spec changed", func(t *testing.T) {
		// The Deployment carries a stale hash, so the spec is genuinely
		// changed and the reserved keys must be reported.
		r, dep := newReconciler(newDeployment("stale-hash"))
		_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a stale spec hash")
		}
		got := drainEvents()
		if len(got) != 2 {
			t.Fatalf("expected exactly 2 ConfigKeyOverridden events (one per reserved key), got %d: %v", len(got), got)
		}
		for _, key := range []string{"heap.opts", "schema.registry.group.id"} {
			found := false
			for _, e := range got {
				if strings.Contains(e, "ConfigKeyOverridden") && strings.Contains(e, key) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a ConfigKeyOverridden event naming %q, events: %v", key, got)
			}
		}
	})
}

// TestUpdateExistingDeploymentPreservesManualReplicas covers B6: a spec
// change must not reset a manually scaled Deployment. The CR exposes no
// replicas field, so the operator never desires a specific replica count and
// the live Spec.Replicas (set by `kubectl scale`) must survive the update;
// a Deployment left at the default (nil) must stay nil.
func TestUpdateExistingDeploymentPreservesManualReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	inst := newTestInstance()
	// PLAINTEXT avoids any JKS/user-secret requirements; only the Kafka CR
	// with status listeners is needed.
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}},
		},
	}

	desiredHash, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("failed to compute spec hash: %v", err)
	}

	newDeployment := func(hash string, replicas *int32) *apps.Deployment {
		return &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        inst.Name + deploySuffix,
				Namespace:   inst.Namespace,
				Annotations: map[string]string{keyPrefix + "/specHash": hash},
			},
			Spec: apps.DeploymentSpec{
				// Operator-created deployments carry the stable identity
				// selector (F1); without it updateExistingDeployment would
				// treat this fixture as a legacy selector and migrate it.
				Selector: &metav1.LabelSelector{MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)},
				Replicas: replicas,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
				},
			},
		}
	}

	newReconciler := func(dep *apps.Deployment) (*StrimziSchemaRegistryReconciler, *apps.Deployment) {
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep).
			Build()
		stored := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, stored); err != nil {
			t.Fatalf("failed to get the seeded deployment: %v", err)
		}
		return &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}, stored
	}

	ctx := context.Background()
	logger := logr.Logger{}

	t.Run("manually scaled replicas survive a spec change", func(t *testing.T) {
		r, dep := newReconciler(newDeployment("stale-hash", ptr.To(int32(3))))
		_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a stale spec hash")
		}
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
			t.Errorf("expected the manually scaled replicas (3) to survive the spec update, got %v", dep.Spec.Replicas)
		}
	})

	t.Run("unset replicas stay unset across a spec change", func(t *testing.T) {
		r, dep := newReconciler(newDeployment("stale-hash", nil))
		_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !specChanged {
			t.Fatal("expected specChanged=true for a stale spec hash")
		}
		if dep.Spec.Replicas != nil {
			t.Errorf("expected replicas to stay unset, got %d", *dep.Spec.Replicas)
		}
	})

	t.Run("steady-state reconcile leaves replicas untouched", func(t *testing.T) {
		r, dep := newReconciler(newDeployment(desiredHash, ptr.To(int32(5))))
		_, specChanged, err := r.updateExistingDeployment(inst, ctx, logger, dep)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if specChanged {
			t.Fatal("expected specChanged=false for an unchanged spec hash")
		}
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 5 {
			t.Errorf("expected replicas to stay 5 in steady state, got %v", dep.Spec.Replicas)
		}
	})
}

// TestBuildDeploymentSpecReplicas covers B20: the desired replica count from
// spec.replicas must be propagated to the Deployment, with an unset value
// defaulting to 1 (the Deployment API default, so CRs created before the
// field existed behave unchanged).
func TestBuildDeploymentSpecReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	build := func(t *testing.T, inst *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) *apps.Deployment {
		t.Helper()
		r := &StrimziSchemaRegistryReconciler{Scheme: scheme}
		dep, _, err := r.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", "",
			kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return dep
	}

	newInst := func() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "PLAINTEXT"
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
		}
		return inst
	}

	t.Run("unset replicas default to 1", func(t *testing.T) {
		dep := build(t, newInst())
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
			t.Errorf("expected the default replica count 1, got %v", dep.Spec.Replicas)
		}
	})

	t.Run("explicit replicas are propagated", func(t *testing.T) {
		inst := newInst()
		inst.Spec.Replicas = ptr.To(int32(3))
		dep := build(t, inst)
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
			t.Errorf("expected the CR replica count 3 on the Deployment, got %v", dep.Spec.Replicas)
		}
	})
}

// TestComputeSpecHashReplicas covers B20: changing spec.replicas must change
// the spec hash so a replica change triggers a deployment update, while an
// explicit 1 and an unset replicas (both meaning 1) must hash identically to
// avoid a spurious rollout.
func TestComputeSpecHashReplicas(t *testing.T) {
	inst := newTestInstance()
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
	}

	unset, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	inst.Spec.Replicas = ptr.To(int32(1))
	explicitOne, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if explicitOne != unset {
		t.Errorf("expected an explicit replicas=1 to hash like the unset default, got %s vs %s", explicitOne, unset)
	}

	inst.Spec.Replicas = ptr.To(int32(2))
	explicitTwo, err := computeSpecHash(inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if explicitTwo == unset {
		t.Error("expected a replicas change (1 -> 2) to change the spec hash")
	}
}

// TestUpdateExistingDeploymentAppliesCRReplicas covers B20: when the CR sets
// spec.replicas, the desired replica count must win over a manually scaled
// Deployment on a spec change. The B6 preservation applies only when the CR
// leaves replicas unset.
func TestUpdateExistingDeploymentAppliesCRReplicas(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	inst := newTestInstance()
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.Replicas = ptr.To(int32(3))
	inst.Spec.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
	}

	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        inst.Name + deploySuffix,
			Namespace:   inst.Namespace,
			Annotations: map[string]string{keyPrefix + "/specHash": "stale-hash"},
		},
		Spec: apps.DeploymentSpec{
			// Operator-created deployments carry the stable identity selector
			// (F1); without it updateExistingDeployment would treat this
			// fixture as a legacy selector and migrate it.
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(inst.Name)},
			// A manual `kubectl scale` left the Deployment at 5 replicas.
			Replicas: ptr.To(int32(5)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), dep).
		Build()
	stored := &apps.Deployment{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, stored); err != nil {
		t.Fatalf("failed to get the seeded deployment: %v", err)
	}

	r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
	_, specChanged, err := r.updateExistingDeployment(inst, context.Background(), logr.Logger{}, stored)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !specChanged {
		t.Fatal("expected specChanged=true for a stale spec hash")
	}
	if stored.Spec.Replicas == nil || *stored.Spec.Replicas != 3 {
		t.Errorf("expected the CR replicas (3) to override the manually scaled 5, got %v", stored.Spec.Replicas)
	}
}

// TestCRDManifestReplicasValidation guards the B20 CRD contract: the
// generated CRD must expose spec.replicas with the kubebuilder validation
// (optional, default 1, minimum 1). Reading the generated manifest keeps the
// regeneration step honest — dropping the markers from the type would fail
// here.
func TestCRDManifestReplicasValidation(t *testing.T) {
	crdPath := filepath.Join("..", "..", "config", "crd", "bases",
		"strimziregistryoperator.randsw.code_strimzischemaregistries.yaml")
	data, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("failed to read the generated CRD: %v", err)
	}

	var crd map[string]any
	if err := sigsyaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("failed to parse the generated CRD: %v", err)
	}

	replicas, err := crdSpecProperty(crd, "replicas")
	if err != nil {
		t.Fatalf("spec.replicas missing from the generated CRD: %v", err)
	}

	if got := replicas["type"]; got != "integer" {
		t.Errorf("expected spec.replicas type integer, got %v", got)
	}
	if got := replicas["default"]; got != float64(1) {
		t.Errorf("expected spec.replicas default 1, got %v", got)
	}
	minimum, ok := replicas["minimum"].(float64)
	if !ok || minimum != 1 {
		t.Errorf("expected spec.replicas minimum 1, got %v", replicas["minimum"])
	}
}

// crdSpecProperty navigates a parsed CRD manifest down to
// spec.versions[0].schema.openAPIV3Schema.properties.spec.properties[name].
func crdSpecProperty(crd map[string]any, name string) (map[string]any, error) {
	step := func(from any, key string) (any, error) {
		m, ok := from.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected a map at key %q", key)
		}
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("key %q not found", key)
		}
		return v, nil
	}

	versions, err := step(crd, "spec")
	if err != nil {
		return nil, err
	}
	versions, err = step(versions, "versions")
	if err != nil {
		return nil, err
	}
	versionList, ok := versions.([]any)
	if !ok || len(versionList) == 0 {
		return nil, fmt.Errorf("expected a non-empty versions list")
	}
	schema, err := step(versionList[0], "schema")
	if err != nil {
		return nil, err
	}
	schema, err = step(schema, "openAPIV3Schema")
	if err != nil {
		return nil, err
	}
	schema, err = step(schema, "properties")
	if err != nil {
		return nil, err
	}
	schema, err = step(schema, "spec")
	if err != nil {
		return nil, err
	}
	schema, err = step(schema, "properties")
	if err != nil {
		return nil, err
	}
	props, ok := schema.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a map of spec properties")
	}
	prop, ok := props[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("property %q not found", name)
	}
	return prop, nil
}

// newTestInstance creates a minimal StrimziSchemaRegistry for testing.
func newTestInstance() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
	return &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sr",
			Namespace: "default",
		},
		Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
			Listener:           "tls",
			SecurityProtocol:   "SSL",
			CompatibilityLevel: "backward",
			SecureHTTP:         false,
			HeapOpts:           "-Xms512M -Xmx512M",
			TLSSecretName:      "test-tls-secret",
			Template:           corev1.PodTemplateSpec{},
		},
	}
}

func TestComputeSpecHash(t *testing.T) {
	t.Run("same spec produces same hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 != hash2 {
			t.Errorf("expected same hash for identical specs: got %q and %q", hash1, hash2)
		}
		if hash1 == "" {
			t.Error("hash should not be empty")
		}
	})

	t.Run("different CompatibilityLevel produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.CompatibilityLevel = "forward"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different CompatibilityLevel: both %q", hash1)
		}
	})

	t.Run("different SecureHTTP produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.SecureHTTP = true

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different SecureHTTP: both %q", hash1)
		}
	})

	t.Run("different HeapOpts produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.HeapOpts = "-Xms1G -Xmx1G"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different HeapOpts: both %q", hash1)
		}
	})

	t.Run("different Listener produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.Listener = "plaintext"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different Listener: both %q", hash1)
		}
	})

	t.Run("different SecurityProtocol produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.SecurityProtocol = "PLAINTEXT"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different SecurityProtocol: both %q", hash1)
		}
	})

	t.Run("different TLSSecretName produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.TLSSecretName = "other-tls-secret"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different TLSSecretName: both %q", hash1)
		}
	})

	// B5 regression test: Template changes must affect the hash
	t.Run("different Template produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"},
				},
			},
		}
		inst2 := newTestInstance()
		inst2.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:7.7.0"},
				},
			},
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)

		if err1 != nil {
			t.Fatalf("unexpected error computing hash1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error computing hash2: %v", err2)
		}
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different container images: both %q", hash1)
		}
	})

	t.Run("different Template resources produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"},
				},
			},
		}
		inst2 := newTestInstance()
		inst2.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:8.4.1",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: mustParseQuantity("500m"),
							},
						},
					},
				},
			},
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)

		if err1 != nil {
			t.Fatalf("unexpected error computing hash1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error computing hash2: %v", err2)
		}
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different resource requirements: both %q", hash1)
		}
	})

	// T4: Template env var changes must affect the hash
	t.Run("different Template env vars produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:8.4.1",
						Env: []corev1.EnvVar{
							{Name: "CUSTOM_ENV", Value: "value1"},
						},
					},
				},
			},
		}
		inst2 := newTestInstance()
		inst2.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:8.4.1",
						Env: []corev1.EnvVar{
							{Name: "CUSTOM_ENV", Value: "value2"},
						},
					},
				},
			},
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)

		if err1 != nil {
			t.Fatalf("unexpected error computing hash1: %v", err1)
		}
		if err2 != nil {
			t.Fatalf("unexpected error computing hash2: %v", err2)
		}
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different container env vars: both %q", hash1)
		}
	})
}

func TestMustParseQuantity(t *testing.T) {
	// Valid quantities
	validCases := []struct {
		name  string
		input string
	}{
		{"512Mi", "512Mi"},
		{"1Gi", "1Gi"},
		{"256M", "256M"},
		{"100m", "100m"},
		{"500Mi", "500Mi"},
		{"2G", "2G"},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			q := mustParseQuantity(tc.input)
			if q.String() == "" {
				t.Errorf("expected non-empty quantity string for %q", tc.input)
			}
		})
	}

	// Invalid quantities should panic
	invalidCases := []string{
		"invalid",
		"abc",
		"",
	}

	for _, tc := range invalidCases {
		t.Run("panics on "+tc, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for invalid quantity %q, but no panic occurred", tc)
				}
			}()
			mustParseQuantity(tc)
		})
	}
}

// TestGetStrimziClusterName covers T3: verify that getStrimziClusterName
// handles nil labels, missing label, empty label, and valid label correctly.
func TestGetStrimziClusterName(t *testing.T) {
	t.Run("nil labels returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = nil

		name, err := getStrimziClusterName(inst)
		if err == nil {
			t.Error("expected error for nil labels, got nil")
		}
		if name != "" {
			t.Errorf("expected empty name for nil labels, got %q", name)
		}
	})

	t.Run("missing strimzi.io/cluster label returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"other-label": "some-value",
		}

		name, err := getStrimziClusterName(inst)
		if err == nil {
			t.Error("expected error for missing strimzi.io/cluster label, got nil")
		}
		if name != "" {
			t.Errorf("expected empty name for missing label, got %q", name)
		}
	})

	t.Run("empty strimzi.io/cluster label value returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "",
		}

		name, err := getStrimziClusterName(inst)
		if err == nil {
			t.Error("expected error for empty strimzi.io/cluster label, got nil")
		}
		if name != "" {
			t.Errorf("expected empty name for empty label value, got %q", name)
		}
	})

	t.Run("valid label returns cluster name", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "my-kafka-cluster",
		}

		name, err := getStrimziClusterName(inst)
		if err != nil {
			t.Fatalf("unexpected error for valid label: %v", err)
		}
		if name != "my-kafka-cluster" {
			t.Errorf("expected cluster name 'my-kafka-cluster', got %q", name)
		}
	})

	t.Run("valid label among other labels returns cluster name", func(t *testing.T) {
		inst := newTestInstance()
		inst.Labels = map[string]string{
			"app.kubernetes.io/name": "my-app",
			"strimzi.io/cluster":     "prod-kafka",
			"other-label":            "other",
		}

		name, err := getStrimziClusterName(inst)
		if err != nil {
			t.Fatalf("unexpected error for valid label: %v", err)
		}
		if name != "prod-kafka" {
			t.Errorf("expected cluster name 'prod-kafka', got %q", name)
		}
	})
}

// TestRenewTLSSecret_NoOp covers T1: verify that renewTLSSecret is a no-op
// when SecureHTTP is false or a custom TLSSecretName is provided. Also tests
// that a missing strimzi.io/cluster label causes an error.
func TestRenewTLSSecret_NoOp(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &StrimziSchemaRegistryReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	t.Run("SecureHTTP=false is no-op", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.TLSSecretName = ""
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "test-cluster",
		}

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err != nil {
			t.Errorf("expected no error when SecureHTTP=false, got: %v", err)
		}
	})

	t.Run("TLSSecretName set is no-op", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "my-custom-tls"
		inst.Labels = map[string]string{
			"strimzi.io/cluster": "test-cluster",
		}

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err != nil {
			t.Errorf("expected no error when TLSSecretName is set, got: %v", err)
		}
	})

	t.Run("missing strimzi.io/cluster label returns error", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = ""
		inst.Labels = nil

		err := reconciler.renewTLSSecret(inst, context.Background(), logr.Logger{})
		if err == nil {
			t.Error("expected error when strimzi.io/cluster label is missing, got nil")
		}
	})
}

// TestBuildProbesInitialDelay verifies that readiness and liveness probes no
// longer carry an InitialDelaySeconds value (the startup probe already gates
// them), while the startup probe keeps its 30s initial delay.
func TestBuildProbesInitialDelay(t *testing.T) {
	t.Run("HTTP probes have no initial delay and startup keeps 30s", func(t *testing.T) {
		readiness, liveness, startup := buildProbes(8081, corev1.URISchemeHTTP)
		if readiness.InitialDelaySeconds != 0 {
			t.Errorf("expected readiness InitialDelaySeconds=0, got %d", readiness.InitialDelaySeconds)
		}
		if liveness.InitialDelaySeconds != 0 {
			t.Errorf("expected liveness InitialDelaySeconds=0, got %d", liveness.InitialDelaySeconds)
		}
		if startup.InitialDelaySeconds != 30 {
			t.Errorf("expected startup InitialDelaySeconds=30, got %d", startup.InitialDelaySeconds)
		}
	})

	t.Run("HTTPS probes have no initial delay and startup keeps 30s", func(t *testing.T) {
		readiness, liveness, startup := buildProbes(8085, corev1.URISchemeHTTPS)
		if readiness.InitialDelaySeconds != 0 {
			t.Errorf("expected readiness InitialDelaySeconds=0, got %d", readiness.InitialDelaySeconds)
		}
		if liveness.InitialDelaySeconds != 0 {
			t.Errorf("expected liveness InitialDelaySeconds=0, got %d", liveness.InitialDelaySeconds)
		}
		if startup.InitialDelaySeconds != 30 {
			t.Errorf("expected startup InitialDelaySeconds=30, got %d", startup.InitialDelaySeconds)
		}
	})
}

// TestBuildPodEnvStorageIdentity verifies the storage and identity env vars,
// including their empty/nil fallback defaults and the nil replication factor
// being omitted entirely.
func TestBuildPodEnvStorageIdentity(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("defaults when fields are empty or nil", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.KafkastoreTopic = ""
		inst.Spec.KafkastoreTopicReplicationFactor = nil
		inst.Spec.SchemaRegistryGroupID = ""
		inst.Spec.MasterEligibility = nil

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", "registry-schemas")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_GROUP_ID", "schema-registry")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_MASTER_ELIGIBILITY", "true")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR")
	})

	t.Run("explicit values are propagated", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.KafkastoreTopic = "custom-schemas"
		rf := int32(1)
		inst.Spec.KafkastoreTopicReplicationFactor = &rf
		inst.Spec.SchemaRegistryGroupID = "custom-group"
		masterEligibility := false
		inst.Spec.MasterEligibility = &masterEligibility

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", "custom-schemas")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR", "1")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_GROUP_ID", "custom-group")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_MASTER_ELIGIBILITY", "false")
	})
}

// TestComputeSpecHashStorageIdentity verifies that the four new spec fields
// participate in the spec hash so changes trigger a deployment rollout.
func TestComputeSpecHashStorageIdentity(t *testing.T) {
	t.Run("different KafkastoreTopic produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.KafkastoreTopic = "custom-schemas"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different KafkastoreTopic: both %q", hash1)
		}
	})

	t.Run("different KafkastoreTopicReplicationFactor produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		rf := int32(1)
		inst2.Spec.KafkastoreTopicReplicationFactor = &rf

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different KafkastoreTopicReplicationFactor: both %q", hash1)
		}
	})

	t.Run("different SchemaRegistryGroupID produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.SchemaRegistryGroupID = "custom-group"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different SchemaRegistryGroupID: both %q", hash1)
		}
	})

	t.Run("different MasterEligibility produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		masterEligibility := false
		inst2.Spec.MasterEligibility = &masterEligibility

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different MasterEligibility: both %q", hash1)
		}
	})
}

func TestConfluentPropertyToEnvName(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"kafkastore.timeout.ms", "SCHEMA_REGISTRY_KAFKASTORE_TIMEOUT_MS"},
		{"access.control.allow.origin", "SCHEMA_REGISTRY_ACCESS_CONTROL_ALLOW_ORIGIN"},
		{"authentication.realm", "SCHEMA_REGISTRY_AUTHENTICATION_REALM"},
		{"debug", "SCHEMA_REGISTRY_DEBUG"},
		{"foo-bar.baz", "SCHEMA_REGISTRY_FOO_BAR_BAZ"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := confluentPropertyToEnvName(tt.key); got != tt.want {
				t.Errorf("confluentPropertyToEnvName(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestBuildPodEnvConfigurationReserved(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("reserved keys do not override managed env vars", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.Configuration = map[string]string{
			"kafkastore.security.protocol":     "SSL",
			"listeners":                        "http://10.0.0.1:9999",
			"kafkastore.topic":                 "attacker-topic",
			"kafkastore.ssl.keystore.location": "/tmp/evil.jks",
			"access.control.allow.origin":      "*",
		}

		env, overridden := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", "PLAINTEXT")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_LISTENERS", "http://0.0.0.0:8081")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", "registry-schemas")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_ACCESS_CONTROL_ALLOW_ORIGIN", "*")

		wantOverridden := []string{
			"kafkastore.security.protocol",
			"kafkastore.ssl.keystore.location",
			"kafkastore.topic",
			"listeners",
		}
		if len(overridden) != len(wantOverridden) {
			t.Fatalf("expected %d overridden keys, got %d: %v", len(wantOverridden), len(overridden), overridden)
		}
		for i := range wantOverridden {
			if overridden[i] != wantOverridden[i] {
				t.Errorf("overridden[%d] = %q, want %q", i, overridden[i], wantOverridden[i])
			}
		}
	})

	t.Run("modern Confluent keys are reserved and cannot be overridden", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true
		inst.Spec.Configuration = map[string]string{
			"ssl.client.authentication":               "NONE",
			"inter.instance.protocol":                 "http",
			"schema.registry.inter.instance.protocol": "http",
			"ssl.keystore.type":                       "PKCS12",
		}

		env, overridden := buildPodEnv(inst, "kafka:9092", "", auth)

		// The operator-managed values win; user-supplied values are ignored.
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION", "REQUIRED")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL", "https")
		// ssl.keystore.type follows spec.keystoretype (JKS here), not the
		// user-supplied configuration value.
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTH")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL")

		wantOverridden := []string{
			"inter.instance.protocol",
			"schema.registry.inter.instance.protocol",
			"ssl.client.authentication",
			"ssl.keystore.type",
		}
		if len(overridden) != len(wantOverridden) {
			t.Fatalf("expected %d overridden keys, got %d: %v", len(wantOverridden), len(overridden), overridden)
		}
		for i := range wantOverridden {
			if overridden[i] != wantOverridden[i] {
				t.Errorf("overridden[%d] = %q, want %q", i, overridden[i], wantOverridden[i])
			}
		}
	})

	t.Run("non-reserved key colliding with managed env is skipped", func(t *testing.T) {
		inst := newTestInstance()
		rf := int32(1)
		inst.Spec.KafkastoreTopicReplicationFactor = &rf
		inst.Spec.Configuration = map[string]string{
			"kafkastore.topic.replication.factor": "3",
		}

		env, overridden := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR", "1")
		if len(overridden) != 1 || overridden[0] != "kafkastore.topic.replication.factor" {
			t.Errorf("expected overridden to contain the replication-factor collision, got %v", overridden)
		}
	})
}

func TestComputeSpecHashConfigurationDeterministic(t *testing.T) {
	t.Run("same configuration with different insertion order produces same hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Configuration = map[string]string{
			"access.control.allow.origin": "*",
			"authentication.realm":        "Basic",
			"debug":                       "true",
		}
		inst2 := newTestInstance()
		inst2.Spec.Configuration = map[string]string{
			"debug":                       "true",
			"access.control.allow.origin": "*",
			"authentication.realm":        "Basic",
		}

		hash1, err1 := computeSpecHash(inst1)
		hash2, err2 := computeSpecHash(inst2)
		if err1 != nil || err2 != nil {
			t.Fatalf("unexpected error: %v %v", err1, err2)
		}
		if hash1 != hash2 {
			t.Errorf("expected same hash for same configuration with different insertion order: %q vs %q", hash1, hash2)
		}
	})

	t.Run("different configuration produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Configuration = map[string]string{"debug": "true"}
		inst2 := newTestInstance()
		inst2.Spec.Configuration = map[string]string{"debug": "false"}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different configuration values: both %q", hash1)
		}
	})
}

// assertQuantity fails the test when the resource quantity does not equal want.
func assertQuantity(t *testing.T, got resource.Quantity, want string) {
	t.Helper()
	if got.Cmp(mustParseQuantity(want)) != 0 {
		t.Errorf("expected quantity %s, got %s", want, got.String())
	}
}

// TestBuildDeploymentSpecResourceDefaults verifies that resource requests and
// limits are defaulted per-field, rather than only when both are nil.
func TestBuildDeploymentSpecResourceDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	t.Run("only limits set gets default requests", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:8.4.1",
						Resources: corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    mustParseQuantity("2000m"),
								corev1.ResourceMemory: mustParseQuantity("2Gi"),
							},
						},
					},
				},
			},
		}

		dep, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", "", kafkaAuth{}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res := dep.Spec.Template.Spec.Containers[0].Resources
		assertQuantity(t, res.Requests[corev1.ResourceCPU], "250m")
		assertQuantity(t, res.Requests[corev1.ResourceMemory], "512Mi")
		assertQuantity(t, res.Limits[corev1.ResourceCPU], "2000m")
		assertQuantity(t, res.Limits[corev1.ResourceMemory], "2Gi")
	})

	t.Run("only requests set gets default limits", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:8.4.1",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    mustParseQuantity("300m"),
								corev1.ResourceMemory: mustParseQuantity("600Mi"),
							},
						},
					},
				},
			},
		}

		dep, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", "", kafkaAuth{}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res := dep.Spec.Template.Spec.Containers[0].Resources
		assertQuantity(t, res.Requests[corev1.ResourceCPU], "300m")
		assertQuantity(t, res.Requests[corev1.ResourceMemory], "600Mi")
		assertQuantity(t, res.Limits[corev1.ResourceCPU], "1000m")
		assertQuantity(t, res.Limits[corev1.ResourceMemory], "1Gi")
	})

	t.Run("neither requests nor limits get full defaults", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"},
				},
			},
		}

		dep, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", "", kafkaAuth{}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		res := dep.Spec.Template.Spec.Containers[0].Resources
		assertQuantity(t, res.Requests[corev1.ResourceCPU], "250m")
		assertQuantity(t, res.Requests[corev1.ResourceMemory], "512Mi")
		assertQuantity(t, res.Limits[corev1.ResourceCPU], "1000m")
		assertQuantity(t, res.Limits[corev1.ResourceMemory], "1Gi")
	})
}

// TestBuildDeploymentSpecDoesNotMutateInput asserts that buildDeploymentSpec
// leaves the given CR untouched: the instance comes from the informer cache,
// and a shallow pod-template copy would share the Containers slice backing
// array (and resource maps) with the cached object, letting the env, resource
// and probe mutations below corrupt it for subsequent reconciles. The
// user-supplied template fields (B3 merge inputs) must also stay pristine.
func TestBuildDeploymentSpecDoesNotMutateInput(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	inst := newTestInstance()
	inst.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"team": "data", "app": "user-value"},
			Annotations: map[string]string{"user": "keep"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "sr",
					Image: "confluentinc/cp-schema-registry:8.4.1",
					Env: []corev1.EnvVar{
						{Name: "MY_CUSTOM", Value: "custom"},
						{Name: "SCHEMA_REGISTRY_HEAP_OPTS", Value: "user-value"},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "extra", MountPath: "/opt/extra"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}

	before := inst.Spec.Template.DeepCopy()

	_, _, err := reconciler.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", "", kafkaAuth{}, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !equality.Semantic.DeepEqual(*before, inst.Spec.Template) {
		t.Errorf("buildDeploymentSpec mutated the input instance.Spec.Template:\nbefore: %+v\nafter:  %+v", *before, inst.Spec.Template)
	}
}

// TestBuildDeploymentSpecDeclaresContainerPort covers B19: the Schema
// Registry listener port must be declared in the container spec (the Service
// targetPort and the probes reference the numeric port), with the name
// matching the Service port name (http/https), and user-declared ports from
// spec.template must be preserved.
func TestBuildDeploymentSpecDeclaresContainerPort(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	build := func(t *testing.T, inst *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) *apps.Deployment {
		t.Helper()
		r := &StrimziSchemaRegistryReconciler{Scheme: scheme}
		dep, _, err := r.buildDeploymentSpec(inst, "kafka:9092", "kafka-cluster", "", "", "", "",
			kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return dep
	}

	t.Run("plain HTTP declares port 8081 named http", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
		}
		ports := build(t, inst).Spec.Template.Spec.Containers[0].Ports
		if len(ports) != 1 {
			t.Fatalf("expected exactly 1 container port, got %v", ports)
		}
		if ports[0].ContainerPort != 8081 || ports[0].Name != "http" || ports[0].Protocol != corev1.ProtocolTCP {
			t.Errorf("expected container port 8081/TCP named http, got %v", ports[0])
		}
	})

	t.Run("secure HTTP declares port 8085 named https", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sr", Image: "confluentinc/cp-schema-registry:8.4.1"}}},
		}
		ports := build(t, inst).Spec.Template.Spec.Containers[0].Ports
		if len(ports) != 1 {
			t.Fatalf("expected exactly 1 container port, got %v", ports)
		}
		if ports[0].ContainerPort != 8085 || ports[0].Name != "https" {
			t.Errorf("expected container port 8085 named https, got %v", ports[0])
		}
	})

	t.Run("user-declared ports are preserved alongside the listener port", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "sr",
				Image: "confluentinc/cp-schema-registry:8.4.1",
				Ports: []corev1.ContainerPort{
					{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
					// Conflicts with the operator listener port: the
					// operator-managed entry wins.
					{Name: "custom", ContainerPort: 8081, Protocol: corev1.ProtocolTCP},
				},
			}}},
		}
		ports := build(t, inst).Spec.Template.Spec.Containers[0].Ports
		if len(ports) != 2 {
			t.Fatalf("expected 2 container ports (listener + user metrics), got %v", ports)
		}
		if ports[0].ContainerPort != 8081 || ports[0].Name != "http" {
			t.Errorf("expected the operator listener port first, got %v", ports[0])
		}
		if ports[1].ContainerPort != 9090 || ports[1].Name != "metrics" {
			t.Errorf("expected the user metrics port preserved, got %v", ports[1])
		}
	})
}

// TestMergeEnvVars is a table-driven test for the B3 env merge: the
// operator-managed env comes first and wins on name conflicts, user-only vars
// are appended in their original order, duplicates inside the user list are
// collapsed, and empty inputs merge without allocating surprises.
func TestMergeEnvVars(t *testing.T) {
	env := func(names ...string) []corev1.EnvVar {
		out := make([]corev1.EnvVar, 0, len(names))
		for _, n := range names {
			out = append(out, corev1.EnvVar{Name: n, Value: "v-" + n})
		}
		return out
	}
	names := func(vars []corev1.EnvVar) []string {
		out := make([]string, 0, len(vars))
		for _, e := range vars {
			out = append(out, e.Name)
		}
		return out
	}

	tests := []struct {
		name     string
		operator []corev1.EnvVar
		user     []corev1.EnvVar
		want     []string
	}{
		{name: "no user env keeps operator list", operator: env("A", "B"), user: nil, want: []string{"A", "B"}},
		{name: "no operator env keeps user list", operator: nil, user: env("X", "Y"), want: []string{"X", "Y"}},
		{name: "both empty yields empty", operator: nil, user: nil, want: nil},
		{name: "operator wins on conflict", operator: env("A", "B"), user: env("B", "X"), want: []string{"A", "B", "X"}},
		{name: "user extras appended in order", operator: env("A"), user: env("Z", "Y", "X"), want: []string{"A", "Z", "Y", "X"}},
		{name: "duplicates inside user list collapse", operator: env("A"), user: env("X", "X", "Y"), want: []string{"A", "X", "Y"}},
		{name: "duplicates inside operator list collapse", operator: env("A", "A"), user: env("A", "B"), want: []string{"A", "A", "B"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeEnvVars(tt.operator, tt.user)
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if want, gotNames := strings.Join(tt.want, ","), strings.Join(names(got), ","); want != gotNames {
				t.Errorf("merged env names mismatch: want [%s], got [%s]", want, gotNames)
			}
			// Conflict keys must carry the operator value.
			operatorCount := map[string]int{}
			for _, e := range tt.operator {
				operatorCount[e.Name]++
			}
			seen := map[string]int{}
			for _, e := range got {
				seen[e.Name]++
				if c := operatorCount[e.Name]; c > 0 && e.Value != "v-"+e.Name {
					t.Errorf("env %q must carry the operator value, got %q", e.Name, e.Value)
				}
			}
			for n, count := range seen {
				// Operator duplicates pass through as-is; user-added names
				// must never duplicate anything.
				if want := operatorCount[n]; want <= 1 && count > 1 {
					t.Errorf("duplicate env name %q in merged output (%d occurrences)", n, count)
				}
			}
		})
	}
}

// TestMergeVolumes covers the B3 volume merge: operator volumes come first
// and win on name conflicts; user volumes are appended in order; duplicates
// inside the user list collapse.
func TestMergeVolumes(t *testing.T) {
	vol := func(names ...string) []corev1.Volume {
		out := make([]corev1.Volume, 0, len(names))
		for _, n := range names {
			out = append(out, corev1.Volume{Name: n, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		}
		return out
	}
	names := func(vols []corev1.Volume) []string {
		out := make([]string, 0, len(vols))
		for _, v := range vols {
			out = append(out, v.Name)
		}
		return out
	}

	tests := []struct {
		name     string
		operator []corev1.Volume
		user     []corev1.Volume
		want     []string
	}{
		{name: "no user volumes", operator: vol("tls"), user: nil, want: []string{"tls"}},
		{name: "no operator volumes", operator: nil, user: vol("extra"), want: []string{"extra"}},
		{name: "empty both", operator: nil, user: nil, want: nil},
		{name: "operator wins on name conflict", operator: vol("tls"), user: vol("tls", "extra"), want: []string{"tls", "extra"}},
		{name: "user extras appended in order", operator: vol("a"), user: vol("z", "y"), want: []string{"a", "z", "y"}},
		{name: "duplicates in user list collapse", operator: vol("a"), user: vol("x", "x"), want: []string{"a", "x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeVolumes(tt.operator, tt.user)
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if want, gotNames := strings.Join(tt.want, ","), strings.Join(names(got), ","); want != gotNames {
				t.Errorf("merged volume names mismatch: want [%s], got [%s]", want, gotNames)
			}
		})
	}
}

// TestMergeVolumeMounts covers the B3 mount merge: operator mounts win on
// both name and mount-path conflicts; user-only mounts are appended.
func TestMergeVolumeMounts(t *testing.T) {
	mnt := func(name, path string) corev1.VolumeMount {
		return corev1.VolumeMount{Name: name, MountPath: path}
	}
	desc := func(mounts []corev1.VolumeMount) []string {
		out := make([]string, 0, len(mounts))
		for _, m := range mounts {
			out = append(out, m.Name+":"+m.MountPath)
		}
		return out
	}

	tests := []struct {
		name     string
		operator []corev1.VolumeMount
		user     []corev1.VolumeMount
		want     []string
	}{
		{
			name:     "no user mounts",
			operator: []corev1.VolumeMount{mnt("rest-api-tls", "/var/rest-api-tls")},
			user:     nil,
			want:     []string{"rest-api-tls:/var/rest-api-tls"},
		},
		{
			name:     "user mount name conflict drops user entry",
			operator: []corev1.VolumeMount{mnt("rest-api-tls", "/var/rest-api-tls")},
			user:     []corev1.VolumeMount{mnt("rest-api-tls", "/elsewhere"), mnt("extra", "/opt/extra")},
			want:     []string{"rest-api-tls:/var/rest-api-tls", "extra:/opt/extra"},
		},
		{
			name:     "user mount path conflict with different name drops user entry",
			operator: []corev1.VolumeMount{mnt("rest-api-tls", "/var/rest-api-tls")},
			user:     []corev1.VolumeMount{mnt("user-vol", "/var/rest-api-tls")},
			want:     []string{"rest-api-tls:/var/rest-api-tls"},
		},
		{
			name:     "user extras preserved in order",
			operator: []corev1.VolumeMount{mnt("tls", "/var/schemaregistry")},
			user:     []corev1.VolumeMount{mnt("c", "/opt/c"), mnt("a", "/opt/a")},
			want:     []string{"tls:/var/schemaregistry", "c:/opt/c", "a:/opt/a"},
		},
		{
			name:     "duplicates within user list collapse",
			operator: nil,
			user:     []corev1.VolumeMount{mnt("x", "/opt/x"), mnt("x2", "/opt/x")},
			want:     []string{"x:/opt/x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeVolumeMounts(tt.operator, tt.user)
			if want, gotDesc := strings.Join(tt.want, ","), strings.Join(desc(got), ","); want != gotDesc {
				t.Errorf("merged mounts mismatch: want [%s], got [%s]", want, gotDesc)
			}
		})
	}
}

// TestBuildDeploymentSpecMergesUserTemplateFields is the end-to-end check for
// B3: user-provided env vars, volumes, mounts, labels and annotations in
// spec.template survive buildDeploymentSpec, operator-managed values win on
// conflicts, and the output is deterministic across calls (spec-hash
// semantics stay stable).
func TestBuildDeploymentSpecMergesUserTemplateFields(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	newInst := func() *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"team": "data", "app": "user-label"},
				Annotations: map[string]string{"user.io/keep": "yes"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "sr",
						Image: "confluentinc/cp-schema-registry:8.4.1",
						Env: []corev1.EnvVar{
							// collides with an operator-managed env var
							{Name: "SCHEMA_REGISTRY_HEAP_OPTS", Value: "user-value"},
							{Name: "MY_CUSTOM", Value: "custom-value"},
						},
						VolumeMounts: []corev1.VolumeMount{
							// collides with the operator rest-api-tls mount name
							{Name: "rest-api-tls", MountPath: "/wrong"},
							{Name: "extra", MountPath: "/opt/extra"},
						},
					},
				},
				Volumes: []corev1.Volume{
					// collides with the operator-managed volume name
					{Name: "rest-api-tls", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					{Name: "extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
			},
		}
		return inst
	}

	envValue := func(env []corev1.EnvVar, name string) (string, int) {
		count := 0
		value := ""
		for _, e := range env {
			if e.Name == name {
				count++
				value = e.Value
			}
		}
		return value, count
	}

	dep, _, err := reconciler.buildDeploymentSpec(newInst(), "kafka:9092", "kafka-cluster", "", "", "", "test-tls-secret", kafkaAuth{}, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	container := dep.Spec.Template.Spec.Containers[0]

	// Operator wins on env name conflicts.
	if v, count := envValue(container.Env, "SCHEMA_REGISTRY_HEAP_OPTS"); count != 1 || v != "-Xms512M -Xmx512M" {
		t.Errorf("expected the operator heap opts value, got %q (count %d)", v, count)
	}
	// User extras preserved.
	if v, count := envValue(container.Env, "MY_CUSTOM"); count != 1 || v != "custom-value" {
		t.Errorf("expected user env MY_CUSTOM to survive, got %q (count %d)", v, count)
	}

	// Volumes: operator-managed rest-api-tls wins (must be secret-backed, not
	// the user's emptyDir), user extra volume preserved.
	var restVol, extraVol *corev1.Volume
	for i := range dep.Spec.Template.Spec.Volumes {
		switch dep.Spec.Template.Spec.Volumes[i].Name {
		case "rest-api-tls":
			restVol = &dep.Spec.Template.Spec.Volumes[i]
		case "extra":
			extraVol = &dep.Spec.Template.Spec.Volumes[i]
		}
	}
	if restVol == nil || restVol.Secret == nil || restVol.Secret.SecretName != "test-tls-secret" {
		t.Errorf("expected the operator-managed secret-backed rest-api-tls volume, got %+v", restVol)
	}
	if extraVol == nil {
		t.Error("expected the user-provided extra volume to survive")
	}

	// Mounts: operator rest-api-tls mount kept at its path, user's colliding
	// mount dropped, user's extra mount preserved.
	var restMnt, userWrong, extraMnt *corev1.VolumeMount
	for i := range container.VolumeMounts {
		m := &container.VolumeMounts[i]
		switch {
		case m.Name == "rest-api-tls" && m.MountPath == "/var/rest-api-tls":
			restMnt = m
		case m.MountPath == "/wrong":
			userWrong = m
		case m.Name == "extra":
			extraMnt = m
		}
	}
	if restMnt == nil {
		t.Error("expected the operator rest-api-tls mount")
	}
	if userWrong != nil {
		t.Error("expected the user mount conflicting on name to be dropped")
	}
	if extraMnt == nil {
		t.Error("expected the user extra volume mount to survive")
	}

	// Labels: operator overrides conflicting keys, user extras preserved.
	if got := dep.Spec.Template.Labels["app"]; got != newInst().Name {
		t.Errorf("expected operator app label %q, got %q", newInst().Name, got)
	}
	if got := dep.Spec.Template.Labels["team"]; got != "data" {
		t.Errorf("expected user label team=data, got %q", got)
	}

	// Annotations: user annotation preserved alongside operator rollout
	// markers.
	if got := dep.Spec.Template.Annotations["user.io/keep"]; got != "yes" {
		t.Errorf("expected user annotation to survive, got %q", got)
	}
	if _, ok := dep.Spec.Template.Annotations[keyPrefix+"/jksVersion"]; !ok {
		t.Error("expected the operator jksVersion annotation")
	}

	// Selector must remain the operator label set (user labels excluded).
	if dep.Spec.Selector.MatchLabels["team"] != "" {
		t.Error("expected user labels NOT to leak into the Deployment selector")
	}

	// Determinism: a second build with an identical input produces an
	// identical pod template, keeping spec-hash comparison semantics stable.
	dep2, _, err := reconciler.buildDeploymentSpec(newInst(), "kafka:9092", "kafka-cluster", "", "", "", "test-tls-secret", kafkaAuth{}, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error on second build: %v", err)
	}
	if !equality.Semantic.DeepEqual(dep.Spec.Template, dep2.Spec.Template) {
		t.Error("buildDeploymentSpec output must be deterministic across calls")
	}
}

// TestReadyCondition is a table-driven test for the Ready condition helper: a
// Deployment with zero replicas (as observed immediately after creation) must
// not be reported as Ready, partial readiness must report NotReady, and a
// scale-down in progress (surplus replicas still running) must not report
// Ready prematurely (B14).
func TestReadyCondition(t *testing.T) {
	tests := []struct {
		name            string
		desiredReplicas int32
		replicas        int32
		readyReplicas   int32
		wantStatus      metav1.ConditionStatus
		wantReason      string
	}{
		{name: "0/0 replicas (fresh Deployment) is NotReady", desiredReplicas: 1, replicas: 0, readyReplicas: 0, wantStatus: metav1.ConditionFalse, wantReason: "NotReady"},
		{name: "1/2 replicas is NotReady", desiredReplicas: 2, replicas: 2, readyReplicas: 1, wantStatus: metav1.ConditionFalse, wantReason: "NotReady"},
		{name: "2/2 replicas is Ready", desiredReplicas: 2, replicas: 2, readyReplicas: 2, wantStatus: metav1.ConditionTrue, wantReason: "Ready"},
		{name: "1/1 replica is Ready", desiredReplicas: 1, replicas: 1, readyReplicas: 1, wantStatus: metav1.ConditionTrue, wantReason: "Ready"},
		{name: "0 ready of 1 replica is NotReady", desiredReplicas: 1, replicas: 1, readyReplicas: 0, wantStatus: metav1.ConditionFalse, wantReason: "NotReady"},
		// B14: during a scale-down from 3 to 1 the old replicas are still
		// running and ready; the deployment has not converged on the desired
		// count and must not be reported as Ready.
		{name: "scale-down in progress (3 running/ready, 1 desired) is NotReady", desiredReplicas: 1, replicas: 3, readyReplicas: 3, wantStatus: metav1.ConditionFalse, wantReason: "NotReady"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond := readyCondition(tt.desiredReplicas, tt.replicas, tt.readyReplicas, 7)
			if cond.Type != "Ready" {
				t.Errorf("expected condition type Ready, got %q", cond.Type)
			}
			if cond.Status != tt.wantStatus {
				t.Errorf("desired=%d replicas=%d readyReplicas=%d: expected status %v, got %v", tt.desiredReplicas, tt.replicas, tt.readyReplicas, tt.wantStatus, cond.Status)
			}
			if cond.Reason != tt.wantReason {
				t.Errorf("desired=%d replicas=%d readyReplicas=%d: expected reason %q, got %q", tt.desiredReplicas, tt.replicas, tt.readyReplicas, tt.wantReason, cond.Reason)
			}
			if cond.ObservedGeneration != 7 {
				t.Errorf("expected ObservedGeneration 7, got %d", cond.ObservedGeneration)
			}
		})
	}
}

// TestMergeServicePorts covers the node-port preservation logic used when
// updating an existing Service: the API-server-allocated nodePort is carried
// over only when the target service type still uses node ports. Copying it
// into a ClusterIP service would make the update fail with
// "nodePort: Forbidden", deadlocking reconciliation.
func TestMergeServicePorts(t *testing.T) {
	newPort := func(nodePort int32) corev1.ServicePort {
		return corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8081), NodePort: nodePort}
	}

	tests := []struct {
		name             string
		desiredType      corev1.ServiceType
		desiredNodePort  int32
		existingNodePort int32
		wantNodePort     int32
	}{
		{name: "NodePort to ClusterIP drops the allocated nodePort", desiredType: corev1.ServiceTypeClusterIP, existingNodePort: 31234, wantNodePort: 0},
		{name: "NodePort to NodePort preserves the allocated nodePort", desiredType: corev1.ServiceTypeNodePort, existingNodePort: 31234, wantNodePort: 31234},
		{name: "NodePort to LoadBalancer preserves the allocated nodePort", desiredType: corev1.ServiceTypeLoadBalancer, existingNodePort: 31234, wantNodePort: 31234},
		{name: "ClusterIP to NodePort picks up the allocated nodePort", desiredType: corev1.ServiceTypeNodePort, existingNodePort: 31234, wantNodePort: 31234},
		{name: "explicitly pinned nodePort is kept as-is", desiredType: corev1.ServiceTypeNodePort, desiredNodePort: 30123, existingNodePort: 31234, wantNodePort: 30123},
		{name: "ClusterIP to ClusterIP stays nodePort-free", desiredType: corev1.ServiceTypeClusterIP, wantNodePort: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := []corev1.ServicePort{newPort(tt.desiredNodePort)}
			existing := []corev1.ServicePort{newPort(tt.existingNodePort)}

			got := mergeServicePorts(tt.desiredType, desired, existing)
			if len(got) != 1 {
				t.Fatalf("expected one port, got %d", len(got))
			}
			if got[0].NodePort != tt.wantNodePort {
				t.Errorf("expected nodePort %d, got %d", tt.wantNodePort, got[0].NodePort)
			}
			if got[0].Port != 80 || got[0].TargetPort.IntVal != 8081 || got[0].Name != "http" {
				t.Errorf("unexpected port mutation: %+v", got[0])
			}
		})
	}

	t.Run("missing existing ports leaves the desired spec untouched", func(t *testing.T) {
		got := mergeServicePorts(corev1.ServiceTypeNodePort, []corev1.ServicePort{newPort(0)}, nil)
		if got[0].NodePort != 0 {
			t.Errorf("expected nodePort 0 with no existing ports, got %d", got[0].NodePort)
		}
	})
}

func TestCreateServicePassthrough(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	inst := newTestInstance()
	inst.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}
	inst.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
		Annotations: map[string]string{"prometheus.io/scrape": "true"},
		Labels:      map[string]string{"monitoring.example.com/team": "schemas"},
	}

	svc, err := reconciler.createService(inst, logr.Logger{})
	if err != nil {
		t.Fatalf("unexpected error creating service: %v", err)
	}

	if svc.Annotations["type"] != "http" {
		t.Errorf("expected operator-managed type annotation 'http', got %q", svc.Annotations["type"])
	}
	if svc.Annotations["prometheus.io/scrape"] != "true" {
		t.Errorf("expected passthrough annotation to be applied, got %v", svc.Annotations)
	}
	if svc.Labels["strimzi.io/cluster"] != "kafka-cluster" {
		t.Errorf("expected CR labels to be preserved, got %v", svc.Labels)
	}
	if svc.Labels["monitoring.example.com/team"] != "schemas" {
		t.Errorf("expected passthrough label to be applied, got %v", svc.Labels)
	}
}

func TestCreateServiceTypePortPassthrough(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	reconciler := &StrimziSchemaRegistryReconciler{Scheme: scheme}

	t.Run("defaults are ClusterIP on 80/443", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		svc, err := reconciler.createService(inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc.Spec.Type != corev1.ServiceTypeClusterIP {
			t.Errorf("expected ClusterIP, got %q", svc.Spec.Type)
		}
		if svc.Spec.Ports[0].Port != 80 {
			t.Errorf("expected port 80, got %d", svc.Spec.Ports[0].Port)
		}
		if svc.Spec.Ports[0].NodePort != 0 {
			t.Errorf("expected no nodePort, got %d", svc.Spec.Ports[0].NodePort)
		}
	})

	t.Run("type, port and nodeport are rendered", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		port := int32(8443)
		nodePort := int32(30443)
		inst.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
			Type:     "NodePort",
			Port:     &port,
			NodePort: &nodePort,
		}
		svc, err := reconciler.createService(inst, logr.Logger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc.Spec.Type != corev1.ServiceTypeNodePort {
			t.Errorf("expected NodePort, got %q", svc.Spec.Type)
		}
		if svc.Spec.Ports[0].Port != 8443 {
			t.Errorf("expected port 8443, got %d", svc.Spec.Ports[0].Port)
		}
		if svc.Spec.Ports[0].NodePort != 30443 {
			t.Errorf("expected nodePort 30443, got %d", svc.Spec.Ports[0].NodePort)
		}
		if svc.Spec.Ports[0].TargetPort.IntVal != 8085 {
			t.Errorf("expected targetPort 8085, got %d", svc.Spec.Ports[0].TargetPort.IntVal)
		}
	})
}

func TestBuildNetworkPolicyCrossNamespaceIngress(t *testing.T) {
	inst := newTestInstance()
	inst.Spec.SecureHTTP = false
	inst.Spec.NetworkPolicy = &strimziregistryoperatorv1alpha1.NetworkPolicy{
		Enabled:                    true,
		AllowCrossNamespaceIngress: true,
	}

	np, err := buildNetworkPolicy(inst, "kafka-cluster", "kafka-cluster-kafka-bootstrap.kafka.svc:9093")
	if err != nil {
		t.Fatalf("unexpected error building networkpolicy: %v", err)
	}

	// An empty From list allows ingress from all namespaces.
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].From) != 0 {
		t.Fatalf("expected a single ingress rule with an empty From list, got %v", np.Spec.Ingress)
	}
}

func TestComputeSpecHashServiceNetworkPolicy(t *testing.T) {
	t.Run("different service annotations produce different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
			Annotations: map[string]string{"prometheus.io/scrape": "true"},
		}
		inst2 := newTestInstance()
		inst2.Spec.Service = &strimziregistryoperatorv1alpha1.Service{
			Annotations: map[string]string{"prometheus.io/scrape": "false"},
		}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different service annotations: both %q", hash1)
		}
	})

	t.Run("different service labels produce different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.Service = &strimziregistryoperatorv1alpha1.Service{Labels: map[string]string{"team": "a"}}
		inst2 := newTestInstance()
		inst2.Spec.Service = &strimziregistryoperatorv1alpha1.Service{Labels: map[string]string{"team": "b"}}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different service labels: both %q", hash1)
		}
	})

	t.Run("networkpolicy enabled produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.NetworkPolicy = &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true}

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes when networkpolicy is enabled: both %q", hash1)
		}
	})

	// Known-answer test pinning the FNV-64a algorithm: the digest of the
	// default test instance must not change unless the hashing scheme is
	// deliberately changed. The value exceeds math.MaxUint32, which also
	// proves the former FNV-32 digest is gone. The digest changed when
	// spec.replicas joined the hashed inputs (B20) — a one-time rewrite of
	// every stored hash that triggers a single rollout on operator upgrade.
	t.Run("known FNV-64a digest for the default test instance", func(t *testing.T) {
		hash, err := computeSpecHash(newTestInstance())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hash != "6521916336062871651" {
			t.Errorf("expected the FNV-64a digest 6521916336062871651, got %q", hash)
		}
	})
}

func TestBuildNetworkPolicy(t *testing.T) {
	inst := newTestInstance()
	inst.Spec.SecureHTTP = false

	np, err := buildNetworkPolicy(inst, "kafka-cluster", "kafka-cluster-kafka-bootstrap.kafka.svc:9093")
	if err != nil {
		t.Fatalf("unexpected error building networkpolicy: %v", err)
	}

	if np.Name != "test-sr-network-policy" {
		t.Errorf("expected name test-sr-network-policy, got %q", np.Name)
	}
	if np.Spec.PodSelector.MatchLabels["app"] != "test-sr" {
		t.Errorf("expected pod selector app=test-sr, got %v", np.Spec.PodSelector.MatchLabels)
	}
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("expected Ingress and Egress policy types, got %v", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].Ports) != 1 {
		t.Fatalf("expected a single ingress rule with one port, got %v", np.Spec.Ingress)
	}
	if np.Spec.Ingress[0].Ports[0].Port.IntVal != 8081 {
		t.Errorf("expected ingress on SR listener port 8081, got %d", np.Spec.Ingress[0].Ports[0].Port.IntVal)
	}
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("expected three egress rules (Kafka + DNS + inter-instance), got %v", np.Spec.Egress)
	}
	if len(np.Spec.Egress[0].Ports) != 1 {
		t.Fatalf("expected a single port on the Kafka egress rule, got %v", np.Spec.Egress[0].Ports)
	}
	if np.Spec.Egress[0].Ports[0].Port.IntVal != 9093 {
		t.Errorf("expected egress to Kafka bootstrap port 9093, got %d", np.Spec.Egress[0].Ports[0].Port.IntVal)
	}
	if len(np.Spec.Egress[0].To) != 1 {
		t.Fatalf("expected one egress peer, got %v", np.Spec.Egress[0].To)
	}
	if np.Spec.Egress[0].To[0].PodSelector.MatchLabels["strimzi.io/cluster"] != "kafka-cluster" {
		t.Errorf("expected egress peer to target strimzi.io/cluster=kafka-cluster, got %v",
			np.Spec.Egress[0].To[0].PodSelector.MatchLabels)
	}

	// The DNS egress rule is required so the Schema Registry can resolve the
	// Kafka bootstrap service name in default-deny namespaces.
	dns := np.Spec.Egress[1]
	if len(dns.Ports) != 2 {
		t.Fatalf("expected two ports (UDP+TCP 53) on the DNS egress rule, got %v", dns.Ports)
	}
	for _, p := range dns.Ports {
		if p.Port.IntVal != 53 {
			t.Errorf("expected DNS port 53, got %d", p.Port.IntVal)
		}
	}
	if dns.Ports[0].Protocol == nil || *dns.Ports[0].Protocol != corev1.ProtocolUDP {
		t.Errorf("expected first DNS port protocol UDP, got %v", dns.Ports[0].Protocol)
	}
	if dns.Ports[1].Protocol == nil || *dns.Ports[1].Protocol != corev1.ProtocolTCP {
		t.Errorf("expected second DNS port protocol TCP, got %v", dns.Ports[1].Protocol)
	}
	if len(dns.To) != 1 {
		t.Fatalf("expected one peer on the DNS egress rule, got %v", dns.To)
	}
	if dns.To[0].NamespaceSelector == nil || dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Errorf("expected DNS peer namespace selector kube-system, got %v", dns.To[0].NamespaceSelector)
	}
	if dns.To[0].PodSelector == nil || dns.To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Errorf("expected DNS peer pod selector k8s-app=kube-dns, got %v", dns.To[0].PodSelector)
	}

	// The DNS egress rule must be rendered from the documented constants, and
	// the constants must keep matching the documented assumption (standard
	// kube-dns in kube-system) — see the README NetworkPolicy section.
	if dns.To[0].NamespaceSelector.MatchLabels[dnsNamespaceLabel] != dnsNamespaceName {
		t.Errorf("expected DNS namespace selector from constants %s=%s, got %v", dnsNamespaceLabel, dnsNamespaceName, dns.To[0].NamespaceSelector.MatchLabels)
	}
	if dns.To[0].PodSelector.MatchLabels[dnsPodLabel] != dnsPodValue {
		t.Errorf("expected DNS pod selector from constants %s=%s, got %v", dnsPodLabel, dnsPodValue, dns.To[0].PodSelector.MatchLabels)
	}

	// Inter-instance egress: replicas advertise their own pod IP as
	// host.name, so peer forwarding connects pod-to-pod on the listener
	// port. Without this rule a multi-replica registry with the policy
	// enabled cannot forward requests between instances.
	peers := np.Spec.Egress[2]
	if len(peers.To) != 1 {
		t.Fatalf("expected one peer on the inter-instance egress rule, got %v", peers.To)
	}
	if peers.To[0].PodSelector == nil || peers.To[0].PodSelector.MatchLabels["app"] != "test-sr" {
		t.Errorf("expected inter-instance peer selector app=test-sr, got %v", peers.To[0].PodSelector)
	}
	if len(peers.Ports) != 1 || peers.Ports[0].Port.IntVal != 8081 {
		t.Errorf("expected inter-instance egress on the listener port 8081, got %v", peers.Ports)
	}
	if peers.Ports[0].Protocol == nil || *peers.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Errorf("expected inter-instance egress protocol TCP, got %v", peers.Ports[0].Protocol)
	}
	if dnsNamespaceLabel != "kubernetes.io/metadata.name" || dnsNamespaceName != "kube-system" || dnsPodLabel != "k8s-app" || dnsPodValue != "kube-dns" {
		t.Errorf("DNS egress constants changed; update the README NetworkPolicy section to match the new assumption: %s=%s in namespace %s (label %s)",
			dnsPodLabel, dnsPodValue, dnsNamespaceName, dnsNamespaceLabel)
	}
}

// TestReconcileNetworkPolicyPreservesAnnotations covers B8: updating an
// existing NetworkPolicy must not wipe annotations set on it. The operator
// never sets annotations and the desired spec carries none, so the previous
// existing.Annotations = desired.Annotations assignment destroyed any
// user-managed annotations on every spec change.
func TestReconcileNetworkPolicyPreservesAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)

	inst := newTestInstance()
	// PLAINTEXT avoids any JKS/user-secret requirements; only the Kafka CR
	// with status listeners is needed.
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "tls"
	inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
	inst.Spec.NetworkPolicy = &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true}

	existing := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inst.Name + networkPolicySuffix,
			Namespace: inst.Namespace,
			Annotations: map[string]string{
				"example.com/owner": "platform-team",
			},
		},
		// A stale spec (no egress rules) so the update path runs.
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": inst.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, newTestKafkaCR("kafka-cluster", inst.Namespace), existing).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

	if err := r.reconcileNetworkPolicy(context.Background(), inst, logr.Logger{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &networkingv1.NetworkPolicy{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: inst.Name + networkPolicySuffix, Namespace: inst.Namespace}, updated); err != nil {
		t.Fatalf("failed to get the updated NetworkPolicy: %v", err)
	}
	if updated.Annotations["example.com/owner"] != "platform-team" {
		t.Errorf("expected the existing annotation to be preserved, got %v", updated.Annotations)
	}
	// The spec must still be reconciled to the desired state.
	if len(updated.Spec.Egress) == 0 {
		t.Error("expected the spec to be updated with the desired egress rules")
	}
}

// TestGetKafkaBootstrapServersError covers B10: the bootstrap lookup error
// must be actionable — it names the requested listener, the Kafka cluster
// and the requesting CR, and lists the listeners the Kafka CR exposes.
func TestGetKafkaBootstrapServersError(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	newInstance := func(listener string) *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
		inst := newTestInstance()
		inst.Spec.Listener = listener
		inst.Labels = map[string]string{strimziClusterLabel: "kafka-cluster"}
		return inst
	}

	t.Run("missing listener names the request and the available listeners", func(t *testing.T) {
		// The Kafka CR exposes only the "plain" listener; the CR asks for "tls".
		kafkaCR := newTestKafkaCR("kafka-cluster", "default")
		kafkaCR.Status.Listeners[0].Name = "plain"
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(newInstance("tls"), kafkaCR).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		_, _, err := r.getKafkaBootstrapServers(newInstance("tls"), context.Background(), logr.Logger{})
		if err == nil {
			t.Fatal("expected an error for a missing listener")
		}
		for _, want := range []string{`"tls"`, "kafka-cluster", "test-sr", "plain"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the error to contain %q, got: %s", want, err.Error())
			}
		}
	})

	t.Run("no listeners at all is reported explicitly", func(t *testing.T) {
		kafkaCR := newTestKafkaCR("kafka-cluster", "default")
		kafkaCR.Status.Listeners = nil
		client := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(newInstance("tls"), kafkaCR).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		_, _, err := r.getKafkaBootstrapServers(newInstance("tls"), context.Background(), logr.Logger{})
		if err == nil {
			t.Fatal("expected an error when the Kafka CR has no listeners")
		}
		if !strings.Contains(err.Error(), "no status.listeners") {
			t.Errorf("expected the error to mention the missing status.listeners, got: %s", err.Error())
		}
	})
}

// TestReconcileSkipsInvalidClientAuthCombination covers B12: when
// spec.clientauth is enabled without spec.securehttp, reconciliation is
// skipped and a previously created Deployment keeps serving unreconciled.
// The skip must be announced with a ReconciliationSkipped Warning event so
// the stale-deployment situation is visible, not just the validation error.
func TestReconcileSkipsInvalidClientAuthCombination(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	recorder := eventsfake.NewFakeRecorder(16)
	events := recorder.Events

	inst := newTestInstance()
	inst.Spec.ClientAuth = true
	inst.Spec.SecureHTTP = false

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst).
		WithStatusSubresource(inst).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme, Recorder: recorder}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for the skipped reconciliation, got %v", result.RequeueAfter)
	}

	drainEvents := func() []string {
		var got []string
		for {
			select {
			case e := <-events:
				got = append(got, e)
			default:
				return got
			}
		}
	}
	got := drainEvents()
	var hasValidation, hasSkip bool
	for _, e := range got {
		if strings.Contains(e, "ClientAuthRequiresSecureHTTP") {
			hasValidation = true
		}
		if strings.Contains(e, "ReconciliationSkipped") {
			hasSkip = true
		}
	}
	if !hasValidation {
		t.Errorf("expected a ClientAuthRequiresSecureHTTP event, got %v", got)
	}
	if !hasSkip {
		t.Errorf("expected a ReconciliationSkipped event announcing the skipped reconciliation, got %v", got)
	}

	// The CR status must still report the invalid spec.
	updated := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}, updated); err != nil {
		t.Fatalf("failed to get the updated CR: %v", err)
	}
	if updated.Status.Status != "InvalidSpec" {
		t.Errorf("expected status.status InvalidSpec, got %q", updated.Status.Status)
	}
}

func TestPortFromBootstrapServer(t *testing.T) {
	t.Run("valid host port", func(t *testing.T) {
		port, err := portFromBootstrapServer("kafka-bootstrap.kafka.svc:9093")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 9093 {
			t.Errorf("expected 9093, got %d", port)
		}
	})

	t.Run("missing port returns error", func(t *testing.T) {
		if _, err := portFromBootstrapServer("kafka-bootstrap.kafka.svc"); err == nil {
			t.Error("expected error for missing port")
		}
	})

	t.Run("non-numeric port returns error", func(t *testing.T) {
		if _, err := portFromBootstrapServer("kafka-bootstrap.kafka.svc:notaport"); err == nil {
			t.Error("expected error for non-numeric port")
		}
	})
}

func TestMigrateFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)

	t.Run("legacy removed and current added in one pass", func(t *testing.T) {
		inst := newTestInstance()
		inst.Finalizers = []string{legacyFinalizer}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		if err := reconciler.migrateFinalizer(context.Background(), inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr", Namespace: "default"}, got); err != nil {
			t.Fatalf("unexpected get error: %v", err)
		}
		if containsFinalizer(got.Finalizers, legacyFinalizer) {
			t.Errorf("legacy finalizer should be removed, got %v", got.Finalizers)
		}
		if !containsFinalizer(got.Finalizers, finalizer) {
			t.Errorf("current finalizer should be added, got %v", got.Finalizers)
		}
	})

	t.Run("already migrated is a no-op", func(t *testing.T) {
		inst := newTestInstance()
		inst.Finalizers = []string{finalizer}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		if err := reconciler.migrateFinalizer(context.Background(), inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr", Namespace: "default"}, got); err != nil {
			t.Fatalf("unexpected get error: %v", err)
		}
		if len(got.Finalizers) != 1 || got.Finalizers[0] != finalizer {
			t.Errorf("finalizers should be unchanged after idempotent migration, got %v", got.Finalizers)
		}
	})

	t.Run("fresh object gets current finalizer", func(t *testing.T) {
		inst := newTestInstance()
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		if err := reconciler.migrateFinalizer(context.Background(), inst, logr.Logger{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: "test-sr", Namespace: "default"}, got); err != nil {
			t.Fatalf("unexpected get error: %v", err)
		}
		if !containsFinalizer(got.Finalizers, finalizer) {
			t.Errorf("expected current finalizer to be added, got %v", got.Finalizers)
		}
	})
}

func containsFinalizer(finalizers []string, target string) bool {
	for _, f := range finalizers {
		if f == target {
			return true
		}
	}
	return false
}

func TestClientAuthValidationError(t *testing.T) {
	t.Run("valid when ClientAuth is false", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.ClientAuth = false
		if msg := clientAuthValidationError(inst); msg != "" {
			t.Errorf("expected no validation error, got %q", msg)
		}
	})

	t.Run("valid when ClientAuth is true with SecureHTTP", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true
		if msg := clientAuthValidationError(inst); msg != "" {
			t.Errorf("expected no validation error, got %q", msg)
		}
	})

	t.Run("invalid when ClientAuth is true without SecureHTTP", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.ClientAuth = true
		if msg := clientAuthValidationError(inst); msg == "" {
			t.Error("expected validation error, got empty message")
		}
	})
}

func TestClientAuthNeedsTruststoreSecret(t *testing.T) {
	t.Run("requires truststore for PLAINTEXT with ClientAuth and SecureHTTP", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true
		if !clientAuthNeedsTruststoreSecret(inst, kafkaAuth{Mode: authPlaintext}) {
			t.Error("expected truststore secret to be required")
		}
	})

	t.Run("no additional truststore for SSL", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true
		if clientAuthNeedsTruststoreSecret(inst, kafkaAuth{Mode: authMTLS, NeedsTruststore: true}) {
			t.Error("expected no additional truststore secret for SSL")
		}
	})

	t.Run("no truststore when ClientAuth is disabled", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = false
		if clientAuthNeedsTruststoreSecret(inst, kafkaAuth{Mode: authPlaintext}) {
			t.Error("expected no truststore secret when ClientAuth is disabled")
		}
	})
}

func TestBuildPodEnvClientAuth(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("client auth env is emitted when SecureHTTP is true", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION", "REQUIRED")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION", "/var/schemaregistry/truststore.jks")
		truststorePassword, ok := envByName(env, "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD")
		if !ok {
			t.Fatal("expected SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD to be present")
		}
		requireSecretRef(t, truststorePassword, inst.Name+jksSecretSuffix, "truststore_password")
	})

	t.Run("client auth env is absent when ClientAuth is false", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = false

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTH")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD")
	})
}

// TestBuildPodEnvNonDeprecatedProperties verifies that the operator emits the
// modern Confluent property spellings: ssl.client.authentication=REQUIRED for
// REST API mutual TLS and inter.instance.protocol=https for secure listeners,
// and never the deprecated ssl.client.auth /
// schema.registry.inter.instance.protocol names.
func TestBuildPodEnvNonDeprecatedProperties(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("secure listener emits inter.instance.protocol=https", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL", "https")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL")
	})

	t.Run("insecure listener emits no inter-instance property", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL")
	})

	t.Run("client auth emits ssl.client.authentication=REQUIRED", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION", "REQUIRED")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_CLIENT_AUTH")
	})
}

// TestBuildPodEnvKeystoreType verifies the REST API TLS keystore env vars for
// both keystore types: JKS (the default, including unset) keeps the current
// behavior (tls-keystore.jks, no ssl.keystore.type property), while PKCS12
// points at tls-keystore.p12 and emits SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE.
func TestBuildPodEnvKeystoreType(t *testing.T) {
	auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}

	t.Run("JKS default keeps the current keystore location and no type property", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.KeystoreType = ""

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", "/var/rest-api-tls/tls-keystore.jks")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE")
	})

	t.Run("explicit JKS behaves like the default", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.KeystoreType = "JKS"

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", "/var/rest-api-tls/tls-keystore.jks")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE")
	})

	t.Run("PKCS12 emits the p12 location and keystore type", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.KeystoreType = "PKCS12"

		env, _ := buildPodEnv(inst, "kafka:9092", inst.Spec.TLSSecretName, auth)

		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", "/var/rest-api-tls/tls-keystore.p12")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE", "PKCS12")
		keystorePassword, ok := envByName(env, "SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD")
		if !ok {
			t.Fatal("expected SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD to be present")
		}
		requireSecretRef(t, keystorePassword, inst.Spec.TLSSecretName, "keystore_password")
	})

	t.Run("keystore type is not emitted without securehttp", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = false
		inst.Spec.KeystoreType = "PKCS12"

		env, _ := buildPodEnv(inst, "kafka:9092", "", auth)

		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE")
	})
}

// TestComputeSpecHashKeystoreType verifies that spec.keystoretype participates
// in the spec hash so switching the keystore type triggers a deployment
// rollout.
func TestComputeSpecHashKeystoreType(t *testing.T) {
	t.Run("different KeystoreType produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.KeystoreType = "PKCS12"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different KeystoreType: both %q", hash1)
		}
	})

	t.Run("unset and explicit JKS produce the same hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst2 := newTestInstance()
		inst2.Spec.KeystoreType = "JKS"

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 != hash2 {
			t.Errorf("expected same hash for unset and explicit JKS: %q vs %q", hash1, hash2)
		}
	})
}

func TestBuildPodVolumesClientAuth(t *testing.T) {
	t.Run("client auth mounts JKS volume even for PLAINTEXT", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.ClientAuth = true
		auth := kafkaAuth{Mode: authPlaintext}

		volumes, mounts := buildPodVolumes(inst, "tls-secret", auth)
		v, ok := volumeByName(volumes, "tls")
		if !ok {
			t.Fatal("expected tls volume for client auth")
		}
		if v.Secret == nil || v.Secret.SecretName != inst.Name+jksSecretSuffix {
			t.Errorf("expected tls volume to reference %q", inst.Name+jksSecretSuffix)
		}
		if len(mounts) == 0 || mounts[0].MountPath != "/var/schemaregistry" {
			t.Errorf("expected /var/schemaregistry volume mount")
		}
	})
}

func TestComputeSpecHashClientAuth(t *testing.T) {
	t.Run("different ClientAuth produces different hash", func(t *testing.T) {
		inst1 := newTestInstance()
		inst1.Spec.SecureHTTP = true
		inst1.Spec.ClientAuth = false
		inst2 := newTestInstance()
		inst2.Spec.SecureHTTP = true
		inst2.Spec.ClientAuth = true

		hash1, _ := computeSpecHash(inst1)
		hash2, _ := computeSpecHash(inst2)
		if hash1 == hash2 {
			t.Errorf("expected different hashes for different ClientAuth: both %q", hash1)
		}
	})
}

// TestMapStrimziResourceToRequests covers the Kafka/KafkaUser watch mapping:
// CRs attached to the same Kafka cluster (strimzi.io/cluster label) are
// enqueued; KafkaUser events additionally require the name to match the CR.
func TestMapStrimziResourceToRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)

	srA := newTestInstance()
	srA.Name = "sr-a"
	srA.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}
	srB := newTestInstance()
	srB.Name = "sr-b"
	srB.Labels = map[string]string{"strimzi.io/cluster": "other-cluster"}
	srC := newTestInstance()
	srC.Name = "sr-c"
	srC.Namespace = "other-ns"
	srC.Labels = map[string]string{"strimzi.io/cluster": "kafka-cluster"}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(srA, srB, srC).Build()
	reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}
	ctx := context.Background()

	t.Run("Kafka event enqueues same-cluster CRs in the same namespace", func(t *testing.T) {
		kafkaObj := &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kafka-cluster",
				Namespace: "default",
				Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
			},
		}
		requests := reconciler.mapStrimziResourceToRequests(ctx, kafkaObj, false)
		if len(requests) != 1 {
			t.Fatalf("expected 1 request, got %v", requests)
		}
		if requests[0].Name != "sr-a" || requests[0].Namespace != "default" {
			t.Errorf("expected request for sr-a in default, got %v", requests[0])
		}
	})

	t.Run("KafkaUser event requires name match", func(t *testing.T) {
		matching := &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sr-a",
				Namespace: "default",
				Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
			},
		}
		requests := reconciler.mapStrimziResourceToRequests(ctx, matching, true)
		if len(requests) != 1 || requests[0].Name != "sr-a" {
			t.Fatalf("expected 1 request for sr-a, got %v", requests)
		}

		nonMatching := &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "unrelated-user",
				Namespace: "default",
				Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
			},
		}
		if requests := reconciler.mapStrimziResourceToRequests(ctx, nonMatching, true); len(requests) != 0 {
			t.Errorf("expected no requests for an unrelated KafkaUser, got %v", requests)
		}
	})

	t.Run("object without the cluster label enqueues nothing", func(t *testing.T) {
		unlabeled := &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: "default"},
		}
		if requests := reconciler.mapStrimziResourceToRequests(ctx, unlabeled, false); len(requests) != 0 {
			t.Errorf("expected no requests without the cluster label, got %v", requests)
		}
	})
}

// TestHandleTLSCertExpiry covers the TLS certificate expiry-based renewal and
// the periodic recheck scheduling:
//   - with the default threshold a freshly generated one-year certificate is
//     not renewed but a standing requeue interval is returned, so the expiry
//     keeps being checked even when no watch event ever fires (B1);
//   - a certificate inside the renewal window (threshold forced larger than
//     its validity, or an actually expired certificate) is renewed in place
//     and the deployment is rolled via the tlsVersion pod-template
//     annotation;
//   - a custom user-managed TLS secret is never touched and never forces a
//     requeue.
func TestHandleTLSCertExpiry(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	cp := certprocessor.NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "test-sr.default")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Labels = map[string]string{"strimzi.io/cluster": cluster}

	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCASuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	caKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCAKeySuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.key": []byte(ca.CAKeyPEM)},
	}
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  keystore,
			"keystore_password": []byte(password),
			"key_password":      []byte(password),
		},
	}
	jksSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name + jksSecretSuffix, Namespace: inst.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"truststore.jks": []byte("stub")},
	}
	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: inst.Name + deploySuffix, Namespace: inst.Namespace},
		Spec: apps.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "confluent-sr"}}},
			},
		},
	}

	t.Run("no renewal while the certificate is valid, standing recheck scheduled", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, caCertSecret, caKeySecret, tlsSecret.DeepCopy(), jksSecret.DeepCopy(), deployment.DeepCopy()).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, recheck, err := reconciler.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no immediate requeue for a valid certificate, got %v", result.RequeueAfter)
		}
		// B1: a valid certificate must still schedule the periodic recheck
		// so the renewal fires on time without any watch event.
		if recheck != tlsCertRecheckInterval {
			t.Errorf("expected the standing recheck interval %v, got %v", tlsCertRecheckInterval, recheck)
		}
	})

	t.Run("renewal window sooner than the standing interval shortens the recheck", func(t *testing.T) {
		// A certificate expiring in 40 days enters the default 30-day
		// renewal window in about 10 days, which is sooner than the
		// (temporarily enlarged) standing interval; the returned recheck
		// must be the shorter value so renewal is observed before the
		// window opens.
		originalInterval := tlsCertRecheckInterval
		tlsCertRecheckInterval = 100 * 24 * time.Hour
		defer func() { tlsCertRecheckInterval = originalInterval }()

		cp := certprocessor.NewCertProcessor(logr.Logger{})
		ca, err := testutil.GenerateTestCA()
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		uc, err := testutil.GenerateTestUserCertWithValidity(ca, "shortpw123", time.Now().AddDate(0, -1, 0), time.Now().AddDate(0, 0, 40))
		if err != nil {
			t.Fatalf("failed to generate user cert: %v", err)
		}
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "shortpw123")
		if err != nil {
			t.Fatalf("failed to build keystore: %v", err)
		}
		freshTLSSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls-keystore.jks":  keystore,
				"keystore_password": []byte(password),
				"key_password":      []byte(password),
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, caCertSecret, caKeySecret, freshTLSSecret, jksSecret.DeepCopy(), deployment.DeepCopy()).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, recheck, err := reconciler.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no immediate requeue (certificate outside the renewal window), got %v", result.RequeueAfter)
		}
		if recheck >= tlsCertRecheckInterval {
			t.Errorf("expected a recheck shortened below the standing interval %v, got %v", tlsCertRecheckInterval, recheck)
		}
		// 40 days lifetime minus the 30-day renewal threshold is about 10 days.
		if recheck < 9*24*time.Hour || recheck > 10*24*time.Hour+time.Minute {
			t.Errorf("expected a recheck of about 10 days, got %v", recheck)
		}
	})

	t.Run("renews and rolls when the certificate is close to expiry", func(t *testing.T) {
		originalThreshold := tlsCertRenewalThreshold
		tlsCertRenewalThreshold = 400 * 24 * time.Hour
		defer func() { tlsCertRenewalThreshold = originalThreshold }()

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, caCertSecret, caKeySecret, tlsSecret.DeepCopy(), jksSecret.DeepCopy(), deployment.DeepCopy()).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, recheck, err := reconciler.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after renewal")
		}
		// The immediate requeue re-runs the check; no standing interval is
		// needed on top of it.
		if recheck != 0 {
			t.Errorf("expected no standing recheck alongside the immediate requeue, got %v", recheck)
		}

		renewed := &corev1.Secret{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, renewed); err != nil {
			t.Fatalf("failed to get renewed TLS secret: %v", err)
		}
		if string(renewed.Data["keystore_password"]) == password {
			t.Error("expected the renewed secret to have fresh keystore material")
		}

		dep := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}, dep); err != nil {
			t.Fatalf("failed to get deployment after renewal: %v", err)
		}
		if dep.Spec.Template.Annotations[tlsVersionKey] == "" {
			t.Error("expected the tlsVersion pod-template annotation to trigger a rollout")
		}
	})

	t.Run("renews without a -jks secret (PLAINTEXT + securehttp)", func(t *testing.T) {
		originalThreshold := tlsCertRenewalThreshold
		tlsCertRenewalThreshold = 400 * 24 * time.Hour
		defer func() { tlsCertRenewalThreshold = originalThreshold }()

		// PLAINTEXT without clientauth never produces a -jks secret. The
		// renewal must still succeed and roll the deployment instead of
		// error-looping once the certificate enters the renewal window.
		plaintextInst := inst.DeepCopy()
		plaintextInst.Spec.SecurityProtocol = "PLAINTEXT"
		plaintextInst.Spec.Listener = "plaintext"

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(plaintextInst, caCertSecret, caKeySecret, tlsSecret.DeepCopy(), deployment.DeepCopy()).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, _, err := reconciler.handleTLSCertExpiry(context.Background(), plaintextInst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after renewal")
		}

		dep := &apps.Deployment{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}, dep); err != nil {
			t.Fatalf("failed to get deployment after renewal: %v", err)
		}
		if dep.Spec.Template.Annotations[tlsVersionKey] == "" {
			t.Error("expected the tlsVersion pod-template annotation to trigger a rollout")
		}
		if got := dep.Spec.Template.Annotations[keyPrefix+"/jksVersion"]; got != "" {
			t.Errorf("expected no jksVersion annotation when no -jks secret exists, got %q", got)
		}
	})

	t.Run("custom TLSSecretName is never touched", func(t *testing.T) {
		inst := newTestInstance()
		inst.Spec.SecureHTTP = true
		inst.Spec.TLSSecretName = "user-managed"
		inst.Labels = map[string]string{"strimzi.io/cluster": cluster}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, recheck, err := reconciler.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue for a custom TLS secret, got %v", result.RequeueAfter)
		}
		// B1: a user-managed TLS secret is never expiry-checked by the
		// operator, so no periodic requeue may be forced.
		if recheck != 0 {
			t.Errorf("expected no periodic recheck for a custom TLS secret, got %v", recheck)
		}
	})

	t.Run("already-expired certificate is renewed with the default threshold", func(t *testing.T) {
		// B1 failure path: with the default 30-day threshold, a certificate
		// whose NotAfter is already in the past must enter the renewal
		// branch immediately (time.Until(notAfter) <= threshold), instead
		// of being skipped until some watch event fires.
		cp := certprocessor.NewCertProcessor(logr.Logger{})
		ca, err := testutil.GenerateTestCA()
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		uc, err := testutil.GenerateTestUserCertWithValidity(ca, "expired123", time.Now().AddDate(0, -2, 0), time.Now().AddDate(0, 0, -1))
		if err != nil {
			t.Fatalf("failed to generate expired user cert: %v", err)
		}
		keystore, password, err := cp.ConvertPEMToJKS(context.Background(), uc.UserCertPEM, uc.UserKeyPEM, "expired123")
		if err != nil {
			t.Fatalf("failed to build keystore from expired cert: %v", err)
		}
		expiredTLSSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace},
			Type:       corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls-keystore.jks":  keystore,
				"keystore_password": []byte(password),
				"key_password":      []byte(password),
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, caCertSecret, caKeySecret, expiredTLSSecret, jksSecret.DeepCopy(), deployment.DeepCopy()).Build()
		reconciler := &StrimziSchemaRegistryReconciler{Client: client, Scheme: scheme}

		result, recheck, err := reconciler.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after renewing the expired certificate")
		}
		if recheck != 0 {
			t.Errorf("expected no standing recheck alongside the immediate requeue, got %v", recheck)
		}

		renewed := &corev1.Secret{}
		if err := client.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, renewed); err != nil {
			t.Fatalf("failed to get renewed TLS secret: %v", err)
		}
		if string(renewed.Data["keystore_password"]) == password {
			t.Error("expected the renewed secret to replace the expired keystore material")
		}
		newNotAfter, err := cp.KeystoreCertExpiry(context.Background(), renewed.Data["tls-keystore.jks"], string(renewed.Data["keystore_password"]))
		if err != nil {
			t.Fatalf("failed to read the renewed keystore expiry: %v", err)
		}
		if !newNotAfter.After(time.Now()) {
			t.Errorf("expected the renewed certificate to be valid, NotAfter=%v", newNotAfter)
		}
	})
}

// TestCertRecheckInterval is a table-driven test for the periodic recheck
// scheduling helper (B1): the standing interval applies while the renewal
// window is further away than the interval; the recheck shortens to the time
// until the window opens when that is sooner; expired or in-window
// certificates fall through to the renewal path so the helper is not used
// there, but it must still never return a non-positive interval.
func TestCertRecheckInterval(t *testing.T) {
	originalThreshold := tlsCertRenewalThreshold
	originalInterval := tlsCertRecheckInterval
	tlsCertRenewalThreshold = 30 * 24 * time.Hour
	// A long standing interval lets the shortening branch be exercised: the
	// recheck is clamped to the time until the renewal window opens when
	// that is sooner than the interval.
	tlsCertRecheckInterval = 100 * 24 * time.Hour
	defer func() {
		tlsCertRenewalThreshold = originalThreshold
		tlsCertRecheckInterval = originalInterval
	}()

	tests := []struct {
		name      string
		notAfter  time.Time
		wantExact bool // interval equals the standing value exactly
		min       time.Duration
		max       time.Duration
	}{
		{
			name:      "far-from-expiry certificate uses the standing interval",
			notAfter:  time.Now().AddDate(0, 6, 0),
			wantExact: true,
		},
		{
			name:     "certificate entering the window in about 10 days shortens the recheck",
			notAfter: time.Now().AddDate(0, 0, 40),
			min:      9 * 24 * time.Hour,
			max:      10*24*time.Hour + time.Minute,
		},
		{
			name:      "already-expired certificate still yields a positive interval",
			notAfter:  time.Now().AddDate(0, 0, -5),
			wantExact: true,
		},
		{
			name:      "inside the renewal window yields the standing interval",
			notAfter:  time.Now().AddDate(0, 0, 10),
			wantExact: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := certRecheckInterval(tt.notAfter)
			if got <= 0 {
				t.Fatalf("recheck interval must be positive, got %v", got)
			}
			if tt.wantExact {
				if got != tlsCertRecheckInterval {
					t.Errorf("expected the standing interval %v, got %v", tlsCertRecheckInterval, got)
				}
				return
			}
			if got < tt.min || got > tt.max {
				t.Errorf("expected a recheck in [%v, %v], got %v", tt.min, tt.max, got)
			}
		})
	}
}

// TestCreateMTLSSecretFieldValidation verifies the explicit presence checks
// for the KafkaUser secret fields consumed by the mTLS keystore pipeline:
// each missing key produces a clear error naming it, a pre-built user.p12
// makes the PEM fields unnecessary (they are only dereferenced on the PEM
// path), and a complete secret proceeds to build the JKS secret.
func TestCreateMTLSSecretFieldValidation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}

	clusterCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-cluster-ca-cert", Namespace: "default"},
		Data:       map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}

	fullPEMData := map[string][]byte{
		"ca.crt":        []byte(ca.CACertPEM),
		"user.crt":      []byte(uc.UserCertPEM),
		"user.key":      []byte(uc.UserKeyPEM),
		"user.password": []byte("test1234"),
	}

	tests := []struct {
		name       string
		userData   map[string][]byte
		wantErrKey string // empty means no validation error is expected
	}{
		{name: "missing ca.crt", userData: dropKey(fullPEMData, "ca.crt"), wantErrKey: "ca.crt"},
		{name: "missing user.crt", userData: dropKey(fullPEMData, "user.crt"), wantErrKey: "user.crt"},
		{name: "missing user.key", userData: dropKey(fullPEMData, "user.key"), wantErrKey: "user.key"},
		{name: "missing user.password", userData: dropKey(fullPEMData, "user.password"), wantErrKey: "user.password"},
		{name: "all PEM fields present proceeds", userData: fullPEMData, wantErrKey: ""},
		{name: "user.p12 present without PEM fields proceeds", userData: map[string][]byte{
			"user.p12":      uc.PKCS12Data,
			"user.password": []byte("test1234"),
		}, wantErrKey: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "test-sr", Namespace: "default"},
				Data:       tt.userData,
			}
			r := &StrimziSchemaRegistryReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterCASecret, userSecret).Build(),
				Scheme: scheme,
			}
			inst := newTestInstance()

			secret, created, err := r.createMTLSSecret(inst, context.Background(), logr.Logger{}, "test-cluster", clusterCASecret, userSecret)
			if tt.wantErrKey != "" {
				if err == nil {
					t.Fatalf("expected an error naming %q, got nil", tt.wantErrKey)
				}
				if !strings.Contains(err.Error(), tt.wantErrKey) {
					t.Errorf("expected the error to name the missing key %q, got: %v", tt.wantErrKey, err)
				}
				if secret != nil || created {
					t.Errorf("expected nil secret and created=false on validation failure, got secret!=nil=%v created=%v", secret != nil, created)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if secret == nil {
				t.Fatal("expected a JKS secret to be produced")
			}
			if !created {
				t.Error("expected created=true for a freshly built JKS secret")
			}
			for _, key := range []string{"truststore.jks", "keystore.jks", "truststore_password", "keystore_password"} {
				if len(secret.Data[key]) == 0 {
					t.Errorf("expected non-empty %q in the produced secret", key)
				}
			}
		})
	}
}

// dropKey returns a copy of the map without the given key.
func dropKey(m map[string][]byte, key string) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}
