package metrics

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// TestRegisterMetricsExposesCorrectedInstanceCountGauge covers B13: the
// instance-count gauge is registered and exposed under the correctly spelled
// name strimzi_schema_registry_instance_current_count (the old misspelled
// strimzi_schema_register_instance_current_count metric was removed).
//
// The gauge's set/read-back behavior is deliberately not asserted: that is
// the Prometheus client library's contract, not the operator's. The operator
// contract is the registration and the exposed name.
func TestRegisterMetricsExposesCorrectedInstanceCountGauge(t *testing.T) {
	RegisterMetrics()

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	found := false
	for _, mf := range families {
		if mf.GetName() == "strimzi_schema_registry_instance_current_count" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected metric strimzi_schema_registry_instance_current_count to be registered and exposed")
	}
}
