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

	//kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("StrimziSchemaRegistry Controller", func() {
	Context("When reconciling a resource", func() {
		const SchemaRegistryName = "test-resource"

		ctx := context.Background()

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SchemaRegistryName,
				Namespace: SchemaRegistryName,
			},
		}

		typeNamespacedName := types.NamespacedName{
			Name:      SchemaRegistryName,
			Namespace: SchemaRegistryName,
		}
		strimzischemaregistry := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}

		// Dynamic certificate variables
		var (
			testCA    *testutil.TestCA
			clusterCA *testutil.TestCA
			userCert  *testutil.TestUserCert
		)

		BeforeEach(func() {
			// Generate test certificates dynamically
			var err error
			testCA, err = testutil.GenerateTestCA()
			Expect(err).NotTo(HaveOccurred())

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace to perform the tests")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			// Create Kafka Cluster

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
							//Raw: []byte(jsonString),
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
						Certificates:     []string{clusterCA.CACertPEM},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			// Update Status of Kafka CR
			UpdateCluser := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, UpdateCluser)
			Expect(err).To(Not(HaveOccurred()))

			UpdateCluser.Status = status
			By("Updating Kafka Cluster")
			Expect(k8sClient.Status().Update(ctx, UpdateCluser)).To(Succeed())

			// Create Kafka User
			By("creating Kafka User")
			user := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: namespace.Name,
					Labels: map[string]string{
						"strimzi.io/cluster": "kafka-cluster",
					},
				},
				Spec: &kafka.KafkaUserSpec{
					Authentication: &kafka.KafkaUserAuthentication{
						Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE,
					},
					Authorization: &kafka.KafkaUserAuthorization{
						Acls: []kafka.AclRule{
							{
								Host: "*",
								Resource: &kafka.AclRuleResource{
									Name:        "registry-schemas",
									PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE,
									Type:        kafka.TOPIC_ACLRULERESOURCETYPE,
								},
								Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION},
							},
						},
						Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE,
					},
				},
			}
			Expect(k8sClient.Create(ctx, user)).To(Succeed())

			//Create Cluster Secret
			By("Creating kafka cluster CA key secret")
			clusterKeySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster-cluster-ca",
					Namespace: namespace.Name,
				},
				Data: map[string][]byte{
					"ca.key": []byte(clusterCA.CAKeyPEM),
				},
			}
			Expect(k8sClient.Create(ctx, clusterKeySecret)).To(Succeed())

			By("Creating kafka cluster CA cert secret")
			clusterSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster-cluster-ca-cert",
					Namespace: namespace.Name,
				},
				Data: map[string][]byte{
					"ca.crt": []byte(clusterCA.CACertPEM),
				},
			}
			Expect(k8sClient.Create(ctx, clusterSecret)).To(Succeed())

			//Create Kafka client secret
			By("Creating kafka user secret")
			clientSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: namespace.Name,
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

			By("Setting the Image ENV VAR which stores the Operand image")
			err = os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")
			Expect(err).To(Not(HaveOccurred()))

			By("creating the custom resource for the Kind StrimziSchemaRegistry")
			err = k8sClient.Get(ctx, typeNamespacedName, strimzischemaregistry)
			if err != nil && errors.IsNotFound(err) {
				resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      SchemaRegistryName,
						Namespace: namespace.Name,
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
										Image: "confluentinc/cp-schema-registry:7.6.5",
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := k8sClient.Get(ctx, typeNamespacedName, found)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance StrimziSchemaRegistry")
			// Remove finalizers so the CR can actually be deleted (otherwise Delete
			// only marks it for deletion and the resource lingers indefinitely).
			if len(found.Finalizers) > 0 {
				found.Finalizers = []string{}
				Expect(k8sClient.Update(ctx, found)).To(Succeed())
			}
			Expect(k8sClient.Delete(ctx, found)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{})
				return errors.IsNotFound(err)
			}, 2*time.Minute, time.Second).Should(BeTrue())

			// TODO(user): Attention if you improve this code by adding other context test you MUST
			// be aware of the current delete namespace limitations.
			// More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			By("Deleting the Namespace to perform the tests")
			_ = k8sClient.Delete(ctx, namespace)

			By("Removing the Image ENV VAR which stores the Operand image")
			_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
		})

		It("should successfully reconcile the resource", func() {
			By("Checking if the custom resource was successfully created")
			Eventually(func() error {
				found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				return k8sClient.Get(ctx, typeNamespacedName, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Reconciling the created resource")
			controllerReconciler := &StrimziSchemaRegistryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was successfully created in the reconciliation")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Checking if Secret was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Secret{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Checking if TLS Secret was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Secret{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Reconciling the created resource")
			controllerReconciler = &StrimziSchemaRegistryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if service was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Service{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute*2, time.Second).Should(Succeed())

			By("Checking if CRD status field is set to Ok")
			Eventually(func() string {
				By("Reconciling the created resource")
				controllerReconciler := &StrimziSchemaRegistryReconciler{
					Client: k8sClient,
					Scheme: k8sClient.Scheme(),
				}
				_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
				instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
				err = k8sClient.Get(ctx, typeNamespaceName, instance)
				Expect(err).NotTo(HaveOccurred())
				contidion := meta.FindStatusCondition(instance.Status.Conditions, "Ready")
				if contidion == nil {
					return ""
				}
				return string(contidion.Status)
			}, time.Minute*20, time.Second).Should(Equal("True"))

			// Test Updating Cluster secret to check if JKS secret is updated too
			// Get JKS secret resourseVersion
			readSecret := &corev1.Secret{}
			typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
			err = k8sClient.Get(ctx, typeNamespaceName, readSecret)
			Expect(err).To(Not(HaveOccurred()))
			// Save JKS secret resource version before update
			JKSResourceVersionOld := readSecret.ResourceVersion
			JKSResourceVersionOldInt, err := strconv.Atoi(JKSResourceVersionOld)
			Expect(err).To(Not(HaveOccurred()))

			// Get JKS secret resourseVersion
			readSecret = &corev1.Secret{}
			typeNamespaceName = types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}
			err = k8sClient.Get(ctx, typeNamespaceName, readSecret)
			Expect(err).To(Not(HaveOccurred()))
			// Save JKS secret resource version before update
			TLSResourceVersionOld := readSecret.ResourceVersion
			TLSResourceVersionOldInt, err := strconv.Atoi(TLSResourceVersionOld)
			Expect(err).To(Not(HaveOccurred()))

			// Update Cluster secret
			readSecret = &corev1.Secret{}
			typeNamespaceName = types.NamespacedName{Name: "kafka-cluster-cluster-ca-cert", Namespace: SchemaRegistryName}
			err = k8sClient.Get(ctx, typeNamespaceName, readSecret)
			Expect(err).To(Not(HaveOccurred()))
			// Add dummy data to change cluster secret ResourceVersion
			data := readSecret.Data
			data["ta.Key"] = []byte(clusterCA.CAKeyPEM)
			readSecret.Data = data
			Expect(k8sClient.Update(ctx, readSecret)).To(Succeed())

			By("Reconciling after cluster secret Update")
			controllerReconciler = &StrimziSchemaRegistryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Read updated JKS secret using Eventually
			Eventually(func() (int, error) {
				secret := &corev1.Secret{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
				err = k8sClient.Get(ctx, typeNamespaceName, secret)
				if err != nil {
					return 0, err
				}
				JKSResourceVersionNewInt, err := strconv.Atoi(secret.ResourceVersion)
				if err != nil {
					return 0, err
				}
				return JKSResourceVersionNewInt, nil
			}, time.Minute, time.Second).Should(BeNumerically(">", JKSResourceVersionOldInt))

			// Read updated TLS secret using Eventually
			Eventually(func() (int, error) {
				secret := &corev1.Secret{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}
				err = k8sClient.Get(ctx, typeNamespaceName, secret)
				if err != nil {
					return 0, err
				}
				TLSResourceVersionNewInt, err := strconv.Atoi(secret.ResourceVersion)
				if err != nil {
					return 0, err
				}
				return TLSResourceVersionNewInt, nil
			}, time.Minute, time.Second).Should(BeNumerically(">", TLSResourceVersionOldInt))

			// Updating User secret to check if JKS secret is updated too
			readSecret = &corev1.Secret{}
			typeNamespaceName = types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
			err = k8sClient.Get(ctx, typeNamespaceName, readSecret)
			Expect(err).To(Not(HaveOccurred()))
			// Save JKS secret resource version before update
			JKSResourceVersionOld = readSecret.ResourceVersion
			JKSResourceVersionOldInt, err = strconv.Atoi(JKSResourceVersionOld)
			Expect(err).To(Not(HaveOccurred()))

			// Update User secret
			readSecret = &corev1.Secret{}
			typeNamespaceName = types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
			err = k8sClient.Get(ctx, typeNamespaceName, readSecret)
			Expect(err).To(Not(HaveOccurred()))
			// Add dummy data to change User secret ResourceVersion
			data = readSecret.Data
			data["ta.Key"] = []byte(clusterCA.CAKeyPEM)
			readSecret.Data = data
			Expect(k8sClient.Update(ctx, readSecret)).To(Succeed())

			By("Reconciling after user secret Update")
			controllerReconciler = &StrimziSchemaRegistryReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Read updated JKS secret using Eventually
			Eventually(func() (int, error) {
				secret := &corev1.Secret{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName + "-jks", Namespace: SchemaRegistryName}
				if err := k8sClient.Get(ctx, typeNamespaceName, secret); err != nil {
					return 0, err
				}
				JKSResourceVersionNewInt, err := strconv.Atoi(secret.ResourceVersion)
				if err != nil {
					return 0, err
				}
				return JKSResourceVersionNewInt, nil
			}, time.Minute, time.Second).Should(BeNumerically(">", JKSResourceVersionOldInt))
		})
	})

	// M1: HTTP Mode (Non-Secure) Reconciliation Test
	Context("When reconciling with HTTP mode (SecureHTTP=false)", func() {
		const SchemaRegistryName = "test-http-mode"

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

			clusterCA, err = testutil.GenerateClusterCACert("STIMZI-SR-TEST-HTTP")
			Expect(err).NotTo(HaveOccurred())

			userCert, err = testutil.GenerateTestUserCert(testCA, "test1234")
			Expect(err).NotTo(HaveOccurred())

			By("Creating the Namespace")
			err = k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

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
							{Name: "plain", Port: 9092, Tls: false, Type: kafka.INTERNAL_KAFKALISTENERTYPE},
							{Name: "tls", Port: 9093, Tls: true, Type: kafka.INTERNAL_KAFKALISTENERTYPE,
								Authentication: &kafka.KafkaListenerAuthentication{Type: kafka.TLS_KAFKALISTENERAUTHENTICATIONTYPE}},
						},
						Version: "4.1.0",
					},
				},
			}
			status := &kafka.KafkaStatus{
				ClusterId: "test-cluster-id",
				Listeners: []kafka.ListenerStatus{
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.http.svc:9092", Name: "plain"},
					{BootstrapServers: "kafka-cluster-kafka-bootstrap.http.svc:9093", Name: "TLS", Certificates: []string{clusterCA.CACertPEM}},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			updateCluster := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, updateCluster)
			Expect(err).To(Not(HaveOccurred()))
			updateCluster.Status = status
			Expect(k8sClient.Status().Update(ctx, updateCluster)).To(Succeed())

			By("Creating Kafka User")
			kafkaUser := &kafka.KafkaUser{
				ObjectMeta: metav1.ObjectMeta{
					Name:      SchemaRegistryName,
					Namespace: namespace.Name,
					Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
				},
				Spec: &kafka.KafkaUserSpec{
					Authentication: &kafka.KafkaUserAuthentication{Type: kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE},
					Authorization: &kafka.KafkaUserAuthorization{
						Acls: []kafka.AclRule{{Host: "*", Resource: &kafka.AclRuleResource{Name: "registry-schemas", PatternType: kafka.LITERAL_ACLRESOURCEPATTERNTYPE, Type: kafka.TOPIC_ACLRULERESOURCETYPE}, Operations: []kafka.StrimziAclOperation{kafka.ALL_STRIMZIACLOPERATION}}},
						Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE,
					},
				},
			}
			Expect(k8sClient.Create(ctx, kafkaUser)).To(Succeed())

			By("Creating cluster CA secrets")
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca", Namespace: namespace.Name}, Data: map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kafka-cluster-cluster-ca-cert", Namespace: namespace.Name}, Data: map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)}})).To(Succeed())

			By("Creating kafka user secret")
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name},
				Data:       map[string][]byte{"ca.crt": []byte(testCA.CACertPEM), "user.crt": []byte(userCert.UserCertPEM), "user.key": []byte(userCert.UserKeyPEM), "user.p12": userCert.PKCS12Data, "user.password": []byte(userCert.Password)},
			})).To(Succeed())

			By("Setting the Image ENV VAR")
			Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())

			By("Creating StrimziSchemaRegistry with SecureHTTP=false")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP:         false,
					Listener:           "TLS",
					CompatibilityLevel: "forward",
					SecurityProtocol:   "SSL",
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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

		It("should successfully reconcile with HTTP mode", func() {
			By("Reconciling the created resource")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Deployment was successfully created")
			Eventually(func() error {
				found := &appsv1.Deployment{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			By("Checking that SCHEMA_REGISTRY_LISTENERS is set to http://0.0.0.0:8081")
			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-deploy", Namespace: SchemaRegistryName}, deployment)).To(Succeed())
			envVars := deployment.Spec.Template.Spec.Containers[0].Env
			listenerEnv := false
			for _, env := range envVars {
				if env.Name == "SCHEMA_REGISTRY_LISTENERS" && env.Value == "http://0.0.0.0:8081" {
					listenerEnv = true
					break
				}
			}
			Expect(listenerEnv).To(BeTrue(), "SCHEMA_REGISTRY_LISTENERS should be http://0.0.0.0:8081")

			By("Checking that no TLS secret was created")
			tlsSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName + "-tls", Namespace: SchemaRegistryName}, tlsSecret)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "TLS secret should not exist when SecureHTTP is false")

			By("Reconciling again to create service")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking if Service was created with port 80 targeting 8081")
			Eventually(func() error {
				found := &corev1.Service{}
				return k8sClient.Get(ctx, types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}, found)
			}, time.Minute, time.Second).Should(Succeed())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, svc)).To(Succeed())
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))
			Expect(svc.Spec.Ports[0].TargetPort.IntVal).To(Equal(int32(8081)))
			Expect(svc.Spec.Ports[0].Name).To(Equal("http"))
		})
	})

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

			By("Creating custom TLS secret")
			customTLSSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "my-custom-tls-secret", Namespace: namespace.Name},
				Type:       corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"tls-keystore.jks":  []byte("fake-keystore-data"),
					"keystore_password": []byte("keystorepass"),
					"key_password":      []byte("keypass"),
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
					Template:      corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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

		It("should add finalizer on first reconcile and remove it on deletion", func() {
			By("Creating StrimziSchemaRegistry without full dependencies (to test finalizer before deployment)")
			resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
				ObjectMeta: metav1.ObjectMeta{Name: SchemaRegistryName, Namespace: namespace.Name, Labels: map[string]string{"strimzi.io/cluster": "kafka-cluster"}},
				Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
					SecureHTTP: true, Listener: "TLS",
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			By("Reconciling to add finalizer (will fail later due to missing deps, but finalizer should be added)")
			controllerReconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			By("Checking that finalizer was added")
			instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, instance)).To(Succeed())
			Expect(instance.Finalizers).To(ContainElement(finalizer), "Finalizer should be present after first reconcile")

			By("Deleting the resource to trigger finalizer removal")
			Expect(k8sClient.Delete(ctx, instance)).To(Succeed())

			By("Reconciling to trigger finalizer removal")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that resource is deleted after finalizer removal")
			Eventually(func() error {
				updatedInstance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
				return k8sClient.Get(ctx, typeNamespacedName, updatedInstance)
			}, time.Minute, time.Second).ShouldNot(Succeed(), "Resource should be deleted after finalizer is removed")
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:   corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:   corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:           corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
								{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"},
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
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
					Template:      corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:7.6.5"}}}},
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
			Expect(np.Spec.Egress).To(HaveLen(1))
			Expect(np.Spec.Egress[0].To).To(HaveLen(1))
			Expect(np.Spec.Egress[0].To[0].PodSelector.MatchLabels["strimzi.io/cluster"]).To(Equal("kafka-cluster"))
		})
	})
})
