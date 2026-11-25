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

// U-1: SetupWithManager is the only production entry point with no other
// coverage. These specs run it against a real envtest manager and prove the
// watch wiring behaviorally — controller-runtime exposes no public registry
// of installed controllers, so "the controller is registered" is asserted by
// the manager-driven reconcile loop producing its expected artifacts.
var _ = Describe("SetupWithManager wiring", func() {
	const SchemaRegistryName = "test-wiring"

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

	var (
		testCA    *testutil.TestCA
		clusterCA *testutil.TestCA
		userCert  *testutil.TestUserCert

		mgr       ctrl.Manager
		mgrCtx    context.Context
		cancelMgr context.CancelFunc
	)

	BeforeEach(func() {
		var err error
		testCA, err = testutil.GenerateTestCA()
		Expect(err).NotTo(HaveOccurred())

		clusterCA, err = testutil.GenerateClusterCACert("STRIMZI-SR-WIRING")
		Expect(err).NotTo(HaveOccurred())

		userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
		Expect(err).NotTo(HaveOccurred())

		By("Creating the namespace to perform the tests")
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		By("Creating the Strimzi Kafka cluster with TLS listener and status")
		// Strimzi never labels the Kafka CR itself: the cluster identity is
		// the CR name, which the CR below carries under strimzi.io/cluster.
		cluster := &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kafka-cluster",
				Namespace: SchemaRegistryName,
			},
			Spec: &kafka.KafkaSpec{
				Kafka: &kafka.KafkaClusterSpec{
					Listeners: []kafka.GenericKafkaListener{
						{
							Name: "plain",
							Port: 9092,
							Tls:  false,
							Type: kafka.INTERNAL_KAFKALISTENERTYPE,
						},
						{
							Name: "tls",
							Port: 9093,
							Tls:  true,
							Type: kafka.INTERNAL_KAFKALISTENERTYPE,
							Authentication: &kafka.KafkaListenerAuthentication{
								Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE,
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
			ClusterId: "wiring-cluster-id",
			Listeners: []kafka.ListenerStatus{
				{
					BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092",
					Name:             "plain",
				},
				{
					BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9093",
					Name:             "TLS",
					Certificates:     []string{clusterCA.CACertPEM},
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, updatedCluster)).To(Succeed())

		By("Creating the KafkaUser")
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
					Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())

		By("Creating the cluster CA secrets")
		clusterKeySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kafka-cluster-cluster-ca",
				Namespace: SchemaRegistryName,
			},
			Data: map[string][]byte{
				"ca.key": []byte(clusterCA.CAKeyPEM),
			},
		}
		Expect(k8sClient.Create(ctx, clusterKeySecret)).To(Succeed())

		clusterCertSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kafka-cluster-cluster-ca-cert",
				Namespace: SchemaRegistryName,
			},
			Data: map[string][]byte{
				"ca.crt": []byte(clusterCA.CACertPEM),
			},
		}
		Expect(k8sClient.Create(ctx, clusterCertSecret)).To(Succeed())

		By("Creating the KafkaUser client secret")
		clientSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
			},
			Data: map[string][]byte{
				"ca.crt":        []byte(testCA.CACertPEM),
				"user.crt":      []byte(userCert.UserCertPEM),
				"user.key":      []byte(userCert.UserKeyPEM),
				"user.p12":      userCert.PKCS12Data,
				"user.password": []byte(userCert.Password),
			},
		}
		Expect(k8sClient.Create(ctx, clientSecret)).To(Succeed())

		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

		By("Creating the StrimziSchemaRegistry CR")
		resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
				Labels: map[string]string{
					"strimzi.io/cluster": "kafka-cluster",
				},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				SecureHTTP:         true,
				Listener:           "TLS",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "SSL",
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

		By("Wiring the controller into a real manager")
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

		By("Starting the manager")
		mgrCtx, cancelMgr = context.WithCancel(context.Background())
		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		Eventually(func() bool {
			return mgr.GetCache().WaitForCacheSync(mgrCtx)
		}, 30*time.Second, 250*time.Millisecond).Should(BeTrue())
	})

	AfterEach(func() {
		By("Stopping the manager")
		cancelMgr()

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

	It("registers the controller and reacts to Secret and Kafka events", func() {
		By("Reconciling the CR through the manager loop (For watch)")
		Eventually(func() error {
			found := &appsv1.Deployment{}
			return k8sClient.Get(ctx, deploymentNamespacedName, found)
		}, time.Minute, time.Second).Should(Succeed())

		By("Waiting for the reconcile loop to settle")
		Eventually(func() error {
			found := &corev1.Secret{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      SchemaRegistryName + "-tls",
				Namespace: SchemaRegistryName,
			}, found)
		}, 2*time.Minute, time.Second).Should(Succeed())
		// Let in-flight reconciles drain so the recreation below can only be
		// caused by the Secret delete event flowing through the watch.
		time.Sleep(5 * time.Second)

		By("Deleting the operator-owned JKS secret")
		jks := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      SchemaRegistryName + "-jks",
			Namespace: SchemaRegistryName,
		}, jks)).To(Succeed())
		Expect(k8sClient.Delete(ctx, jks)).To(Succeed())

		By("Verifying the secret is recreated through the Secret watch pipeline")
		Eventually(func() error {
			recreated := &corev1.Secret{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      SchemaRegistryName + "-jks",
				Namespace: SchemaRegistryName,
			}, recreated)
		}, time.Minute, time.Second).Should(Succeed())

		By("Changing the Kafka status bootstrap servers")
		const newBootstrap = "kafka-cluster-kafka-bootstrap.moved.svc:9093"
		updatedCluster := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updatedCluster)).To(Succeed())
		for i := range updatedCluster.Status.Listeners {
			if updatedCluster.Status.Listeners[i].Name == "TLS" {
				updatedCluster.Status.Listeners[i].BootstrapServers = newBootstrap
			}
		}
		Expect(k8sClient.Status().Update(ctx, updatedCluster)).To(Succeed())

		By("Verifying the deployment env follows the new bootstrap servers")
		Eventually(func() string {
			dep := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, deploymentNamespacedName, dep); err != nil {
				return ""
			}
			env, ok := envByName(dep.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS")
			if !ok {
				return ""
			}
			return env.Value
		}, time.Minute, time.Second).Should(Equal(newBootstrap))
	})
})

// T-2 (C-1): the Kafka watch must converge listener/status changes for a
// real-shaped Kafka CR — Strimzi never puts the strimzi.io/cluster label on
// the Kafka CR itself, so the watch can only work because mapKafkaToRequests
// matches the Kafka CR name against the StrimziSchemaRegistry label value.
//
// The wiring spec above allows a full minute for convergence, which the H-3
// 60-second requeue could satisfy on its own. The positive spec below bounds
// convergence at 20 seconds (and the negative control at 3 seconds), so only
// a watch-driven enqueue can explain the result.
var _ = Describe("Kafka CR watch pipeline (T-2)", func() {
	const (
		namespacePrefix = "test-kafka-watch"
		registryName    = "watch-sr"
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

	It("converges within 20s when an unlabeled Kafka CR changes its listener bootstrap", func() {
		kafkaName := "kafka"
		initialBootstrap := fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9092", kafkaName, specNamespace)
		newBootstrap := fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9192", kafkaName, specNamespace)

		createKafkaWithStatus(kafkaName, initialBootstrap)
		createRegistry(registryName, kafkaName)
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
