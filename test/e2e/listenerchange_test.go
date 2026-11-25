//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
)

// listenerKafkaGVK is the Strimzi Kafka resource identity. The suite talks to
// the shared Kafka CR as an unstructured object because its scheme does not
// register the Strimzi API types.
var listenerKafkaGVK = schema.GroupVersionKind{
	Group:   "kafka.strimzi.io",
	Version: "v1",
	Kind:    "Kafka",
}

// The listener-change scenario (T-8, C-1) proves on a real cluster that a
// Kafka listener change — not just a CA-secret bump as in carotation_test.go —
// reaches the Schema Registry without touching its CR: Strimzi notices the new
// listener port, updates Kafka.status.listeners, the operator's Kafka watch
// enqueues reconciliation and the managed Deployment rolls with the new
// bootstrap servers. The REST round-trip afterwards proves the new endpoint is
// actually usable, so the assertion is not merely a config diff.
//
// The spec is enabled for the plaintext scenario only. TLS/SASL listeners are
// re-provisioned together with their client-authentication material and are
// covered by the CA and credential rotation specs; changing their port in every
// matrix leg would add broker restarts without covering a different operator
// code path.
var _ = Describe("StrimziSchemaRegistry e2e Kafka listener change scenario (T-8)", Ordered, Serial, func() {
	var (
		fixture *registryFixture
		ca      *clusterCA

		listenerName    string
		originalPort    int64
		restoreRequired bool
	)

	BeforeAll(func() {
		fixture = newRegistryFixture("listener-change")
		var err error
		ca, err = fetchClusterCA(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()

		if restoreRequired {
			By(fmt.Sprintf("restoring the original Kafka listener %q port %d", listenerName, originalPort))
			patchKafkaListenerPort(ctx, listenerName, originalPort)
			waitForStatusBootstrapPort(ctx, listenerName, originalPort)
		}

		By("deleting the isolated StrimziSchemaRegistry CR to leave a clean slate")
		fixture.delete(ctx)
		By("deleting the isolated KafkaUser")
		fixture.deleteKafkaUser(ctx)
	})

	It("converges to a changed Kafka listener port without touching the SR CR", func() {
		if scenario.SecurityProtocol != "PLAINTEXT" {
			Skip("T-8 listener-port change is enabled for the plaintext scenario; TLS/SASL listeners are re-provisioned with their client-auth material and are covered by the CA/credential rotation specs")
		}

		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs (none for PLAINTEXT)")
		fixture.ensureKafkaUser(ctx)

		By("applying the CR and establishing the baseline endpoint")
		Expect(fixture.apply(ctx, false, true, nil)).To(Succeed())
		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)
		fixture.verifyRoundTrip(ca, nil)

		name, port, ok := kafkaSpecListener(ctx)
		Expect(ok).To(BeTrue(), "the Kafka CR has no listener matching the active E2E_LISTENER %q", scenario.Listener)
		listenerName, originalPort = name, port
		restoreRequired = true

		newPort := port + 100
		if newPort > 65535 {
			Skip(fmt.Sprintf("cannot derive a valid new port from %d", port))
		}

		By(fmt.Sprintf("changing Kafka listener %q port %d -> %d", name, port, newPort))
		patchKafkaListenerPort(ctx, name, newPort)

		By("waiting for Strimzi to publish the new listener port in Kafka.status.listeners")
		newBootstrap := waitForStatusBootstrapPort(ctx, name, newPort)

		By("waiting for the SR Deployment to pick up the new bootstrap servers")
		waitForDeploymentEnvFor(ctx, fixture.Name, readyTimeout,
			"SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS", newBootstrap)

		By("waiting for the rolled Deployment to become available again")
		waitForDeploymentRolloutFor(ctx, fixture.Name, readyTimeout)
		fixture.waitReady(ctx)

		By("asserting the REST round-trip still works against the new listener")
		fixture.verifyRoundTrip(ca, nil)
	})
})

// kafkaSpecListener returns the name and port of the Kafka CR listener matching
// the active scenario listener (spec.listener is matched case-insensitively by
// the operator). It reports false when no listener matches.
func kafkaSpecListener(ctx context.Context) (string, int64, bool) {
	kafkaCR := &unstructured.Unstructured{}
	kafkaCR.SetGroupVersionKind(listenerKafkaGVK)
	Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name:      scenario.ClusterName,
		Namespace: scenario.Namespace,
	}, kafkaCR)).To(Succeed())

	listeners, _, err := unstructured.NestedSlice(kafkaCR.Object, "spec", "kafka", "listeners")
	Expect(err).NotTo(HaveOccurred())
	for _, raw := range listeners {
		listener, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := listener["name"].(string)
		if !strings.EqualFold(name, scenario.Listener) {
			continue
		}
		port, _ := listener["port"].(int64)
		return name, port, true
	}
	return "", 0, false
}

// patchKafkaListenerPort sets spec.kafka.listeners[<listener>].port on the
// shared Kafka CR, retrying on conflict so it never races Strimzi's own
// status/ spec writes.
func patchKafkaListenerPort(ctx context.Context, listenerName string, port int64) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		kafkaCR := &unstructured.Unstructured{}
		kafkaCR.SetGroupVersionKind(listenerKafkaGVK)
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.ClusterName,
			Namespace: scenario.Namespace,
		}, kafkaCR); err != nil {
			return err
		}

		listeners, _, err := unstructured.NestedSlice(kafkaCR.Object, "spec", "kafka", "listeners")
		if err != nil {
			return err
		}
		matched := false
		for i, raw := range listeners {
			listener, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := listener["name"].(string)
			if !strings.EqualFold(name, listenerName) {
				continue
			}
			listener["port"] = port
			listeners[i] = listener
			matched = true
		}
		if !matched {
			return fmt.Errorf("the Kafka CR %s/%s no longer exposes listener %q",
				scenario.Namespace, scenario.ClusterName, listenerName)
		}
		if err := unstructured.SetNestedSlice(kafkaCR.Object, listeners, "spec", "kafka", "listeners"); err != nil {
			return err
		}
		return k8sClient.Update(ctx, kafkaCR)
	})
	Expect(err).NotTo(HaveOccurred(),
		"failed to set Kafka listener %q port to %d", listenerName, port)
}

// waitForStatusBootstrapPort waits until Kafka.status.listeners reports the
// named listener with a bootstrap address ending in :<port> and returns that
// address. Strimzi only publishes the new address after it has re-provisioned
// the listener, so this is the signal the operator's Kafka watch reacts to.
func waitForStatusBootstrapPort(ctx context.Context, listenerName string, port int64) string {
	expectedSuffix := fmt.Sprintf(":%d", port)
	var bootstrap string // populated inside Eventually on success

	Eventually(func(g Gomega) {
		kafkaCR := &unstructured.Unstructured{}
		kafkaCR.SetGroupVersionKind(listenerKafkaGVK)
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.ClusterName,
			Namespace: scenario.Namespace,
		}, kafkaCR)).To(Succeed())

		listeners, _, err := unstructured.NestedSlice(kafkaCR.Object, "status", "listeners")
		g.Expect(err).NotTo(HaveOccurred())
		found := false
		for _, raw := range listeners {
			listener, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := listener["name"].(string)
			if !strings.EqualFold(name, listenerName) {
				continue
			}
			found = true
			address, _ := listener["bootstrapServers"].(string)
			g.Expect(address).To(HaveSuffix(expectedSuffix),
				"Kafka.status.listeners[%q].bootstrapServers is %q, want a :%d suffix", listenerName, address, port)
			bootstrap = address
		}
		g.Expect(found).To(BeTrue(),
			"Kafka.status.listeners no longer reports listener %q", listenerName)
	}, readyTimeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for Kafka status listener %q on port %d", readyTimeout, listenerName, port)

	return bootstrap
}
