//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	"software.sslmate.com/src/go-pkcs12"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// The user-provided TLS secret scenario (E2-1/E2-2) covers the documented
// spec.tlssecretname feature end to end: a pre-built JKS keystore secret and
// a standard kubernetes.io/tls (PEM) secret, the latter exercising the
// operator's conversion into the derived <registry>-<tlssecretname>-jks
// secret and the mount repoint + pod rollout on secret rotation.
//
// The scenario runs as an additional Ordered container inside every existing
// matrix job (one kind cluster per security protocol), so the user-TLS path
// is proven in all four auth modes without a fifth cluster.
const (
	userTLSSecretName     = "schema-registry-tls"
	userTLSKeystorePass   = "e2e-keystore-pass"
	restAPITLSVolumeName  = "rest-api-tls"
	restAPIKeystoreLocJKS = "/var/rest-api-tls/tls-keystore.jks"
)

var _ = Describe("StrimziSchemaRegistry e2e user TLS secret scenario", Ordered, Serial, func() {
	var (
		testCA  *testutil.TestCA
		userCA  *clusterCA
		certPEM []byte
		keyPEM  []byte
	)

	BeforeAll(func() {
		By("generating an external CA and a REST API server certificate for the registry service name")
		var err error
		testCA, err = testutil.GenerateTestCA()
		Expect(err).NotTo(HaveOccurred())

		certPEM, keyPEM, err = signUserTLSServerCert(testCA, userTLSDNSNames())
		Expect(err).NotTo(HaveOccurred())

		userCA = &clusterCA{
			certPEM: []byte(testCA.CACertPEM),
			cert:    testCA.CACert,
			key:     testCA.CAKey,
		}
	})

	AfterAll(func() {
		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(context.Background(), readyTimeout)

		By("deleting the user-provided TLS secret")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userTLSSecretName,
				Namespace: scenario.Namespace,
			},
		}
		err := k8sClient.Delete(context.Background(), secret)
		Expect(client.IgnoreNotFound(err)).To(Succeed())
	})

	// E2-1: a pre-built JKS keystore secret is mounted as-is; the operator
	// must not create its own <name>-tls secret and the REST API must serve
	// the user certificate.
	It("phase 1: deploys with a user-provided JKS keystore secret", func() {
		ctx := context.Background()

		By("building the JKS keystore from the user certificate")
		keystore, _, err := certprocessor.NewCertProcessor(logr.Logger{}).ConvertPEMToJKS(
			ctx, string(certPEM), string(keyPEM), testCA.CACertPEM, userTLSKeystorePass)
		Expect(err).NotTo(HaveOccurred())

		By("creating the user-provided TLS secret in the JKS shape")
		applyUserTLSSecret(ctx, map[string][]byte{
			"tls-keystore.jks":  keystore,
			"keystore_password": []byte(userTLSKeystorePass),
			"key_password":      []byte(userTLSKeystorePass),
		})

		By("applying the CR with spec.tlssecretname")
		Expect(applyRegistryCR(ctx, false, true, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.TLSSecretName = userTLSSecretName
		})).To(Succeed())
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("verifying the operator did not create its own <name>-tls secret")
		managed := &corev1.Secret{}
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName + "-tls",
			Namespace: scenario.Namespace,
		}, managed)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"the operator must not create <name>-tls when spec.tlssecretname is set (err: %v)", err)

		By("verifying the deployment mounts the user secret and points at the JKS keystore")
		Expect(restAPITLSMountedSecret(ctx)).To(Equal(userTLSSecretName))
		waitForDeploymentEnv(ctx, readyTimeout, "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", restAPIKeystoreLocJKS)

		By("passing the REST round-trip trusting the user CA")
		verifyRoundTrip(userCA, nil)
	})

	// E2-2: switching the same secret to the kubernetes.io/tls (PEM) shape
	// bumps its resourceVersion, so the operator converts the material into
	// the derived <registry>-<tlssecretname>-jks secret, repoints the
	// deployment mounts and rolls the pods; the REST API keeps serving the
	// user certificate.
	It("phase 2: converts a kubernetes.io/tls PEM secret and rolls on rotation", func() {
		ctx := context.Background()

		By("rotating the user secret to the PEM shape")
		applyUserTLSSecret(ctx, map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
			"ca.crt":  []byte(testCA.CACertPEM),
		})

		By("waiting for the derived JKS secret to be created")
		derivedName := scenario.RegistryName + "-" + userTLSSecretName + "-jks"
		Eventually(func(g Gomega) {
			derived := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      derivedName,
				Namespace: scenario.Namespace,
			}, derived)).To(Succeed())
			g.Expect(derived.Data).To(HaveKey("tls-keystore.jks"))
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"timed out waiting for the derived secret %s/%s", scenario.Namespace, derivedName)

		By("waiting for the deployment mounts to be repointed at the derived secret")
		Eventually(func() string {
			return restAPITLSMountedSecret(ctx)
		}, statusTimeout, 2*time.Second).Should(Equal(derivedName),
			"the rest-api-tls volume must mount the derived secret")

		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("passing the REST round-trip trusting the user CA after the rollout")
		verifyRoundTrip(userCA, nil)
	})

	// E2-3: a PKCS12 keystore secret (spec.keystoretype: PKCS12) is mounted
	// as-is and the deployment points SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION
	// at the .p12 file (plus the non-default SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE
	// env var); the REST API must serve the user certificate.
	It("phase 3: deploys with a user-provided PKCS12 keystore secret", func() {
		ctx := context.Background()

		By("building the PKCS12 keystore from the user certificate")
		serverKey, err := parsePrivateKey(keyPEM)
		Expect(err).NotTo(HaveOccurred())
		p12, err := pkcs12.Legacy.Encode(serverKey, userCACert(certPEM), []*x509.Certificate{testCA.CACert}, userTLSKeystorePass)
		Expect(err).NotTo(HaveOccurred())

		By("rotating the user secret to the PKCS12 shape")
		applyUserTLSSecret(ctx, map[string][]byte{
			"tls-keystore.p12":  p12,
			"keystore_password": []byte(userTLSKeystorePass),
			"key_password":      []byte(userTLSKeystorePass),
		})

		By("applying the CR with spec.keystoretype=PKCS12")
		Expect(applyRegistryCR(ctx, false, true, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.TLSSecretName = userTLSSecretName
			spec.KeystoreType = "PKCS12"
		})).To(Succeed())
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("verifying the deployment points at the .p12 keystore location")
		waitForDeploymentEnv(ctx, readyTimeout, "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", "/var/rest-api-tls/tls-keystore.p12")
		waitForDeploymentEnv(ctx, readyTimeout, "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE", "PKCS12")

		By("verifying the deployment mounts the user secret")
		Expect(restAPITLSMountedSecret(ctx)).To(Equal(userTLSSecretName))

		By("passing the REST round-trip trusting the user CA")
		verifyRoundTrip(userCA, nil)
	})
})

// userTLSDNSNames returns the DNS SANs the user REST API certificate of the
// scenario's shared CR must carry.
func userTLSDNSNames() []string {
	return userTLSDNSNamesFor(scenario.RegistryName)
}

// userTLSDNSNamesFor returns the DNS SANs a user REST API certificate for the
// named registry must carry: the names REST clients verify against (the
// registry service DNS name is what the e2e REST client sets as TLS
// ServerName).
func userTLSDNSNamesFor(registryName string) []string {
	registry := registryName + "." + scenario.Namespace
	return []string{
		registryName,
		registry + ".svc",
		registry + ".svc.cluster.local",
	}
}

// userCACert parses the PEM-encoded user REST API server certificate.
func userCACert(certPEM []byte) *x509.Certificate {
	cert, err := parseCertificate(certPEM)
	Expect(err).NotTo(HaveOccurred())
	return cert
}

// signUserTLSServerCert signs a REST API server certificate for the given
// DNS names with the provided CA and returns the PEM-encoded certificate and
// private key (the kubernetes.io/tls secret material).
func signUserTLSServerCert(ca *testutil.TestCA, dnsNames []string) ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate the user TLS server key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate a serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.CACert, &key.PublicKey, ca.CAKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign the user TLS server certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal the user TLS server key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// applyUserTLSSecret creates or updates the user-provided TLS secret with the
// given data. Updating an existing secret bumps its resourceVersion, which
// the operator detects as a rotation.
func applyUserTLSSecret(ctx context.Context, data map[string][]byte) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userTLSSecretName,
			Namespace: scenario.Namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &corev1.Secret{}
		getErr := k8sClient.Get(ctx, types.NamespacedName{
			Name:      userTLSSecretName,
			Namespace: scenario.Namespace,
		}, existing)
		if apierrors.IsNotFound(getErr) {
			return k8sClient.Create(ctx, secret)
		}
		if getErr != nil {
			return getErr
		}
		existing.Data = data
		return k8sClient.Update(ctx, existing)
	})
	Expect(err).NotTo(HaveOccurred(),
		"failed to apply the user TLS secret %s/%s", scenario.Namespace, userTLSSecretName)
}

// restAPITLSMountedSecret returns the secret name the shared CR's deployment
// rest-api-tls volume currently mounts (empty when the volume is absent).
func restAPITLSMountedSecret(ctx context.Context) string {
	return restAPITLSMountedSecretFor(ctx, scenario.RegistryName)
}

// restAPITLSMountedSecretFor returns the secret name the named CR's deployment
// rest-api-tls volume currently mounts (empty when the volume is absent).
func restAPITLSMountedSecretFor(ctx context.Context, name string) string {
	dep := &appsv1.Deployment{}
	Expect(k8sClient.Get(ctx, deploymentKeyFor(name), dep)).To(Succeed())
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == restAPITLSVolumeName && vol.Secret != nil {
			return vol.Secret.SecretName
		}
	}
	return ""
}
