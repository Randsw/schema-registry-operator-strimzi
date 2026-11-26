//go:build e2e

package e2e

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
)

// The service-networking scenario (E2-5) covers the documented
// spec.networkpolicy feature end to end: with spec.networkpolicy.enabled the
// operator-created NetworkPolicy restricts ingress to the CR's namespace — a
// curl pod in the CR namespace reaches the Schema Registry REST API while a pod
// in a foreign namespace is blocked. Real CNI enforcement cannot be unit-tested,
// so this scenario is the only place it is exercised.
//
// The scenario deploys with securehttp disabled (plain HTTP on port 80), so
// the probes need no TLS material.
var _ = Describe("StrimziSchemaRegistry e2e service networking scenario", Ordered, Serial, func() {
	// foreignNamespace is the namespace the E2-5 blocked-curl pod runs in.
	const foreignNamespace = "e2e-networkpolicy-foreign"

	// The probe pods are long-lived and reused for every assertion: one in
	// the CR namespace (allowed by the NetworkPolicy) and one in a foreign
	// namespace (blocked). Creating them once removes the per-probe pod
	// scheduling and image-pull latency.
	var (
		allowedProbe *probePod
		blockedProbe *probePod
	)

	BeforeAll(func() {
		ctx := context.Background()
		ensureNamespace(ctx, foreignNamespace)

		allowedProbe = newProbePod(scenario.Namespace, "networkpolicy-allowed")
		blockedProbe = newProbePod(foreignNamespace, "networkpolicy-blocked")
		allowedProbe.start(ctx)
		blockedProbe.start(ctx)
	})

	AfterAll(func() {
		ctx := context.Background()

		By("deleting the NetworkPolicy probe pods")
		allowedProbe.stop(ctx)
		blockedProbe.stop(ctx)

		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(ctx, readyTimeout)

		By("deleting the foreign namespace")
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: foreignNamespace}}
		_ = k8sClient.Delete(ctx, ns)
	})

	// E2-5: the NetworkPolicy is created and enforced against real traffic.
	It("enforces the NetworkPolicy against same-namespace and foreign traffic", func() {
		ctx := context.Background()

		By("applying the CR with the NetworkPolicy enabled")
		Expect(applyRegistryCR(ctx, false, false, nil, func(spec *v1beta1.StrimziSchemaRegistrySpec) {
			spec.NetworkPolicy = &v1beta1.NetworkPolicy{Enabled: true}
		})).To(Succeed())
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("verifying the NetworkPolicy exists with the expected shape")
		np := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName + "-network-policy",
			Namespace: scenario.Namespace,
		}, np)).To(Succeed())
		Expect(np.Spec.PodSelector.MatchLabels).To(HaveKeyWithValue("app", scenario.RegistryName))
		Expect(np.Spec.PolicyTypes).To(ContainElements(
			networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
		// Ingress is restricted to the CR's namespace (a non-empty From with
		// an empty pod selector matches all pods of the same namespace).
		Expect(np.Spec.Ingress).To(HaveLen(1))
		Expect(np.Spec.Ingress[0].From).NotTo(BeEmpty())
		Expect(np.Spec.Ingress[0].Ports).To(ContainElement(
			networkingv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP),
				Port: &intstr.IntOrString{Type: intstr.Int, IntVal: 8081}}))

		By("probing the REST API from a pod in the CR namespace (allowed)")
		out, err := allowedProbe.curl(ctx, registryServiceURL())
		Expect(err).NotTo(HaveOccurred(), "the allowed probe failed with output %q", out)
		// GET / on Schema Registry answers with an empty JSON object; any
		// non-empty body proves the request traversed the NetworkPolicy and
		// the REST API answered.
		Expect(out).To(ContainSubstring("{}"),
			"expected the Schema Registry REST API to answer through the NetworkPolicy")

		By("probing the REST API from a pod in a foreign namespace (blocked)")
		out, err = blockedProbe.curl(ctx, registryServiceURL())
		Expect(err).To(HaveOccurred(),
			"expected the foreign-namespace probe to be blocked, got output %q", out)
		Expect(out).NotTo(ContainSubstring("{}"),
			"expected the foreign-namespace probe to be blocked by the NetworkPolicy")
	})
})

// registryServiceURL returns the in-cluster URL of the operator-created Schema
// Registry Service (plain HTTP, Service port 80).
func registryServiceURL() string {
	return fmt.Sprintf("http://%s.%s.svc:80", scenario.RegistryName, scenario.Namespace)
}
