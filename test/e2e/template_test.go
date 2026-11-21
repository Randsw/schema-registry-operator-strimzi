//go:build e2e

package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

const (
	// templateMarkerEnv is the user env var set through spec.template.
	templateMarkerEnv = "E2E_TEMPLATE_MARKER"

	// templateCacheVolume is the user volume set through spec.template.
	templateCacheVolume = "template-cache"
)

// The spec.template passthrough scenario (plan §4.3 item 5) closes the loop on
// the operator's merge behavior: user env vars, volumes, affinity/node
// selectors and resource requests/limits set through spec.template must land on
// the managed Deployment, while operator-managed fields (identity labels,
// rollout annotations, bootstrap env, default resources) must not be clobbered
// or dropped.
//
// spec.template.metadata (pod labels/annotations) is deliberately not asserted:
// controller-gen renders it as a bare `object` in the CRD schema without
// x-kubernetes-preserve-unknown-fields, so the API server prunes user-provided
// metadata before the operator ever sees it. Passing pod labels/annotations
// through would require a production CRD change, which is out of scope for this
// test-only phase.
var _ = Describe("StrimziSchemaRegistry e2e template passthrough scenario", Ordered, Serial, func() {
	// Built in BeforeAll: the Describe body runs before BeforeSuite populates
	// the matrix scenario configuration.
	var fixture *registryFixture

	BeforeAll(func() {
		fixture = newRegistryFixture("template")
	})

	AfterAll(func() {
		ctx := context.Background()
		By("deleting the isolated StrimziSchemaRegistry CR to leave a clean slate")
		fixture.delete(ctx)
		By("deleting the isolated KafkaUser")
		fixture.deleteKafkaUser(ctx)
	})

	It("passes spec.template fields through without clobbering operator-managed fields", func() {
		ctx := context.Background()

		By("creating the KafkaUser the isolated CR needs")
		fixture.ensureKafkaUser(ctx)

		By("applying the CR with custom template env, volumes, affinity, node selector and resources")
		Expect(fixture.apply(ctx, false, false, nil,
			func(spec *v1alpha1.StrimziSchemaRegistrySpec) {
				spec.Template.Spec.NodeSelector = map[string]string{"kubernetes.io/os": "linux"}
				spec.Template.Spec.Affinity = &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{{
								MatchExpressions: []corev1.NodeSelectorRequirement{{
									Key:      "kubernetes.io/os",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"linux"},
								}},
							}},
						},
					},
				}
				spec.Template.Spec.Volumes = append(spec.Template.Spec.Volumes, corev1.Volume{
					Name:         templateCacheVolume,
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				})
				spec.Template.Spec.Containers[0].VolumeMounts = append(
					spec.Template.Spec.Containers[0].VolumeMounts,
					corev1.VolumeMount{Name: templateCacheVolume, MountPath: "/var/template-cache"})
				spec.Template.Spec.Containers[0].Env = append(
					spec.Template.Spec.Containers[0].Env,
					corev1.EnvVar{Name: templateMarkerEnv, Value: "present"})
				spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("300m"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("768Mi"),
					},
				}
			})).To(Succeed())

		fixture.waitReady(ctx)
		fixture.waitRollout(ctx)

		dep := deploymentFor(ctx, fixture.Name)

		By("verifying the operator identity labels and rollout markers are intact")
		Expect(dep.Spec.Template.Labels).To(HaveKeyWithValue("app", fixture.Name))
		Expect(dep.Spec.Template.Annotations).To(HaveKey(jksVersionAnnotationKey),
			"the operator-managed jksVersion rollout marker must be present")

		By("verifying the user node selector and affinity are preserved")
		Expect(dep.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("kubernetes.io/os", "linux"))
		Expect(dep.Spec.Template.Spec.Affinity).NotTo(BeNil())
		Expect(dep.Spec.Template.Spec.Affinity.NodeAffinity).NotTo(BeNil())
		terms := dep.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		Expect(terms).NotTo(BeNil())
		Expect(terms.NodeSelectorTerms).To(HaveLen(1))
		Expect(terms.NodeSelectorTerms[0].MatchExpressions).To(ContainElement(
			corev1.NodeSelectorRequirement{
				Key:      "kubernetes.io/os",
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"linux"},
			}))

		By("verifying the user volume and mount are preserved")
		Expect(dep.Spec.Template.Spec.Volumes).To(ContainElement(And(
			HaveField("Name", templateCacheVolume),
			HaveField("VolumeSource.EmptyDir", Not(BeNil())),
		)))
		Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: templateCacheVolume, MountPath: "/var/template-cache"}))

		By("verifying the user env var reached the container next to the operator-managed env")
		env := dep.Spec.Template.Spec.Containers[0].Env
		Expect(env).To(ContainElement(corev1.EnvVar{Name: templateMarkerEnv, Value: "present"}))
		Expect(envValue(env, "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS")).NotTo(BeEmpty(),
			"the operator-managed bootstrap env var must not be clobbered")

		By("verifying custom resources are preserved and missing defaults are filled")
		resources := dep.Spec.Template.Spec.Containers[0].Resources
		Expect(resources.Requests.Cpu().String()).To(Equal("300m"),
			"the user-supplied CPU request must not be overwritten")
		Expect(resources.Limits.Memory().String()).To(Equal("768Mi"),
			"the user-supplied memory limit must not be overwritten")
		Expect(resources.Requests.Memory().String()).To(Equal("512Mi"),
			"the operator default memory request must fill the missing value")
		Expect(resources.Limits.Cpu().String()).To(Equal("1"),
			"the operator default CPU limit (1000m) must fill the missing value")
	})
})

// envValue returns the value of the named env var (empty when absent).
func envValue(env []corev1.EnvVar, name string) string {
	for _, item := range env {
		if item.Name == name {
			return item.Value
		}
	}
	return ""
}
