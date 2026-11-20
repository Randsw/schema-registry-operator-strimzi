//go:build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

const (
	// multiReplicaTLSSecretSuffix names the user-provided REST API keystore
	// secret of the multi-replica HTTPS scenario.
	multiReplicaTLSSecretSuffix = "-user-tls"

	// multiReplicaTruststoreSecretSuffix names the secret carrying the JKS
	// truststore replicas use to verify each other's REST API certificate.
	multiReplicaTruststoreSecretSuffix = "-inter-trust"

	// interInstanceTrustMountPath is where the truststore secret is mounted.
	interInstanceTrustMountPath = "/var/inter-instance-trust"
)

// The multi-replica HTTPS scenario (plan §4.3 item 3) covers the supported
// HA-with-HTTPS path: spec.replicas=2, securehttp=true and a user-provided
// spec.tlssecretname. The operator-managed certificate is rejected for this
// combination (Ready=False/ReplicasRequireUserTLSSecret) because its SANs are
// DNS-only while replicas advertise their pod IP as host.name.
//
// Following the README HA guidance, the CR supplies the inter-instance
// truststore and disables endpoint identification through the supported
// escape hatches: the truststore location/password environment variables and
// its volume come from spec.template, and
// ssl.endpoint.identification.algorithm is set to "" via spec.configuration.
// The scenario then asserts two ready replicas, the user secret mount and a
// passing REST round-trip (including a write, which exercises inter-instance
// HTTPS forwarding to the elected master).
var _ = Describe("StrimziSchemaRegistry e2e multi-replica HTTPS scenario", Ordered, Serial, func() {
	// Built in BeforeAll: the Describe body runs before BeforeSuite populates
	// the matrix scenario configuration.
	var fixture *registryFixture

	var (
		material         *userTLSMaterial
		userTLSSecret    string
		truststoreSecret string
	)

	BeforeAll(func() {
		fixture = newRegistryFixture("multitls")
		material = newUserTLSMaterial(fixture.Name)
		userTLSSecret = fixture.Name + multiReplicaTLSSecretSuffix
		truststoreSecret = fixture.Name + multiReplicaTruststoreSecretSuffix
	})

	AfterAll(func() {
		ctx := context.Background()
		By("deleting the isolated StrimziSchemaRegistry CR to leave a clean slate")
		fixture.delete(ctx)
		By("deleting the user-provided TLS and truststore secrets")
		deleteSecretIfExists(ctx, userTLSSecret)
		deleteSecretIfExists(ctx, truststoreSecret)
		By("deleting the copied KafkaUser secret")
		deleteSecretIfExists(ctx, fixture.Name)
	})

	It("runs two replicas over HTTPS with a user TLS secret and passes the REST round-trip", func() {
		ctx := context.Background()

		By("creating the KafkaUser secret the isolated CR needs")
		fixture.ensureUserSecret(ctx)

		By("creating the user-provided JKS keystore secret")
		applySecret(ctx, userTLSSecret, corev1.SecretTypeOpaque, material.jksData(ctx))

		By("creating the inter-instance truststore secret for the user CA")
		truststore, truststorePassword := material.truststoreData(ctx)
		applySecret(ctx, truststoreSecret, corev1.SecretTypeOpaque, map[string][]byte{
			"truststore.jks":      truststore,
			"truststore_password": []byte(truststorePassword),
		})

		By("applying the CR with replicas=2, securehttp and the user TLS secret")
		Expect(fixture.apply(ctx, false, true, ptr.To(int32(2)),
			func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
				spec.TLSSecretName = userTLSSecret
				// Inter-instance HTTPS contacts pod IPs, which cannot be
				// pre-provisioned into a static certificate; the README
				// documents disabling endpoint identification for that case.
				spec.Configuration = map[string]string{
					"ssl.endpoint.identification.algorithm": "",
				}
				spec.Template.Spec.Volumes = append(spec.Template.Spec.Volumes, corev1.Volume{
					Name: "inter-instance-trust",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: truststoreSecret},
					},
				})
				spec.Template.Spec.Containers[0].VolumeMounts = append(
					spec.Template.Spec.Containers[0].VolumeMounts,
					corev1.VolumeMount{
						Name:      "inter-instance-trust",
						MountPath: interInstanceTrustMountPath,
						ReadOnly:  true,
					})
				spec.Template.Spec.Containers[0].Env = append(
					spec.Template.Spec.Containers[0].Env,
					corev1.EnvVar{
						Name:  "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION",
						Value: interInstanceTrustMountPath + "/truststore.jks",
					},
					corev1.EnvVar{
						Name:  "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD",
						Value: truststorePassword,
					})
			})).To(Succeed())

		By("waiting for both replicas to become ready")
		waitForDeploymentReplicasFor(ctx, fixture.Name, readyTimeout, 2)

		By("verifying the HTTPS inter-instance configuration reached the pod template")
		waitForDeploymentEnvFor(ctx, fixture.Name, readyTimeout,
			"SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL", "https")
		waitForDeploymentEnvFor(ctx, fixture.Name, readyTimeout,
			"SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION", interInstanceTrustMountPath+"/truststore.jks")
		waitForDeploymentEnvFor(ctx, fixture.Name, readyTimeout,
			"SCHEMA_REGISTRY_SSL_ENDPOINT_IDENTIFICATION_ALGORITHM", "")

		By("verifying the deployment mounts the user-provided TLS secret")
		Eventually(func() string {
			return fixture.mountedTLSSecret(ctx)
		}, statusTimeout, 2*time.Second).Should(Equal(userTLSSecret),
			"the rest-api-tls volume must mount the user-provided secret")

		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)

		By("passing the REST round-trip trusting the user CA")
		fixture.verifyRoundTrip(material.cluster, nil)
	})
})
