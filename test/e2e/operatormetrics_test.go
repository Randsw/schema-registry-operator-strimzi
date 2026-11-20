//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The operator-metrics scenario (E2-8) verifies the operator's own metrics
// endpoint end to end: the instance-count gauge tracks the live
// StrimziSchemaRegistry CR count (up on creation, down after deletion) and
// the reconcile counters are exposed.
//
// The chart installs the operator with metrics.enabled=true served over
// plain HTTP (metrics.secure=false) on port 8080 behind the
// <fullname>-metrics-service Service, so the endpoint is reachable via a
// plain kubectl port-forward.
const (
	operatorMetricsServiceName = "ssr-operator-metrics-service"
	operatorMetricsRemotePort  = 8080
)

var _ = Describe("StrimziSchemaRegistry e2e operator metrics scenario", Ordered, func() {
	AfterAll(func() {
		By("deleting the StrimziSchemaRegistry CR to leave a clean slate")
		deleteRegistryCR(context.Background(), readyTimeout)
	})

	It("tracks the CR count in the gauge and exposes the counters", func() {
		ctx := context.Background()

		By("creating a CR so the gauge has a non-zero value")
		Expect(applyRegistryCR(ctx, false, false, nil)).To(Succeed())
		waitForReadyCondition(ctx, readyTimeout)
		waitForDeploymentRollout(ctx, readyTimeout)

		By("port-forwarding the operator metrics endpoint")
		localPort, stop, err := startPortForward(ctx, operatorMetricsServiceName, operatorMetricsRemotePort)
		Expect(err).NotTo(HaveOccurred())
		defer stop()

		By("scraping the metrics endpoint")
		var body string
		Eventually(func(g Gomega) {
			var scrapeErr error
			body, scrapeErr = scrapeMetrics(localPort)
			g.Expect(scrapeErr).NotTo(HaveOccurred())
			g.Expect(body).To(ContainSubstring("strimzi_schema_registry_instance_current_count"))
		}, readyTimeout, 2*time.Second).Should(Succeed())

		By("verifying the gauge matches the live CR count")
		crList := &v1alpha1.StrimziSchemaRegistryList{}
		Expect(k8sClient.List(ctx, crList)).To(Succeed())
		Expect(metricValue(body, "strimzi_schema_registry_instance_current_count")).To(
			Equal(float64(len(crList.Items))),
			"the instance-count gauge must track the live CR count")

		By("verifying the reconcile counters are exposed")
		for _, name := range []string{
			"strimzi_schema_registry_reconcile_errors_total",
			"strimzi_schema_registry_secret_rotation_total",
			"strimzi_schema_registry_deployment_update_total",
		} {
			Expect(body).To(ContainSubstring(name), "expected metric %s to be exposed", name)
		}

		By("deleting the CR and verifying the gauge drops to zero")
		deleteRegistryCR(ctx, readyTimeout)
		Eventually(func(g Gomega) {
			fresh, scrapeErr := scrapeMetrics(localPort)
			g.Expect(scrapeErr).NotTo(HaveOccurred())
			g.Expect(metricValue(fresh, "strimzi_schema_registry_instance_current_count")).To(
				Equal(0.0), "the instance-count gauge must drop after the CR is deleted")
		}, readyTimeout, 2*time.Second).Should(Succeed())
	})
})

// scrapeMetrics fetches the operator metrics endpoint through the
// port-forward and returns the Prometheus text body.
func scrapeMetrics(port int) (string, error) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// metricValue returns the value of the given (unlabeled) metric in a
// Prometheus text body, or -1 when the metric is absent.
func metricValue(body, name string) float64 {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if value, err := strconv.ParseFloat(fields[1], 64); err == nil {
			return value
		}
	}
	return -1
}
