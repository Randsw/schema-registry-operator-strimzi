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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// The CRD carries CEL cross-field validation rules (see the
// +kubebuilder:validation:XValidation markers in
// api/v1alpha1/strimzischemaregistry_types.go) that the API server evaluates
// at admission time, so invalid spec combinations are rejected by kubectl
// apply instead of surfacing later as an InvalidSpec status. Each rule is
// covered here by a rejection case (the create fails with an Invalid error
// carrying the rule message) and by the valid variants around it (accepted).
// The controller re-checks the same invariants as defense-in-depth; that
// behavior is covered by the fake-client tests in k8s_test.go, which are not
// subject to admission validation.
var _ = Describe("StrimziSchemaRegistry CRD validation rules", func() {
	ctx := context.Background()

	// crdValidationSpec returns a minimal valid spec the individual cases
	// perturb. No reconciliation is performed on accepted resources: these
	// specs only exercise admission validation.
	crdValidationSpec := func() strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec {
		return strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
			SecureHTTP:       false,
			Listener:         "plain",
			SecurityProtocol: "PLAINTEXT",
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
			}},
		}
	}

	// createCRDValidationCR creates the namespace and the CR, registering
	// cleanup for both, and returns the create error so callers can assert
	// admission rejection.
	createCRDValidationCR := func(name string, mutate func(*strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec)) error {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() {
			found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: name}, found); err == nil {
				if len(found.Finalizers) > 0 {
					found.Finalizers = []string{}
					_ = k8sClient.Update(ctx, found)
				}
				_ = k8sClient.Delete(ctx, found)
			}
			_ = k8sClient.Delete(ctx, namespace)
		})

		spec := crdValidationSpec()
		if mutate != nil {
			mutate(&spec)
		}
		resource := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: name,
				Labels:    map[string]string{"strimzi.io/cluster": "kafka-cluster"},
			},
			Spec: spec,
		}
		return k8sClient.Create(ctx, resource)
	}

	It("rejects clientauth without securehttp and accepts the valid variants", func() {
		By("rejecting clientauth with securehttp=false")
		err := createCRDValidationCR("crdval-clientauth-invalid", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.ClientAuth = true
			s.SecureHTTP = false
		})
		Expect(errors.IsInvalid(err)).To(BeTrue(), "expected an Invalid admission error, got %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.clientauth requires spec.securehttp"))

		By("accepting clientauth with securehttp=true")
		Expect(createCRDValidationCR("crdval-clientauth-mtls", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.ClientAuth = true
			s.SecureHTTP = true
		})).To(Succeed())

		By("accepting the clientauth default (false) without securehttp")
		Expect(createCRDValidationCR("crdval-clientauth-default", nil)).To(Succeed())
	})

	It("rejects multi-replica HTTPS without a user TLS secret and accepts the valid variants", func() {
		By("rejecting replicas>1 with securehttp and no tlssecretname")
		err := createCRDValidationCR("crdval-replicas-invalid", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.SecureHTTP = true
			s.Replicas = ptr.To(int32(2))
			s.TLSSecretName = ""
		})
		Expect(errors.IsInvalid(err)).To(BeTrue(), "expected an Invalid admission error, got %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.replicas > 1 with spec.securehttp requires spec.tlssecretname"))

		By("accepting replicas>1 with securehttp and a tlssecretname")
		Expect(createCRDValidationCR("crdval-replicas-tls", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.SecureHTTP = true
			s.Replicas = ptr.To(int32(2))
			s.TLSSecretName = "user-tls-secret"
		})).To(Succeed())

		By("accepting a single replica with securehttp and no tlssecretname")
		Expect(createCRDValidationCR("crdval-replicas-single", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.SecureHTTP = true
		})).To(Succeed())

		By("accepting replicas>1 without securehttp")
		Expect(createCRDValidationCR("crdval-replicas-http", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.Replicas = ptr.To(int32(2))
		})).To(Succeed())
	})

	It("rejects service.nodeport with ClusterIP and accepts the valid variants", func() {
		By("rejecting nodeport with an explicit ClusterIP type")
		err := createCRDValidationCR("crdval-nodeport-clusterip", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.Service = &strimziregistryoperatorv1alpha1.Service{Type: "ClusterIP", NodePort: ptr.To(int32(30123))}
		})
		Expect(errors.IsInvalid(err)).To(BeTrue(), "expected an Invalid admission error, got %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.service.nodeport requires spec.service.type to be NodePort or LoadBalancer"))

		By("rejecting nodeport with the defaulted ClusterIP type")
		err = createCRDValidationCR("crdval-nodeport-default", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.Service = &strimziregistryoperatorv1alpha1.Service{NodePort: ptr.To(int32(30123))}
		})
		Expect(errors.IsInvalid(err)).To(BeTrue(), "expected an Invalid admission error, got %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.service.nodeport requires spec.service.type"))

		By("accepting nodeport with the NodePort type")
		Expect(createCRDValidationCR("crdval-nodeport-nodeport", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.Service = &strimziregistryoperatorv1alpha1.Service{Type: "NodePort", NodePort: ptr.To(int32(30123))}
		})).To(Succeed())

		By("accepting nodeport with the LoadBalancer type")
		Expect(createCRDValidationCR("crdval-nodeport-lb", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.Service = &strimziregistryoperatorv1alpha1.Service{Type: "LoadBalancer", NodePort: ptr.To(int32(30123))}
		})).To(Succeed())

		By("accepting a ClusterIP service without nodeport")
		Expect(createCRDValidationCR("crdval-nodeport-absent", func(s *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
			s.Service = &strimziregistryoperatorv1alpha1.Service{Type: "ClusterIP"}
		})).To(Succeed())
	})
})
