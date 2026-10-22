//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

const (
	// localPort is the local port used to port-forward the Schema Registry
	// service (remote port 443, the TLS REST API port).
	localPort = 18443

	// testSubject is the schema subject used for the REST round-trip.
	testSubject = "e2e-test-value"

	// testSchema is an Avro schema registered under testSubject.
	testSchema = `{"type":"record","name":"e2etest","fields":[{"name":"user","type":"string"},{"name":"car","type":"string"},{"name":"color","type":"string"}]}`

	// readyTimeout is the maximum time to wait for the CR or deployment
	// transitions.
	readyTimeout = 5 * time.Minute
)

// The scenario runs three phases against the same Kafka cluster:
//
//  1. clientauth=false, securehttp=true — the Schema Registry deploys and a
//     REST round-trip (register/fetch/config) succeeds, proving the Kafka
//     connection works with the scenario's securityprotocol (schemas are
//     persisted to the registry-schemas Kafka topic).
//  2. clientauth=true, securehttp=false — the operator must reject the
//     invalid combination (ClientAuthRequiresSecureHTTP).
//  3. clientauth=true, securehttp=true — the deployment is updated, the REST
//     round-trip succeeds with a cluster-CA-signed client certificate and
//     requests without a client certificate are rejected.
var _ = Describe("StrimziSchemaRegistry e2e scenario", Ordered, func() {
	var ca *clusterCA

	BeforeAll(func() {
		By(fmt.Sprintf("scenario: securityprotocol=%s listener=%s namespace=%s cluster=%s registry=%s",
			scenario.SecurityProtocol, scenario.Listener, scenario.Namespace,
			scenario.ClusterName, scenario.RegistryName))

		var err error
		ca, err = fetchClusterCA(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	It("phase 1: deploys with clientauth disabled and passes the REST round-trip", func() {
		ctx := context.Background()

		applyRegistryCR(ctx, false, true)
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		verifyRoundTrip(ca, nil)
	})

	It("rejects clientauth without securehttp", func() {
		ctx := context.Background()

		applyRegistryCR(ctx, true, false)
		waitForCondition(ctx, readyTimeout, func(cond *metav1.Condition) bool {
			return cond.Status == metav1.ConditionFalse && cond.Reason == "ClientAuthRequiresSecureHTTP"
		})
	})

	It("phase 2: redeploys with clientauth enabled and enforces mutual TLS on the REST API", func() {
		ctx := context.Background()

		applyRegistryCR(ctx, true, true)

		By("waiting for the deployment to pick up the clientauth configuration")
		waitForDeploymentEnv(ctx, readyTimeout, "SCHEMA_REGISTRY_SSL_CLIENT_AUTH", "true")
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		clientCert, err := ca.signClientCert("e2e-client")
		Expect(err).NotTo(HaveOccurred())

		By("positive: REST round-trip with a cluster-CA-signed client certificate")
		verifyRoundTrip(ca, &clientCert)

		By("negative: REST request without a client certificate is rejected")
		verifyClientAuthEnforced(ca)
	})
})

// applyRegistryCR creates or updates the StrimziSchemaRegistry CR for the
// scenario with the given clientauth/securehttp values.
func applyRegistryCR(ctx context.Context, clientAuth, secureHTTP bool) {
	replicationFactor := int32(1)

	cr := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scenario.RegistryName,
			Namespace: scenario.Namespace,
			Labels: map[string]string{
				"strimzi.io/cluster": scenario.ClusterName,
			},
		},
		Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
			Listener:                         scenario.Listener,
			SecurityProtocol:                 scenario.SecurityProtocol,
			CompatibilityLevel:               "forward",
			SecureHTTP:                       secureHTTP,
			ClientAuth:                       clientAuth,
			HeapOpts:                         "-Xms512M -Xmx512M",
			KafkastoreTopic:                  "registry-schemas",
			KafkastoreTopicReplicationFactor: &replicationFactor,
			SchemaRegistryGroupID:            "schema-registry",
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{{
						Name:            "confluent-sr",
						Image:           "confluentinc/cp-schema-registry:7.6.5",
						ImagePullPolicy: corev1.PullIfNotPresent,
					}},
				},
			},
		},
	}

	existing := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())
		return
	}
	Expect(err).NotTo(HaveOccurred())
	existing.Spec = cr.Spec
	Expect(k8sClient.Update(ctx, existing)).To(Succeed())
}

// waitForReadyCondition waits until the CR reports the Ready=True condition.
func waitForReadyCondition(ctx context.Context, timeout time.Duration) {
	waitForCondition(ctx, timeout, func(cond *metav1.Condition) bool {
		return cond.Status == metav1.ConditionTrue && cond.Reason == "Ready"
	})
}

// waitForCondition polls the CR's Ready condition until match returns true.
func waitForCondition(ctx context.Context, timeout time.Duration, match func(*metav1.Condition) bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cr := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName,
			Namespace: scenario.Namespace,
		}, cr)
		Expect(err).NotTo(HaveOccurred())

		if cond := meta.FindStatusCondition(cr.Status.Conditions, "Ready"); cond != nil && match(cond) {
			return
		}
		time.Sleep(5 * time.Second)
	}
	Fail(fmt.Sprintf("timed out after %s waiting for the Ready condition of %s/%s",
		timeout, scenario.Namespace, scenario.RegistryName))
}

// waitForDeploymentEnv waits until the Schema Registry deployment pod template
// carries the given environment variable value on the confluent-sr container.
func waitForDeploymentEnv(ctx context.Context, timeout time.Duration, envName, envValue string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dep := &appsv1.Deployment{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName + "-deploy",
			Namespace: scenario.Namespace,
		}, dep)
		Expect(err).NotTo(HaveOccurred())

		for _, container := range dep.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == envName && env.Value == envValue {
					return
				}
			}
		}
		time.Sleep(5 * time.Second)
	}
	Fail(fmt.Sprintf("timed out after %s waiting for env %s=%s on deployment %s/%s",
		timeout, envName, envValue, scenario.Namespace, scenario.RegistryName+"-deploy"))
}

// waitForDeploymentRollout waits until the Schema Registry deployment has
// rolled out the latest generation (all replicas updated and available).
func waitForDeploymentRollout(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dep := &appsv1.Deployment{}
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName + "-deploy",
			Namespace: scenario.Namespace,
		}, dep)
		Expect(err).NotTo(HaveOccurred())

		desired := int32(1)
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		if dep.Status.ObservedGeneration >= dep.Generation &&
			dep.Status.UpdatedReplicas == desired &&
			dep.Status.AvailableReplicas == desired {
			return
		}
		time.Sleep(5 * time.Second)
	}
	Fail(fmt.Sprintf("timed out after %s waiting for the rollout of deployment %s/%s",
		timeout, scenario.Namespace, scenario.RegistryName+"-deploy"))
}

// verifyRoundTrip exercises the Schema Registry REST API: list subjects,
// register an Avro schema, fetch it back by ID, verify the subject is listed
// and check the global compatibility level. Registering a schema persists it
// to the registry-schemas Kafka topic, so a successful round-trip proves the
// Kafka connection works with the scenario's security protocol.
func verifyRoundTrip(ca *clusterCA, clientCert *tls.Certificate) {
	withRegistryClient(ca, clientCert, func(client *registryClient) {
		By("listing subjects")
		_, err := client.listSubjects()
		Expect(err).NotTo(HaveOccurred())

		By("registering an Avro schema")
		schemaID, err := client.registerSchema(testSubject, testSchema)
		Expect(err).NotTo(HaveOccurred())
		Expect(schemaID).To(BeNumerically(">", 0))

		By("fetching the schema back by ID")
		fetched, err := client.getSchemaByID(schemaID)
		Expect(err).NotTo(HaveOccurred())

		var stored, expected any
		Expect(json.Unmarshal([]byte(fetched), &stored)).To(Succeed())
		Expect(json.Unmarshal([]byte(testSchema), &expected)).To(Succeed())
		Expect(stored).To(Equal(expected), "fetched schema %q does not match the registered schema", fetched)

		By("verifying the subject is listed")
		subjects, err := client.listSubjects()
		Expect(err).NotTo(HaveOccurred())
		Expect(subjects).To(ContainElement(testSubject))

		By("verifying the global compatibility level")
		level, err := client.getGlobalConfig()
		Expect(err).NotTo(HaveOccurred())
		Expect(level).To(Equal("FORWARD"))
	})
}

// verifyClientAuthEnforced asserts that a REST request without a client
// certificate fails while clientauth is enabled on the Schema Registry.
func verifyClientAuthEnforced(ca *clusterCA) {
	withRegistryClient(ca, nil, func(client *registryClient) {
		_, err := client.listSubjects()
		Expect(err).To(HaveOccurred(),
			"request without a client certificate should fail while clientauth is enabled")
	})
}
