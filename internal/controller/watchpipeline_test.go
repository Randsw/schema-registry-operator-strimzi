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
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
)

// I-1: the KafkaUser secret watch pipeline end to end. The unit tests pin
// the mapper and the rotation handler in isolation; this spec proves the
// real envtest watch pipeline: bumping the KafkaUser secret's resourceVersion
// (a SCRAM credential rotation) flows through the Secret watch, the mapper
// enqueues the CR, and the deployment's saslUserSecretVersion annotation is
// updated — the marker that drives the pod rollout.
//
// The scenario runs in the SASL_PLAINTEXT mode (the simplest SASL mode: no
// truststore/keystore material, only the SCRAM credentials).
var _ = Describe("KafkaUser secret watch pipeline", func() {
	const SchemaRegistryName = "test-watch-sasl"

	ctx := context.Background()

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: SchemaRegistryName,
		},
	}

	typeNamespacedName := types.NamespacedName{
		Name:      SchemaRegistryName,
		Namespace: SchemaRegistryName,
	}
	deploymentNamespacedName := types.NamespacedName{
		Name:      SchemaRegistryName + "-deploy",
		Namespace: SchemaRegistryName,
	}

	BeforeEach(func() {
		By("Creating the namespace to perform the tests")
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Creating the Strimzi Kafka cluster with a SASL PLAINTEXT listener and status")
		cluster := &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kafka-cluster",
				Namespace: SchemaRegistryName,
				Labels: map[string]string{
					"strimzi.io/cluster": "kafka-cluster",
				},
			},
			Spec: &kafka.KafkaSpec{
				Kafka: &kafka.KafkaClusterSpec{
					Listeners: []kafka.GenericKafkaListener{
						{
							Name: "saslplain",
							Port: 9092,
							Tls:  false,
							Type: kafka.INTERNAL_KAFKALISTENERTYPE,
							Authentication: &kafka.KafkaListenerAuthentication{
								Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE,
							},
						},
					},
					Version: "4.1.0",
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

		updatedCluster := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updatedCluster)).To(Succeed())
		updatedCluster.Status = &kafka.KafkaStatus{
			ClusterId: "watch-cluster-id",
			Listeners: []kafka.ListenerStatus{
				{
					BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092",
					Name:             "saslplain",
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, updatedCluster)).To(Succeed())

		By("Creating the SCRAM KafkaUser")
		user := &kafka.KafkaUser{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
				Labels: map[string]string{
					"strimzi.io/cluster": "kafka-cluster",
				},
			},
			Spec: &kafka.KafkaUserSpec{
				Authentication: &kafka.KafkaUserAuthentication{
					Type: kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())

		By("Creating the KafkaUser SCRAM secret")
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
			},
			Data: map[string][]byte{
				"sasl.jaas.config": []byte("org.apache.kafka.common.security.scram.ScramLoginModule required username=\"user\" password=\"pass\";"),
				"password":         []byte("pass"),
			},
		}
		Expect(k8sClient.Create(ctx, userSecret)).To(Succeed())

		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		By("Creating the StrimziSchemaRegistry CR in SASL_PLAINTEXT mode")
		resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
				Labels: map[string]string{
					"strimzi.io/cluster": "kafka-cluster",
				},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				SecureHTTP:         false,
				Listener:           "saslplain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "SASL_PLAINTEXT", Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "confluentinc/cp-schema-registry:8.4.1",
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())

		By("Reconciling the CR so the deployment carries the initial saslUserSecretVersion")
		reconciler := &StrimziSchemaRegistryReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for the deployment to exist")
		Eventually(func() error {
			found := &appsv1.Deployment{}
			return k8sClient.Get(ctx, deploymentNamespacedName, found)
		}, time.Minute, time.Second).Should(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up the CR and the namespace")
		found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
			if len(found.Finalizers) > 0 {
				found.Finalizers = []string{}
				_ = k8sClient.Update(ctx, found)
			}
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("updates the saslUserSecretVersion annotation when the KafkaUser secret rotates", func() {
		By("Reading the recorded KafkaUser secret version")
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentNamespacedName, dep)).To(Succeed())
		recorded := dep.Spec.Template.Annotations[saslUserVersionKey]
		Expect(recorded).NotTo(BeEmpty(), "expected the initial saslUserSecretVersion annotation")

		By("Rotating the KafkaUser secret (bumping its resourceVersion)")
		userSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, typeNamespacedName, userSecret)).To(Succeed())
		userSecret.Data["password"] = []byte("new-pass")
		Expect(k8sClient.Update(ctx, userSecret)).To(Succeed())

		By("Reconciling and verifying the annotation follows the new secret version")
		reconciler := &StrimziSchemaRegistryReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		Eventually(func(g Gomega) string {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			g.Expect(err).NotTo(HaveOccurred())

			updated := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentNamespacedName, updated)).To(Succeed())
			return updated.Spec.Template.Annotations[saslUserVersionKey]
		}, time.Minute, time.Second).ShouldNot(Equal(recorded),
			"the saslUserSecretVersion annotation must follow the rotated KafkaUser secret version")
	})
})

// I-2: the spec.service ClusterIP→NodePort transition on a live CR. The unit
// tests pin createService and mergeServicePorts in isolation; this spec
// proves the transition through a real reconcile against the envtest API
// server: the Service is updated in place to NodePort with the pinned port,
// and un-pinning the port preserves the API-server-allocated node port.
var _ = Describe("Service ClusterIP to NodePort transition", func() {
	const SchemaRegistryName = "test-watch-nodeport"

	ctx := context.Background()

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: SchemaRegistryName,
		},
	}

	typeNamespacedName := types.NamespacedName{
		Name:      SchemaRegistryName,
		Namespace: SchemaRegistryName,
	}
	deploymentNamespacedName := types.NamespacedName{
		Name:      SchemaRegistryName + "-deploy",
		Namespace: SchemaRegistryName,
	}
	serviceNamespacedName := types.NamespacedName{
		Name:      SchemaRegistryName,
		Namespace: SchemaRegistryName,
	}

	// pinnedNodePort is the node port the spec pins; it must be in the
	// API server's default node-port range (30000-32767).
	const pinnedNodePort = int32(31456)

	BeforeEach(func() {
		By("Creating the namespace to perform the tests")
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Creating the Strimzi Kafka cluster with a plain listener and status")
		cluster := &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kafka-cluster",
				Namespace: SchemaRegistryName,
				Labels: map[string]string{
					"strimzi.io/cluster": "kafka-cluster",
				},
			},
			Spec: &kafka.KafkaSpec{
				Kafka: &kafka.KafkaClusterSpec{
					Listeners: []kafka.GenericKafkaListener{
						{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
					},
					Version: "4.1.0",
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

		updatedCluster := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updatedCluster)).To(Succeed())
		updatedCluster.Status = &kafka.KafkaStatus{
			ClusterId: "nodeport-cluster-id",
			Listeners: []kafka.ListenerStatus{
				{
					BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092",
					Name:             "plain",
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, updatedCluster)).To(Succeed())

		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		By("Creating the StrimziSchemaRegistry CR with the default ClusterIP Service")
		resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
				Labels: map[string]string{
					"strimzi.io/cluster": "kafka-cluster",
				},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				SecureHTTP:         false,
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test",
								Image: "confluentinc/cp-schema-registry:8.4.1",
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())

		By("Reconciling until the deployment and the ClusterIP Service exist")
		reconciler := &StrimziSchemaRegistryReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		Eventually(func(g Gomega) {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			g.Expect(err).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, deploymentNamespacedName, dep)).To(Succeed())

			svc := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, serviceNamespacedName, svc)).To(Succeed())
			g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
		}, time.Minute, time.Second).Should(Succeed())
	})

	AfterEach(func() {
		By("Cleaning up the CR and the namespace")
		found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
			if len(found.Finalizers) > 0 {
				found.Finalizers = []string{}
				_ = k8sClient.Update(ctx, found)
			}
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("transitions the Service to NodePort and preserves the allocation when un-pinned", func() {
		By("Pinning the node port on the live CR")
		Expect(transitionServiceSpec(typeNamespacedName, func(spec *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			spec.Service = &strimziregistryoperatorv1alpha1.Service{
				Type:     "NodePort",
				NodePort: ptr.To(pinnedNodePort),
			}
		})).To(Succeed())

		By("Reconciling and verifying the Service exposes the pinned node port")
		reconciler := &StrimziSchemaRegistryReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		Eventually(func(g Gomega) int32 {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			g.Expect(err).NotTo(HaveOccurred())

			svc := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, serviceNamespacedName, svc)).To(Succeed())
			g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
			g.Expect(svc.Spec.Ports).To(HaveLen(1))
			return svc.Spec.Ports[0].NodePort
		}, time.Minute, time.Second).Should(Equal(pinnedNodePort),
			"the Service must expose the pinned node port")

		By("Un-pinning the node port")
		Expect(transitionServiceSpec(typeNamespacedName, func(spec *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			spec.Service = &strimziregistryoperatorv1alpha1.Service{Type: "NodePort"}
		})).To(Succeed())

		By("Reconciling and verifying the allocated node port is preserved")
		Eventually(func(g Gomega) int32 {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			g.Expect(err).NotTo(HaveOccurred())

			svc := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, serviceNamespacedName, svc)).To(Succeed())
			g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
			g.Expect(svc.Spec.Ports).To(HaveLen(1))
			return svc.Spec.Ports[0].NodePort
		}, time.Minute, time.Second).Should(Equal(pinnedNodePort),
			"the API-server-allocated node port must be preserved when the pin is removed")
	})
})

// transitionServiceSpec updates the live CR's spec.service through the
// retry-on-conflict pattern (the operator mutates the CR while reconciling
// it, so a plain Get+Update races the reconcile).
func transitionServiceSpec(key types.NamespacedName, mutate func(*strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, key, existing); err != nil {
			return err
		}
		mutate(&existing.Spec)
		return k8sClient.Update(ctx, existing)
	})
}
