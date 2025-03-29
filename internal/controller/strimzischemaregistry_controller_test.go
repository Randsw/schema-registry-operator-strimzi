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

	_ "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
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

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests")
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))

			//TODO Create Kafka User, Kafka Secret and Kafka Cluster

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
