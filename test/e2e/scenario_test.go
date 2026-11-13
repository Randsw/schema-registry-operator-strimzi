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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

	// deploymentSuffix is appended to the CR name to form the name of the
	// operator-created Schema Registry Deployment. It mirrors deploySuffix in
	// internal/controller/k8s.go.
	deploymentSuffix = "-deploy"
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

	// Ginkgo randomizes spec order and every spec shares the same CR name and
	// namespace, so each container must leave no CR behind: a leftover CR (for
	// example with spec.replicas=2 from the multi-replica scenario) is still
	// being reconciled by the operator while the next spec updates it, which
	// both poisons the next spec's expectations and makes its CR updates race
	// the reconcile (409 Conflict).
	AfterAll(func() {
		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(context.Background(), readyTimeout)
	})

	It("phase 1: deploys with clientauth disabled and passes the REST round-trip", func() {
		ctx := context.Background()

		applyRegistryCR(ctx, false, true, nil)
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		verifyRoundTrip(ca, nil)
	})

	It("rejects clientauth without securehttp", func() {
		ctx := context.Background()

		applyRegistryCR(ctx, true, false, nil)
		waitForCondition(ctx, readyTimeout, func(cond *metav1.Condition) bool {
			return cond.Status == metav1.ConditionFalse && cond.Reason == "ClientAuthRequiresSecureHTTP"
		})
	})

	It("phase 2: redeploys with clientauth enabled and enforces mutual TLS on the REST API", func() {
		ctx := context.Background()

		applyRegistryCR(ctx, true, true, nil)

		By("waiting for the deployment to pick up the clientauth configuration")
		waitForDeploymentEnv(ctx, readyTimeout, "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION", "REQUIRED")
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
// scenario with the given clientauth/securehttp values. A nil replicas leaves
// spec.replicas unset (the operator default of 1 applies).
func applyRegistryCR(ctx context.Context, clientAuth, secureHTTP bool, replicas *int32) {
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
						Image:           "confluentinc/cp-schema-registry:8.4.1",
						ImagePullPolicy: corev1.PullIfNotPresent,
					}},
				},
			},
		},
	}

	if replicas != nil {
		cr.Spec.Replicas = replicas
	}
	desiredSpec := cr.Spec

	// The operator mutates the CR while reconciling it (it adds its finalizer
	// and writes the status subresource), so a plain Get followed by Update
	// races the reconcile and fails with a 409 Conflict. Retry on conflict,
	// re-reading the object inside the closure so every attempt carries the
	// latest resourceVersion.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, existing)
		if apierrors.IsNotFound(err) {
			err = k8sClient.Create(ctx, cr)
			if apierrors.IsAlreadyExists(err) {
				// Lost a create race; surface it as a conflict so the retry
				// re-reads the object and takes the update path.
				return apierrors.NewConflict(
					schema.GroupResource{
						Group:    strimziregistryoperatorv1alpha1.GroupVersion.Group,
						Resource: "strimzischemaregistries",
					},
					cr.Name, err)
			}
			return err
		}
		if err != nil {
			return err
		}
		existing.Spec = desiredSpec
		return k8sClient.Update(ctx, existing)
	})
	Expect(err).NotTo(HaveOccurred(),
		"failed to apply the StrimziSchemaRegistry CR %s/%s", scenario.Namespace, scenario.RegistryName)
}

// waitForReadyCondition waits until the CR reports the Ready=True condition.
func waitForReadyCondition(ctx context.Context, timeout time.Duration) {
	waitForCondition(ctx, timeout, func(cond *metav1.Condition) bool {
		return cond.Status == metav1.ConditionTrue && cond.Reason == "Ready"
	})
}

// waitForCondition polls the CR's Ready condition until match returns true.
// The Get runs inside the Eventually closure, so a CR that does not exist yet
// (or a transient API error) is retried instead of failing the spec on the
// first poll.
func waitForCondition(ctx context.Context, timeout time.Duration, match func(*metav1.Condition) bool) {
	Eventually(func(g Gomega) {
		cr := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName,
			Namespace: scenario.Namespace,
		}, cr)).To(Succeed())

		cond := meta.FindStatusCondition(cr.Status.Conditions, "Ready")
		g.Expect(cond).NotTo(BeNil(), "the CR has no Ready condition yet")
		g.Expect(match(cond)).To(BeTrue(),
			"Ready condition is %s/%s (message: %s)", cond.Status, cond.Reason, cond.Message)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the Ready condition of %s/%s",
		timeout, scenario.Namespace, scenario.RegistryName)
}

// waitForDeploymentEnv waits until the Schema Registry deployment pod template
// carries the given environment variable value on the confluent-sr container.
// The Get runs inside the Eventually closure, so a deployment that does not
// exist yet (the operator has not reconciled the CR yet) is retried instead of
// failing the spec on the first poll.
func waitForDeploymentEnv(ctx context.Context, timeout time.Duration, envName, envValue string) {
	Eventually(func(g Gomega) {
		dep := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, registryDeploymentKey(), dep)).To(Succeed())

		found := false
		for _, container := range dep.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == envName && env.Value == envValue {
					found = true
				}
			}
		}
		g.Expect(found).To(BeTrue(),
			"env %s=%s is not set on the confluent-sr container", envName, envValue)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for env %s=%s on deployment %s/%s",
		timeout, envName, envValue, scenario.Namespace, scenario.RegistryName+deploymentSuffix)
}

// waitForDeploymentRollout waits until the Schema Registry deployment has
// rolled out the latest generation (all replicas updated and available).
func waitForDeploymentRollout(ctx context.Context, timeout time.Duration) {
	Eventually(func(g Gomega) {
		dep := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, registryDeploymentKey(), dep)).To(Succeed())

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
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the rollout of deployment %s/%s",
		timeout, scenario.Namespace, scenario.RegistryName+deploymentSuffix)
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

// The multi-replica scenario (B20) deploys the Schema Registry with
// spec.replicas=2 and verifies that the operator propagates the replica
// count to the Deployment and both replicas become ready behind the Service.
//
// securehttp is deliberately false: with securehttp=true the inter-instance
// protocol is https while each replica advertises its pod IP as host.name,
// and the operator-managed REST TLS certificate SANs are DNS-only (the
// Service DNS name), so inter-instance HTTPS forwarding between replicas
// fails TLS verification (see the README HA section). Plain HTTP has no such
// constraint, so it is the supported multi-replica configuration; the REST
// round-trip itself is covered by the main scenario above (its port-forward
// helper targets the TLS port 443).
//
// master.eligibility keeps its per-CR semantics: every replica of this CR is
// master-eligible and Schema Registry performs the election among them;
// host.name is resolved per pod through the downward API (status.podIP), so
// each replica advertises a unique address.
var _ = Describe("StrimziSchemaRegistry e2e multi-replica scenario", Ordered, func() {
	// Same CR name and namespace as the main scenario above; clean up so the
	// specs stay order-independent under Ginkgo's randomized ordering.
	AfterAll(func() {
		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(context.Background(), readyTimeout)
	})

	It("deploys and scales to multiple replicas behind the Service", func() {
		ctx := context.Background()

		By("applying the CR with spec.replicas=2 and securehttp disabled")
		applyRegistryCR(ctx, false, false, ptr.To(int32(2)))

		By("waiting for the deployment to carry the per-pod host.name downward API env")
		waitForDeploymentEnvFieldRef(ctx, readyTimeout, "SCHEMA_REGISTRY_HOST_NAME", "status.podIP")

		By("waiting for 2 ready replicas on the Deployment")
		waitForDeploymentReplicas(ctx, readyTimeout, 2)

		By("waiting for the CR Ready condition and the rollout")
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)
	})
})

// waitForDeploymentEnvFieldRef waits until the Schema Registry deployment pod
// template carries the given environment variable resolved from the given
// downward API field path on the confluent-sr container. The Get runs inside
// the Eventually closure, so a deployment that does not exist yet (the
// operator has not reconciled the CR yet) is retried instead of failing the
// spec on the first poll.
func waitForDeploymentEnvFieldRef(ctx context.Context, timeout time.Duration, envName, fieldPath string) {
	Eventually(func(g Gomega) {
		dep := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, registryDeploymentKey(), dep)).To(Succeed())

		found := false
		for _, container := range dep.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == envName && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil &&
					env.ValueFrom.FieldRef.FieldPath == fieldPath {
					found = true
				}
			}
		}
		g.Expect(found).To(BeTrue(),
			"env %s (fieldRef %s) is not set on the confluent-sr container", envName, fieldPath)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for env %s (fieldRef %s) on deployment %s/%s",
		timeout, envName, fieldPath, scenario.Namespace, scenario.RegistryName+deploymentSuffix)
}

// waitForDeploymentReplicas waits until the Schema Registry deployment
// desires and has ready the given number of replicas.
func waitForDeploymentReplicas(ctx context.Context, timeout time.Duration, want int32) {
	Eventually(func(g Gomega) {
		dep := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, registryDeploymentKey(), dep)).To(Succeed())

		g.Expect(dep.Spec.Replicas).NotTo(BeNil(), "spec.replicas is not set yet")
		g.Expect(*dep.Spec.Replicas).To(Equal(want), "spec.replicas is %d", *dep.Spec.Replicas)
		g.Expect(dep.Status.ReadyReplicas).To(Equal(want),
			"%d of %d replicas are ready", dep.Status.ReadyReplicas, want)
		g.Expect(dep.Status.ObservedGeneration).To(BeNumerically(">=", dep.Generation),
			"generation %d has not been observed yet", dep.Generation)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for %d ready replicas on deployment %s/%s",
		timeout, want, scenario.Namespace, scenario.RegistryName+deploymentSuffix)
}

// registryDeploymentKey returns the NamespacedName of the operator-created
// Schema Registry Deployment (CR name + deploymentSuffix).
func registryDeploymentKey() types.NamespacedName {
	return types.NamespacedName{
		Name:      scenario.RegistryName + deploymentSuffix,
		Namespace: scenario.Namespace,
	}
}

// deleteRegistryCR deletes the scenario CR and waits until the CR and its
// operator-managed Deployment are gone, so a spec that fails mid-way cannot
// leave a CR behind to poison the next spec (which shares the same CR name
// and namespace).
func deleteRegistryCR(ctx context.Context, timeout time.Duration) {
	cr := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: scenario.RegistryName, Namespace: scenario.Namespace},
	}
	err := k8sClient.Delete(ctx, cr)
	Expect(client.IgnoreNotFound(err)).To(Succeed(),
		"failed to delete the StrimziSchemaRegistry CR %s/%s", scenario.Namespace, scenario.RegistryName)

	Eventually(func(g Gomega) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName,
			Namespace: scenario.Namespace,
		}, &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the CR is still present (err: %v)", err)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the deletion of the CR %s/%s",
		timeout, scenario.Namespace, scenario.RegistryName)

	Eventually(func(g Gomega) {
		err := k8sClient.Get(ctx, registryDeploymentKey(), &appsv1.Deployment{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the Deployment is still present (err: %v)", err)
	}, timeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the deletion of the Deployment %s/%s",
		timeout, scenario.Namespace, scenario.RegistryName+deploymentSuffix)
}
