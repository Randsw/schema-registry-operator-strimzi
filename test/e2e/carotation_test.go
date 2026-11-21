//go:build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The cluster-CA rotation scenario (plan §4.3 item 1) proves the F2 renewal
// path against a live operator: when the Strimzi cluster CA cert secret
// changes, the operator must renew the operator-managed REST API TLS secret
// (new CAVersion annotation + rebuilt JKS keystore), emit the
// TLSCertificateRenewed event and roll the Deployment without dropping the
// Service (zero-downtime continuity).
//
// The rotation is simulated by bumping the resourceVersion of the
// <cluster>-cluster-ca-cert secret: the operator detects rotations through
// that version, and a real Strimzi CA rotation would invalidate the Kafka TLS
// material of the running cluster.
var _ = Describe("StrimziSchemaRegistry e2e cluster CA rotation scenario", Ordered, Serial, func() {
	// The fixture is built in BeforeAll: scenario is populated by BeforeSuite,
	// which runs after the spec tree (and this Describe body) is constructed.
	var fixture *registryFixture

	var ca *clusterCA

	BeforeAll(func() {
		fixture = newRegistryFixture("ca-rotation")
		var err error
		ca, err = fetchClusterCA(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()
		By("deleting the isolated StrimziSchemaRegistry CR to leave a clean slate")
		fixture.delete(ctx)
		By("deleting the isolated KafkaUser")
		fixture.deleteKafkaUser(ctx)
	})

	It("renews the REST API TLS certificate and rolls without downtime after the cluster CA rotates", func() {
		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs")
		fixture.ensureKafkaUser(ctx)

		By("applying the CR with securehttp enabled and an operator-managed TLS secret")
		Expect(fixture.apply(ctx, false, true, nil)).To(Succeed())
		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)
		fixture.verifyRoundTrip(ca, nil)

		tlsSecretName := fixture.Name + "-tls"
		By("recording the operator-managed REST API TLS secret before the rotation")
		before := waitForSecretWithKey(ctx, tlsSecretName, "tls-keystore.jks")
		Expect(before.Annotations).To(HaveKey(caVersionAnnotationKey))
		beforeTLSVersion := before.Annotations[caVersionAnnotationKey]
		beforeKeystore := before.Data["tls-keystore.jks"]

		By("simulating a Strimzi cluster CA rotation by bumping the CA cert secret version")
		bumpSecretVersion(ctx, scenario.ClusterName+"-cluster-ca-cert", "e2e.randsw.code/ca-rotation")

		By("waiting for the operator to renew the REST API TLS secret")
		// The recorded CA version is compared against the CA cert secret's
		// CURRENT version rather than the version right after the bump: Strimzi
		// may rewrite its own CA secret (bumping the version again) before the
		// operator reconciles, and the operator always records the latest one.
		var renewed *corev1.Secret
		Eventually(func(g Gomega) {
			current := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      tlsSecretName,
				Namespace: scenario.Namespace,
			}, current)).To(Succeed())

			caCertSecret := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      scenario.ClusterName + "-cluster-ca-cert",
				Namespace: scenario.Namespace,
			}, caCertSecret)).To(Succeed())

			g.Expect(current.Annotations).To(HaveKeyWithValue(caVersionAnnotationKey, caCertSecret.ResourceVersion),
				"the TLS secret must record the current cluster CA cert secret version")
			g.Expect(current.Annotations[caVersionAnnotationKey]).NotTo(Equal(beforeTLSVersion),
				"the TLS secret CA version did not change after the rotation")
			renewed = current
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the renewed TLS secret CA version", statusTimeout)

		By("verifying the keystore material was rebuilt")
		Expect(renewed.Data["tls-keystore.jks"]).NotTo(Equal(beforeKeystore),
			"the renewed REST API TLS secret must carry a freshly built keystore")

		By("waiting for the deployment pod template to record the renewed TLS secret version")
		Eventually(func(g Gomega) {
			dep := deploymentFor(ctx, fixture.Name)
			current := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      tlsSecretName,
				Namespace: scenario.Namespace,
			}, current)).To(Succeed())
			g.Expect(dep.Spec.Template.Annotations).To(
				HaveKeyWithValue(tlsVersionAnnotationKey, current.ResourceVersion),
				"the tlsVersion pod-template annotation must track the renewed secret")
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the tlsVersion rollout marker", statusTimeout)

		By("asserting the TLSCertificateRenewed event fired")
		Eventually(func(g Gomega) []string {
			reasons, err := listRegistryEventsFor(ctx, fixture.Name)
			g.Expect(err).NotTo(HaveOccurred())
			return reasons
		}, statusTimeout, 2*time.Second).Should(ContainElement("TLSCertificateRenewed"),
			"expected a TLSCertificateRenewed event for the rotating CA")

		By("waiting for the zero-downtime rollout")
		waitForDeploymentRolloutKeepingAvailability(ctx, fixture.Name, readyTimeout)
		fixture.waitReady(ctx)

		By("passing the REST round-trip after the rotation")
		fixture.verifyRoundTrip(ca, nil)
	})
})
