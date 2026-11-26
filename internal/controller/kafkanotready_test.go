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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
)

// C-2: a Strimzi Kafka cluster that is not ready yet (its CR carries no
// status.listeners) must make Reconcile wait gracefully: surface an actionable
// error on the CR, create no partial resources, and converge on its own once
// the listener appears. The getKafkaBootstrapServers error strings are
// unit-asserted; this spec proves the full Reconcile behavior around them.
var _ = Describe("Reconcile with a Kafka cluster that is not ready (C-2)", func() {
	const registryName = "kafka-not-ready-sr"

	ctx := context.Background()
	specCounter := 0

	var (
		namespace   *corev1.Namespace
		registryKey types.NamespacedName
		kafkaKey    types.NamespacedName
	)

	BeforeEach(func() {
		specCounter++
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-kafka-not-ready-%d", specCounter),
		}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		registryKey = types.NamespacedName{Name: registryName, Namespace: namespace.Name}
		kafkaKey = types.NamespacedName{Name: "kafka-cluster", Namespace: namespace.Name}
		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
	})

	AfterEach(func() {
		found := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, registryKey, found); err == nil {
			if len(found.Finalizers) > 0 {
				found.Finalizers = []string{}
				_ = k8sClient.Update(ctx, found)
			}
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("waits for status.listeners, creates no partial resources, then converges", func() {
		By("creating the Kafka CR without status listeners")
		Expect(k8sClient.Create(ctx, &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
			Spec: &kafka.KafkaSpec{Kafka: &kafka.KafkaClusterSpec{
				Listeners: []kafka.GenericKafkaListener{{
					Name: "plain",
					Port: 9092,
					Tls:  false,
					Type: kafka.INTERNAL_KAFKALISTENERTYPE,
				}},
				Version: "4.1.0",
			}},
		})).To(Succeed())

		By("creating the registry CR pointing at the plain listener")
		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: namespace.Name,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: strimziregistryoperatorv1beta1.StrimziSchemaRegistrySpec{
				SecureHTTP:         false,
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())

		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		By("reconciling while the Kafka CR exposes no listeners")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no status.listeners"))

		By("asserting the failure is surfaced on the CR status")
		instance := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, instance)).To(Succeed())
		Expect(instance.Status.Status).To(Equal("ReconcileError"))
		ready := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil(), "expected a Ready condition on the CR")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("ReconcileError"))
		Expect(ready.Message).To(ContainSubstring("no status.listeners"))

		By("asserting no partial resources were created")
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: registryName + deploySuffix, Namespace: namespace.Name},
			&appsv1.Deployment{}))).To(BeTrue(), "no Deployment may be created before the listener exists")
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: registryName, Namespace: namespace.Name},
			&corev1.Service{}))).To(BeTrue(), "no Service may be created before the listener exists")
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: registryName + networkPolicySuffix, Namespace: namespace.Name},
			&networkingv1.NetworkPolicy{}))).To(BeTrue(), "no NetworkPolicy may be created before the listener exists")

		By("exposing the matching listener on the Kafka CR status")
		currentKafka := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, kafkaKey, currentKafka)).To(Succeed())
		bootstrap := "kafka-cluster-kafka-bootstrap." + namespace.Name + ".svc:9092"
		currentKafka.Status = &kafka.KafkaStatus{
			ClusterId: "not-ready-cluster",
			Listeners: []kafka.ListenerStatus{{Name: "plain", BootstrapServers: bootstrap}},
		}
		Expect(k8sClient.Status().Update(ctx, currentKafka)).To(Succeed())

		By("reconciling until the Deployment carries the listener bootstrap address")
		Eventually(func(g Gomega) string {
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName + deploySuffix,
				Namespace: namespace.Name,
			}, dep)).To(Succeed())
			env, ok := envByName(dep.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS")
			g.Expect(ok).To(BeTrue(), "the bootstrap env var must be wired on the container")
			return env.Value
		}, 30*time.Second, 250*time.Millisecond).Should(Equal(bootstrap))

		By("asserting the status leaves the error state")
		// Both creations and the status write happen on separate passes, so
		// keep driving Reconcile while polling the CR status.
		Eventually(func(g Gomega) string {
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())

			current := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
			g.Expect(k8sClient.Get(ctx, registryKey, current)).To(Succeed())
			return current.Status.Status
		}, 30*time.Second, 250*time.Millisecond).ShouldNot(Equal("ReconcileError"))
	})
})
