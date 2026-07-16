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
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/errors"
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
			Cluster := &kafka.Kafka{
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
			Expect(k8sClient.Create(ctx, Cluster)).To(Succeed())
			// Update Status of Kafka CR
			UpdateCluser := &kafka.Kafka{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "kafka-cluster", Namespace: SchemaRegistryName}, UpdateCluser)
			Expect(err).To(Not(HaveOccurred()))

			UpdateCluser.Status = status
			By("Updating Kafka Cluster")
			Expect(k8sClient.Status().Update(ctx, UpdateCluser)).To(Succeed())

			// Create Kafka User
			By("creating Kafka User")
			User := &kafka.KafkaUser{
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
								Operations: []kafka.AclOperation{kafka.ALL_ACLOPERATION},
							},
						},
						Type: kafka.SIMPLE_KAFKAUSERAUTHORIZATIONTYPE,
					},
				},
			}
			Expect(k8sClient.Create(ctx, User)).To(Succeed())

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
			Expect(k8sClient.Delete(ctx, found)).To(Succeed())
			Eventually(func() error {
				return k8sClient.Delete(context.TODO(), found)
			}, 2*time.Minute, time.Second).Should(Succeed())

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
				return instance.Status.Status
			}, time.Minute*20, time.Second).Should(Equal("Ok"))

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
})
