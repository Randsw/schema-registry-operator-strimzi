//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// Operator-managed pod-template/secret annotation keys. They mirror the
// constants in internal/controller (the key prefix and suffixes are
// unexported there; CAVersionKey is exported but importing the controller
// package into the e2e suite is unnecessary).
const (
	caVersionAnnotationKey       = "strimziregistryoperator.randsw.code/caSecretVersion"
	tlsVersionAnnotationKey      = "strimziregistryoperator.randsw.code/tlsVersion"
	jksVersionAnnotationKey      = "strimziregistryoperator.randsw.code/jksVersion"
	saslUserVersionAnnotationKey = "strimziregistryoperator.randsw.code/saslUserSecretVersion"
)

// registryFixture is an isolated registry scenario. Its CR name, kafkastore
// topic and group.id are unique, so it can never share leader-election state
// or schemas with another scenario, and its Deployment/Service never collide
// with the shared fixtures used by the original scenarios.
type registryFixture struct {
	Name    string
	Topic   string
	GroupID string
}

// newRegistryFixture derives an isolated fixture from the matrix scenario
// registry name: the CR is <registry>-<suffix>, and the Kafka topic/group.id
// carry the same suffix.
func newRegistryFixture(suffix string) *registryFixture {
	return &registryFixture{
		Name:    scenario.RegistryName + "-" + suffix,
		Topic:   "registry-schemas-" + suffix,
		GroupID: "schema-registry-" + suffix,
	}
}

// deploymentKey returns the NamespacedName of the fixture's Deployment.
func (f *registryFixture) deploymentKey() types.NamespacedName {
	return deploymentKeyFor(f.Name)
}

// apply creates or updates the fixture CR with the shared spec builder.
func (f *registryFixture) apply(ctx context.Context, clientAuth, secureHTTP bool, replicas *int32,
	mutators ...func(*v1alpha1.StrimziSchemaRegistrySpec)) error {
	return applyRegistryCRFor(ctx, f.Name, f.Topic, f.GroupID, clientAuth, secureHTTP, replicas, mutators...)
}

// ensureUserSecret copies the Strimzi-managed KafkaUser secret of the matrix
// scenario to a secret named after the fixture CR: the operator looks the
// KafkaUser secret up by instance name. The copied material belongs to the
// same KafkaUser principal, so the existing ACLs apply. PLAINTEXT modes need
// no KafkaUser secret.
func (f *registryFixture) ensureUserSecret(ctx context.Context) {
	if scenario.SecurityProtocol == "PLAINTEXT" {
		return
	}
	source := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name:      scenario.RegistryName,
		Namespace: scenario.Namespace,
	}, source)).To(Succeed(),
		"the Strimzi KafkaUser secret %s/%s must exist for the %s matrix job",
		scenario.Namespace, scenario.RegistryName, scenario.SecurityProtocol)
	applySecret(ctx, f.Name, source.Type, source.DeepCopy().Data)
}

// delete removes the fixture CR and waits until its Deployment is gone.
func (f *registryFixture) delete(ctx context.Context) {
	deleteRegistryCRFor(ctx, f.Name, readyTimeout)
}

// waitReady waits until the fixture CR reports Ready=True.
func (f *registryFixture) waitReady(ctx context.Context) {
	waitForReadyConditionFor(ctx, f.Name, readyTimeout)
}

// waitRollout waits until the fixture Deployment has rolled out.
func (f *registryFixture) waitRollout(ctx context.Context) {
	waitForDeploymentRolloutFor(ctx, f.Name, readyTimeout)
}

// verifyRoundTrip runs the REST round-trip against the fixture's Service.
func (f *registryFixture) verifyRoundTrip(ca *clusterCA, clientCert *tls.Certificate) {
	verifyRoundTripFor(f.Name, ca, clientCert)
}

// mountedTLSSecret returns the secret the fixture Deployment's rest-api-tls
// volume currently mounts.
func (f *registryFixture) mountedTLSSecret(ctx context.Context) string {
	return restAPITLSMountedSecretFor(ctx, f.Name)
}

// applySecret creates or updates a secret with the given type and data,
// retrying on conflict so it never races the operator or Strimzi.
func applySecret(ctx context.Context, name string, secretType corev1.SecretType, data map[string][]byte) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: scenario.Namespace},
		Type:       secretType,
		Data:       data,
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &corev1.Secret{}
		getErr := k8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: scenario.Namespace,
		}, existing)
		if apierrors.IsNotFound(getErr) {
			return k8sClient.Create(ctx, secret)
		}
		if getErr != nil {
			return getErr
		}
		existing.Type = secretType
		existing.Data = data
		return k8sClient.Update(ctx, existing)
	})
	Expect(err).NotTo(HaveOccurred(), "failed to apply the secret %s/%s", scenario.Namespace, name)
}

// deleteSecretIfExists deletes the named secret, ignoring NotFound.
func deleteSecretIfExists(ctx context.Context, name string) {
	err := k8sClient.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: scenario.Namespace},
	})
	Expect(apierrors.IsNotFound(err) || err == nil).To(BeTrue(),
		"failed to delete the secret %s/%s: %v", scenario.Namespace, name, err)
}

// bumpSecretVersion changes a secret's metadata without touching its data and
// returns the new resourceVersion. The operator detects Strimzi CA and
// KafkaUser credential rotations through the secret's resourceVersion, so this
// simulates a rotation from the operator's point of view while keeping the live
// Kafka TLS/SCRAM material valid (a real CA rotation would break the running
// Kafka cluster).
func bumpSecretVersion(ctx context.Context, secretName, annotationKey string) string {
	var updated string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      secretName,
			Namespace: scenario.Namespace,
		}, secret); err != nil {
			return err
		}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[annotationKey] = fmt.Sprintf("%d", time.Now().UnixNano())
		if err := k8sClient.Update(ctx, secret); err != nil {
			return err
		}
		updated = secret.ResourceVersion
		return nil
	})
	Expect(err).NotTo(HaveOccurred(), "failed to bump the secret %s/%s", scenario.Namespace, secretName)
	return updated
}

// deploymentFor returns the named CR's Deployment.
func deploymentFor(ctx context.Context, name string) *appsv1.Deployment {
	dep := &appsv1.Deployment{}
	Expect(k8sClient.Get(ctx, deploymentKeyFor(name), dep)).To(Succeed())
	return dep
}

// waitForDeploymentAnnotationFor waits until the named CR's Deployment pod
// template carries annotationKey=want (the operator's rollout markers).
func waitForDeploymentAnnotationFor(ctx context.Context, name string, timeout time.Duration, annotationKey, want string) {
	Eventually(func(g Gomega) {
		dep := deploymentFor(ctx, name)
		g.Expect(dep.Spec.Template.Annotations).To(HaveKeyWithValue(annotationKey, want),
			"pod-template annotation %s is %q, want %q", annotationKey, dep.Spec.Template.Annotations[annotationKey], want)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for annotation %s=%s on deployment %s/%s",
		timeout, annotationKey, want, scenario.Namespace, name+deploymentSuffix)
}

// waitForDeploymentRolloutKeepingAvailability waits for the named CR's
// Deployment to finish rolling while asserting it never drops to zero
// available replicas. A rolling update of a single-replica Deployment with
// maxUnavailable=0 starts the replacement pod before terminating the old one,
// so the Service keeps serving throughout (zero-downtime continuity).
func waitForDeploymentRolloutKeepingAvailability(ctx context.Context, name string, timeout time.Duration) {
	sawAvailable := false
	Eventually(func(g Gomega) {
		dep := deploymentFor(ctx, name)

		if dep.Status.AvailableReplicas > 0 {
			sawAvailable = true
		}
		if sawAvailable {
			g.Expect(dep.Status.AvailableReplicas).To(BeNumerically(">=", 1),
				"the registry had zero available replicas mid-rollout")
		}

		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		g.Expect(dep.Status.ObservedGeneration).To(BeNumerically(">=", dep.Generation),
			"generation %d has not been observed yet", dep.Generation)
		g.Expect(dep.Status.UpdatedReplicas).To(Equal(desired),
			"%d of %d updated replicas so far", dep.Status.UpdatedReplicas, desired)
		g.Expect(dep.Status.AvailableReplicas).To(Equal(desired),
			"%d of %d available replicas so far", dep.Status.AvailableReplicas, desired)
	}, timeout, time.Second).Should(Succeed(),
		"timed out after %s waiting for the zero-downtime rollout of deployment %s/%s",
		timeout, scenario.Namespace, name+deploymentSuffix)
}

// waitForSecretDeletion waits until the named secret is gone.
func waitForSecretDeletion(ctx context.Context, name string) {
	Eventually(func(g Gomega) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: scenario.Namespace,
		}, &corev1.Secret{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"the secret %s is still present (err: %v)", name, err)
	}, statusTimeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the deletion of the secret %s/%s",
		statusTimeout, scenario.Namespace, name)
}

// waitForSecretWithKey waits until the named secret exists and carries key.
func waitForSecretWithKey(ctx context.Context, name, key string) *corev1.Secret {
	var secret *corev1.Secret
	Eventually(func(g Gomega) {
		current := &corev1.Secret{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: scenario.Namespace,
		}, current)).To(Succeed())
		g.Expect(current.Data).To(HaveKey(key))
		secret = current
	}, statusTimeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the secret %s/%s with key %q",
		statusTimeout, scenario.Namespace, name, key)
	return secret
}

// userTLSMaterial bundles a freshly generated user REST API server certificate
// (PEM) with the private CA that signed it and the matching CA view used by the
// REST client.
type userTLSMaterial struct {
	ca      *testutil.TestCA
	cluster *clusterCA
	certPEM []byte
	keyPEM  []byte
}

// newUserTLSMaterial generates a private CA and a server certificate for the
// named registry's Service DNS names.
func newUserTLSMaterial(registryName string) *userTLSMaterial {
	ca, err := testutil.GenerateTestCA()
	Expect(err).NotTo(HaveOccurred())
	certPEM, keyPEM, err := signUserTLSServerCert(ca, userTLSDNSNamesFor(registryName))
	Expect(err).NotTo(HaveOccurred())
	return &userTLSMaterial{
		ca:      ca,
		cluster: &clusterCA{certPEM: []byte(ca.CACertPEM), cert: ca.CACert, key: ca.CAKey},
		certPEM: certPEM,
		keyPEM:  keyPEM,
	}
}

// pemData returns the kubernetes.io/tls secret data.
func (m *userTLSMaterial) pemData() map[string][]byte {
	return map[string][]byte{
		"tls.crt": m.certPEM,
		"tls.key": m.keyPEM,
		"ca.crt":  []byte(m.ca.CACertPEM),
	}
}

// jksData builds the pre-built JKS keystore secret data.
func (m *userTLSMaterial) jksData(ctx context.Context) map[string][]byte {
	keystore, _, err := certprocessor.NewCertProcessor(logr.Logger{}).ConvertPEMToJKS(
		ctx, string(m.certPEM), string(m.keyPEM), m.ca.CACertPEM, userTLSKeystorePass)
	Expect(err).NotTo(HaveOccurred())
	return map[string][]byte{
		"tls-keystore.jks":  keystore,
		"keystore_password": []byte(userTLSKeystorePass),
		"key_password":      []byte(userTLSKeystorePass),
	}
}

// truststoreData builds a JKS truststore containing the user CA, used by
// replicas to verify each other's certificate over inter-instance HTTPS.
func (m *userTLSMaterial) truststoreData(ctx context.Context) ([]byte, string) {
	truststore, password, err := certprocessor.NewCertProcessor(logr.Logger{}).
		CreateTruststore(ctx, m.ca.CACertPEM, "")
	Expect(err).NotTo(HaveOccurred())
	return []byte(truststore), password
}
