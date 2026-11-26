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
	"sort"
	"testing"

	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
)

// sortedRequestNames returns the requested names as a deterministically
// ordered slice so assertions never depend on List/watch ordering.
func sortedRequestNames(requests []reconcile.Request) []types.NamespacedName {
	names := make([]types.NamespacedName, 0, len(requests))
	for _, req := range requests {
		names = append(names, req.NamespacedName)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i].Namespace != names[j].Namespace {
			return names[i].Namespace < names[j].Namespace
		}
		return names[i].Name < names[j].Name
	})
	return names
}

// T-1: the Kafka CR watch must map by the Kafka CR's name — Strimzi never
// sets the strimzi.io/cluster label on the Kafka CR itself, only on the
// resources it manages and (mandatorily) on KafkaUser CRs. The fixtures below
// therefore deliberately use an unlabeled, real-shaped Kafka CR. If the mapper
// regresses to the old label lookup, every Kafka subtest fails.
func TestMapKafkaToRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)

	matching := newTestInstance()
	matching.Name = "sr-matching"
	matching.Namespace = "default"
	matching.Labels = map[string]string{strimziClusterLabel: "kafka"}

	otherCluster := newTestInstance()
	otherCluster.Name = "sr-other-cluster"
	otherCluster.Namespace = "default"
	otherCluster.Labels = map[string]string{strimziClusterLabel: "other"}

	otherNamespace := newTestInstance()
	otherNamespace.Name = "sr-other-namespace"
	otherNamespace.Namespace = "other-ns"
	otherNamespace.Labels = map[string]string{strimziClusterLabel: "kafka"}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(matching, otherCluster, otherNamespace).Build()
	reconciler := &StrimziSchemaRegistryReconciler{Client: fakeClient, Scheme: scheme}
	ctx := context.Background()

	// newKafkaCR builds a Kafka object the way Strimzi actually presents it:
	// no strimzi.io/cluster label, cluster identity in the name, and a
	// populated status.listeners entry as emitted by the Kafka operator.
	newKafkaCR := func(name, namespace, bootstrap string) *kafka.Kafka {
		return &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Status: &kafka.KafkaStatus{
				Listeners: []kafka.ListenerStatus{
					{
						Name:             "tls",
						BootstrapServers: bootstrap,
					},
				},
			},
		}
	}

	t.Run("unlabeled Kafka CR enqueues CRs labelled with its name", func(t *testing.T) {
		kafkaObj := newKafkaCR("kafka", "default", "kafka-kafka-bootstrap.default.svc:9093")
		if _, ok := kafkaObj.Labels[strimziClusterLabel]; ok {
			t.Fatalf("fixture must not carry the %q label", strimziClusterLabel)
		}

		got := sortedRequestNames(reconciler.mapKafkaToRequests(ctx, kafkaObj))
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 request, got %v", got)
		}
		if got[0].Name != "sr-matching" || got[0].Namespace != "default" {
			t.Errorf("expected sr-matching in default, got %v", got[0])
		}
	})

	t.Run("unlabeled Kafka CR for an unknown cluster enqueues nothing", func(t *testing.T) {
		kafkaObj := newKafkaCR("unrelated-kafka", "default", "unrelated-kafka-bootstrap.default.svc:9093")
		if got := reconciler.mapKafkaToRequests(ctx, kafkaObj); len(got) != 0 {
			t.Errorf("expected no requests for a Kafka CR no CR references, got %v", got)
		}
	})

	t.Run("Kafka CR in another namespace does not reach CRs of the same cluster", func(t *testing.T) {
		kafkaObj := newKafkaCR("kafka", "other-ns", "kafka-kafka-bootstrap.other-ns.svc:9093")
		got := sortedRequestNames(reconciler.mapKafkaToRequests(ctx, kafkaObj))
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 namespace-scoped request, got %v", got)
		}
		if got[0].Name != "sr-other-namespace" || got[0].Namespace != "other-ns" {
			t.Errorf("expected sr-other-namespace in other-ns, got %v", got[0])
		}
	})

	t.Run("legacy KafkaUser label plus name path still maps", func(t *testing.T) {
		user := &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sr-matching",
				Namespace: "default",
				Labels:    map[string]string{strimziClusterLabel: "kafka"},
			},
		}
		got := sortedRequestNames(reconciler.mapStrimziResourceToRequests(ctx, user, true))
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 request for the labelled KafkaUser, got %v", got)
		}
		if got[0].Name != "sr-matching" || got[0].Namespace != "default" {
			t.Errorf("expected sr-matching in default, got %v", got[0])
		}

		unlabeled := &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{Name: "sr-matching", Namespace: "default"},
		}
		if got := reconciler.mapStrimziResourceToRequests(ctx, unlabeled, true); len(got) != 0 {
			t.Errorf("expected no requests for an unlabelled KafkaUser, got %v", got)
		}
	})
}

// P3.7: SetupWithManager installs the Kafka watch mapper through
// kafkaWatchMapper(), whose value can be supplied as a constructor argument
// (KafkaWatchMapper). A nil field must select the production name-based mapper
// so the C-1 behavior is unchanged; a set field must be used verbatim so the
// watch pipeline can be exercised without reaching into package internals.
func TestKafkaWatchMapperInjection(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)

	matching := newTestInstance()
	matching.Name = "sr-injected"
	matching.Namespace = "default"
	matching.Labels = map[string]string{strimziClusterLabel: "kafka"}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(matching).Build()
	ctx := context.Background()
	obj := &kafka.Kafka{ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: "default"}}

	t.Run("nil mapper falls back to the production mapper", func(t *testing.T) {
		reconciler := &StrimziSchemaRegistryReconciler{Client: fakeClient, Scheme: scheme}
		got := sortedRequestNames(reconciler.kafkaWatchMapper()(ctx, obj))
		if len(got) != 1 || got[0].Name != "sr-injected" || got[0].Namespace != "default" {
			t.Errorf("expected the default mapper to enqueue sr-injected/default, got %v", got)
		}
	})

	t.Run("injected mapper is used verbatim", func(t *testing.T) {
		calls := 0
		want := reconcile.Request{NamespacedName: types.NamespacedName{Name: "stub", Namespace: "stub-ns"}}
		reconciler := &StrimziSchemaRegistryReconciler{
			Client: fakeClient,
			Scheme: scheme,
			KafkaWatchMapper: func(context.Context, client.Object) []reconcile.Request {
				calls++
				return []reconcile.Request{want}
			},
		}

		got := reconciler.kafkaWatchMapper()(ctx, obj)
		if calls != 1 {
			t.Errorf("expected the injected mapper to be called once, got %d", calls)
		}
		if len(got) != 1 || got[0].NamespacedName != want.NamespacedName {
			t.Errorf("expected the injected mapper result %v, got %v", want, got)
		}
	})
}
