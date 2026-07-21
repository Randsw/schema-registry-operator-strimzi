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
	"StrimziSchemaRegistryCurrentInstanceCount": {
		Name: "strimzi_schema_register_instance_current_count",
		Help: "Current number of running strimzi schema register instance in cluster",
		Type: "Gauge",
	},
	"StrimziSchemaRegistryReconcileErrorsTotal": {
		Name: "strimzi_schema_registry_reconcile_errors_total",
		Help: "Total number of reconciliation errors encountered by the operator",
		Type: "Counter",
	},
	"StrimziSchemaRegistrySecretRotationTotal": {
		Name: "strimzi_schema_registry_secret_rotation_total",
		Help: "Total number of secret rotation events triggered by the operator",
		Type: "Counter",
	},
	"StrimziSchemaRegistryDeploymentUpdateTotal": {
		Name: "strimzi_schema_registry_deployment_update_total",
		Help: "Total number of deployment updates performed by the operator",
		Type: "Counter",
	},
}

var (
	// StrimziSchemaRegistryCurrentInstanceCount tracks the current number of
	// StrimziSchemaRegistry instances managed by the operator.
	StrimziSchemaRegistryCurrentInstanceCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: metricDescription["StrimziSchemaRegistryCurrentInstanceCount"].Name,
			Help: metricDescription["StrimziSchemaRegistryCurrentInstanceCount"].Help,
		},
	)

	// StrimziSchemaRegistryReconcileErrorsTotal counts reconciliation errors.
	StrimziSchemaRegistryReconcileErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: metricDescription["StrimziSchemaRegistryReconcileErrorsTotal"].Name,
			Help: metricDescription["StrimziSchemaRegistryReconcileErrorsTotal"].Help,
		},
	)

	// StrimziSchemaRegistrySecretRotationTotal counts secret rotation events.
	StrimziSchemaRegistrySecretRotationTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: metricDescription["StrimziSchemaRegistrySecretRotationTotal"].Name,
			Help: metricDescription["StrimziSchemaRegistrySecretRotationTotal"].Help,
		},
	)

	// StrimziSchemaRegistryDeploymentUpdateTotal counts deployment updates.
	StrimziSchemaRegistryDeploymentUpdateTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: metricDescription["StrimziSchemaRegistryDeploymentUpdateTotal"].Name,
			Help: metricDescription["StrimziSchemaRegistryDeploymentUpdateTotal"].Help,
		},
	)
)

// RegisterMetrics will register metrics with the global prometheus registry
func RegisterMetrics() {
	metrics.Registry.MustRegister(
		StrimziSchemaRegistryCurrentInstanceCount,
		StrimziSchemaRegistryReconcileErrorsTotal,
		StrimziSchemaRegistrySecretRotationTotal,
		StrimziSchemaRegistryDeploymentUpdateTotal,
	)
}
