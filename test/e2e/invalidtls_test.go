//go:build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The InvalidTLSSecret recovery scenario (plan §4.3 item 6) proves the
// user-TLS validation and convergence loop against a live operator: a missing
// spec.tlssecretname secret must surface as Ready=False/InvalidTLSSecret with a
// Warning event and must not create a Deployment; creating the secret must let
// reconciliation converge to Ready=True and serve the REST API.
var _ = Describe("StrimziSchemaRegistry e2e InvalidTLSSecret recovery scenario", Ordered, Serial, func() {
	// Built in BeforeAll: the Describe body runs before BeforeSuite populates
	// the matrix scenario configuration.
	var fixture *registryFixture

	var (
		material      *userTLSMaterial
		missingSecret string
	)

	BeforeAll(func() {
		fixture = newRegistryFixture("invalidtls")
		material = newUserTLSMaterial(fixture.Name)
		missingSecret = fixture.Name + "-missing-tls"
	})

	AfterAll(func() {
		ctx := context.Background()
		By("deleting the isolated StrimziSchemaRegistry CR to leave a clean slate")
		fixture.delete(ctx)
		By("deleting the user-provided TLS secret")
		deleteSecretIfExists(ctx, missingSecret)
		By("deleting the isolated KafkaUser")
		fixture.deleteKafkaUser(ctx)
	})

	It("reports InvalidTLSSecret for missing material and converges once the secret is fixed", func() {
		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs")
		fixture.ensureKafkaUser(ctx)

		By("applying the CR pointing at a missing TLS secret")
		Expect(fixture.apply(ctx, false, true, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.TLSSecretName = missingSecret
		})).To(Succeed())

		By("waiting for Ready=False with the InvalidTLSSecret reason")
		waitForConditionFor(ctx, fixture.Name, statusTimeout, func(cond *metav1.Condition) bool {
			return cond.Status == metav1.ConditionFalse && cond.Reason == "InvalidTLSSecret"
		})

		By("verifying the CR status mirrors the invalid TLS secret")
		cr := &v1alpha1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      fixture.Name,
			Namespace: scenario.Namespace,
		}, cr)).To(Succeed())
		Expect(cr.Status.Status).To(Equal("InvalidTLSSecret"))

		By("verifying the InvalidTLSSecret Warning event was emitted")
		Eventually(func(g Gomega) []string {
			reasons, err := listRegistryEventsFor(ctx, fixture.Name)
			g.Expect(err).NotTo(HaveOccurred())
			return reasons
		}, statusTimeout, 2*time.Second).Should(ContainElement("InvalidTLSSecret"),
			"expected an InvalidTLSSecret event while the secret is missing")

		By("verifying no Deployment was created for the invalid CR")
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, deploymentKeyFor(fixture.Name),
			&appsv1.Deployment{}))).To(BeTrue(),
			"the operator must not create a Deployment until the TLS secret is valid")

		By("creating the valid user-provided JKS keystore secret")
		applySecret(ctx, missingSecret, corev1.SecretTypeOpaque, material.jksData(ctx))

		By("waiting for the CR to converge")
		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)

		cr = &v1alpha1.StrimziSchemaRegistry{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      fixture.Name,
			Namespace: scenario.Namespace,
		}, cr)).To(Succeed())
		Expect(cr.Status.Status).NotTo(Equal("InvalidTLSSecret"))

		By("verifying the deployment mounts the fixed secret")
		Eventually(func() string {
			return fixture.mountedTLSSecret(ctx)
		}, statusTimeout, 2*time.Second).Should(Equal(missingSecret),
			"the rest-api-tls volume must mount the fixed user secret")

		By("passing the REST round-trip trusting the user CA")
		fixture.verifyRoundTrip(material.cluster, nil)
	})
})
