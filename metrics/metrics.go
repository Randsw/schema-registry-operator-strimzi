package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// MetricDescription is an exported struct that defines the metric description (Name, Help)
// as a new type named MetricDescription.
type MetricDescription struct {
	Name string
	Help string
	Type string
}

// metricsDescription is a map of string keys (metrics) to MetricDescription values (Name, Help).
var metricDescription = map[string]MetricDescription{
	"StrimziSchemaRegisterCurrentInstanceCount": {
		Name: "strimzi_schema_register_instance_current_count",
		Help: "Current number of running strimzi schema register instance in cluster",
		Type: "Gauge",
	},
}

var (
	// MemcachedDeploymentSizeUndesiredCountTotal will count how many times was required
	// to perform the operation to ensure that the number of replicas on the cluster
	// is the same as the quantity desired and specified via the custom resource size spec.
	StrimziSchemaRegisterCurrentInstanceCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: metricDescription["StrimziSchemaRegisterCurrentInstanceCount"].Name,
			Help: metricDescription["StrimziSchemaRegisterCurrentInstanceCount"].Help,
		},
	)
)

// RegisterMetrics will register metrics with the global prometheus registry
func RegisterMetrics() {
	metrics.Registry.MustRegister(StrimziSchemaRegisterCurrentInstanceCount)
}
