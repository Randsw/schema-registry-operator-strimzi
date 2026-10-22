//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os/exec"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	. "github.com/onsi/gomega"
)

// withRegistryClient port-forwards the operator-created Schema Registry
// service (remote port 443, the TLS REST API port) to a local port and
// invokes f with a REST client connected to it.
func withRegistryClient(ca *clusterCA, clientCert *tls.Certificate, f func(*registryClient)) {
	waitForService(context.Background(), 2*time.Minute)

	stop, err := startPortForward(localPort)
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

// startPortForward starts `kubectl port-forward` for the Schema Registry
// service and returns a stop function that must be called when done.
func startPortForward(port int) (func(), error) {
	cmd := exec.Command("kubectl", "port-forward",
		"-n", scenario.Namespace,
		fmt.Sprintf("svc/%s", scenario.RegistryName),
		fmt.Sprintf("%d:443", port),
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start kubectl port-forward: %w", err)
	}

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	}

	address := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return nil, fmt.Errorf("kubectl port-forward to svc/%s/%s exited before the port became ready",
				scenario.Namespace, scenario.RegistryName)
		default:
		}
		if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			_ = conn.Close()
			return stop, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	stop()
	return nil, fmt.Errorf("port-forward to svc/%s/%s did not become ready within 30s",
		scenario.Namespace, scenario.RegistryName)
}
