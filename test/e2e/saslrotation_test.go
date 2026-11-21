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

// The SASL truststore rotation scenario (plan §4.3 item 2) runs only in the
// SASL_SSL matrix job. It proves two distinct rotation paths against a live
// operator:
//
//   - a cluster CA rotation rebuilds only the truststore of the shared
//     <name>-jks secret (SASL_SSL never builds a keystore) and rolls the
//     Deployment from the jksVersion marker;
//   - a KafkaUser SCRAM credential rotation rolls the Deployment from the
//     saslUserSecretVersion marker without touching the truststore secret.
//
// Both rotations are simulated by bumping the respective secret's
// resourceVersion (the operator's rotation signal); replacing the real CA or
// SCRAM material would break the running Kafka cluster.
var _ = Describe("StrimziSchemaRegistry e2e SASL truststore rotation scenario", Ordered, Serial, func() {
	// Built in BeforeAll: the Describe body runs before BeforeSuite populates
	// the matrix scenario configuration.
	var fixture *registryFixture

	var ca *clusterCA

	BeforeAll(func() {
		fixture = newRegistryFixture("sasl-rotation")
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

	It("rebuilds only the truststore on a CA rotation and rolls on a SCRAM credential rotation", func() {
		if scenario.SecurityProtocol != "SASL_SSL" {
			Skip("the truststore-only rebuild path is only exercised by the SASL_SSL matrix job")
		}
		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs")
		fixture.ensureKafkaUser(ctx)

		By("applying the CR with securehttp enabled")
		Expect(fixture.apply(ctx, false, true, nil)).To(Succeed())
		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)
		fixture.verifyRoundTrip(ca, nil)

		jksSecretName := fixture.Name + "-jks"
		By("recording the truststore-only JKS secret")
		before := waitForSecretWithKey(ctx, jksSecretName, "truststore.jks")
		Expect(before.Data).NotTo(HaveKey("keystore.jks"),
			"SASL_SSL must never build a client keystore")
		Expect(before.Data).NotTo(HaveKey("keystore_password"),
			"SASL_SSL must never build a client keystore")
		beforeJKSVersion := before.ResourceVersion

		By("simulating a cluster CA rotation by bumping the CA cert secret version")
		newCAVersion := bumpSecretVersion(ctx, scenario.ClusterName+"-cluster-ca-cert",
			"e2e.randsw.code/sasl-ca-rotation")

		By("waiting for the truststore-only rebuild")
		var rebuilt *corev1.Secret
		Eventually(func(g Gomega) {
			current := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      jksSecretName,
				Namespace: scenario.Namespace,
			}, current)).To(Succeed())
			g.Expect(current.Annotations).To(HaveKeyWithValue(caVersionAnnotationKey, newCAVersion))
			rebuilt = current
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the JKS secret CAVersion to record %s",
			statusTimeout, newCAVersion)
		Expect(rebuilt.Data).To(HaveKey("truststore.jks"))
		Expect(rebuilt.Data).NotTo(HaveKey("keystore.jks"),
			"the CA rotation must rebuild the truststore only")
		Expect(rebuilt.ResourceVersion).NotTo(Equal(beforeJKSVersion))

		By("waiting for the deployment to roll for the rebuilt truststore")
		waitForDeploymentAnnotationFor(ctx, fixture.Name, statusTimeout,
			jksVersionAnnotationKey, rebuilt.ResourceVersion)
		fixture.waitRollout(ctx)
		fixture.waitReady(ctx)
		fixture.verifyRoundTrip(ca, nil)

		By("recording the truststore secret after the CA rotation")
		afterCA := waitForSecretWithKey(ctx, jksSecretName, "truststore.jks")
		afterCAResourceVersion := afterCA.ResourceVersion

		By("recording the deployment's SASL credential marker before the rotation")
		beforeUserVersion := deploymentFor(ctx, fixture.Name).Spec.Template.Annotations[saslUserVersionAnnotationKey]
		Expect(beforeUserVersion).NotTo(BeEmpty(),
			"the deployment must record the KafkaUser secret version from creation")

		By("simulating a SCRAM credential rotation by bumping the KafkaUser secret version")
		bumpSecretVersion(ctx, fixture.Name, "e2e.randsw.code/sasl-credential-rotation")

		By("waiting for the deployment to record a rotated SASL secret version")
		// The annotation is not pinned to one resourceVersion: Strimzi's user
		// operator may rewrite its credentials secret around the same time,
		// so only the transition away from the pre-rotation value matters.
		Eventually(func(g Gomega) {
			dep := deploymentFor(ctx, fixture.Name)
			g.Expect(dep.Spec.Template.Annotations).To(HaveKey(saslUserVersionAnnotationKey))
			g.Expect(dep.Spec.Template.Annotations[saslUserVersionAnnotationKey]).NotTo(
				BeEmpty(), "the rotated SASL secret version must be recorded")
			g.Expect(dep.Spec.Template.Annotations[saslUserVersionAnnotationKey]).NotTo(
				Equal(beforeUserVersion), "the SASL secret version marker did not change")
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the SASL credential rollout marker", statusTimeout)
		fixture.waitRollout(ctx)
		fixture.waitReady(ctx)

		By("verifying the SCRAM rotation did not rebuild the truststore")
		current := waitForSecretWithKey(ctx, jksSecretName, "truststore.jks")
		Expect(current.ResourceVersion).To(Equal(afterCAResourceVersion),
			"a SCRAM credential rotation must not rebuild the truststore")
		Expect(current.Data).NotTo(HaveKey("keystore.jks"))

		By("passing the REST round-trip after the credential rotation")
		fixture.verifyRoundTrip(ca, nil)
	})
})
