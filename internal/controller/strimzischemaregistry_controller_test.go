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
	"strconv"
	"time"

	"github.com/go-logr/logr"
	//kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/client-go/kubernetes"
	eventsfake "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("StrimziSchemaRegistry Controller", func() {
	// M2: Custom TLS Secret Name Test
	Context("When reconciling with custom TLS secret name", func() {
		const SchemaRegistryName = "test-custom-tls"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-CUSTOM")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5, "lettuce": 7},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-cluster-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.custom.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.custom.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Creating custom TLS secret with decodable JKS material")
			keystoreBytes, _, err := certprocessor.NewCertProcessor(logr.Logger{}).ConvertPEMToJKS(
				ctx, string(userCert.UserCertPEM), string(userCert.UserKeyPEM), string(testCA.CACertPEM), "keystorepass")
			Expect(err).NotTo(HaveOccurred())
			customTLSSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-custom-tls-secret", Namespace: namespace.Name},
				Type:       corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"tls-keystore.jks":  keystoreBytes,
					"keystore_password": []byte("keystorepass"),
					"key_password":      []byte("keystorepass"),
				},
			}
			Expect(k8sClient.Create(ctx, customTLSSecret)).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with custom TLSSecretName")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:    true,
					TLSSecretName: "my-custom-tls-secret",
					Listener:      "TLS",
					Template:      corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should use the custom TLS secret without creating a new one", func() {
			By("Reconciling the created resource")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was successfully created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Verifying the deployment references the custom TLS secret")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
					Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Equal("my-custom-tls-secret"))
				}
				if env.Name == "SCHEMA_REGISTRY_SSL_KEY_PASSWORD" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
					Expect(env.ValueFrom.SecretKeyRef.LocalObjectReference.Name).To(Equal("my-custom-tls-secret"))
				}
			}

			By("Checking that no auto-generated TLS secret was created")
			tlsSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, tlsSecret)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "Auto-generated TLS secret should not exist when TLSSecretName is provided")
		})
	})

	// M3: Error Path Tests
	Context("When dependencies are missing", func() {
		ctx := context.Background()

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		It("should return an error when Kafka cluster is missing", func() {
			const SchemaRegistryName = "test-err-no-kafka"
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())
			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-ERR")
			Expect(err).NotTo(HaveOccurred())
			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE") })

			By("Creating minimal prerequisites (KafkaUser + secrets but no Kafka cluster)")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true, Listener: "TLS",
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling should fail because Kafka cluster is not found")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())
		})

		It("should return an error when KafkaUser secret is missing", func() {
			const SchemaRegistryName = "test-err-no-user-secret"
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())
			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-ERR2")
			Expect(err).NotTo(HaveOccurred())
			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE") })

			By("Creating Kafka cluster and CA secrets but not the user secret")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners:     []kafka.GenericKafkaListener{{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE}},
						Version:       "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{{BootstrapServers: "kafka-cluster-kafka-bootstrap.err.svc:9093", Name: "TLS"}}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true, Listener: "TLS",
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling should fail because KafkaUser secret is not found")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())
		})

		It("should return an error when cluster CA secret is missing", func() {
			const SchemaRegistryName = "test-err-no-ca-secret"
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())
			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-ERR3")
			Expect(err).NotTo(HaveOccurred())
			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE") })

			By("Creating Kafka cluster and user secret but not cluster CA secrets")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners:     []kafka.GenericKafkaListener{{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE}},
						Version:       "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{{BootstrapServers: "kafka-cluster-kafka-bootstrap.err2.svc:9093", Name: "TLS"}}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true, Listener: "TLS",
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling should fail because cluster CA secret is not found")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())
		})
	})

	// M4: Finalizer Removal Test
	Context("When testing finalizer behavior", func() {
		const SchemaRegistryName = "test-finalizer"

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

		BeforeEach(func() {
			var err error

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				found.Finalizers = []string{} // Remove finalizers to allow deletion
				_ = k8sClient.Update(ctx, found)
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should not add a finalizer and delete without operator involvement", func() {
			By("Creating StrimziSchemaRegistry without full dependencies")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true, Listener: "TLS",
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling (later steps fail on missing deps, but no finalizer may be added)")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			By("Checking that no finalizer was added (H-2)")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(instance.Finalizers).To(BeEmpty(), "owner references already handle GC; no finalizer should be added")

			By("Deleting the resource")
			Expect(k8sClient.Delete(ctx, instance)).To(Succeed())

			By("Checking that the resource is deleted without a reconcile")
			Eventually(func() error {
				updatedInstance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				return k8sClient.Get(ctx, typeNamespacedName, updatedInstance)
			}, time.Minute, time.Second).ShouldNot(Succeed(), "Resource without a finalizer should be deleted immediately")
		})
	})

	// M5: Default Values Test
	Context("When reconciling with minimal spec (default values)", func() {
		const SchemaRegistryName = "test-defaults"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-DEF")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.def.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.def.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with minimal spec (empty CompatibilityLevel, SecurityProtocol, Listener)")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					CompatibilityLevel: "", // Should default to "forward"
					SecurityProtocol:   "", // Should default to "SSL"
					Listener:           "", // Should default to "tls"
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should apply correct default values for CompatibilityLevel, SecurityProtocol, and Listener", func() {
			By("Reconciling the created resource")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was successfully created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env

			By("Verifying default CompatibilityLevel is 'forward'")
			compatLevel := ""
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL" {
					compatLevel = env.Value
					break
				}
			}
			Expect(compatLevel).To(Equal("forward"), "Default CompatibilityLevel should be 'forward'")

			By("Verifying default SecurityProtocol is 'SSL'")
			securityProtocol := ""
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL" {
					securityProtocol = env.Value
					break
				}
			}
			Expect(securityProtocol).To(Equal("SSL"), "Default SecurityProtocol should be 'SSL'")

			By("Verifying default HeapOpts is '-Xms512M -Xmx512M'")
			heapOpts := ""
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_HEAP_OPTS" {
					heapOpts = env.Value
					break
				}
			}
			Expect(heapOpts).To(Equal("-Xms512M -Xmx512M"), "Default HeapOpts should be '-Xms512M -Xmx512M'")
		})
	})

	// M6: Deployment Spec Update Test
	Context("When updating deployment after spec change", func() {
		const SchemaRegistryName = "test-deploy-update"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-DEPLOY-UPDATE")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.deploy-upd.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.deploy-upd.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should update the deployment when CompatibilityLevel changes", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First reconcile to create the deployment")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that deployment was created with initial CompatibilityLevel")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			initialHash := deployment.Annotations[keyPrefix+"/specHash"]
			Expect(initialHash).NotTo(BeEmpty())

			By("Updating CompatibilityLevel to 'backward'")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.CompatibilityLevel = "backward"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			By("Reconciling again to trigger deployment update")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that deployment hash annotation changed")
			Eventually(func() string {
				updated := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updated)
				if err != nil {
					return ""
				}
				return updated.Annotations[keyPrefix+"/specHash"]
			}, time.Minute, time.Second).ShouldNot(Equal(initialHash))

			By("Verifying the env var was updated to 'backward'")
			updatedDeployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updatedDeployment)).To(Succeed())
			envVars := updatedDeployment.Spec.Template.Spec.Containers[0].Env
			compatLevel := ""
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL" {
					compatLevel = env.Value
					break
				}
			}
			Expect(compatLevel).To(Equal("backward"), "CompatibilityLevel should be updated to 'backward'")
		})
	})

	// M6b: KafkastoreTopicReplicationFactor Update Test (exercises the
	// computeSpecHash path for the new storage spec fields).
	Context("When updating KafkastoreTopicReplicationFactor", func() {
		const SchemaRegistryName = "test-rf-update"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-RF-UPDATE")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.rf-upd.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.rf-upd.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should update the deployment when KafkastoreTopicReplicationFactor changes", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First reconcile to create the deployment")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that deployment was created without a replication factor env var")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			initialHash := deployment.Annotations[keyPrefix+"/specHash"]
			Expect(initialHash).NotTo(BeEmpty())
			_, rfPresent := envByName(deployment.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR")
			Expect(rfPresent).To(BeFalse(), "replication factor env var should be absent when unset")

			By("Updating KafkastoreTopicReplicationFactor to 1")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.KafkastoreTopicReplicationFactor = ptr.To(int32(1))
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			By("Reconciling again to trigger deployment update")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that deployment hash annotation changed")
			Eventually(func() string {
				updated := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updated)
				if err != nil {
					return ""
				}
				return updated.Annotations[keyPrefix+"/specHash"]
			}, time.Minute, time.Second).ShouldNot(Equal(initialHash))

			By("Verifying the replication factor env var was updated")
			updatedDeployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updatedDeployment)).To(Succeed())
			rfEnv, rfPresent := envByName(updatedDeployment.Spec.Template.Spec.Containers[0].Env, "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR")
			Expect(rfPresent).To(BeTrue(), "replication factor env var should be present after update")
			Expect(rfEnv.Value).To(Equal("1"))
		})
	})

	// M8: PodTemplateSpec Update Test (B5 regression test)
	Context("When updating deployment after Template change", func() {
		const SchemaRegistryName = "test-template-update"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-TEMPLATE")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.template.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.template.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with initial image")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should update the deployment when the container resources change", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First reconcile to create the deployment")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that deployment was created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			initialHash := deployment.Annotations[keyPrefix+"/specHash"]
			Expect(initialHash).NotTo(BeEmpty())

			By("Updating the container resources in the CR template")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: mustParseQuantity("500m"),
				},
			}
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			By("Reconciling again to trigger deployment update")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that deployment hash annotation changed (B5: Template changes now trigger updates)")
			Eventually(func() string {
				updated := &appsv1.Deployment{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updated)
				if err != nil {
					return ""
				}
				return updated.Annotations[keyPrefix+"/specHash"]
			}, time.Minute, time.Second).ShouldNot(Equal(initialHash))

			By("Verifying the resources were updated")
			updatedDeployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updatedDeployment)).To(Succeed())
			Expect(updatedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("500m"))
		})
	})

	// M7: Service Update Test
	Context("When updating service after SecureHTTP change", func() {
		const SchemaRegistryName = "test-svc-update"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-SVC-UPDATE")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.svc-upd.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.svc-upd.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with SecureHTTP=true")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true,
					Listener:   "TLS",
					Template:   corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should update the service ports when SecureHTTP changes from true to false", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First two reconciles to create deployment and service")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that service was created with HTTPS port")
			Eventually(func() error {
				found := &corev1.Service{}
				return k8sClient.Get(ctx, typeNamespacedName, found)
			}, time.Minute, time.Second).Should(Succeed())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(443)))
			Expect(svc.Spec.Ports[0].Name).To(Equal("https"))

			By("Updating SecureHTTP to false")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.SecureHTTP = false
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			By("Reconciling again to trigger deployment update (spec hash changed)")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Reconciling once more to trigger service update")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that service ports were updated to HTTP")
			Eventually(func() int32 {
				updatedSvc := &corev1.Service{}
				err := k8sClient.Get(ctx, typeNamespacedName, updatedSvc)
				if err != nil {
					return 0
				}
				if len(updatedSvc.Spec.Ports) == 0 {
					return 0
				}
				return updatedSvc.Spec.Ports[0].Port
			}, time.Minute, time.Second).Should(Equal(int32(80)))

			updatedSvc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedSvc)).To(Succeed())
			Expect(updatedSvc.Spec.Ports[0].Name).To(Equal("http"))
			Expect(updatedSvc.Spec.Ports[0].TargetPort.IntVal).To(Equal(int32(8081)))
		})
	})

	// T1: renewTLSSecret integration test — verifies TLS secret is created/updated
	// when the cluster CA certificate changes and SecureHTTP is enabled.
	Context("When renewing TLS secret after cluster CA cert change", func() {
		const SchemaRegistryName = "test-tls-renew"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-TLS-RENEW")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-tls-renew-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.tls-renew.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.tls-renew.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with SecureHTTP=true")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true,
					Listener:   "TLS",
					Template:   corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should create TLS secret on first reconcile and update it when cluster CA cert changes", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First reconcile to create deployment and TLS secret")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that TLS secret was created")
			Eventually(func() error {
				found := &corev1.Secret{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			tlsSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, tlsSecret)).To(Succeed())
			oldTLSResourceVersion := tlsSecret.ResourceVersion

			// Generate a new cluster CA to simulate rotation
			newClusterCA, err := testutil.GenerateClusterCACert("STIMZI-SR-TEST-TLS-RENEW-V2")
			Expect(err).NotTo(HaveOccurred())

			By("Updating cluster CA cert and key secrets to trigger TLS secret renewal")
			caCertSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster-cluster-ca-cert", Namespace: SchemaRegistryName}, caCertSecret)).To(Succeed())
			caCertSecret.Data["ca.crt"] = []byte(newClusterCA.CACertPEM)
			Expect(k8sClient.Update(ctx, caCertSecret)).To(Succeed())

			// The CA key must also be updated to match the new cert, otherwise
			// x509 signing will fail with "provided PrivateKey doesn't match parent's PublicKey".
			caKeySecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster-cluster-ca", Namespace: SchemaRegistryName}, caKeySecret)).To(Succeed())
			caKeySecret.Data["ca.key"] = []byte(newClusterCA.CAKeyPEM)
			Expect(k8sClient.Update(ctx, caKeySecret)).To(Succeed())

			By("Reconciling to trigger secret rotation and TLS renewal")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// F2: the rotation pass rebuilds the -jks secret and returns a 1s
			// requeue; the TLS renewal happens in handleTLSCertExpiry on the
			// following reconcile.
			By("Reconciling again so handleTLSCertExpiry renews the TLS secret")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that TLS secret was updated")
			Eventually(func() string {
				updatedTLS := &corev1.Secret{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, updatedTLS)
				if err != nil {
					return ""
				}
				return updatedTLS.ResourceVersion
			}, time.Minute*2, time.Second).ShouldNot(Equal(oldTLSResourceVersion))

			By("Verifying updated TLS secret contains valid data")
			updatedTLS := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, updatedTLS)).To(Succeed())
			Expect(updatedTLS.Data).To(HaveKey("tls-keystore.jks"))
			Expect(updatedTLS.Data).To(HaveKey("keystore_password"))
			Expect(updatedTLS.Data).To(HaveKey("key_password"))
		})
	})

	// T2: updateExistingDeployment side-effect test — verifies that when only spec
	// fields change, updateExistingDeployment does NOT create or delete secrets.
	Context("When updating deployment after spec change (no secret side effects)", func() {
		const SchemaRegistryName = "test-no-secret-sideeffects"

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

		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-NO-SIDE")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespace.Name},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{TopicOperator: &kafka.EntityTopicOperatorSpec{}, UserOperator: &kafka.EntityUserOperatorSpec{}},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{SuperUsers: []string{"CN=root"}, Type: kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE)},
						Config:        kafka.MapStringObject{"apple": 5},
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE, Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{ClusterId: "test-no-side-id", Listeners: []kafka.ListenerStatus{
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.no-side.svc:9092", Name: "plain"},
				{BootstrapServers: "kafka-cluster-kafka-bootstrap.no-side.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
			}}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}}, Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with SecureHTTP=true")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should update deployment without creating or deleting secrets", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First reconcile to create deployment and secrets")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for JKS secret to be created")
			Eventually(func() error {
				found := &corev1.Secret{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Waiting for TLS secret to be created")
			Eventually(func() error {
				found := &corev1.Secret{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Waiting for deployment to be created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			// Capture secret ResourceVersions before spec change
			jksBefore := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, jksBefore)).To(Succeed())

			tlsBefore := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, tlsBefore)).To(Succeed())

			By("Changing CompatibilityLevel to trigger deployment update")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.CompatibilityLevel = "backward"
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			By("Reconciling after spec change")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying JKS secret was NOT recreated (ResourceVersion unchanged)")
			jksAfter := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, jksAfter)).To(Succeed())
			Expect(jksAfter.ResourceVersion).To(Equal(jksBefore.ResourceVersion),
				"JKS secret should not be modified when only spec field changes")

			By("Verifying TLS secret was NOT recreated (ResourceVersion unchanged)")
			tlsAfter := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, tlsAfter)).To(Succeed())
			Expect(tlsAfter.ResourceVersion).To(Equal(tlsBefore.ResourceVersion),
				"TLS secret should not be modified when only spec field changes")

			By("Verifying deployment was updated")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env
			compatLevel := ""
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL" {
					compatLevel = env.Value
					break
				}
			}
			Expect(compatLevel).To(Equal("backward"), "Deployment should reflect updated CompatibilityLevel")
		})
	})

	// S1: SASL_SSL (SCRAM-SHA-512) reconciliation
	Context("When reconciling with SASL_SSL and a SCRAM-SHA-512 KafkaUser", func() {
		const SchemaRegistryName = "test-sasl-ssl"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		var clusterCA *testutil.TestCA

		BeforeEach(func() {
			var err error
			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-SASL-SSL")
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE,
								Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "sasl-ssl-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.sasl-ssl.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: SchemaRegistryName},
				Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
			})).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName},
				Data: map[string][]byte{
					"password":         []byte("scram-password"),
					"sasl.jaas.config": []byte(`org.apache.kafka.common.security.scram.ScramLoginModule required username="test-sasl-ssl" password="scram-password";`),
				},
			})).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SASL_SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should produce SASL env vars and a truststore-only JKS secret", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env

			By("Verifying security protocol is SASL_SSL")
			proto, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL")
			Expect(ok).To(BeTrue())
			Expect(proto.Value).To(Equal("SASL_SSL"))

			By("Verifying SASL mechanism and JAAS config reference the KafkaUser secret")
			mech, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM")
			Expect(ok).To(BeTrue())
			Expect(mech.Value).To(Equal("SCRAM-SHA-512"))
			jaas, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
			Expect(ok).To(BeTrue())
			Expect(jaas.ValueFrom).NotTo(BeNil())
			Expect(jaas.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(jaas.ValueFrom.SecretKeyRef.Name).To(Equal(SchemaRegistryName))
			Expect(jaas.ValueFrom.SecretKeyRef.Key).To(Equal("sasl.jaas.config"))

			By("Verifying truststore env is present but keystore env is absent")
			_, truststoreOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
			Expect(truststoreOK).To(BeTrue())
			_, keystoreOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
			Expect(keystoreOK).To(BeFalse())

			By("Verifying the JKS secret contains only truststore material")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, &corev1.Secret{})
			}, time.Minute*2, time.Second).Should(Succeed())
			jks := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, jks)).To(Succeed())
			Expect(jks.Data).To(HaveKey("truststore.jks"))
			Expect(jks.Data).To(HaveKey("truststore_password"))
			Expect(jks.Data).NotTo(HaveKey("keystore.jks"))
			Expect(jks.Data).NotTo(HaveKey("keystore_password"))
		})
	})

	// S2: SASL_PLAINTEXT (SCRAM-SHA-512, no cert material) reconciliation
	Context("When reconciling with SASL_PLAINTEXT and a SCRAM-SHA-512 KafkaUser", func() {
		const SchemaRegistryName = "test-sasl-plaintext"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE,
								Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "sasl-plaintext-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.sasl-plaintext.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName},
				Data: map[string][]byte{
					"password":         []byte("scram-password"),
					"sasl.jaas.config": []byte(`org.apache.kafka.common.security.scram.ScramLoginModule required username="test-sasl-plaintext" password="scram-password";`),
				},
			})).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SASL_PLAINTEXT",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should produce SASL env vars with no JKS secret and no TLS env", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env

			By("Verifying security protocol is SASL_PLAINTEXT")
			proto, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL")
			Expect(ok).To(BeTrue())
			Expect(proto.Value).To(Equal("SASL_PLAINTEXT"))

			By("Verifying SASL env is present")
			mech, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM")
			Expect(ok).To(BeTrue())
			Expect(mech.Value).To(Equal("SCRAM-SHA-512"))
			jaas, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
			Expect(ok).To(BeTrue())
			Expect(jaas.ValueFrom.SecretKeyRef.Name).To(Equal(SchemaRegistryName))
			Expect(jaas.ValueFrom.SecretKeyRef.Key).To(Equal("sasl.jaas.config"))

			By("Verifying no TLS env vars are present")
			_, ksOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
			Expect(ksOK).To(BeFalse())
			_, tsOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
			Expect(tsOK).To(BeFalse())

			By("Verifying no JKS volume is mounted")
			_, tlsVolumeOK := volumeByName(deployment.Spec.Template.Spec.Volumes, "tls")
			Expect(tlsVolumeOK).To(BeFalse())

			By("Verifying no JKS secret was created")
			jks := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, jks)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "JKS secret should not exist for SASL_PLAINTEXT")
		})
	})

	// S3: PLAINTEXT (no KafkaUser, no secrets) reconciliation
	Context("When reconciling with PLAINTEXT and no KafkaUser", func() {
		const SchemaRegistryName = "test-plaintext"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "plaintext-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.plaintext.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should reconcile with no TLS/SASL env and no JKS secret", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env

			By("Verifying security protocol is PLAINTEXT")
			proto, ok := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL")
			Expect(ok).To(BeTrue())
			Expect(proto.Value).To(Equal("PLAINTEXT"))

			By("Verifying no TLS env vars are present")
			_, ksOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
			Expect(ksOK).To(BeFalse())
			_, tsOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
			Expect(tsOK).To(BeFalse())

			By("Verifying no SASL env vars are present")
			_, mechOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM")
			Expect(mechOK).To(BeFalse())
			_, jaasOK := envByName(envVars, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
			Expect(jaasOK).To(BeFalse())

			By("Verifying no JKS volume is mounted")
			_, tlsVolumeOK := volumeByName(deployment.Spec.Template.Spec.Volumes, "tls")
			Expect(tlsVolumeOK).To(BeFalse())

			By("Verifying no JKS secret was created")
			jks := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, jks)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "JKS secret should not exist for PLAINTEXT")
		})
	})

	// S4: SASL_SSL with a TLS KafkaUser (mismatch) surfaces a clear error
	Context("When SASL_SSL is configured with a TLS KafkaUser", func() {
		const SchemaRegistryName = "test-sasl-ssl-mismatch"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE,
								Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "sasl-ssl-mismatch",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.mismatch.svc:9093", Name: "TLS"},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SASL_SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should return a clear validation error", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires a KafkaUser with scram-sha-512 authentication"))
		})
	})

	// Phase 0: verify status update behavior using the minimal PLAINTEXT setup
	// (no KafkaUser or secrets required), which exercises the same status-write
	// code path without depending on the full SSL bootstrap.
	Context("When verifying status update behavior", func() {
		ctx := context.Background()
		specCounter := 0

		var (
			namespaceName      string
			typeNamespacedName types.NamespacedName
		)

		BeforeEach(func() {
			specCounter++
			namespaceName = "test-status-" + strconv.Itoa(specCounter)
			typeNamespacedName = types.NamespacedName{Name: namespaceName, Namespace: namespaceName}

			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespaceName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: namespaceName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "status-test",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.status-test.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
					Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
				},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should not rewrite status when it is unchanged", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(meta.FindStatusCondition(instance.Status.Conditions, "Ready")).NotTo(BeNil())
			beforeResourceVersion := instance.ResourceVersion

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			after := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, after)).To(Succeed())
			Expect(after.ResourceVersion).To(Equal(beforeResourceVersion))
		})

		It("should format the NotReady condition with a CamelCase reason and fixed message", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespaceName + "-deploy", Namespace: namespaceName}, deployment)).To(Succeed())
			deployment.Status.Replicas = 1
			deployment.Status.ReadyReplicas = 0
			Expect(k8sClient.Status().Update(ctx, deployment)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())

			cond := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("NotReady"))
			Expect(cond.Reason).NotTo(ContainSubstring(" "))
			Expect(cond.Message).To(ContainSubstring("is not ready"))
			Expect(cond.Message).NotTo(ContainSubstring("is no ready"))
			Expect(cond.ObservedGeneration).To(Equal(instance.Generation))
		})
	})

	// Phase 1b: the unsupported multi-replica HTTPS combination (replicas > 1
	// with securehttp and no user-provided TLS secret) is now rejected by the
	// CRD CEL validation rules at admission time, so it can no longer be
	// created through the API server. The controller-side defense-in-depth
	// behavior (InvalidSpec, no Deployment) is covered by the fake-client test
	// TestReconcileSkipsMultiReplicaHTTPSWithoutUserTLSSecret in k8s_test.go,
	// and the admission-time rejection by crdvalidation_test.go.

	// Phase 2: arbitrary configuration escape hatch.
	Context("When reconciling with spec.configuration", func() {
		const SchemaRegistryName = "test-configuration"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "configuration-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.config.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Configuration: map[string]string{
						"access.control.allow.origin": "*",
						"authentication.realm":        "Basic",
						"debug":                       "true",
					},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should render configuration entries as env vars and update the hash when the map changes", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the deployment was created")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, &appsv1.Deployment{})
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())

			envVars := deployment.Spec.Template.Spec.Containers[0].Env
			cors, ok := envByName(envVars, "SCHEMA_REGISTRY_ACCESS_CONTROL_ALLOW_ORIGIN")
			Expect(ok).To(BeTrue())
			Expect(cors.Value).To(Equal("*"))
			realm, ok := envByName(envVars, "SCHEMA_REGISTRY_AUTHENTICATION_REALM")
			Expect(ok).To(BeTrue())
			Expect(realm.Value).To(Equal("Basic"))
			debug, ok := envByName(envVars, "SCHEMA_REGISTRY_DEBUG")
			Expect(ok).To(BeTrue())
			Expect(debug.Value).To(Equal("true"))

			initialHash := deployment.Annotations[keyPrefix+"/specHash"]
			Expect(initialHash).NotTo(BeEmpty())

			By("Updating the configuration map")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			instance.Spec.Configuration = map[string]string{
				"access.control.allow.origin": "*",
				"authentication.realm":        "Basic",
				"debug":                       "false",
			}
			Expect(k8sClient.Update(ctx, instance)).To(Succeed())

			By("Reconciling again to trigger a deployment update")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the spec hash changed")
			Eventually(func() string {
				updated := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, updated); err != nil {
					return ""
				}
				return updated.Annotations[keyPrefix+"/specHash"]
			}, time.Minute, time.Second).ShouldNot(Equal(initialHash))
		})
	})

	// Phase 4: service passthrough and NetworkPolicy reconciliation.
	Context("When reconciling with service passthrough and networkpolicy", func() {
		const SchemaRegistryName = "test-service-networkpolicy"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "service-networkpolicy-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.svc-networkpolicy.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Service: &strimziregistryoperatorv1alpha1.Service{
						Annotations: map[string]string{"prometheus.io/scrape": "true"},
						Labels:      map[string]string{"monitoring.example.com/team": "schemas"},
					},
					NetworkPolicy: &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true},
					Template:      corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should create the service with passthrough metadata and a NetworkPolicy", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("Reconciling until the deployment, service, and NetworkPolicy are created")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the service carries the passthrough annotations and labels")
			Eventually(func() error {
				svc := &corev1.Service{}
				return k8sClient.Get(ctx, typeNamespacedName, svc)
			}, time.Minute, time.Second).Should(Succeed())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, svc)).To(Succeed())
			Expect(svc.Annotations["prometheus.io/scrape"]).To(Equal("true"))
			Expect(svc.Labels["monitoring.example.com/team"]).To(Equal("schemas"))

			By("Checking the NetworkPolicy was created with ingress and egress rules")
			Eventually(func() error {
				np := &networkingv1.NetworkPolicy{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-network-policy", Namespace: SchemaRegistryName}, np)
			}, time.Minute, time.Second).Should(Succeed())

			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-network-policy", Namespace: SchemaRegistryName}, np)).To(Succeed())
			Expect(np.Spec.Ingress).To(HaveLen(1))
			Expect(np.Spec.Ingress[0].Ports).To(HaveLen(1))
			Expect(np.Spec.Ingress[0].Ports[0].Port.IntVal).To(Equal(int32(8081)))
			Expect(np.Spec.Egress).To(HaveLen(3))
			Expect(np.Spec.Egress[0].To).To(HaveLen(1))
			Expect(np.Spec.Egress[0].To[0].PodSelector.MatchLabels["strimzi.io/cluster"]).To(Equal("kafka-cluster"))
			// DNS egress to kube-dns is required for bootstrap name resolution.
			Expect(np.Spec.Egress[1].Ports).To(HaveLen(2))
			Expect(np.Spec.Egress[1].Ports[0].Port.IntVal).To(Equal(int32(53)))
			Expect(np.Spec.Egress[1].To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]).To(Equal("kube-system"))
			// Inter-instance egress is required for multi-replica peer
			// forwarding: replicas advertise their own pod IP as host.name.
			Expect(np.Spec.Egress[2].To[0].PodSelector.MatchLabels["app"]).To(Equal(SchemaRegistryName))
			Expect(np.Spec.Egress[2].Ports[0].Port.IntVal).To(Equal(int32(8081)))
		})
	})

	// Owned-watch coverage for the managed Service: manual deletion must be
	// recoverable by a subsequent reconciliation, which recreates the Service
	// with the passthrough metadata. The spec is self-contained (own
	// namespace, DeferCleanup) like the other single-spec contexts, because
	// the shared BeforeEach fixtures cannot re-create their namespace object
	// for a second spec.
	Context("When the managed Service is deleted", func() {
		const SchemaRegistryName = "test-service-delete"

		ctx := context.Background()

		It("should recreate the Service with the passthrough metadata on the next reconcile", func() {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "service-delete-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE") })

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Service: &strimziregistryoperatorv1alpha1.Service{
						Annotations: map[string]string{"prometheus.io/scrape": "true"},
						Labels:      map[string]string{"monitoring.example.com/team": "schemas"},
					},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("Reconciling until the service exists")
			for i := 0; i < 3; i++ {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespacedName, &corev1.Service{})
			}, time.Minute, time.Second).Should(Succeed())

			By("Deleting the managed Service")
			Expect(k8sClient.Delete(ctx, &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName},
			})).To(Succeed())
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, typeNamespacedName, &corev1.Service{}))
			}, time.Minute, time.Second).Should(BeTrue())

			By("Reconciling and expecting the Service to be recreated with the passthrough metadata")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespacedName, &corev1.Service{})
			}, time.Minute, time.Second).Should(Succeed())

			recreated := &corev1.Service{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, recreated)).To(Succeed())
			Expect(recreated.Annotations["prometheus.io/scrape"]).To(Equal("true"))
			Expect(recreated.Labels["monitoring.example.com/team"]).To(Equal("schemas"))
		})
	})

	// Owned-watch coverage for the managed NetworkPolicy: manual deletion of
	// the policy created for spec.networkpolicy.enabled must be recoverable
	// by a subsequent reconciliation. Self-contained (own namespace,
	// DeferCleanup) for the same reason as the Service-deletion context.
	Context("When the managed NetworkPolicy is deleted", func() {
		const SchemaRegistryName = "test-networkpolicy-delete"

		ctx := context.Background()

		It("should recreate the NetworkPolicy on the next reconcile", func() {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
			npName := types.NamespacedName{Name: SchemaRegistryName + "-network-policy", Namespace: SchemaRegistryName}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "networkpolicy-delete-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE") })

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					NetworkPolicy:      &strimziregistryoperatorv1alpha1.NetworkPolicy{Enabled: true},
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("Reconciling until the NetworkPolicy exists")
			for i := 0; i < 3; i++ {
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}
			Eventually(func() error {
				return k8sClient.Get(ctx, npName, &networkingv1.NetworkPolicy{})
			}, time.Minute, time.Second).Should(Succeed())

			By("Deleting the managed NetworkPolicy")
			Expect(k8sClient.Delete(ctx, &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: npName.Name, Namespace: npName.Namespace},
			})).To(Succeed())
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, npName, &networkingv1.NetworkPolicy{}))
			}, time.Minute, time.Second).Should(BeTrue())

			By("Reconciling and expecting the NetworkPolicy to be recreated")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() error {
				return k8sClient.Get(ctx, npName, &networkingv1.NetworkPolicy{})
			}, time.Minute, time.Second).Should(Succeed())

			recreated := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, npName, recreated)).To(Succeed())
			Expect(recreated.Spec.Ingress).To(HaveLen(1))
			Expect(recreated.Spec.Egress).To(HaveLen(3))
		})
	})

	// Phase 3: REST endpoint mutual TLS.
	Context("When reconciling with clientauth enabled", func() {
		const SchemaRegistryName = "test-clientauth"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		var clusterCA *testutil.TestCA

		BeforeEach(func() {
			var err error
			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-CLIENTAUTH")
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "clientauth-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.clientauth.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: SchemaRegistryName},
				Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: SchemaRegistryName},
				Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
			})).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					ClientAuth:         true,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should mount the truststore and set client auth env vars", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the deployment was created")
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, &appsv1.Deployment{})
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env

			clientAuth, ok := envByName(envVars, "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION")
			Expect(ok).To(BeTrue())
			Expect(clientAuth.Value).To(Equal("REQUIRED"))

			truststoreLocation, ok := envByName(envVars, "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION")
			Expect(ok).To(BeTrue())
			Expect(truststoreLocation.Value).To(Equal("/var/schemaregistry/truststore.jks"))

			_, truststorePasswordOK := envByName(envVars, "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD")
			Expect(truststorePasswordOK).To(BeTrue())

			By("Verifying the cluster-CA truststore volume is mounted")
			_, tlsVolumeOK := volumeByName(deployment.Spec.Template.Spec.Volumes, "tls")
			Expect(tlsVolumeOK).To(BeTrue())

			By("Verifying the client-auth truststore secret was created")
			jks := &corev1.Secret{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}, jks)
			}, time.Minute*2, time.Second).Should(Succeed())
			Expect(jks.Data).To(HaveKey("truststore.jks"))
			Expect(jks.Data).To(HaveKey("truststore_password"))
		})
	})

	// Owned-watch coverage for the operator-managed JKS secret: manual
	// deletion of the <name>-jks truststore secret must be observed and the
	// secret regenerated by a subsequent reconciliation, while the secret's
	// own Create/Update events stay suppressed (no reconcile loop).
	// Self-contained (own namespace, DeferCleanup) for the same reason as the
	// Service-deletion context.
	Context("When the operator-managed JKS secret is deleted", func() {
		const SchemaRegistryName = "test-jks-secret-delete"

		ctx := context.Background()

		It("should regenerate the secret on the next reconcile without re-enqueuing its own events", func() {
			var err error
			clusterCA, err := testutil.GenerateClusterCACert("STIMZI-SR-JKS-DELETE")
			Expect(err).NotTo(HaveOccurred())

			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
			jksName := types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if getErr := k8sClient.Get(ctx, typeNamespacedName, found); getErr == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "jks-delete-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: SchemaRegistryName},
				Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: SchemaRegistryName},
				Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
			})).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			DeferCleanup(func() { _ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE") })

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					ClientAuth:         true,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Waiting for the operator-managed JKS secret")
			Eventually(func() error {
				return k8sClient.Get(ctx, jksName, &corev1.Secret{})
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Deleting the JKS secret")
			Expect(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: jksName.Name, Namespace: jksName.Namespace},
			})).To(Succeed())
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, jksName, &corev1.Secret{}))
			}, time.Minute, time.Second).Should(BeTrue())

			By("Reconciling and expecting the secret to be regenerated")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() error {
				return k8sClient.Get(ctx, jksName, &corev1.Secret{})
			}, time.Minute*2, time.Second).Should(Succeed())

			recreated := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, jksName, recreated)).To(Succeed())
			Expect(recreated.Data).To(HaveKey("truststore.jks"))
			Expect(recreated.Data).To(HaveKey("truststore_password"))

			By("Verifying the regenerated secret is controller-owned and its own Create/Update events stay suppressed")
			owner := metav1.GetControllerOf(recreated)
			Expect(owner).NotTo(BeNil())
			Expect(owner.Kind).To(Equal("StrimziSchemaRegistry"))
			p := secretWatchPredicate()
			Expect(p.Create(event.CreateEvent{Object: recreated})).To(BeFalse())
			Expect(p.Update(event.UpdateEvent{ObjectOld: recreated, ObjectNew: recreated})).To(BeFalse())
			Expect(p.Delete(event.DeleteEvent{Object: recreated})).To(BeTrue())

			By("Verifying a Delete event maps back to the owning CR")
			requests := controllerReconciler.mapSecretToRequests(ctx, recreated)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal(SchemaRegistryName))
			Expect(requests[0].Namespace).To(Equal(SchemaRegistryName))
		})
	})

	// Phase 3: standard kubernetes.io/tls user secrets are accepted and
	// converted to a JKS keystore in an operator-managed derived secret
	// (<tlssecretname>-jks), which is what the Deployment mounts.
	Context("When using a kubernetes.io/tls user TLS secret", func() {
		const SchemaRegistryName = "test-pem-user-tls"
		const UserTLSSecretName = "cert-manager-tls"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		var userTLSSecret *corev1.Secret

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: SchemaRegistryName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "pem-user-tls-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap." + SchemaRegistryName + ".svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)).To(Succeed())
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			// A real certificate and key so the PEM → JKS conversion runs the
			// actual pure-Go conversion pipeline.
			ca, err := testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())
			uc, err := testutil.GenerateTestUserCert(ca, "")
			Expect(err).NotTo(HaveOccurred())

			userTLSSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      UserTLSSecretName,
					Namespace: SchemaRegistryName,
				},
				// The type cert-manager issues by default.
				Type: corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte(uc.UserCertPEM),
					"tls.key": []byte(uc.UserKeyPEM),
				},
			}
			Expect(k8sClient.Create(ctx, userTLSSecret)).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					TLSSecretName:      UserTLSSecretName,
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should convert the PEM secret and mount the derived JKS secret", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the derived JKS secret was created with the JKS contract")
			derived := &corev1.Secret{}
			// The derived secret is named per registry:
			// <registry>-<tlssecretname>-jks.
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-" + UserTLSSecretName + "-jks", Namespace: SchemaRegistryName}, derived)
			}, time.Minute, time.Second).Should(Succeed())
			Expect(derived.Data).To(HaveKey("tls-keystore.jks"))
			Expect(derived.Data).To(HaveKey("keystore_password"))
			Expect(derived.Data).To(HaveKey("key_password"))
			Expect(derived.Annotations[userSecretVersionKey]).NotTo(BeEmpty())

			By("Checking the user secret resourceVersion is tracked for the rollout")
			userSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: UserTLSSecretName, Namespace: SchemaRegistryName}, userSecret)).To(Succeed())

			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)
			}, time.Minute, time.Second).Should(Succeed())

			By("Checking the Deployment mounts the derived secret")
			volumeMounted := false
			for _, vol := range deployment.Spec.Template.Spec.Volumes {
				if vol.Name == restAPITLSVolumeName && vol.Secret != nil {
					volumeMounted = true
					Expect(vol.Secret.SecretName).To(Equal(SchemaRegistryName + "-" + UserTLSSecretName + "-jks"))
				}
			}
			Expect(volumeMounted).To(BeTrue())
			for _, envName := range []string{sslKeystorePasswordEnv, sslKeyPasswordEnv} {
				found := false
				for _, e := range deployment.Spec.Template.Spec.Containers[0].Env {
					if e.Name == envName {
						found = true
						Expect(e.ValueFrom).NotTo(BeNil())
						Expect(e.ValueFrom.SecretKeyRef.Name).To(Equal(SchemaRegistryName + "-" + UserTLSSecretName + "-jks"))
					}
				}
				Expect(found).To(BeTrue())
			}
			Expect(deployment.Spec.Template.Annotations[tlsVersionKey]).To(Equal(userSecret.ResourceVersion))

			By("Checking a second reconcile is stable (no derived-secret churn)")
			derivedVersion := derived.ResourceVersion
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			stable := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-" + UserTLSSecretName + "-jks", Namespace: SchemaRegistryName}, stable)).To(Succeed())
			Expect(stable.ResourceVersion).To(Equal(derivedVersion))
		})
	})

	// Phase 3: clientauth validation. The clientauth-without-securehttp
	// combination is now rejected by the CRD CEL validation rules at admission
	// time, so it can no longer be created through the API server. The
	// controller-side defense-in-depth behavior (InvalidSpec,
	// ClientAuthRequiresSecureHTTP) is covered by the fake-client test
	// TestReconcileSkipsInvalidClientAuthCombination in k8s_test.go, and the
	// admission-time rejection by crdvalidation_test.go.

	// Phase 3 (events): the recorded validation events must be persisted by
	// the API server with a non-empty action. The production recorder
	// (mgr.GetEventRecorder) records events.k8s.io/v1 Events, which the API
	// server rejects with a 422 when the action is empty, so a persisted
	// event proves both the RBAC-visible create path and the action field.
	// The spec is self-contained (own namespace, DeferCleanup) like the
	// multi-spec contexts above, because the shared BeforeEach fixtures
	// cannot re-create their namespace object for a second spec.
	Context("When recording validation events for an invalid user TLS secret", func() {
		const SchemaRegistryName = "test-tlssecret-events"

		ctx := context.Background()

		It("should persist the validation events with a non-empty action on the API server", func() {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
			typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
					if len(found.Finalizers) > 0 {
						found.Finalizers = []string{}
						_ = k8sClient.Update(ctx, found)
					}
					_ = k8sClient.Delete(ctx, found)
				}
				_ = k8sClient.Delete(ctx, namespace)
			})

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: SchemaRegistryName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:    true,
					TLSSecretName: "missing-user-tls-secret",
					Listener:      "plain",
					Template:      corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			// Wire a real recorder to the test API server the same way the
			// manager does (the client-go FakeRecorder used elsewhere
			// flattens events and drops the action).
			cs, err := kubernetes.NewForConfig(cfg)
			Expect(err).NotTo(HaveOccurred())
			broadcaster := eventsfake.NewBroadcaster(&eventsfake.EventSinkImpl{Interface: cs.EventsV1()})
			sinkCtx, sinkCancel := context.WithCancel(context.Background())
			defer sinkCancel()
			Expect(broadcaster.StartRecordingToSinkWithContext(sinkCtx)).To(Succeed())
			rec := broadcaster.NewRecorder(k8sClient.Scheme(), "test-events-controller")
			// The concrete recorder implements both Eventf and
			// AnnotatedEventf, but its static EventRecorderLogger interface
			// does not declare AnnotatedEventf; assert and combine the two
			// interfaces the same way controller-runtime's lazyRecorder does.
			annotated, ok := rec.(eventsfake.AnnotatedEventRecorder)
			Expect(ok).To(BeTrue())
			recorder := struct {
				eventsfake.EventRecorderLogger
				eventsfake.AnnotatedEventRecorder
			}{rec, annotated}

			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: recorder}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// The validation event must exist with the non-empty Validate
			// action; Eventually absorbs the broadcaster's asynchronous
			// write to the sink.
			actionFor := func(reason string) func() string {
				return func() string {
					list := &eventsv1.EventList{}
					if err := k8sClient.List(ctx, list, client.InNamespace(SchemaRegistryName)); err != nil {
						return ""
					}
					for _, ev := range list.Items {
						if ev.Reason == reason {
							return ev.Action
						}
					}
					return ""
				}
			}
			// The InvalidTLSSecret path emits a single validation event (no
			// ReconciliationSkipped event: the Secret watch re-enqueues
			// reconciliation when the user fixes the secret).
			Eventually(actionFor("InvalidTLSSecret"), time.Minute, time.Second).Should(Equal("Validate"))
		})
	})

	// Phase 3 (B7): reconcile errors must be surfaced on the CR status. A
	// missing KafkaUser for the SSL security protocol fails reconciliation
	// with backoff; the CR must show Ready=False/ReconcileError carrying the
	// error message so `kubectl get strimzischemaregistry` explains why
	// nothing is happening instead of leaving the Ready condition stale.
	Context("When the KafkaUser is missing for an SSL Schema Registry", func() {
		const SchemaRegistryName = "test-missing-kafkauser"

		ctx := context.Background()

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName}}
		typeNamespacedName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating a strimzi kafka cluster with a tls listener")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster",
					Namespace: SchemaRegistryName,
				},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{
						TopicOperator: &kafka.EntityTopicOperatorSpec{},
						UserOperator:  &kafka.EntityUserOperatorSpec{},
					},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{
							SuperUsers: []string{"CN=root"},
							Type:       kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE),
						},
						Config: kafka.MapStringObject{
							"apple":   5,
							"lettuce": 7,
						},
						Listeners: []kafka.GenericKafkaListener{
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
			status := &kafka.KafkaStatus{
				ClusterId: "Ypb68J1GSu-jqq0N0ama0w",
				Listeners: []kafka.ListenerStatus{
					{
						Addresses: []kafka.ListenerAddress{
							{
								Host: "kafka-cluster-kafka-bootstrap.kafka.svc",
								Port: ptr.To(int32(9093)),
							},
						},
						BootstrapServers: "kafka-cluster-kafka-bootstrap.kafka.svc:9093",
						Name:             "tls",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updatedCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updatedCluster)).To(Succeed())
			updatedCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updatedCluster)).To(Succeed())

			By("Creating a StrimziSchemaRegistry with the SSL protocol and no KafkaUser")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: SchemaRegistryName,
					Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
				},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					Listener:           "tls",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
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

		It("should set Ready=False with the ReconcileError reason and the error message", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).To(HaveOccurred())

			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())

			cond := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("ReconcileError"))
			Expect(cond.Message).To(ContainSubstring("KafkaUser"))
			Expect(instance.Status.Status).To(Equal("ReconcileError"))

			By("Verifying no Deployment was created")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).NotTo(Succeed())
		})
	})

	// Phase 1: user-provided TLS secret validation. A missing or incomplete
	// user TLS secret must surface as Ready=False/InvalidTLSSecret and block
	// Deployment creation; fixing the secret lets reconciliation proceed.
	Context("When using an invalid user-provided TLS secret", func() {
		const SchemaRegistryName = "test-invalid-user-tls"
		const UserTLSSecretName = "user-tls-secret"

		ctx := context.Background()

		// envtest never fully deletes namespaces (no namespace controller), so
		// each spec gets its own uniquely named namespace.
		specNamespace := ""
		namespaceCounter := 0
		var typeNamespacedName types.NamespacedName
		var deploymentNamespacedName types.NamespacedName

		BeforeEach(func() {
			specNamespace = SchemaRegistryName + "-" + strconv.Itoa(namespaceCounter)
			namespaceCounter++
			typeNamespacedName = types.NamespacedName{Name: SchemaRegistryName, Namespace: specNamespace}
			deploymentNamespacedName = types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: specNamespace}

			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: specNamespace}}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating strimzi kafka cluster")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster",
					Namespace: namespace.Name,
				},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.EntityOperatorSpec{
						TopicOperator: &kafka.EntityTopicOperatorSpec{},
						UserOperator:  &kafka.EntityUserOperatorSpec{},
					},
					Kafka: &kafka.KafkaClusterSpec{
						Authorization: &kafka.KafkaAuthorization{
							SuperUsers: []string{"CN=root"},
							Type:       kafka.KafkaAuthorizationType(kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE),
						},
						Config: kafka.MapStringObject{
							"apple":   5,
							"lettuce": 7,
						},
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
			status := &kafka.KafkaStatus{
				ClusterId: "Ypb68J1GSu-jqq0N0ama0w",
				Listeners: []kafka.ListenerStatus{
					{
						Addresses: []kafka.ListenerAddress{
							{
								Host: "kafka-cluster-kafka-bootstrap.kafka.svc",
								Port: ptr.To(int32(9092)),
							},
						},
						BootstrapServers: "kafka-cluster-kafka-bootstrap.kafka.svc:9092",
						Name:             "plain",
					},
					{
						Addresses: []kafka.ListenerAddress{
							{
								Host: "kafka-cluster-kafka-bootstrap.kafka.svc",
								Port: ptr.To(int32(9093)),
							},
						},
						BootstrapServers: "kafka-cluster-kafka-bootstrap.kafka.svc:9093",
						Name:             "TLS",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updatedCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: specNamespace}, updatedCluster)).To(Succeed())
			updatedCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updatedCluster)).To(Succeed())

			By("Creating a StrimziSchemaRegistry referencing a missing user TLS secret")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: specNamespace,
					Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
				},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         true,
					TLSSecretName:      UserTLSSecretName,
					Listener:           "plain",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "PLAINTEXT",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: specNamespace}})
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should set Ready=False with the InvalidTLSSecret reason and not create the Deployment", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())

			cond := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidTLSSecret"))
			Expect(cond.Message).To(ContainSubstring(UserTLSSecretName))
			Expect(instance.Status.Status).To(Equal("InvalidTLSSecret"))

			By("Verifying no Deployment was created")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentNamespacedName, deployment)).NotTo(Succeed())
		})

		It("should proceed with reconciliation once the secret is fixed", func() {
			By("Reconciling with the missing secret first")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Creating a valid user TLS secret with decodable JKS material")
			ca, err := testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())
			userCert, err := testutil.GenerateTestUserCert(ca, "store-password")
			Expect(err).NotTo(HaveOccurred())
			keystoreBytes, _, err := certprocessor.NewCertProcessor(logr.Logger{}).ConvertPEMToJKS(
				ctx, string(userCert.UserCertPEM), string(userCert.UserKeyPEM), string(ca.CACertPEM), "store-password")
			Expect(err).NotTo(HaveOccurred())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: UserTLSSecretName, Namespace: specNamespace},
				Data: map[string][]byte{
					"tls-keystore.jks":  keystoreBytes,
					"keystore_password": []byte("store-password"),
					"key_password":      []byte("store-password"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			secretVersion := secret.ResourceVersion
			Expect(secretVersion).NotTo(BeEmpty())

			By("Reconciling again — the Deployment must now be created")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentNamespacedName, deployment)).To(Succeed())

			By("Verifying the deployment records the user TLS secret version for rotation")
			Expect(deployment.Spec.Template.Annotations[tlsVersionKey]).To(Equal(secretVersion))

			// The post-create reconcile returns early with a requeue, so the
			// Ready condition is refreshed on the following reconcile.
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Ready condition is no longer InvalidTLSSecret")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			cond := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).NotTo(Equal("InvalidTLSSecret"))
			Expect(instance.Status.Status).NotTo(Equal("InvalidTLSSecret"))
		})

		// T-5 (H-1): pre-built keystore material must be decoded at validation
		// time. Garbage bytes used to pass validation (keys present) and only
		// surfaced as a container crash loop with no status signal; now they
		// produce InvalidTLSSecret + a Warning event and no Deployment, and
		// fixing the secret converges without an operator restart.
		It("should reject garbage pre-built keystore material and converge once it is fixed (T-5)", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("Creating a user TLS secret whose JKS bytes are garbage")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: UserTLSSecretName, Namespace: specNamespace},
				Data: map[string][]byte{
					"tls-keystore.jks":  []byte("not-a-keystore"),
					"keystore_password": []byte("store-password"),
					"key_password":      []byte("store-password"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Reconciling: the garbage keystore must route to InvalidTLSSecret")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			cond := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidTLSSecret"))
			Expect(cond.Message).To(ContainSubstring("tls-keystore.jks"))
			Expect(instance.Status.Status).To(Equal("InvalidTLSSecret"))

			By("Verifying no Deployment was created")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deploymentNamespacedName, deployment)).NotTo(Succeed())

			By("Fixing the secret with decodable JKS material")
			ca, err := testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())
			userCert, err := testutil.GenerateTestUserCert(ca, "store-password")
			Expect(err).NotTo(HaveOccurred())
			keystoreBytes, _, err := certprocessor.NewCertProcessor(logr.Logger{}).ConvertPEMToJKS(
				ctx, string(userCert.UserCertPEM), string(userCert.UserKeyPEM), string(ca.CACertPEM), "store-password")
			Expect(err).NotTo(HaveOccurred())
			secret.Data["tls-keystore.jks"] = keystoreBytes
			Expect(k8sClient.Update(ctx, secret)).To(Succeed())

			By("Reconciling: the Deployment must now be created")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, deploymentNamespacedName, deployment)).To(Succeed())

			By("Reconciling again so the Ready condition leaves InvalidTLSSecret")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			cond = meta.FindStatusCondition(instance.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).NotTo(Equal("InvalidTLSSecret"))
			Expect(instance.Status.Status).NotTo(Equal("InvalidTLSSecret"))
		})
	})

	// Phase 4 (plan §2.10 L5): the HeapOpts pattern must accept plain digits
	// only — hex letters like "12a" are rejected by CRD validation at the API
	// server. F7 loosened the pattern: "-Xms<size>" and "-Xmx<size>" are also
	// accepted alone (either unit case, flexible spacing), while garbage,
	// unit-less sizes and the reversed option order stay rejected.
	Context("When validating the HeapOpts pattern at the API server", func() {
		ctx := context.Background()

		It("rejects invalid heap sizes and accepts the documented forms", func() {
			namespaceName := "heapopts-validation"
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, namespace)
			})

			newCR := func(name, heapOpts string) *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
				return &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespaceName},
					Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
						SecureHTTP: false,
						HeapOpts:   heapOpts,
						Template:   corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
					},
				}
			}

			By("Rejecting heap sizes with hex letters (e.g. 12a)")
			err := k8sClient.Create(ctx, newCR("sr-hex-heap", "-Xms12aM -Xmx12aM"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("heapopts"))

			By("Rejecting non-numeric heap sizes (abc)")
			err = k8sClient.Create(ctx, newCR("sr-abc-heap", "-XmsabcM -XmxabcM"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("heapopts"))

			By("Accepting plain digit heap sizes")
			Expect(k8sClient.Create(ctx, newCR("sr-digit-heap", "-Xms1024M -Xmx1024M"))).To(Succeed())

			By("Accepting the empty HeapOpts (operator default applies)")
			Expect(k8sClient.Create(ctx, newCR("sr-empty-heap", ""))).To(Succeed())

			By("Accepting -Xmx alone (F7)")
			Expect(k8sClient.Create(ctx, newCR("sr-xmx-only-heap", "-Xmx1g"))).To(Succeed())

			By("Accepting -Xms alone (F7)")
			Expect(k8sClient.Create(ctx, newCR("sr-xms-only-heap", "-Xms512m"))).To(Succeed())

			By("Accepting k/K units and multiple spaces (F7)")
			Expect(k8sClient.Create(ctx, newCR("sr-units-heap", "-Xms1K  -Xmx2048k"))).To(Succeed())

			By("Rejecting garbage values (F7)")
			err = k8sClient.Create(ctx, newCR("sr-garbage-heap", "foo"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("heapopts"))

			By("Rejecting unit-less heap sizes (F7)")
			err = k8sClient.Create(ctx, newCR("sr-unitless-heap", "-Xmx1024"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("heapopts"))

			By("Rejecting the reversed -Xmx ... -Xms order (F7)")
			err = k8sClient.Create(ctx, newCR("sr-reversed-heap", "-Xmx1g -Xms512m"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("heapopts"))
		})
	})

	// F1 regression: the Deployment selector must contain stable identity
	// labels only. Changing the container image (which drives the mutable
	// app.kubernetes.io/version label) must update the Deployment in place,
	// and a Deployment created before the fix (mutable selector) must be
	// migrated by delete+re-create instead of failing with "spec.selector:
	// field is immutable".
	Context("When changing the container image of an existing Deployment", func() {
		ctx := context.Background()
		specCounter := 0

		var (
			namespaceName      string
			typeNamespacedName types.NamespacedName
		)

		newCR := func(image string) *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
			return &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      namespaceName,
					Namespace: namespaceName,
					Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
				},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: false,
					Listener:   "plain",
					// PLAINTEXT keeps the scenario free of KafkaUser and
					// CA-secret fixtures: the image change exercises the
					// Deployment update path only.
					SecurityProtocol: "PLAINTEXT",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: image}}},
					},
				},
			}
		}

		reconcileCR := func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
		}

		BeforeEach(func() {
			// A unique namespace per spec: envtest has no namespace
			// controller, so a deleted namespace stays terminating and a
			// same-name create would fail in the next spec.
			specCounter++
			namespaceName = "test-image-change-" + strconv.Itoa(specCounter)
			typeNamespacedName = types.NamespacedName{Name: namespaceName, Namespace: namespaceName}

			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())

			By("Creating strimzi kafka cluster with a plain listener")
			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespaceName},
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
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: namespaceName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "image-change-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.image-change.svc:9092", Name: "plain"},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
			Expect(k8sClient.Create(ctx, newCR("confluentinc/cp-schema-registry:7.6.1"))).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should update the Deployment in place when the image changes", func() {
			deployName := types.NamespacedName{Name: namespaceName + "-deploy", Namespace: namespaceName}

			By("Reconciling to create the Deployment with image A")
			reconcileCR()
			Eventually(func() error {
				return k8sClient.Get(ctx, deployName, &appsv1.Deployment{})
			}, time.Minute, time.Second).Should(Succeed())

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deployName, deployment)).To(Succeed())
			Expect(deployment.Spec.Selector.MatchLabels).To(Equal(selectorLabelsForStrimziSchemaRegistryOperator(namespaceName)))
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal("confluentinc/cp-schema-registry:7.6.1"))
			originalUID := deployment.UID

			By("Changing the CR image to B")
			cr := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, cr)).To(Succeed())
			cr.Spec.Template.Spec.Containers[0].Image = "confluentinc/cp-schema-registry:8.4.1"
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			By("Reconciling: the Deployment must update, not fail on the immutable selector")
			reconcileCR()

			updated := &appsv1.Deployment{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, deployName, updated); err != nil {
					return ""
				}
				return updated.Spec.Template.Spec.Containers[0].Image
			}, time.Minute, time.Second).Should(Equal("confluentinc/cp-schema-registry:8.4.1"))

			Expect(k8sClient.Get(ctx, deployName, updated)).To(Succeed())
			// Same UID: the Deployment was updated in place, not recreated.
			Expect(updated.UID).To(Equal(originalUID))
			Expect(updated.Spec.Selector.MatchLabels).To(Equal(selectorLabelsForStrimziSchemaRegistryOperator(namespaceName)))
			// The mutable labels remain on the pod template (they drive the rollout).
			Expect(updated.Spec.Template.Labels["app.kubernetes.io/version"]).To(Equal("8.4.1"))
		})

		It("should recreate a legacy-selector Deployment instead of failing on the immutable selector", func() {
			deployName := types.NamespacedName{Name: namespaceName + "-deploy", Namespace: namespaceName}

			By("Reconciling to create the Deployment")
			reconcileCR()
			Eventually(func() error {
				return k8sClient.Get(ctx, deployName, &appsv1.Deployment{})
			}, time.Minute, time.Second).Should(Succeed())

			By("Replacing it with a pre-fix Deployment whose selector contains mutable labels")
			legacySelector := map[string]string{
				"app":                          namespaceName,
				"strimzi-schema-registry":      namespaceName,
				"app.kubernetes.io/instance":   namespaceName,
				"app.kubernetes.io/name":       "strimzischemaregistry",
				"app.kubernetes.io/managed-by": "strimzi-registry-operator",
				"app.kubernetes.io/part-of":    namespaceName,
				"app.kubernetes.io/version":    "7.6.1",
				"strimzi.io/cluster":           "kafka-cluster",
			}
			legacy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deployName, legacy)).To(Succeed())
			legacy.Spec.Selector = &metav1.LabelSelector{MatchLabels: legacySelector}
			legacy.Spec.Template.Labels = legacySelector
			// The selector is immutable, so the pre-fix object can only be
			// produced by deleting and re-creating it.
			Expect(k8sClient.Delete(ctx, legacy)).To(Succeed())
			legacy.ResourceVersion = ""
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
			legacyDeployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deployName, legacyDeployment)).To(Succeed())
			legacyUID := legacyDeployment.UID

			By("Changing the CR image to B")
			cr := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, cr)).To(Succeed())
			cr.Spec.Template.Spec.Containers[0].Image = "confluentinc/cp-schema-registry:8.4.1"
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			By("Reconciling with an event recorder: the legacy Deployment must be recreated")
			recorder := eventsfake.NewFakeRecorder(16)
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: recorder}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment was recreated with the stable selector and image B")
			migrated := &appsv1.Deployment{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, deployName, migrated); err != nil {
					return ""
				}
				return migrated.Spec.Template.Spec.Containers[0].Image
			}, time.Minute, time.Second).Should(Equal("confluentinc/cp-schema-registry:8.4.1"))
			Expect(k8sClient.Get(ctx, deployName, migrated)).To(Succeed())
			Expect(migrated.UID).NotTo(Equal(legacyUID))
			Expect(migrated.Spec.Selector.MatchLabels).To(Equal(selectorLabelsForStrimziSchemaRegistryOperator(namespaceName)))
			Expect(migrated.Spec.Selector.MatchLabels).NotTo(HaveKey("app.kubernetes.io/version"))
			Expect(migrated.Spec.Selector.MatchLabels).NotTo(HaveKey("strimzi.io/cluster"))

			By("Verifying the DeploymentRecreated event was emitted")
			Eventually(func() string {
				select {
				case e := <-recorder.Events:
					return e
				default:
					return ""
				}
			}, time.Minute, time.Second).Should(ContainSubstring("DeploymentRecreated"))
		})
	})

	// F2: a Strimzi cluster CA rotation must renew the operator-managed REST
	// API TLS certificate in every auth mode — here SASL_SSL with
	// securehttp — not only mTLS. The -tls secret records the signing CA
	// cert secret's resourceVersion; handleTLSCertExpiry renews when it
	// differs and rolls the deployment via the tlsVersion annotation.
	Context("When the cluster CA rotates in SASL_SSL mode with securehttp", func() {
		ctx := context.Background()
		specCounter := 0

		var (
			namespaceName      string
			typeNamespacedName types.NamespacedName
			clusterCA          *testutil.TestCA
		)

		BeforeEach(func() {
			var err error
			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-CA-ROTATION")
			Expect(err).NotTo(HaveOccurred())

			specCounter++
			namespaceName = "test-ca-rotation-" + strconv.Itoa(specCounter)
			typeNamespacedName = types.NamespacedName{Name: namespaceName, Namespace: namespaceName}

			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())

			cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster", Namespace: namespaceName},
				Spec: &kafka.KafkaSpec{
					Kafka: &kafka.KafkaClusterSpec{
						Listeners: []kafka.GenericKafkaListener{
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE,
								Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.SCRAM_SHA_512_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: namespaceName}, updateCluster)).To(Succeed())
			updateCluster.Status = &kafka.KafkaStatus{
				ClusterId: "ca-rotation-cluster",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.ca-rotation.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
				},
			}
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceName, Namespace: namespaceName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec:       &kafka.KafkaUserSpec{Authentication: &kafka.KafkaUserAuthentication{Type: kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE}},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA cert and key secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespaceName},
				Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespaceName},
				Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
			})).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceName, Namespace: namespaceName},
				Data: map[string][]byte{
					"password":         []byte("scram-password"),
					"sasl.jaas.config": []byte(`org.apache.kafka.common.security.scram.ScramLoginModule required username="` + namespaceName + `" password="scram-password";`),
				},
			})).To(Succeed())

			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceName, Namespace: namespaceName, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:       true,
					Listener:         "TLS",
					SecurityProtocol: "SASL_SSL",
					Template:         corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			if err := k8sClient.Get(ctx, typeNamespacedName, found); err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should renew the -tls secret and roll the Deployment after the cluster CA rotates", func() {
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			By("First reconcile to create the Deployment, -jks and -tls secrets")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			tlsName := types.NamespacedName{Name: namespaceName + "-tls", Namespace: namespaceName}
			Eventually(func() error {
				return k8sClient.Get(ctx, tlsName, &corev1.Secret{})
			}, time.Minute*2, time.Second).Should(Succeed())

			tlsSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, tlsName, tlsSecret)).To(Succeed())
			oldTLSResourceVersion := tlsSecret.ResourceVersion
			caCertName := types.NamespacedName{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespaceName}
			caCertSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, caCertName, caCertSecret)).To(Succeed())
			Expect(tlsSecret.Annotations[CAVersionKey]).To(Equal(caCertSecret.ResourceVersion))

			deployName := types.NamespacedName{Name: namespaceName + "-deploy", Namespace: namespaceName}
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, deployName, deployment)).To(Succeed())
			oldTLSVersionAnnotation := deployment.Spec.Template.Annotations[tlsVersionKey]

			By("Rotating the cluster CA (new cert and key bump the resourceVersions)")
			newClusterCA, err := testutil.GenerateClusterCACert("STIMZI-SR-CA-ROTATION-V2")
			Expect(err).NotTo(HaveOccurred())
			caCertSecret.Data["ca.crt"] = []byte(newClusterCA.CACertPEM)
			Expect(k8sClient.Update(ctx, caCertSecret)).To(Succeed())
			// The CA key must also be updated to match the new cert, otherwise
			// x509 signing will fail with "provided PrivateKey doesn't match
			// parent's PublicKey".
			caKeySecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster-cluster-ca", Namespace: namespaceName}, caKeySecret)).To(Succeed())
			caKeySecret.Data["ca.key"] = []byte(newClusterCA.CAKeyPEM)
			Expect(k8sClient.Update(ctx, caKeySecret)).To(Succeed())

			By("Reconciling: the SASL_SSL rotation pass rebuilds the truststore")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Reconciling again: handleTLSCertExpiry renews the -tls secret on the CA change")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that the -tls secret was renewed for the new CA")
			renewed := &corev1.Secret{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, tlsName, renewed); err != nil {
					return ""
				}
				return renewed.ResourceVersion
			}, time.Minute*2, time.Second).ShouldNot(Equal(oldTLSResourceVersion))
			Expect(k8sClient.Get(ctx, caCertName, caCertSecret)).To(Succeed())
			Expect(renewed.Annotations[CAVersionKey]).To(Equal(caCertSecret.ResourceVersion))
			Expect(renewed.Data).To(HaveKey("tls-keystore.jks"))
			Expect(renewed.Data).To(HaveKey("keystore_password"))

			By("Checking that the Deployment rolled via the tlsVersion annotation")
			rolled := &appsv1.Deployment{}
			Eventually(func() string {
				if err := k8sClient.Get(ctx, deployName, rolled); err != nil {
					return ""
				}
				return rolled.Spec.Template.Annotations[tlsVersionKey]
			}, time.Minute, time.Second).ShouldNot(Equal(oldTLSVersionAnnotation))
			Expect(rolled.Spec.Template.Annotations[tlsVersionKey]).To(Equal(renewed.ResourceVersion))
		})
	})

	// The operator no longer adds a finalizer (H-2), but CRs created by older
	// releases still carry one. Reconciling them must remove it — live or
	// deleting — without ever adding it back, otherwise upgraded CRs would stay
	// stuck in Terminating forever.
	Context("When deleting CRs that carry legacy or current finalizers", func() {
		ctx := context.Background()
		specCounter := 0

		var (
			namespaceName      string
			typeNamespacedName types.NamespacedName
		)

		newFinalizerResource := func(finalizers []string) *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry {
			return &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:       namespaceName,
					Namespace:  namespaceName,
					Labels:     map[string]string{"strimzi.io/cluster": "kafka-cluster"},
					Finalizers: finalizers,
				},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true, Listener: "TLS",
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}}}},
				},
			}
		}

		BeforeEach(func() {
			// A unique namespace per spec: re-creating a Namespace object that
			// still carries a resourceVersion from a previous create is
			// rejected by the API server.
			specCounter++
			namespaceName = "test-finalizer-deletion-" + strconv.Itoa(specCounter)
			typeNamespacedName = types.NamespacedName{Name: namespaceName, Namespace: namespaceName}

			By("Creating the Namespace")
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
		})

		AfterEach(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			if err == nil {
				found.Finalizers = []string{} // Remove finalizers to allow deletion
				_ = k8sClient.Update(ctx, found)
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should delete a CR that carries only the legacy finalizer", func() {
			By("Creating a CR that still carries the legacy finalizer")
			Expect(k8sClient.Create(ctx, newFinalizerResource([]string{legacyFinalizer}))).To(Succeed())

			By("Deleting the CR (the legacy finalizer keeps it in Terminating)")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(k8sClient.Delete(ctx, instance)).To(Succeed())

			By("Reconciling: the legacy finalizer must be removed without adding the current one")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred(), "reconcile must not add a finalizer to a deleting object")

			By("Checking that the CR is fully deleted")
			Eventually(func() error {
				updatedInstance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				return k8sClient.Get(ctx, typeNamespacedName, updatedInstance)
			}, time.Minute, time.Second).ShouldNot(Succeed(), "CR with only the legacy finalizer should be deleted")
		})

		It("should remove the current finalizer and complete deletion", func() {
			By("Creating a CR that carries the current finalizer")
			Expect(k8sClient.Create(ctx, newFinalizerResource([]string{finalizer}))).To(Succeed())

			By("Deleting the CR")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(k8sClient.Delete(ctx, instance)).To(Succeed())

			By("Reconciling to trigger finalizer removal")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that the CR is fully deleted")
			Eventually(func() error {
				updatedInstance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				return k8sClient.Get(ctx, typeNamespacedName, updatedInstance)
			}, time.Minute, time.Second).ShouldNot(Succeed(), "CR with the current finalizer should be deleted")
		})

		It("should remove the legacy finalizer from a live CR without adding a new one", func() {
			By("Creating a live CR that carries only the legacy finalizer")
			Expect(k8sClient.Create(ctx, newFinalizerResource([]string{legacyFinalizer}))).To(Succeed())

			By("Reconciling (later steps may fail on missing dependencies; cleanup runs first)")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			By("Checking that the legacy finalizer was removed and no new one was added")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(instance.Finalizers).To(BeEmpty(), "the no-op finalizer must not be replaced")
		})

		It("should remove the no-op current finalizer from a live CR", func() {
			By("Creating a live CR that carries the current no-op finalizer")
			Expect(k8sClient.Create(ctx, newFinalizerResource([]string{finalizer}))).To(Succeed())

			By("Reconciling")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			By("Checking that the no-op finalizer was removed")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(instance.Finalizers).To(BeEmpty(), "upgraded live CRs must drop the no-op finalizer")
		})
	})
})
