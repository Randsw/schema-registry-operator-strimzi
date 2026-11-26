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
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// expectControllerOwner asserts that obj carries exactly one controller owner
// reference pointing at the given StrimziSchemaRegistry, with the CR's real UID
// and blockOwnerDeletion set. Garbage collection of every operator-managed
// resource depends on this contract: a missing or wrong UID silently leaks the
// object after the CR is deleted, and a missing controller flag means the first
// owner wins instead of the CR.
func expectControllerOwner(obj client.Object, cr *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) {
	GinkgoHelper()
	ref := metav1.GetControllerOf(obj)
	Expect(ref).NotTo(BeNil(), "expected %T %s/%s to carry a controller owner reference",
		obj, obj.GetNamespace(), obj.GetName())
	Expect(ref.APIVersion).To(Equal(strimziregistryoperatorv1alpha1.GroupVersion.String()))
	Expect(ref.Kind).To(Equal("StrimziSchemaRegistry"))
	Expect(ref.Name).To(Equal(cr.Name))
	Expect(ref.UID).To(Equal(cr.UID), "the owner UID must be the live CR UID, not a stale one")
	Expect(ptr.Deref(ref.BlockOwnerDeletion, false)).To(BeTrue(),
		"blockOwnerDeletion must be set so foreground deletion waits for the owned object")
}

// T-7: after a full reconcile every operator-managed object must be owned by
// the CR (controller reference + live UID). The suite asserts existence and
// content of these objects but rarely ownership, so a regression that drops
// SetControllerReference would leak resources after CR deletion without any
// failing test. A generated REST API TLS secret and a user-provided PEM TLS
// secret need separate specs because the operator-managed -tls secret and the
// derived user-secret JKS conversion are mutually exclusive.
var _ = Describe("Managed object ownership (T-7)", func() {
	const registryName = "ownership-sr"

	ctx := context.Background()
	specCounter := 0

	var (
		namespace   *corev1.Namespace
		registryKey types.NamespacedName
		cr          *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry
	)

	newNamespace := func(prefix string) string {
		specCounter++
		name := fmt.Sprintf("%s-%d", prefix, specCounter)
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		registryKey = types.NamespacedName{Name: registryName, Namespace: name}
		return name
	}

	reconcileUntilSettled := func() {
		GinkgoHelper()
		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		// Deployment, then Service, then NetworkPolicy are each created on a
		// separate pass (every create requeues), so run a few passes.
		for i := 0; i < 4; i++ {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			Expect(err).NotTo(HaveOccurred())
		}
		// Fetch the live CR for its server-assigned UID.
		cr = &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, cr)).To(Succeed())
	}

	createKafka := func(nsName, listenerName string, scram bool) {
		GinkgoHelper()
		listener := kafka.GenericKafkaListener{
			Name: listenerName,
			Port: 9093,
			Tls:  scram,
			Type: kafka.INTERNAL_KAFKALISTENERTYPE,
		}
		if scram {
			listener.Authentication = &kafka.KafkaListenerAuthentication{
				Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE,
			}
		}
		Expect(k8sClient.Create(ctx, &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: nsName},
			Spec: &kafka.KafkaSpec{
				Kafka: &kafka.KafkaClusterSpec{
					Listeners: []kafka.GenericKafkaListener{listener},
					Version:   "4.1.0",
				},
			},
		})).To(Succeed())
		created := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: nsName}, created)).To(Succeed())
		created.Status = &kafka.KafkaStatus{
			ClusterId: "ownership-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             listenerName,
				BootstrapServers: "kafka-cluster-kafka-bootstrap." + nsName + ".svc:9093",
			}},
		}
		Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())
	}

	AfterEach(func() {
		found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, registryKey, found); err == nil {
			_ = k8sClient.Delete(ctx, found)
		}
		if namespace != nil {
			_ = k8sClient.Delete(ctx, namespace)
		}
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("owns the Deployment, Service, NetworkPolicy, -jks and -tls secrets", func() {
		nsName := newNamespace("test-ownership-generated-tls")
		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		createKafka(nsName, "tls", true)

		clusterCA, err := testutil.GenerateClusterCACert("T7-GENERATED")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: nsName},
			Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: nsName},
			Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
		})).To(Succeed())

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
			Data:       map[string][]byte{"sasl.jaas.config": []byte("jaas")},
		})).To(Succeed())

		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: nsName,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				SecureHTTP:         true,
				Listener:           "tls",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "SASL_SSL",
				// Enable the optional NetworkPolicy so its ownership is asserted.
				NetworkPolicy: &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())

		reconcileUntilSettled()

		By("Asserting the Deployment is controlled by the CR")
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: registryName + deploySuffix, Namespace: nsName}, dep)).To(Succeed())
		expectControllerOwner(dep, cr)

		By("Asserting the Service is controlled by the CR")
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: registryName, Namespace: nsName}, svc)).To(Succeed())
		expectControllerOwner(svc, cr)

		By("Asserting the NetworkPolicy is controlled by the CR")
		np := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: registryName + networkPolicySuffix, Namespace: nsName}, np)).To(Succeed())
		expectControllerOwner(np, cr)

		By("Asserting the Kafka JKS truststore secret is controlled by the CR")
		jks := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: registryName + jksSecretSuffix, Namespace: nsName}, jks)).To(Succeed())
		expectControllerOwner(jks, cr)

		By("Asserting the operator-managed REST API TLS secret is controlled by the CR")
		tls := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: registryName + tlsSecretSuffix, Namespace: nsName}, tls)).To(Succeed())
		expectControllerOwner(tls, cr)
	})

	It("owns the derived JKS secret converted from a user-provided PEM TLS secret", func() {
		nsName := newNamespace("test-ownership-user-tls")
		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		createKafka(nsName, "plain", false)

		userCA, err := testutil.GenerateTestCA()
		Expect(err).NotTo(HaveOccurred())
		userCert, err := testutil.GenerateTestUserCert(userCA, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "user-tls", Namespace: nsName},
			Type:       corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"tls.crt": []byte(userCert.UserCertPEM),
				"tls.key": []byte(userCert.UserKeyPEM),
				"ca.crt":  []byte(userCA.CACertPEM),
			},
		})).To(Succeed())

		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: nsName,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				SecureHTTP:         true,
				TLSSecretName:      "user-tls",
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())

		reconcileUntilSettled()

		By("Asserting the derived JKS secret is controlled by the CR")
		derived := &corev1.Secret{}
		derivedKey := types.NamespacedName{
			Name:      registryName + "-user-tls" + jksSecretSuffix,
			Namespace: nsName,
		}
		Expect(k8sClient.Get(ctx, derivedKey, derived)).To(Succeed())
		expectControllerOwner(derived, cr)
		Expect(derived.Data).To(HaveKey("tls-keystore.jks"))
	})
})
