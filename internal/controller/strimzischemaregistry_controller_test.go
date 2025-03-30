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

	kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const cert string = `-----BEGIN CERTIFICATE-----
MIIFLTCCAxWgAwIBAgIUbe6ebrD+ANcQf1kYa8e4ZJJs/QwwDQYJKoZIhvcNAQEN
BQAwLTETMBEGA1UECgwKaW8uc3RyaW16aTEWMBQGA1UEAwwNY2x1c3Rlci1jYSB2
MDAeFw0yNTAzMTQxNTUzMjZaFw0yNjAzMTQxNTUzMjZaMC0xEzARBgNVBAoMCmlv
LnN0cmltemkxFjAUBgNVBAMMDWNsdXN0ZXItY2EgdjAwggIiMA0GCSqGSIb3DQEB
AQUAA4ICDwAwggIKAoICAQDJtPr6eazRhGz28FNiJkmXQQ14ZKvfGr+2pXyQE985
izJ4HIHjqdbnL0GabuTxwJBOF5zWtb8TAzsI5cB98W3IHJHPCNX2qEI9DqWd4x1m
Qm8jV36O8OGQ/Xz5hYzlKmbXNqErzW5qkM0hFk9YK50raltHusfYnUIkRypPheY8
ZuxIUFisWz/QT/d98+K+wet7bEVae4hlyWu5hZ5Io+cgf4dlTUh8XiAoHlmnPnsv
BE1tc5Zl7Tya7R1QlbTJYTFemF9B8ZuwHbHGHOA+VlImwPn7LrIY1JoHQ1o6zZVB
2e2DwPtGTU5dMe3YU8h8vmyF5mk3m1Sk5ZphfP40JeFNmiLNXwilub06hHoy2H5+
PXudh+I/Q/pxhXyA4D0qB8BSVCry5T/lXseOIM5FUVNhK0kSYzkjwsfIHMwJC8VT
TK+3SuhAM5bkb/8+Vp2Jn+xsJqKr7i7SS2MR6pT6Twv4rcW+soEVfW5R3wEcNH6x
dVMQsYjfllwGQD6oYeNr3bTzj4QTCHqAxce6doAba6lSSwyshMh7jqZ2nhQD9FqA
VsfCxVQLRapcjOluY/htRwptCSq8aHCNOgrEfbQMO7zte0LwY8/Q818T4J19+mpy
SlOe+MS6zKan9PutHhKvP7727/G5WQOy31CGSv72wEfvN/ggrjGwYNgcekOHrvh0
rwIDAQABo0UwQzAdBgNVHQ4EFgQUxWIOTnlDuqTWTxr3/0ck49fRiuMwEgYDVR0T
AQH/BAgwBgEB/wIBADAOBgNVHQ8BAf8EBAMCAQYwDQYJKoZIhvcNAQENBQADggIB
AL+fUCf5ccVzFDxYAS6wSF0kUAs8JZDDIDwnIWb0BF5nvjaHsCJgG2Yk3s8cX15d
k/ELwH0XZP9Yc5fSyEz3XPy2pdHByS3XgLCDTIzfYmGr6FFRI1IrV0BTHeTw4nWQ
kIH+7QqabamyF9vJdSM1vZdXJLLOybU29MqpLQpJTiADyqawbcCQXAEuRwkzrHHf
nxkbxtdVcYtRvhv1jvQLLkFYqXnVIaMYptf+aBgGON25c/Svtr1tHrZ6r5a2zR3Z
xJO22DiRtXlN/HnEKZALCdmvBbUgWAZh3O14RsFSgKIsg+qVwqd5Ha1IO/kvulJy
AloIBwNiqJinjmJ/daF/cPn4r4rVP8iXlpUXmmnjEDBeN0OMm/5omXdOjm8kQ4ig
chociQrxWqNiS35iqvvyo9gxPjpFoxqxmecVMedA4lM1VyYgF5oOwekfA+cJgAib
bkSuMcjoAsHcJZRi7Frb4eM8FSMHfUoftVULz4xzAtPzxw7d50KihKtQdH/eXggB
Jt+U/PiN6LL6X4adQkbBAK1mXg/FnKyzvpw4fXCHGlcfUEXeZCYbjh9mXxo2H2RA
YOfNN2wulDZ7o+4nU8lAgzZPIQZpBPMRT/lTfyBzbzGcAAS26G5VDehNKijGu0PL
C1Su8qJSFefPuGQm0ht3N1RNpZF21NCKK4umiUrVS9VB
-----END CERTIFICATE-----
`

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

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			// Create Kafka Cluster

			By("Creating strimzi kafka cluster")
			Cluster := &kafka.Kafka{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kafka-cluster",
					Namespace: namespace.Name,
				},
				Spec: &kafka.KafkaSpec{
					EntityOperator: &kafka.KafkaSpecEntityOperator{
						TopicOperator: &kafka.KafkaSpecEntityOperatorTopicOperator{},
						UserOperator:  &kafka.KafkaSpecEntityOperatorUserOperator{},
					},
					Kafka: kafka.KafkaSpecKafka{
						Authorization: &kafka.KafkaSpecKafkaAuthorization{
							SuperUsers: []string{"CN=root"},
							Type:       kafka.KafkaSpecKafkaAuthorizationTypeSimple,
						},
						Config: &v1.JSON{
							Raw: []byte("{'default.replication.factor':3,'min.insync.replicas':2,'offsets.topic.replication.factor':3,'transaction.state.log.min.isr':2, 'transaction.state.log.replication.factor':3}"),
						},
						Listeners: []kafka.KafkaSpecKafkaListenersElem{
							{
								Name: "plain",
								Port: 9092,
								Tls:  false,
								Type: kafka.KafkaSpecKafkaListenersElemTypeInternal,
							},
							{
								Name: "tls",
								Port: 9093,
								Tls:  true,
								Type: kafka.KafkaSpecKafkaListenersElemTypeInternal,
								Authentication: &kafka.KafkaSpecKafkaListenersElemAuthentication{
									Type: kafka.KafkaSpecKafkaListenersElemAuthenticationTypeTls,
								},
							},
						},
						Version: ptr.To("3.9.0"),
					},
				},
				Status: &kafka.KafkaStatus{
					ClusterId: ptr.To("Ypb68J1GSu-jqq0N0ama0w"),
					Listeners: []kafka.KafkaStatusListenersElem{
						{
							Addresses: []kafka.KafkaStatusListenersElemAddressesElem{
								{
									Host: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc"),
									Port: ptr.To(int32(9092)),
								},
							},
							BootstrapServers: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc:9092"),
							Name:             ptr.To("plain"),
						},
						{
							Addresses: []kafka.KafkaStatusListenersElemAddressesElem{
								{
									Host: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc"),
									Port: ptr.To(int32(9093)),
								},
							},
							BootstrapServers: ptr.To("kafka-cluster-kafka-bootstrap.kafka.svc:9093"),
							Name:             ptr.To("TLS"),
							Certificates:     []string{cert},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, Cluster)).To(Succeed())
			Expect(err).To(Not(HaveOccurred()))
			//TODO Create Kafka User, Kafka Secret
			User := &kafka.KafkaUser{}
			Expect(k8sClient.Create(ctx, User)).To(Succeed())
			Expect(err).To(Not(HaveOccurred()))

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
					},
					Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
						StrimziVersion: "v1beta2",
						SecureHTTP:     false,
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

			By("Checking if service was successfully created in the reconciliation")
			Eventually(func() error {
				found := &corev1.Service{}
				typeNamespaceName := types.NamespacedName{Name: SchemaRegistryName, Namespace: SchemaRegistryName}
				return k8sClient.Get(ctx, typeNamespaceName, found)
			}, time.Minute*2, time.Second).Should(Succeed())
		})
	})
})
