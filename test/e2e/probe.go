//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// probeContainerName is the container the probes are executed in.
	probeContainerName = "probe"

	// probeTimeout bounds a single curl execution inside the probe pod.
	probeTimeout = 30 * time.Second
)

// probePod is a long-lived pod used to run curl probes against the Schema
// Registry Service from inside the cluster. It is created once per namespace
// and reused for every assertion, so pod scheduling and image pulls are paid
// once instead of once per probe. The image is the same Confluent image the
// scenario already runs, so the node has it cached and no extra image pull is
// needed.
type probePod struct {
	namespace string
	name      string
}

func newProbePod(namespace, name string) *probePod {
	return &probePod{namespace: namespace, name: name}
}

// start creates the probe pod when it does not exist yet and waits until it is
// Running and Ready. Calling it on an already running pod is a no-op.
func (p *probePod) start(ctx context.Context) {
	Eventually(func(g Gomega) {
		err := k8sClient.Create(ctx, p.build())
		if err != nil && !apierrors.IsAlreadyExists(err) {
			g.Expect(err).NotTo(HaveOccurred())
		}

		current := &corev1.Pod{}
		g.Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: p.name, Namespace: p.namespace}, current)).To(Succeed())
		g.Expect(current.Status.Phase).To(Equal(corev1.PodRunning),
			"the probe pod phase is %s", current.Status.Phase)
		g.Expect(podReady(current)).To(BeTrue(), "the probe pod is not ready yet")
	}, statusTimeout, 2*time.Second).Should(Succeed(),
		"probe pod %s/%s did not become ready", p.namespace, p.name)
}

func (p *probePod) build() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: p.namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:            probeContainerName,
				Image:           registryImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sleep", "infinity"},
			}},
		},
	}
}

// curl executes curl inside the probe pod and returns the combined stdout and
// stderr. A non-zero curl exit status is surfaced as an error, which lets the
// NetworkPolicy scenario assert the blocked probe while still inspecting its
// output.
func (p *probePod) curl(ctx context.Context, target string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req := kubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(p.namespace).
		Name(p.name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: probeContainerName,
			Command:   []string{"curl", "-s", "-m", "10", target},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restConfig, http.MethodPost, req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to build the exec request for pod %s/%s: %w",
			p.namespace, p.name, err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(probeCtx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.String() + stderr.String(), err
}

// stop deletes the probe pod and waits until it is gone.
func (p *probePod) stop(ctx context.Context) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: p.namespace}}
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))).To(Succeed(),
		"failed to delete the probe pod %s/%s", p.namespace, p.name)

	Eventually(func(g Gomega) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: p.name, Namespace: p.namespace},
			&corev1.Pod{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"the probe pod is still present (err: %v)", err)
	}, statusTimeout, 2*time.Second).Should(Succeed(),
		"timed out waiting for the deletion of the probe pod %s/%s", p.namespace, p.name)
}

// podReady reports whether the pod is running with every container ready.
func podReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if !status.Ready {
			return false
		}
	}
	return true
}

// ensureNamespace creates the namespace when it does not exist yet.
func ensureNamespace(ctx context.Context, name string) {
	err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "failed to create the namespace %s", name)
	}
}
