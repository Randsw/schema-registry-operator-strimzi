//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// The operator-metrics scenario (E2-8) verifies the operator's own metrics
// endpoint end to end: the instance-count gauge tracks the live
// StrimziSchemaRegistry CR count (up on creation, down after deletion) and
// the reconcile counters are exposed.
//
// The chart installs the operator with metrics.enabled=true and the secure
// default (metrics.secure=true: HTTPS with authentication/authorization) on
// port 8080 behind the <fullname>-metrics-service Service. The scenario binds
// the chart's metrics-reader ClusterRole to a scraper ServiceAccount, mints a
// token for it and scrapes HTTPS through a client-go port-forward, proving the
// secure endpoint actually serves metrics. The plain-HTTP opt-out is covered
// by the chart rendering tests.
const (
	operatorMetricsServiceName = "ssr-operator-metrics-service"
	operatorMetricsRemotePort  = 8080

	// metricsScraperServiceAccount is the ServiceAccount the scenario binds to
	// the chart's metrics-reader ClusterRole to obtain a scrape token.
	metricsScraperServiceAccount = "ssr-operator-metrics-scraper"

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

		By("minting a bearer token allowed to get the non-resource /metrics URL")
		metricsToken := metricsScraperToken(ctx)

		By("scraping the secure metrics endpoint")
		var body string
		Eventually(func(g Gomega) {
			var scrapeErr error
			body, scrapeErr = scrapeMetrics(localPort, metricsToken)
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
			fresh, scrapeErr := scrapeMetrics(localPort, metricsToken)
			g.Expect(scrapeErr).NotTo(HaveOccurred())
			value, metricErr := metricValue(fresh, instanceCountMetric)
			g.Expect(metricErr).NotTo(HaveOccurred())
			g.Expect(value).To(Equal(0.0),
				"the instance-count gauge must drop after the CR is deleted")
		}, statusTimeout, 2*time.Second).Should(Succeed())
	})
})

// metricsHTTPClient skips TLS verification: the port-forward targets the
// operator pod directly and controller-runtime's in-memory self-signed serving
// certificate is not signed by the cluster CA. Authentication is bearer-token
// based, so skipping verification here does not weaken the authn/authz coverage.
var metricsHTTPClient = &http.Client{ //nolint:gosec // self-signed serving cert; authn is token-based
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
}

// scrapeMetrics fetches the secure operator metrics endpoint through the
// port-forward using the scraper token and returns the Prometheus text body.
func scrapeMetrics(port int, token string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d/metrics", port), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := metricsHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metrics endpoint returned %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// metricsScraperToken provisions a ServiceAccount bound to the chart's
// metrics-reader ClusterRole and returns a token for it. The secure metrics
// endpoint authenticates the bearer token with the API server (TokenReview)
// and authorizes non-resource GET /metrics through SubjectAccessReview, so the
// scrape must present a token whose RBAC grants that access.
func metricsScraperToken(ctx context.Context) string {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:      metricsScraperServiceAccount,
		Namespace: scenario.Namespace,
	}}
	if err := k8sClient.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}

	// Cluster-scoped RBAC names embed the release namespace (the chart's
	// rbacName helper), and the e2e release is "ssr-operator" in the scenario
	// namespace.
	readerRole := fmt.Sprintf("%s-ssr-operator-metrics-reader", scenario.Namespace)
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: metricsScraperServiceAccount},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     readerRole,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      metricsScraperServiceAccount,
			Namespace: scenario.Namespace,
		}},
	}
	if err := k8sClient.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}

	var token string
	Eventually(func(g Gomega) {
		tr, err := kubeClient.CoreV1().ServiceAccounts(scenario.Namespace).
			CreateToken(ctx, metricsScraperServiceAccount, &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
		g.Expect(err).NotTo(HaveOccurred())
		token = tr.Status.Token
		g.Expect(token).NotTo(BeEmpty())
	}, statusTimeout, 2*time.Second).Should(Succeed())
	return token
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
