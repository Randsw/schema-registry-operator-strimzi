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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
)

// T-2 (C-1,U-1): the watch pipeline wired by SetupWithManager. The Kafka watch
// must converge listener/status changes for a real-shaped Kafka CR — Strimzi
// never puts the strimzi.io/cluster label on the Kafka CR itself, so the watch
// can only work because mapKafkaToRequests matches the Kafka CR name against
// the StrimziSchemaRegistry label value. The Secret watch must recreate an
// operator-owned secret after it is deleted.
//
// Convergence is bounded at 20 seconds (and the negative control at 3
// seconds), well below the H-3 60-second requeue, so only a watch-driven
// enqueue can explain the result.
var _ = Describe("Kafka CR watch pipeline (T-2)", func() {
	const (
		namespacePrefix = "test-kafka-watch"
		registryName    = "watch-sr"

		// userTLSSecretName is the PEM user TLS secret the positive spec
		// installs so the operator creates a derived JKS secret the Secret
		// watch can recreate after deletion.
		userTLSSecretName = "watch-user-tls"
	)

	ctx := context.Background()
	specCounter := 0

	var (
		specNamespace string
		registryKey   types.NamespacedName
		deploymentKey types.NamespacedName

		mgr       ctrl.Manager
		cancelMgr context.CancelFunc
	)

	BeforeEach(func() {
		specCounter++
		specNamespace = fmt.Sprintf("%s-%d", namespacePrefix, specCounter)
		registryKey = types.NamespacedName{Name: registryName, Namespace: specNamespace}
		deploymentKey = types.NamespacedName{Name: registryName + "-deploy", Namespace: specNamespace}

		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: specNamespace},
		})).To(Succeed())
		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
	})

	AfterEach(func() {
		if cancelMgr != nil {
			cancelMgr()
		}
		found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, registryKey, found); err == nil {
			if len(found.Finalizers) > 0 {
				found.Finalizers = []string{}
				_ = k8sClient.Update(ctx, found)
			}
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: specNamespace}})
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	// newKafkaCR builds the real Strimzi shape: identity in the name, no
	// strimzi.io/cluster label on the Kafka object.
	newKafkaCR := func(name string) *kafka.Kafka {
		return &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: specNamespace},
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
	}

	createKafkaWithStatus := func(name, bootstrap string) {
		Expect(k8sClient.Create(ctx, newKafkaCR(name))).To(Succeed())
		created := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: specNamespace}, created)).To(Succeed())
		created.Status = &kafka.KafkaStatus{
			ClusterId: name + "-cluster-id",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plain",
				BootstrapServers: bootstrap,
			}},
		}
		Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())
	}

	createRegistry := func(name, clusterLabel string) {
		resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: specNamespace,
				Labels:    map[string]string{"strimzi.io/cluster": clusterLabel},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				SecureHTTP:         false,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
	}

	// createRegistryWithUserTLS creates a PEM user TLS secret plus a SecureHTTP
	// registry that references it, so the operator creates the derived
	// <name>-<tlssecretname>-jks secret owned by the CR.
	createRegistryWithUserTLS := func(name, clusterLabel, tlsSecretName string) {
		userCA, err := testutil.GenerateTestCA()
		Expect(err).NotTo(HaveOccurred())
		userCert, err := testutil.GenerateTestUserCert(userCA, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: tlsSecretName, Namespace: specNamespace},
			Type:       corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"tls.crt": []byte(userCert.UserCertPEM),
				"tls.key": []byte(userCert.UserKeyPEM),
				"ca.crt":  []byte(userCA.CACertPEM),
			},
		})).To(Succeed())

		resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: specNamespace,
				Labels:    map[string]string{"strimzi.io/cluster": clusterLabel},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				Listener:           "plain",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				SecureHTTP:         true,
				TLSSecretName:      tlsSecretName,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
	}

	startManager := func() {
		var err error
		// Each spec starts its own manager, but controller-runtime enforces
		// controller-name uniqueness process-wide; skip that validation here
		// because the managers never run concurrently.
		mgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     scheme.Scheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler := &StrimziSchemaRegistryReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("strimzischemaregistry-controller"),
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		var mgrCtx context.Context
		mgrCtx, cancelMgr = context.WithCancel(context.Background())
		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		Eventually(func() bool {
			return mgr.GetCache().WaitForCacheSync(mgrCtx)
		}, 30*time.Second, 250*time.Millisecond).Should(BeTrue())
	}

	deploymentBootstrap := func(key types.NamespacedName) string {
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, key, dep); err != nil {
			return ""
		}
		env, ok := envByName(dep.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS")
		if !ok {
			return ""
		}
		return env.Value
	}

	It("converges on Kafka changes within 20s and recreates a deleted operator secret", func() {
		kafkaName := "kafka"
		initialBootstrap := fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9092", kafkaName, specNamespace)
		newBootstrap := fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9192", kafkaName, specNamespace)

		createKafkaWithStatus(kafkaName, initialBootstrap)
		createRegistryWithUserTLS(registryName, kafkaName, userTLSSecretName)
		startManager()

		By("letting the first reconcile settle on the initial bootstrap servers")
		Eventually(func() string {
			return deploymentBootstrap(deploymentKey)
		}, 20*time.Second, 250*time.Millisecond).Should(Equal(initialBootstrap))

		By("mutating Kafka.status.listeners without touching the Kafka CR label")
		updated := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kafkaName, Namespace: specNamespace}, updated)).To(Succeed())
		updated.Status.Listeners[0].BootstrapServers = newBootstrap
		Expect(k8sClient.Status().Update(ctx, updated)).To(Succeed())

		By("asserting the Deployment env converges through the Kafka watch")
		// 20s is deliberately well below the 60s H-3 requeue still present in
		// Reconcile: convergence inside this window can only come from the
		// Kafka watch enqueue, not from a periodic requeue.
		Eventually(func() string {
			return deploymentBootstrap(deploymentKey)
		}, 20*time.Second, 250*time.Millisecond).Should(Equal(newBootstrap))

		// Secret-watch recreation: deleting an operator-owned secret must
		// trigger a reconcile that puts it back.
		derivedKey := types.NamespacedName{
			Name:      registryName + "-" + userTLSSecretName + jksSecretSuffix,
			Namespace: specNamespace,
		}

		By("waiting for the operator-owned derived JKS secret")
		Eventually(func() error {
			return k8sClient.Get(ctx, derivedKey, &corev1.Secret{})
		}, 20*time.Second, 250*time.Millisecond).Should(Succeed())

		By("deleting the operator-owned derived JKS secret")
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      derivedKey.Name,
			Namespace: derivedKey.Namespace,
		}})).To(Succeed())

		By("waiting until the deletion is actually observed (drain proof)")
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, derivedKey, &corev1.Secret{}))
		}, 20*time.Second, 100*time.Millisecond).Should(BeTrue())

		By("asserting the Secret watch recreates the secret")
		Eventually(func() error {
			return k8sClient.Get(ctx, derivedKey, &corev1.Secret{})
		}, 20*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("does not reconcile a CR when a different Kafka CR changes", func() {
		otherKafka := "other-kafka"
		otherBootstrap := fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9092", otherKafka, specNamespace)
		otherRegistry := "other-sr"
		otherDeploymentKey := types.NamespacedName{Name: otherRegistry + "-deploy", Namespace: specNamespace}
		registryKey = types.NamespacedName{Name: otherRegistry, Namespace: specNamespace}

		createKafkaWithStatus(otherKafka, otherBootstrap)
		createRegistry(otherRegistry, otherKafka)
		startManager()

		Eventually(func() string {
			return deploymentBootstrap(otherDeploymentKey)
		}, 20*time.Second, 250*time.Millisecond).Should(Equal(otherBootstrap))

		By("creating a Kafka CR whose name matches no StrimziSchemaRegistry cluster label")
		unrelatedKafka := "unrelated-kafka"
		createKafkaWithStatus(unrelatedKafka, fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9192", unrelatedKafka, specNamespace))

		By("asserting the unrelated CR's Deployment is not reconciled")
		// The 3s window is far below the 60s H-3 requeue, so the assertion is
		// not masked by a periodic pass; a watch mapper that matched on any
		// Kafka event would enqueue and rewrite the env in this window.
		Consistently(func() string {
			return deploymentBootstrap(otherDeploymentKey)
		}, 3*time.Second, 250*time.Millisecond).Should(Equal(otherBootstrap))
	})
})
