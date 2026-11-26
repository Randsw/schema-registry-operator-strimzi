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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
)

// C-4: Reconcile must be idempotent in steady state — running it again on a
// converged CR must not write to any owned object. A write on every pass would
// bump resourceVersions, re-trigger the For() watch, and hot-loop the
// operator. The spec uses PLAINTEXT so the owned-object set is just the
// Deployment, the Service and the CR status.
var _ = Describe("Reconcile idempotency in steady state (C-4)", func() {
	const registryName = "idempotency-sr"

	ctx := context.Background()
	specCounter := 0

	var (
		namespace   *corev1.Namespace
		registryKey types.NamespacedName
	)

	BeforeEach(func() {
		specCounter++
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-idempotency-%d", specCounter),
		}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		registryKey = types.NamespacedName{Name: registryName, Namespace: namespace.Name}
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

	It("performs no writes when reconciling an already settled CR", func() {
		By("creating the Kafka CR with a plaintext listener and its status")
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
		currentKafka := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: namespace.Name}, currentKafka)).To(Succeed())
		currentKafka.Status = &kafka.KafkaStatus{
			ClusterId: "idempotency-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plain",
				BootstrapServers: "kafka-cluster-kafka-bootstrap." + namespace.Name + ".svc:9092",
			}},
		}
		Expect(k8sClient.Status().Update(ctx, currentKafka)).To(Succeed())

		By("creating the PLAINTEXT registry CR")
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

		By("reconciling until a pass performs no work")
		Eventually(func(g Gomega) time.Duration {
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName + deploySuffix,
				Namespace: namespace.Name,
			}, &appsv1.Deployment{})).To(Succeed())
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName,
				Namespace: namespace.Name,
			}, &corev1.Service{})).To(Succeed())
			instance := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
			g.Expect(k8sClient.Get(ctx, registryKey, instance)).To(Succeed())
			g.Expect(instance.Status.Status).NotTo(BeEmpty())
			return result.RequeueAfter
		}, 30*time.Second, 250*time.Millisecond).Should(BeZero())

		By("snapshotting the resourceVersion of the CR and every owned object")
		instance := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, instance)).To(Succeed())
		deployment := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      registryName + deploySuffix,
			Namespace: namespace.Name,
		}, deployment)).To(Succeed())
		service := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      registryName,
			Namespace: namespace.Name,
		}, service)).To(Succeed())

		crVersion := instance.ResourceVersion
		deploymentVersion := deployment.ResourceVersion
		serviceVersion := service.ResourceVersion

		By("reconciling twice more")
		for i := 0; i < 2; i++ {
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero(),
				"a settled CR must not schedule a requeue-with-write on pass %d", i+1)
		}

		By("asserting zero writes across the CR and every owned object")
		Expect(k8sClient.Get(ctx, registryKey, instance)).To(Succeed())
		Expect(instance.ResourceVersion).To(Equal(crVersion),
			"reconciling a settled CR must not write its status")
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      registryName + deploySuffix,
			Namespace: namespace.Name,
		}, deployment)).To(Succeed())
		Expect(deployment.ResourceVersion).To(Equal(deploymentVersion),
			"reconciling a settled CR must not update the Deployment")
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      registryName,
			Namespace: namespace.Name,
		}, service)).To(Succeed())
		Expect(service.ResourceVersion).To(Equal(serviceVersion),
			"reconciling a settled CR must not update the Service")
	})
})
