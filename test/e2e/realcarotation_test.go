//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/x509"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pavlo-v-chernykh/keystore-go/v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
)

// clusterCAForceReplaceAnnotation is the Strimzi annotation that makes the
// Cluster Operator generate a new CA private key and a matching new CA
// certificate for the annotated CA key secret. Unlike strimzi.io/force-renew,
// which renews a CA certificate reusing its existing private key, force-replace
// changes the actual signing key. The old CA stays in the ca.crt bundle (the
// old key is kept under a timestamped key) for a grace period, so a naive CA
// swap never happens and a running Kafka cluster keeps trusting the material it
// already serves.
const clusterCAForceReplaceAnnotation = "strimzi.io/force-replace"

// caRotationSettleTimeout bounds the CA key replacement: Strimzi renews every
// component certificate signed by the CA and rolls the Kafka brokers, which
// takes longer than a plain operator reconcile.
const caRotationSettleTimeout = 10 * time.Minute

// The real cluster-CA rotation scenario (plan §6.1 item 3) is the counterpart
// of the simulated rotation in carotation_test.go: instead of bumping the CA
// secret's resourceVersion (which only exercises the operator's version-based
// trigger), it forces Strimzi to rotate the cluster CA key and certificate for
// real and then proves the operator:
//
//   - renews the REST API TLS secret from the newly rotated CA (the renewed
//     private key entry verifies against the new CA certificate, not only the
//     old one),
//   - rebuilds the Kafka TLS keystore/truststore for the rotated CA (SSL mode),
//   - rolls the Deployment and keeps the REST API serving over HTTPS.
//
// The spec is enabled for the SSL matrix leg only: a real CA rotation rolls
// the Kafka brokers, and the REST API renewal trigger is auth-mode independent
// (the version-based path is already covered in every leg by
// carotation_test.go), so rotating the CA in all four clusters would only add
// broker restarts.
var _ = Describe("StrimziSchemaRegistry e2e real cluster CA rotation scenario", Ordered, Serial, func() {
	var (
		fixture *registryFixture
		ca      *clusterCA
	)

	BeforeAll(func() {
		fixture = newRegistryFixture("real-ca-rotation")
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

	It("renews the REST API certificate from the rotated CA and keeps serving", func() {
		if scenario.SecurityProtocol != "SSL" {
			Skip("the real CA rotation rolls the Kafka brokers and is enabled for the SSL leg only; the version-based CA renewal path runs in every leg (carotation_test.go)")
		}

		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs")
		fixture.ensureKafkaUser(ctx)

		By("applying the CR with securehttp enabled and an operator-managed TLS secret")
		Expect(fixture.apply(ctx, false, true, nil)).To(Succeed())
		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)
		fixture.verifyRoundTrip(ca, nil)
		waitForKafkaReady(ctx)

		tlsSecretName := fixture.Name + "-tls"
		By("recording the REST API certificate and the cluster CA before the rotation")
		before := waitForSecretWithKey(ctx, tlsSecretName, "tls-keystore.jks")
		beforeLeaf := jksLeafCertificate(before.Data["tls-keystore.jks"], before.Data["keystore_password"])
		beforeCACerts := clusterCACertificates(ctx)
		Expect(leafSignedBy(beforeLeaf, beforeCACerts)).To(BeTrue(),
			"the initial REST API certificate must be signed by the cluster CA")

		By("forcing a genuine Strimzi cluster CA key renewal on the CA key secret")
		annotateClusterCAKeyForReplacement(ctx, scenario.ClusterName+"-cluster-ca")

		By("waiting until Strimzi publishes the renewed CA certificate")
		renewedCACerts := waitForRenewedClusterCA(ctx, beforeCACerts)

		By("waiting until the renewed CA private key is the active signing key")
		waitForRotatedCAKey(ctx, renewedCACerts)

		// Strimzi persists the rotated CA certificate bundle and the rotated CA
		// private key in two separate secrets, and their write order is not part
		// of Strimzi's contract. Bumping the CA cert secret version once after
		// both carry the rotated material guarantees a reconcile that observes
		// the final CA state, so the renewal assertion below is deterministic
		// instead of racing Strimzi's internal write order. The certificate the
		// operator signs is still the genuinely rotated CA.
		By("triggering a reconcile against the final rotated CA material")
		bumpSecretVersion(ctx, scenario.ClusterName+"-cluster-ca-cert", "e2e.randsw.code/real-ca-rotation")

		By("waiting for the operator to renew the REST API TLS secret from the new CA")
		Eventually(func(g Gomega) {
			caCertSecret, err := getSecret(ctx, scenario.ClusterName+"-cluster-ca-cert")
			g.Expect(err).NotTo(HaveOccurred())
			current, err := getSecret(ctx, tlsSecretName)
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(current.Annotations).To(HaveKeyWithValue(caVersionAnnotationKey, caCertSecret.ResourceVersion),
				"the REST API TLS secret must record the rotated cluster CA version")
			g.Expect(current.ResourceVersion).NotTo(Equal(before.ResourceVersion),
				"the REST API TLS secret was not renewed after the CA rotation")

			leaf := jksLeafCertificate(current.Data["tls-keystore.jks"], current.Data["keystore_password"])
			g.Expect(leafSignedBy(leaf, renewedCACerts)).To(BeTrue(),
				"the renewed REST API certificate is not signed by the rotated CA")
		}, caRotationSettleTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the REST API certificate renewed from the rotated CA", caRotationSettleTimeout)

		By("waiting for the deployment pod template to record the renewed TLS secret version")
		// Compare against the TLS secret's CURRENT version: Strimzi may keep
		// updating the CA cert secret (and therefore trigger further renewals)
		// while the brokers roll onto the rotated CA, so pinning a snapshot
		// would race the operator's next renewal.
		Eventually(func(g Gomega) {
			current, err := getSecret(ctx, tlsSecretName)
			g.Expect(err).NotTo(HaveOccurred())
			dep := deploymentFor(ctx, fixture.Name)
			g.Expect(dep.Spec.Template.Annotations).To(
				HaveKeyWithValue(tlsVersionAnnotationKey, current.ResourceVersion),
				"the tlsVersion pod-template annotation must track the renewed secret")
		}, caRotationSettleTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the tlsVersion rollout marker", caRotationSettleTimeout)

		By("waiting for the operator to rebuild the Kafka TLS keystore for the rotated CA")
		jksSecretName := fixture.Name + "-jks"
		Eventually(func(g Gomega) {
			caCertSecret, err := getSecret(ctx, scenario.ClusterName+"-cluster-ca-cert")
			g.Expect(err).NotTo(HaveOccurred())
			jks, err := getSecret(ctx, jksSecretName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(jks.Annotations).To(HaveKeyWithValue(caVersionAnnotationKey, caCertSecret.ResourceVersion),
				"the Kafka JKS secret must be rebuilt for the rotated cluster CA")
			g.Expect(jks.Data).To(HaveKey("keystore.jks"))
		}, caRotationSettleTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for the Kafka keystore rebuild", caRotationSettleTimeout)

		By("waiting for the Kafka cluster and the registry to settle")
		waitForKafkaReady(ctx)
		waitForDeploymentRolloutFor(ctx, fixture.Name, readyTimeout)
		fixture.waitReady(ctx)

		By("passing the REST round-trip over HTTPS against the rotated CA")
		verifyRoundTripTrustingClusterCA(ctx, fixture.Name)
	})
})

// getSecret returns the named secret in the scenario namespace.
func getSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: scenario.Namespace,
	}, secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// annotateClusterCAKeyForReplacement adds the Strimzi force-replace annotation
// to the cluster CA key secret, retrying on conflict so it never races
// Strimzi's own writes to the secret.
func annotateClusterCAKeyForReplacement(ctx context.Context, name string) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := getSecret(ctx, name)
		if err != nil {
			return err
		}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[clusterCAForceReplaceAnnotation] = "true"
		return k8sClient.Update(ctx, secret)
	})
	Expect(err).NotTo(HaveOccurred(),
		"failed to annotate the cluster CA key secret %s/%s for key replacement", scenario.Namespace, name)
}

// clusterCACertificates returns every certificate of the cluster CA cert
// secret's ca.crt bundle. During a rotation the bundle holds both the new and
// the retired CA certificate, so callers must not assume a single one.
func clusterCACertificates(ctx context.Context) []*x509.Certificate {
	secret, err := getSecret(ctx, scenario.ClusterName+"-cluster-ca-cert")
	Expect(err).NotTo(HaveOccurred())
	return parseCABundle(secret)
}

// parseCABundle parses the ca.crt bundle of the given CA cert secret.
func parseCABundle(secret *corev1.Secret) []*x509.Certificate {
	certs, err := certprocessor.StringToCertificates(string(secret.Data["ca.crt"]))
	Expect(err).NotTo(HaveOccurred(), "failed to parse the cluster CA certificate bundle")
	return certs
}

// waitForRenewedClusterCA waits until the cluster CA cert secret carries a
// certificate that was not part of the pre-rotation bundle and returns the new
// certificates. The retired CA certificate remains in the bundle for the trust
// grace period, so only the unseen certificates are returned.
func waitForRenewedClusterCA(ctx context.Context, before []*x509.Certificate) []*x509.Certificate {
	beforeDER := make(map[string]struct{}, len(before))
	for _, cert := range before {
		beforeDER[string(cert.Raw)] = struct{}{}
	}

	var renewed []*x509.Certificate
	Eventually(func(g Gomega) {
		secret, err := getSecret(ctx, scenario.ClusterName+"-cluster-ca-cert")
		g.Expect(err).NotTo(HaveOccurred())
		certs := parseCABundle(secret)

		fresh := make([]*x509.Certificate, 0, len(certs))
		for _, cert := range certs {
			if _, seen := beforeDER[string(cert.Raw)]; !seen {
				fresh = append(fresh, cert)
			}
		}
		g.Expect(fresh).NotTo(BeEmpty(),
			"the cluster CA cert secret still holds only the original CA certificate")
		renewed = fresh
	}, caRotationSettleTimeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for Strimzi to publish the renewed cluster CA certificate", caRotationSettleTimeout)

	return renewed
}

// waitForRotatedCAKey waits until the ca.key in the cluster CA key secret
// matches one of the CA certificates that appeared in the rotated ca.crt
// bundle. The public-key comparison makes the renewal assertion independent of
// the order Strimzi persists the two secrets in.
func waitForRotatedCAKey(ctx context.Context, renewedCACerts []*x509.Certificate) {
	Eventually(func(g Gomega) {
		secret, err := getSecret(ctx, scenario.ClusterName+"-cluster-ca")
		g.Expect(err).NotTo(HaveOccurred())
		key, err := certprocessor.StringToPrivateKey(string(secret.Data["ca.key"]))
		g.Expect(err).NotTo(HaveOccurred(), "failed to parse the cluster CA private key")

		matched := false
		for _, cert := range renewedCACerts {
			if reflect.DeepEqual(cert.PublicKey, key.Public()) {
				matched = true
			}
		}
		g.Expect(matched).To(BeTrue(),
			"the cluster CA key secret still holds the retired CA private key")
	}, caRotationSettleTimeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the rotated cluster CA private key", caRotationSettleTimeout)
}

// jksLeafCertificate decodes a JKS keystore and returns the leaf certificate of
// its private key entry (the Schema Registry REST API server certificate).
func jksLeafCertificate(keystoreBytes, password []byte) *x509.Certificate {
	store := keystore.New(keystore.WithOrderedAliases())
	Expect(store.Load(bytes.NewReader(keystoreBytes), password)).To(Succeed(),
		"failed to decode the REST API TLS keystore")

	for _, alias := range store.Aliases() {
		if !store.IsPrivateKeyEntry(alias) {
			continue
		}
		chain, err := store.GetPrivateKeyEntryCertificateChain(alias)
		Expect(err).NotTo(HaveOccurred())
		Expect(chain).NotTo(BeEmpty(), "the REST API keystore private key entry has no certificate")
		leaf, err := x509.ParseCertificate(chain[0].Content)
		Expect(err).NotTo(HaveOccurred())
		return leaf
	}

	Fail("the REST API TLS keystore has no private key entry")
	return nil
}

// leafSignedBy reports whether any of the given CA certificates directly signed
// the leaf certificate.
func leafSignedBy(leaf *x509.Certificate, caCerts []*x509.Certificate) bool {
	for _, ca := range caCerts {
		if leaf.CheckSignatureFrom(ca) == nil {
			return true
		}
	}
	return false
}

// verifyRoundTripTrustingClusterCA runs the REST round-trip against the named
// registry, trusting every certificate currently in the cluster CA bundle: the
// pod may still serve the retired certificate until its rollout completes.
func verifyRoundTripTrustingClusterCA(ctx context.Context, name string) {
	secret, err := getSecret(ctx, scenario.ClusterName+"-cluster-ca-cert")
	Expect(err).NotTo(HaveOccurred())
	pool := x509.NewCertPool()
	for _, cert := range parseCABundle(secret) {
		pool.AddCert(cert)
	}

	waitForServiceFor(ctx, name, statusTimeout)
	port, stop, err := startPortForward(ctx, name, registryServicePort)
	Expect(err).NotTo(HaveOccurred())
	defer stop()

	assertRoundTrip(newRegistryClientFor(name, pool, nil, port))
}

// waitForKafkaReady waits until the shared Strimzi Kafka CR reports Ready=True.
// A real CA rotation renews the broker certificates and rolls the brokers, so
// the REST round-trip must only run once Kafka is serving again.
func waitForKafkaReady(ctx context.Context) {
	Eventually(func(g Gomega) {
		kafkaCR := &unstructured.Unstructured{}
		kafkaCR.SetGroupVersionKind(listenerKafkaGVK)
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.ClusterName,
			Namespace: scenario.Namespace,
		}, kafkaCR)).To(Succeed())

		conditions, _, err := unstructured.NestedSlice(kafkaCR.Object, "status", "conditions")
		g.Expect(err).NotTo(HaveOccurred())
		ready := false
		for _, raw := range conditions {
			condition, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if condition["type"] == "Ready" && condition["status"] == "True" {
				ready = true
			}
		}
		g.Expect(ready).To(BeTrue(), "the Kafka cluster %s/%s is not Ready yet",
			scenario.Namespace, scenario.ClusterName)
	}, caRotationSettleTimeout, 5*time.Second).Should(Succeed(),
		"timed out after %s waiting for the Kafka cluster to become Ready again", caRotationSettleTimeout)
}
