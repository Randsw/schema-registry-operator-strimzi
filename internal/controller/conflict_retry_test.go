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
	"errors"
	"fmt"
	"os"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// conflictInjectingClient fails the first `remaining` Deployment Update calls
// with a synthetic 409 Conflict, exactly how the API server reports a stale
// resourceVersion. Every other call (including Status().Update and the
// actual Deployment writes once the budget is spent) is delegated unchanged.
type conflictInjectingClient struct {
	client.Client

	mu        sync.Mutex
	remaining int
	attempts  int
}

func (c *conflictInjectingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*appsv1.Deployment); ok {
		c.mu.Lock()
		if c.remaining > 0 {
			c.remaining--
			c.attempts++
			c.mu.Unlock()
			return apierrors.NewConflict(
				appsv1.Resource("deployments"),
				obj.GetName(),
				errors.New("injected mid-reconcile conflict"),
			)
		}
		c.mu.Unlock()
	}
	return c.Client.Update(ctx, obj, opts...)
}

func (c *conflictInjectingClient) attemptsSoFar() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts
}

// T-3: a 409 Conflict injected on the Deployment Update in the middle of a
// reconcile must surface as a returned error (so the workqueue retries), leave
// the live Deployment untouched, and be resolved by the next reconcile with
// the rollout markers intact — not rolled a second time. The existing suite
// asserts conflict handling only at the helper level; this spec drives it
// through the whole Reconcile flow against a real API server.
var _ = Describe("Mid-reconcile conflict retry (T-3)", func() {
	const (
		namespacePrefix = "test-conflict-retry"
		registryName    = "conflict-sr"
	)

	ctx := context.Background()
	specCounter := 0

	var (
		namespace     *corev1.Namespace
		registryKey   types.NamespacedName
		deploymentKey types.NamespacedName
		clusterCA     *testutil.TestCA
	)

	BeforeEach(func() {
		specCounter++
		nsName := fmt.Sprintf("%s-%d", namespacePrefix, specCounter)
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		registryKey = types.NamespacedName{Name: registryName, Namespace: nsName}
		deploymentKey = types.NamespacedName{Name: registryName + "-deploy", Namespace: nsName}

		var err error
		clusterCA, err = testutil.GenerateClusterCACert("T3-CONFLICT")
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Creating the Kafka CR with a PLAINTEXT listener and a populated status")
		cluster := &kafka.Kafka{
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
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		created := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: nsName}, created)).To(Succeed())
		created.Status = &kafka.KafkaStatus{
			ClusterId: "conflict-retry-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plain",
				BootstrapServers: "kafka-cluster-kafka-bootstrap." + nsName + ".svc:9092",
			}},
		}
		Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())

		By("Creating the cluster CA cert and key secrets the operator-managed -tls secret needs")
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: nsName},
			Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: nsName},
			Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
		})).To(Succeed())

		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		By("Creating the StrimziSchemaRegistry CR")
		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: nsName,
				Labels:    map[string]string{strimziClusterLabel: "kafka-cluster"},
			},
			Spec: strimziregistryoperatorv1beta1.StrimziSchemaRegistrySpec{
				SecureHTTP:         true,
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
		found := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, registryKey, found); err == nil {
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("retries the Deployment update after a conflict and never regresses the rollout marker", func() {
		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		By("First reconcile creates the Deployment and the -tls secret")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())

		deployed := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, deployed)).To(Succeed())
		stableTLSVersion := deployed.Spec.Template.Annotations[tlsVersionKey]
		Expect(stableTLSVersion).NotTo(BeEmpty(), "the operator-managed -tls secret must seed the tlsVersion rollout marker")

		By("Changing the CR spec so the next reconcile must update the Deployment")
		current := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, current)).To(Succeed())
		current.Spec.CompatibilityLevel = "full"
		Expect(k8sClient.Update(ctx, current)).To(Succeed())

		By("Injecting a 409 Conflict on the mid-reconcile Deployment update")
		conflicting := &conflictInjectingClient{Client: k8sClient, remaining: 1}
		conflictReconciler := &StrimziSchemaRegistryReconciler{Client: conflicting, Scheme: k8sClient.Scheme()}

		_, err = conflictReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).To(HaveOccurred(), "the injected conflict must be returned so the workqueue retries")
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "expected a 409 conflict, got %v", err)
		Expect(conflicting.attemptsSoFar()).To(Equal(1))

		By("Asserting the conflict left the Deployment and its rollout marker untouched")
		unchanged := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, unchanged)).To(Succeed())
		Expect(unchanged.Spec.Template.Annotations[tlsVersionKey]).To(Equal(stableTLSVersion))
		env, ok := envByName(unchanged.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL")
		Expect(ok).To(BeTrue())
		Expect(env.Value).To(Equal("forward"), "the failed update must not partially apply the spec change")

		By("Asserting the CR records the conflict as a ReconcileError")
		failed := &strimziregistryoperatorv1beta1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, registryKey, failed)).To(Succeed())
		Expect(failed.Status.Status).To(Equal("ReconcileError"))

		By("The next reconcile converges and preserves the rollout marker")
		_, err = conflictReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())

		converged := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, converged)).To(Succeed())
		env, ok = envByName(converged.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL")
		Expect(ok).To(BeTrue())
		Expect(env.Value).To(Equal("full"), "the retried pass must apply the pending spec change")
		Expect(converged.Spec.Template.Annotations[tlsVersionKey]).To(Equal(stableTLSVersion),
			"the retry must not roll the pods again: the tlsVersion marker must stay put")
	})
})
