//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The operator-metrics scenario (E2-8) verifies the operator's own metrics
// endpoint end to end: the instance-count gauge tracks the live
// StrimziSchemaRegistry CR count (up on creation, down after deletion) and
// the reconcile counters are exposed.
//
// The chart installs the operator with metrics.enabled=true served over
// plain HTTP (metrics.secure=false) on port 8080 behind the
// <fullname>-metrics-service Service, so the endpoint is reachable through a
// plain client-go port-forward.
const (
	operatorMetricsServiceName = "ssr-operator-metrics-service"
	operatorMetricsRemotePort  = 8080

	// instanceCountMetric is the operator gauge tracking the live
	// StrimziSchemaRegistry CR count.
	instanceCountMetric = "strimzi_schema_registry_instance_current_count"
)

var _ = Describe("StrimziSchemaRegistry e2e operator metrics scenario", Ordered, Serial, func() {
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
			g.Expect(body).To(ContainSubstring(instanceCountMetric))
		}, statusTimeout, 2*time.Second).Should(Succeed(),
			"the metrics endpoint did not expose %s within %s", instanceCountMetric, statusTimeout)

		By("verifying the gauge matches the live CR count")
		crList := &v1alpha1.StrimziSchemaRegistryList{}
		Expect(k8sClient.List(ctx, crList)).To(Succeed())
		gauge, err := metricValue(body, instanceCountMetric)
		Expect(err).NotTo(HaveOccurred())
		Expect(gauge).To(Equal(float64(len(crList.Items))),
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
			value, metricErr := metricValue(fresh, instanceCountMetric)
			g.Expect(metricErr).NotTo(HaveOccurred())
			g.Expect(value).To(Equal(0.0),
				"the instance-count gauge must drop after the CR is deleted")
		}, statusTimeout, 2*time.Second).Should(Succeed())
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

// metricValue returns the value of the given unlabeled metric in a
// Prometheus text body. The body is parsed with the official expfmt text
// parser instead of string splitting, so HELP/TYPE lines, labels, float
// formatting and the OpenMetrics/exposition variants are handled correctly.
func metricValue(body, name string) (float64, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to parse the metrics body: %w", err)
	}
	family, ok := families[name]
	if !ok {
		return 0, fmt.Errorf("metric %q is not exposed", name)
	}
	for _, metric := range family.GetMetric() {
		// The operator metrics asserted here carry no labels; skip labeled
		// samples so a future label dimension cannot silently return the
		// wrong series.
		if len(metric.GetLabel()) != 0 {
			continue
		}
		switch {
		case metric.GetGauge() != nil:
			return metric.GetGauge().GetValue(), nil
		case metric.GetCounter() != nil:
			return metric.GetCounter().GetValue(), nil
		case metric.GetUntyped() != nil:
			return metric.GetUntyped().GetValue(), nil
		default:
			return 0, fmt.Errorf("metric %q has an unsupported type", name)
		}
	}
	return 0, fmt.Errorf("metric %q has no unlabeled sample", name)
}
