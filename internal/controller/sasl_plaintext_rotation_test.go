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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// T-6: SASL_PLAINTEXT with REST API mutual TLS. This is the only auth mode
// where the cluster-CA truststore is built by the client-auth path rather than
// the Kafka TLS path, so the rotation logic differs from SASL_SSL. The unit
// suite covers the handler; this spec proves the composed reconcile against a
// real API server: a cluster CA rotation rebuilds the truststore and rolls the
// Deployment via the jksVersion marker exactly once.
var _ = Describe("SASL_PLAINTEXT client-auth CA rotation (T-6)", func() {
	const (
		namespacePrefix = "test-sasl-plaintext-rotation"
		registryName    = "sasl-plaintext-sr"
	)

	ctx := context.Background()
	specCounter := 0

	var (
		namespace     *corev1.Namespace
		registryKey   types.NamespacedName
		deploymentKey types.NamespacedName
		jksKey        types.NamespacedName
		caCertKey     types.NamespacedName
		caKeyKey      types.NamespacedName
		clusterCA     *testutil.TestCA
	)

	BeforeEach(func() {
		specCounter++
		nsName := fmt.Sprintf("%s-%d", namespacePrefix, specCounter)
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		registryKey = types.NamespacedName{Name: registryName, Namespace: nsName}
		deploymentKey = types.NamespacedName{Name: registryName + "-deploy", Namespace: nsName}
		jksKey = types.NamespacedName{Name: registryName + jksSecretSuffix, Namespace: nsName}
		caCertKey = types.NamespacedName{Name: "kafka-cluster" + clusterCASuffix, Namespace: nsName}
		caKeyKey = types.NamespacedName{Name: "kafka-cluster" + clusterCAKeySuffix, Namespace: nsName}

		var err error
		clusterCA, err = testutil.GenerateClusterCACert("T6-SASL-PLAINTEXT")
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Creating the Kafka CR with a SCRAM SASL_PLAINTEXT listener")
		cluster := &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: nsName},
			Spec: &kafka.KafkaSpec{
				Kafka: &kafka.KafkaClusterSpec{
					Listeners: []kafka.GenericKafkaListener{{
						Name: "plain",
						Port: 9092,
						Tls:  false,
						Type: kafka.INTERNAL_KAFKALISTENERTYPE,
						Authentication: &kafka.KafkaListenerAuthentication{
							Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE,
						},
					}},
					Version: "4.1.0",
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		created := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: nsName}, created)).To(Succeed())
		created.Status = &kafka.KafkaStatus{
			ClusterId: "sasl-plaintext-rotation-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plain",
				BootstrapServers: "kafka-cluster-kafka-bootstrap." + nsName + ".svc:9092",
			}},
		}
		Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())

		By("Creating the SCRAM KafkaUser and its credential secret")
		Expect(k8sClient.Create(ctx, &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: nsName,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: &kafka.KafkaUserSpec{
				Authentication: &kafka.KafkaUserAuthentication{
					Type: kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE,
				},
			},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: registryName, Namespace: nsName},
			Data: map[string][]byte{
				"password":         []byte("scram-password"),
				"sasl.jaas.config": []byte(`org.apache.kafka.common.security.scram.ScramLoginModule required username="sasl-plaintext-sr" password="scram-password";`),
			},
		})).To(Succeed())

		By("Creating cluster CA cert and key secrets")
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: caCertKey.Name, Namespace: nsName},
			Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: caKeyKey.Name, Namespace: nsName},
			Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
		})).To(Succeed())

		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		By("Creating the SASL_PLAINTEXT StrimziSchemaRegistry with client auth enabled")
		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: nsName,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: strimziregistryoperatorv1beta1.StrimziSchemaRegistrySpec{
				SecureHTTP:         true,
				ClientAuth:         true,
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "SASL_PLAINTEXT",
				Replicas:           ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		found := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, registryKey, found); err == nil {
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("rebuilds the truststore and rolls the Deployment once when the cluster CA rotates", func() {
		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		By("First reconcile creates the Deployment, the client-auth truststore and the -tls secret")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())

		deployed := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, deployed)).To(Succeed())
		oldJKSVersion := deployed.Spec.Template.Annotations[jksVersionKey]
		Expect(oldJKSVersion).NotTo(BeEmpty(), "SASL_PLAINTEXT + clientauth must record the client-auth truststore version")

		oldJKS := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, jksKey, oldJKS)).To(Succeed())
		Expect(oldJKS.Data).To(HaveKey("truststore.jks"))
		Expect(oldJKS.Annotations[CAVersionKey]).NotTo(BeEmpty())

		By("Rotating the cluster CA (cert and key must stay a matching pair)")
		newClusterCA, err := testutil.GenerateClusterCACert("T6-SASL-PLAINTEXT-V2")
		Expect(err).NotTo(HaveOccurred())
		caCert := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, caCertKey, caCert)).To(Succeed())
		caCert.Data["ca.crt"] = []byte(newClusterCA.CACertPEM)
		Expect(k8sClient.Update(ctx, caCert)).To(Succeed())
		caKey := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, caKeyKey, caKey)).To(Succeed())
		caKey.Data["ca.key"] = []byte(newClusterCA.CAKeyPEM)
		Expect(k8sClient.Update(ctx, caKey)).To(Succeed())

		By("Reconciling: the SASL_PLAINTEXT client-auth path rebuilds the truststore")
		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).NotTo(BeZero(), "the rotation rollout must requeue the reconcile")

		Expect(k8sClient.Get(ctx, caCertKey, caCert)).To(Succeed())
		rebuilt := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, jksKey, rebuilt)).To(Succeed())
		Expect(rebuilt.Annotations[CAVersionKey]).To(Equal(caCert.ResourceVersion),
			"the truststore must record the rotated cluster CA version")
		Expect(rebuilt.Data["truststore.jks"]).NotTo(BeEmpty())

		rolled := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, rolled)).To(Succeed())
		newJKSVersion := rolled.Spec.Template.Annotations[jksVersionKey]
		Expect(newJKSVersion).NotTo(Equal(oldJKSVersion),
			"the Deployment must roll onto the rebuilt truststore")
		Expect(newJKSVersion).To(Equal(rebuilt.ResourceVersion),
			"the jksVersion marker must point at the rebuilt truststore secret")

		By("The following reconcile does not roll the Deployment again")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())
		stable := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, stable)).To(Succeed())
		Expect(stable.Spec.Template.Annotations[jksVersionKey]).To(Equal(newJKSVersion),
			"the truststore rollout must be idempotent")
	})
})
