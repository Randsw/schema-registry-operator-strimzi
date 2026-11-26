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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
)

// C-5: a SASL registry whose KafkaUser SCRAM credentials secret does not exist
// yet must not produce a Deployment whose pods cannot start. Reconcile must
// surface the missing secret on the CR status and create nothing, then
// converge once the secret appears. The secret is named after the registry,
// matching what handleSASLSecretRotation and the Deployment env reference.
var _ = Describe("Reconcile with a missing KafkaUser SCRAM credential secret (C-5)", func() {
	const registryName = "scram-secret-sr"

	ctx := context.Background()
	specCounter := 0

	var (
		namespace   *corev1.Namespace
		registryKey types.NamespacedName
	)

	BeforeEach(func() {
		specCounter++
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-scram-secret-%d", specCounter),
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

	It("surfaces the missing credential secret, creates nothing, then converges", func() {
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
			ClusterId: "scram-secret-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plain",
				BootstrapServers: "kafka-cluster-kafka-bootstrap." + namespace.Name + ".svc:9092",
			}},
		}
		Expect(k8sClient.Status().Update(ctx, currentKafka)).To(Succeed())

		By("creating the KafkaUser with scram-sha-512 auth but not its credentials secret")
		Expect(k8sClient.Create(ctx, &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: namespace.Name,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: &kafka.KafkaUserSpec{
				Authentication: &kafka.KafkaUserAuthentication{
					Type: kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE,
				},
			},
		})).To(Succeed())

		By("creating the SASL_PLAINTEXT registry CR")
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
				SecurityProtocol:   "SASL_PLAINTEXT",
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())

		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		By("reconciling while the credentials secret is missing")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).To(HaveOccurred())

		By("asserting the failure is surfaced on the CR status")
		instance := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, instance)).To(Succeed())
		Expect(instance.Status.Status).To(Equal("ReconcileError"))
		ready := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil(), "expected a Ready condition on the CR")
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("ReconcileError"))
		Expect(ready.Message).To(ContainSubstring(registryName))

		By("asserting no half-configured Deployment or Service was rolled out")
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: registryName + deploySuffix, Namespace: namespace.Name},
			&appsv1.Deployment{}))).To(BeTrue(), "no Deployment may be created without the SCRAM credentials")
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: registryName, Namespace: namespace.Name},
			&corev1.Service{}))).To(BeTrue(), "no Service may be created without the SCRAM credentials")

		By("creating the SCRAM credentials secret the operator reads")
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: registryName, Namespace: namespace.Name},
			Data:       map[string][]byte{"sasl.jaas.config": []byte("jaas")},
		})).To(Succeed())

		By("reconciling until the Deployment carries the SASL credential reference")
		Eventually(func(g Gomega) string {
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      registryName + deploySuffix,
				Namespace: namespace.Name,
			}, dep)).To(Succeed())
			for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
				if env.Name != "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG" {
					continue
				}
				g.Expect(env.ValueFrom).NotTo(BeNil())
				g.Expect(env.ValueFrom.SecretKeyRef).NotTo(BeNil())
				return env.ValueFrom.SecretKeyRef.Name
			}
			return ""
		}, 30*time.Second, 250*time.Millisecond).Should(Equal(registryName))

		By("asserting the status leaves the error state")
		Eventually(func(g Gomega) string {
			_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())

			current := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
			g.Expect(k8sClient.Get(ctx, registryKey, current)).To(Succeed())
			return current.Status.Status
		}, 30*time.Second, 250*time.Millisecond).ShouldNot(Equal("ReconcileError"))
	})
})
