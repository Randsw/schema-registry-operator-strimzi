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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
)

// T-10: deleting a StrimziSchemaRegistry must not depend on a live operator.
// The no-op finalizer was removed in H-2, so the CR the operator reconciled
// carries no finalizers and the API server deletes it immediately — no
// reconciliation, no operator restart required. The companion T-9 event
// contract for the SASL credential roll is locked by
// TestRecordedEventsCarryNonEmptyAction in events_test.go.
var _ = Describe("CR deletion without a finalizer (T-10)", func() {
	const (
		namespacePrefix = "test-deletion-no-finalizer"
		registryName    = "deletion-sr"
	)

	ctx := context.Background()
	specCounter := 0

	var (
		namespace   *corev1.Namespace
		registryKey types.NamespacedName
	)

	BeforeEach(func() {
		specCounter++
		nsName := fmt.Sprintf("%s-%d", namespacePrefix, specCounter)
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		registryKey = types.NamespacedName{Name: registryName, Namespace: nsName}

		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Creating a Kafka CR with a PLAINTEXT listener and a populated status")
		Expect(k8sClient.Create(ctx, &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: nsName},
			Spec: &kafka.KafkaSpec{
				Kafka: &kafka.KafkaClusterSpec{
					Listeners: []kafka.GenericKafkaListener{{
						Name: "plain",
						Port: 9092,
						Tls:  false,
						Type: kafka.INTERNAL_KAFKALISTENERTYPE,
					}},
					Version: "4.1.0",
				},
			},
		})).To(Succeed())
		created := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: nsName}, created)).To(Succeed())
		created.Status = &kafka.KafkaStatus{
			ClusterId: "deletion-no-finalizer-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plain",
				BootstrapServers: "kafka-cluster-kafka-bootstrap." + nsName + ".svc:9092",
			}},
		}
		Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())

		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: nsName,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: strimziregistryoperatorv1beta1.StrimziSchemaRegistrySpec{
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("finalizes the CR without any operator running", func() {
		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		By("Reconciling once so the CR has been through the operator")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())

		live := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, live)).To(Succeed())
		Expect(live.Finalizers).To(BeEmpty(),
			"the operator must not add a finalizer: owner references already handle cleanup (H-2)")

		By("Deleting the CR with no operator running")
		// No manager is started in this suite: the delete is served entirely
		// by the API server, exactly like a deleted operator pod.
		Expect(k8sClient.Delete(ctx, live)).To(Succeed())

		By("Asserting the CR is gone instead of stuck Terminating")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, registryKey, &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{})
			return apierrors.IsNotFound(err)
		}, 30, 1).Should(BeTrue(), "the CR must be removed without a live operator")
	})
})
