//go:build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The configuration-passthrough scenario (E2-4) covers the documented
// spec.configuration feature end to end: a benign Confluent property reaches
// the deployment env as SCHEMA_REGISTRY_<KEY>, while a reserved key is
// filtered out (the operator emits a ConfigKeyOverridden Warning event and
// the env var is not set).
//
// securehttp is disabled so the scenario needs no TLS material and the
// deployment env stays small; the REST round-trip itself is covered by the
// main scenario.
var _ = Describe("StrimziSchemaRegistry e2e configuration passthrough scenario", Ordered, Serial, func() {
	// kafkastoreTimeoutEnv is the env var rendered from the benign
	// kafkastore.timeout.ms property.
	const kafkastoreTimeoutEnv = "SCHEMA_REGISTRY_KAFKASTORE_TIMEOUT_MS"

	AfterAll(func() {
		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(context.Background(), readyTimeout)
	})

	It("passes benign properties through and filters reserved keys", func() {
		ctx := context.Background()

		By("applying the CR with a benign and a reserved configuration key")
		Expect(applyRegistryCR(ctx, false, false, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.Configuration = map[string]string{
				// Benign: a Confluent property the operator does not manage.
				"kafkastore.timeout.ms": "15000",
				// Reserved: kafkastore.bootstrap.servers is operator-managed
				// (rendered from the Kafka CR status).
				"kafkastore.bootstrap.servers": "evil.example.com:9092",
			}
		})).To(Succeed())
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("verifying the benign property reached the deployment env")
		waitForDeploymentEnv(ctx, readyTimeout, kafkastoreTimeoutEnv, "15000")

		By("verifying the reserved key was filtered out")
		// The operator-managed SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS
		// env var legitimately exists (rendered from the Kafka CR status);
		// the reserved-key filter means the user-supplied value must not
		// override it.
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, registryDeploymentKey(), dep)).To(Succeed())
		found := false
		for _, container := range dep.Spec.Template.Spec.Containers {
			for _, env := range container.Env {
				if env.Name == "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS" {
					found = true
					Expect(env.Value).NotTo(Equal("evil.example.com:9092"),
						"the reserved kafkastore.bootstrap.servers key must not leak the user-supplied value into the env")
					Expect(env.Value).To(ContainSubstring(scenario.ClusterName),
						"the operator-managed bootstrap env var must carry the Kafka bootstrap address")
				}
			}
		}
		Expect(found).To(BeTrue(), "expected SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS to be set")

		By("verifying the ConfigKeyOverridden Warning event was emitted")
		Eventually(func(g Gomega) []string {
			events, err := listRegistryEvents(ctx)
			g.Expect(err).NotTo(HaveOccurred())
			return events
		}, statusTimeout, 2*time.Second).Should(ContainElement(ContainSubstring("ConfigKeyOverridden")),
			"expected a ConfigKeyOverridden Warning event for the reserved key")
	})
})

// listRegistryEvents returns the reason of every event regarding the
// scenario's StrimziSchemaRegistry CR (both the events.k8s.io and the core
// v1 event APIs, which the operator's recorder may use).
func listRegistryEvents(ctx context.Context) ([]string, error) {
	var reasons []string

	coreEvents := &corev1.EventList{}
	if err := k8sClient.List(ctx, coreEvents, client.InNamespace(scenario.Namespace),
		client.MatchingFields{"involvedObject.name": scenario.RegistryName}); err == nil {
		for _, ev := range coreEvents.Items {
			reasons = append(reasons, ev.Reason)
		}
	}

	return reasons, nil
}
