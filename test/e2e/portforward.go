//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// registryServicePort is the operator-created Schema Registry REST API
// Service port (443 when securehttp is enabled).
const registryServicePort = 443

// withRegistryClient port-forwards the operator-created Schema Registry
// service to an OS-assigned local port and invokes f with a REST client
// connected to it.
func withRegistryClient(ca *clusterCA, clientCert *tls.Certificate, f func(*registryClient)) {
	ctx := context.Background()
	waitForService(ctx, statusTimeout)

	localPort, stop, err := startPortForward(ctx, scenario.RegistryName, registryServicePort)
	Expect(err).NotTo(HaveOccurred())
	defer stop()

	f(newRegistryClient(ca.certPool(), clientCert, localPort))
}

// waitForService waits until the operator-created Schema Registry service
// exists. The service is created after the deployment becomes ready, so this
// also acts as an additional readiness gate before port-forwarding.
func waitForService(ctx context.Context, timeout time.Duration) {
	Eventually(func(g Gomega) {
		svc := &corev1.Service{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name:      scenario.RegistryName,
			Namespace: scenario.Namespace,
		}, svc)).To(Succeed())
	}, timeout, 2*time.Second).Should(Succeed())
}

// startPortForward forwards a Service port to an OS-assigned local port using
// the client-go port-forward API (no kubectl shell-out). It resolves the
// Service to a ready backing pod and forwards to that pod's target container
// port, mirroring `kubectl port-forward svc/...`. Binding the local side to
// port 0 makes concurrent e2e runs (and other services on the host) unable to
// collide. It returns the local port and a stop function that must be called
// when done.
func startPortForward(ctx context.Context, serviceName string, servicePort int) (int, func(), error) {
	var (
		pod        corev1.Pod
		targetPort int32
	)
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, statusTimeout, true,
		func(ctx context.Context) (bool, error) {
			p, port, err := serviceEndpoint(ctx, serviceName, servicePort)
			if err != nil {
				// The operator creates the Service and its pods
				// asynchronously, so a missing or not-yet-ready backend is
				// retried instead of failing the spec on the first poll.
				return false, nil
			}
			pod, targetPort = p, port
			return true, nil
		})
	if err != nil {
		return 0, nil, fmt.Errorf("service %s/%s has no ready backend pod: %w",
			scenario.Namespace, serviceName, err)
	}

	req := kubeClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to build the port-forward transport: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport},
		http.MethodPost, req.URL())

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"},
		[]string{fmt.Sprintf("0:%d", targetPort)}, stopChan, readyChan, io.Discard, io.Discard)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create the port-forwarder: %w", err)
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- forwarder.ForwardPorts()
	}()

	select {
	case <-readyChan:
	case err := <-errChan:
		return 0, nil, fmt.Errorf("port-forward to pod %s/%s failed before becoming ready: %w",
			pod.Namespace, pod.Name, err)
	case <-time.After(statusTimeout):
		close(stopChan)
		return 0, nil, fmt.Errorf("port-forward to pod %s/%s did not become ready within %s",
			pod.Namespace, pod.Name, statusTimeout)
	}

	ports, err := forwarder.GetPorts()
	if err != nil {
		close(stopChan)
		return 0, nil, fmt.Errorf("failed to read the forwarded port: %w", err)
	}
	if len(ports) != 1 {
		close(stopChan)
		return 0, nil, fmt.Errorf("expected 1 forwarded port, got %d", len(ports))
	}
	localPort := int(ports[0].Local)

	stop := func() {
		close(stopChan)
		select {
		case <-errChan:
		case <-time.After(5 * time.Second):
		}
	}
	return localPort, stop, nil
}

// serviceEndpoint resolves a Service to a ready pod backing it and the target
// container port the Service port forwards to.
func serviceEndpoint(ctx context.Context, serviceName string, servicePort int) (corev1.Pod, int32, error) {
	svc := &corev1.Service{}
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      serviceName,
		Namespace: scenario.Namespace,
	}, svc); err != nil {
		return corev1.Pod{}, 0, err
	}
	if len(svc.Spec.Selector) == 0 {
		return corev1.Pod{}, 0, fmt.Errorf("service %s/%s has no pod selector", svc.Namespace, svc.Name)
	}

	targetPort := int32(servicePort)
	for _, port := range svc.Spec.Ports {
		if int(port.Port) != servicePort {
			continue
		}
		if port.TargetPort.IntVal != 0 {
			targetPort = port.TargetPort.IntVal
		}
		break
	}
	if targetPort <= 0 {
		return corev1.Pod{}, 0, fmt.Errorf("service %s/%s port %d has no numeric target port",
			svc.Namespace, svc.Name, servicePort)
	}

	pods := &corev1.PodList{}
	if err := k8sClient.List(ctx, pods, client.InNamespace(svc.Namespace),
		client.MatchingLabels(svc.Spec.Selector)); err != nil {
		return corev1.Pod{}, 0, err
	}
	for _, pod := range pods.Items {
		if podReady(&pod) {
			return pod, targetPort, nil
		}
	}
	return corev1.Pod{}, 0, fmt.Errorf("service %s/%s has no ready pod", svc.Namespace, svc.Name)
}
