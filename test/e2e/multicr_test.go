//go:build e2e

package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The multiple-CR scenario (plan §6.2 item 6) proves that two
// StrimziSchemaRegistry instances sharing one Strimzi Kafka cluster are fully
// isolated: distinct kafkastore topics and group IDs, independent Deployments,
// Services, status and ownership. Topic isolation is the decisive assertion —
// each registry persists schemas to its own topic, so a subject written through
// one registry must never be visible through the other.
var _ = Describe("StrimziSchemaRegistry e2e multiple CRs scenario", Ordered, Serial, func() {
	var (
		registryA *registryFixture
		registryB *registryFixture
		ca        *clusterCA
	)

	BeforeAll(func() {
		registryA = newRegistryFixture("multi-a")
		registryB = newRegistryFixture("multi-b")
		var err error
		ca, err = fetchClusterCA(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		ctx := context.Background()
		By("deleting both isolated StrimziSchemaRegistry CRs to leave a clean slate")
		registryA.delete(ctx)
		registryB.delete(ctx)
		By("deleting both isolated KafkaUsers")
		registryA.deleteKafkaUser(ctx)
		registryB.deleteKafkaUser(ctx)
	})

	It("runs two isolated registries against one Kafka cluster", func() {
		ctx := context.Background()

		subjectA := "multicr-a-value"
		subjectB := "multicr-b-value"

		By("creating the KafkaUser each isolated CR needs")
		registryA.ensureKafkaUser(ctx)
		registryB.ensureKafkaUser(ctx)

		By("applying both CRs against the same Kafka cluster")
		Expect(registryA.apply(ctx, false, true, nil)).To(Succeed())
		Expect(registryB.apply(ctx, false, true, nil)).To(Succeed())

		By("waiting for both registries to become ready")
		registryA.waitReady(ctx)
		registryB.waitReady(ctx)
		registryA.waitRollout(ctx)
		registryB.waitRollout(ctx)

		By("registering a subject through each registry")
		registerSubjectForRegistry(ctx, registryA.Name, ca, subjectA)
		registerSubjectForRegistry(ctx, registryB.Name, ca, subjectB)

		By("proving topic isolation: neither registry sees the other's subject")
		subjectsA := mustListSubjectsForRegistry(ctx, registryA.Name, ca)
		subjectsB := mustListSubjectsForRegistry(ctx, registryB.Name, ca)
		Expect(subjectsA).To(ContainElement(subjectA))
		Expect(subjectsA).NotTo(ContainElement(subjectB),
			"registry A must not see a subject persisted to registry B's topic")
		Expect(subjectsB).To(ContainElement(subjectB))
		Expect(subjectsB).NotTo(ContainElement(subjectA),
			"registry B must not see a subject persisted to registry A's topic")

		By("snapshotting registry B's Deployment before mutating registry A")
		beforeB := deploymentFor(ctx, registryB.Name)
		beforeBResourceVersion := beforeB.ResourceVersion

		By("mutating only registry A's configuration")
		Expect(registryA.apply(ctx, false, true, nil,
			func(spec *strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec) {
				spec.Configuration = map[string]string{"kafkastore.timeout.ms": "2000"}
			})).To(Succeed())
		waitForDeploymentEnvFor(ctx, registryA.Name, readyTimeout,
			"SCHEMA_REGISTRY_KAFKASTORE_TIMEOUT_MS", "2000")
		waitForDeploymentRolloutFor(ctx, registryA.Name, readyTimeout)
		registryA.waitReady(ctx)

		By("asserting registry B was not touched by registry A's change")
		Consistently(func(g Gomega) {
			current := deploymentFor(ctx, registryB.Name)
			g.Expect(current.ResourceVersion).To(Equal(beforeBResourceVersion),
				"registry B's Deployment was rewritten while only registry A changed")
			g.Expect(current.Spec.Template).To(Equal(beforeB.Spec.Template),
				"registry B's pod template drifted while only registry A changed")
		}, 10*time.Second, 2*time.Second).Should(Succeed())

		By("deleting registry A and asserting its owned resources are garbage-collected")
		registryA.delete(ctx)
		waitForServiceDeletion(ctx, registryA.Name)
		waitForSecretDeletion(ctx, registryA.Name+"-tls")

		By("asserting registry B still exists and keeps serving after registry A is gone")
		deploymentFor(ctx, registryB.Name)
		Eventually(func(g Gomega) {
			subjects, err := listSubjectsForRegistry(ctx, registryB.Name, ca)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(subjects).To(ContainElement(subjectB))
			g.Expect(subjects).NotTo(ContainElement(subjectA))
		}, readyTimeout, 2*time.Second).Should(Succeed(),
			"timed out after %s waiting for registry B to keep serving its own subject", readyTimeout)
		registryB.waitReady(ctx)
	})
})

// listSubjectsForRegistry port-forwards the named registry's Service and
// returns its subject list. Unlike withRegistryClientFor it surfaces every
// failure as an error, so callers can poll it inside Eventually.
func listSubjectsForRegistry(ctx context.Context, name string, ca *clusterCA) ([]string, error) {
	port, stop, err := startPortForward(ctx, name, registryServicePort)
	if err != nil {
		return nil, err
	}
	defer stop()

	return newRegistryClientFor(name, ca.certPool(), nil, port).listSubjects()
}

// mustListSubjectsForRegistry is listSubjectsForRegistry for call sites that
// assert the result directly instead of polling.
func mustListSubjectsForRegistry(ctx context.Context, name string, ca *clusterCA) []string {
	subjects, err := listSubjectsForRegistry(ctx, name, ca)
	Expect(err).NotTo(HaveOccurred(), "failed to list the subjects of registry %s", name)
	return subjects
}

// registerSubjectForRegistry registers the test schema under the given subject
// through the named registry. The first write can race the registry's internal
// Kafka leader election after pod readiness, so it is retried on the same warm-up
// window the shared round-trip uses.
func registerSubjectForRegistry(ctx context.Context, name string, ca *clusterCA, subject string) {
	port, stop, err := startPortForward(ctx, name, registryServicePort)
	Expect(err).NotTo(HaveOccurred())
	defer stop()

	client := newRegistryClientFor(name, ca.certPool(), nil, port)
	Eventually(func() error {
		_, err := client.registerSchema(subject, testSchema)
		return err
	}, 60*time.Second, 2*time.Second).Should(Succeed(),
		"failed to register subject %q through registry %s", subject, name)
}

// waitForServiceDeletion waits until the named Service is gone (owner-reference
// garbage collection after the owning CR is deleted).
func waitForServiceDeletion(ctx context.Context, name string) {
	Eventually(func(g Gomega) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: scenario.Namespace,
		}, &corev1.Service{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"the Service %s is still present (err: %v)", name, err)
	}, statusTimeout, 2*time.Second).Should(Succeed(),
		"timed out after %s waiting for the deletion of the Service %s/%s",
		statusTimeout, scenario.Namespace, name)
}
