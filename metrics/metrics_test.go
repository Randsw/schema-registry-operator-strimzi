package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// TestRegisterMetricsExposesCorrectedInstanceCountGauge covers B13: the
// instance-count gauge is exposed under the correctly spelled name
// strimzi_schema_registry_instance_current_count (the old misspelled
// strimzi_schema_register_instance_current_count metric was removed).
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

// TestInstanceCountGaugeRegisterCleanly verifies the corrected instance-count
// gauge can be registered into a fresh Prometheus registry without conflicts
// and exposes the set value.
func TestInstanceCountGaugeRegisterCleanly(t *testing.T) {
	registry := prometheus.NewRegistry()
	if err := registry.Register(StrimziSchemaRegistryCurrentInstanceCount); err != nil {
		t.Fatalf("failed to register the instance count gauge: %v", err)
	}

	StrimziSchemaRegistryCurrentInstanceCount.Set(3)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}
	value := -1.0
	for _, mf := range families {
		if mf.GetName() != "strimzi_schema_registry_instance_current_count" {
			continue
		}
		for _, m := range mf.GetMetric() {
			value = m.GetGauge().GetValue()
		}
	}
	if value != 3 {
		t.Errorf("expected the gauge to expose 3, got %v", value)
	}
}
