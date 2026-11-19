//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	AfterAll(func() {
		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(context.Background(), readyTimeout)

		By("deleting the foreign namespace")
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: foreignNamespace}}
		_ = k8sClient.Delete(context.Background(), ns)
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
		out := curlFromPod(ctx, scenario.Namespace, "networkpolicy-allowed")
		Expect(out).To(ContainSubstring(`"error_code":401`),
			"expected the Schema Registry REST API to answer (401 for an unauthenticated probe is fine)")

		By("probing the REST API from a pod in a foreign namespace (blocked)")
		out = curlFromPod(ctx, foreignNamespace, "networkpolicy-blocked")
		Expect(out).NotTo(ContainSubstring(`"error_code":401`),
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
		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName,
			Namespace: scenario.Namespace,
		}, svc)).To(Succeed())
		Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeNodePort))
		Expect(svc.Spec.Ports).To(HaveLen(1))
		Expect(svc.Spec.Ports[0].NodePort).To(Equal(pinnedNodePort))

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
		}, readyTimeout, 2*time.Second).Should(Equal(pinnedNodePort),
			"the API-server-allocated node port must be preserved when the pin is removed")
	})
})

// curlFromPod runs a short-lived busybox pod in the given namespace that
// curls the Schema Registry REST API through the operator-created Service
// and returns the combined stdout/stderr. The pod image is the same
// confluent image the scenario already pulls (curl is bundled in it), so no
// extra image is needed.
func curlFromPod(ctx context.Context, namespace, name string) string {
	By(fmt.Sprintf("running the probe pod %s/%s", namespace, name))
	if namespace != scenario.Namespace {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, &corev1.Namespace{})
		if apierrors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
	}

	target := fmt.Sprintf("http://%s.%s.svc:80", scenario.RegistryName, scenario.Namespace)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   "curlimages/curl:8.10.1",
				Command: []string{"curl", "-s", "-m", "10", target},
			}},
		},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	defer func() {
		_ = k8sClient.Delete(ctx, pod)
	}()

	var logs string
	Eventually(func(g Gomega) {
		// The pod must reach a terminal phase before its logs are complete.
		fetched := &corev1.Pod{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, fetched)).To(Succeed())
		g.Expect(fetched.Status.Phase).To(Equal(corev1.PodSucceeded),
			"the probe pod has not finished yet (phase %s)", fetched.Status.Phase)

		out, err := podLogs(ctx, namespace, name)
		g.Expect(err).NotTo(HaveOccurred())
		logs = out
	}, 2*time.Minute, 2*time.Second).Should(Succeed(),
		"timed out waiting for the probe pod %s/%s", namespace, name)
	return logs
}

// podLogs fetches the logs of a pod via kubectl (the controller-runtime
// client does not expose pod log subresources).
func podLogs(ctx context.Context, namespace, name string) (string, error) {
	cmd := exec.Command("kubectl", "logs", "-n", namespace, name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl logs %s/%s failed: %v: %s", namespace, name, err, out)
	}
	return string(out), nil
}
