//go:build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The derived-JKS garbage-collection scenario (plan §4.3 item 4) proves the
// gcOrphanedSecrets/isStaleDerivedJKSSecret path against a live operator.
//
// Both user TLS secrets are standard kubernetes.io/tls (PEM) secrets, so the
// operator converts each into its own derived <name>-<tlssecretname>-jks
// secret. Switching spec.tlssecretname from the first to the second must
// repoint the Deployment mounts at the new derived secret and delete the stale
// derived secret of the old one.
var _ = Describe("StrimziSchemaRegistry e2e derived JKS garbage collection scenario", Ordered, Serial, func() {
	// Built in BeforeAll: the Describe body runs before BeforeSuite populates
	// the matrix scenario configuration.
	var fixture *registryFixture

	var (
		materialA *userTLSMaterial
		materialB *userTLSMaterial
		secretA   string
		secretB   string
	)

	BeforeAll(func() {
		fixture = newRegistryFixture("jksgc")
		materialA = newUserTLSMaterial(fixture.Name)
		materialB = newUserTLSMaterial(fixture.Name)
		secretA = fixture.Name + "-tls-a"
		secretB = fixture.Name + "-tls-b"
	})

	AfterAll(func() {
		ctx := context.Background()
		By("deleting the isolated StrimziSchemaRegistry CR to leave a clean slate")
		fixture.delete(ctx)
		By("deleting the user-provided TLS secrets")
		deleteSecretIfExists(ctx, secretA)
		deleteSecretIfExists(ctx, secretB)
		By("deleting the isolated KafkaUser")
		fixture.deleteKafkaUser(ctx)
	})

	It("garbage-collects the stale derived JKS secret when tlssecretname changes", func() {
		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs")
		fixture.ensureKafkaUser(ctx)

		By("creating the first user PEM TLS secret")
		applySecret(ctx, secretA, corev1.SecretTypeTLS, materialA.pemData())

		By("applying the CR with the first secret as spec.tlssecretname")
		Expect(fixture.apply(ctx, false, true, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.TLSSecretName = secretA
		})).To(Succeed())

		derivedA := fixture.Name + "-" + secretA + "-jks"
		By("waiting for the derived JKS secret of the first secret")
		waitForSecretWithKey(ctx, derivedA, "tls-keystore.jks")
		Eventually(func() string {
			return fixture.mountedTLSSecret(ctx)
		}, statusTimeout, 2*time.Second).Should(Equal(derivedA),
			"the rest-api-tls volume must mount the derived secret")

		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)
		fixture.verifyRoundTrip(materialA.cluster, nil)

		By("creating the second user PEM TLS secret")
		applySecret(ctx, secretB, corev1.SecretTypeTLS, materialB.pemData())

		By("switching spec.tlssecretname to the second secret")
		Expect(fixture.apply(ctx, false, true, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.TLSSecretName = secretB
		})).To(Succeed())

		derivedB := fixture.Name + "-" + secretB + "-jks"
		By("waiting for the derived JKS secret of the second secret")
		waitForSecretWithKey(ctx, derivedB, "tls-keystore.jks")
		Eventually(func() string {
			return fixture.mountedTLSSecret(ctx)
		}, statusTimeout, 2*time.Second).Should(Equal(derivedB),
			"the rest-api-tls volume must be repointed at the new derived secret")

		By("waiting for the stale derived JKS secret to be garbage-collected")
		waitForSecretDeletion(ctx, derivedA)

		By("waiting for the deployment to roll onto the user secret's new version")
		Eventually(func(g Gomega) {
			dep := deploymentFor(ctx, fixture.Name)
			current := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      secretB,
				Namespace: scenario.Namespace,
			}, current)).To(Succeed())
			g.Expect(dep.Spec.Template.Annotations).To(
				HaveKeyWithValue(tlsVersionAnnotationKey, current.ResourceVersion),
				"the tlsVersion marker must track the new user TLS secret")
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the rollout marker", statusTimeout)

		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)

		By("passing the REST round-trip trusting the second user CA")
		fixture.verifyRoundTrip(materialB.cluster, nil)
	})
})
