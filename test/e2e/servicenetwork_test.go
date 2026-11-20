//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The service-networking scenario (E2-5/E2-6) covers the documented
// spec.networkpolicy and spec.service features end to end:
//
//   - E2-5: with spec.networkpolicy.enabled the operator-created NetworkPolicy
//     restricts ingress to the CR's namespace — a curl pod in the CR namespace
//     reaches the Schema Registry REST API while a pod in a foreign namespace
//     is blocked.
//   - E2-6: spec.service.type=NodePort with a pinned node port is exposed by
//     the Service; un-pinning the port preserves the API-server-allocated
//     node port instead of dropping it.
//
// The scenario deploys with securehttp disabled (plain HTTP on port 80), so
// the probes need no TLS material.
var _ = Describe("StrimziSchemaRegistry e2e service networking scenario", Ordered, func() {
	// pinnedNodePort is the node port the E2-6 phase pins. It must be in the
	// API server's default node-port range (30000-32767) and unlikely to
	// collide on a fresh kind cluster.
	const pinnedNodePort = int32(31234)

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
		Expect(applyRegistryCR(ctx, false, false, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.NetworkPolicy = &v1alpha1.NetworkPolicy{Enabled: true}
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

	// E2-6: the pinned node port is exposed and survives un-pinning.
	It("exposes a pinned NodePort and preserves the allocation when un-pinned", func() {
		ctx := context.Background()

		By("applying the CR with a NodePort Service and a pinned node port")
		Expect(applyRegistryCR(ctx, false, false, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.Service = &v1alpha1.Service{
				Type:     "NodePort",
				NodePort: ptr.To(pinnedNodePort),
			}
		})).To(Succeed())
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("verifying the Service exposes the pinned node port")
		// The operator reconciles the Service asynchronously after the CR
		// apply, so the type/nodePort transition is polled.
		Eventually(func(g Gomega) corev1.ServicePort {
			svc := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      scenario.RegistryName,
				Namespace: scenario.Namespace,
			}, svc)).To(Succeed())
			g.Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort),
				"the Service type must transition to NodePort")
			g.Expect(svc.Spec.Ports).To(HaveLen(1))
			return svc.Spec.Ports[0]
		}, statusTimeout, 2*time.Second).Should(
			HaveField("NodePort", Equal(pinnedNodePort)),
			"the Service must expose the pinned node port")

		By("un-pinning the node port")
		Expect(applyRegistryCR(ctx, false, false, nil, func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
			spec.Service = &v1alpha1.Service{Type: "NodePort"}
		})).To(Succeed())

		By("verifying the allocated node port is preserved")
		Eventually(func(g Gomega) int32 {
			updated := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      scenario.RegistryName,
				Namespace: scenario.Namespace,
			}, updated)).To(Succeed())
			g.Expect(updated.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
			g.Expect(updated.Spec.Ports).To(HaveLen(1))
			return updated.Spec.Ports[0].NodePort
		}, statusTimeout, 2*time.Second).Should(Equal(pinnedNodePort),
			"the API-server-allocated node port must be preserved when the pin is removed")
	})
})

// registryServiceURL returns the in-cluster URL of the operator-created Schema
// Registry Service (plain HTTP, Service port 80).
func registryServiceURL() string {
	return fmt.Sprintf("http://%s.%s.svc:80", scenario.RegistryName, scenario.Namespace)
}
