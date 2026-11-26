//go:build e2e

package e2e

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// listRegistryEventsFor returns the reason of every event regarding the named
// StrimziSchemaRegistry CR.
func listRegistryEventsFor(ctx context.Context, name string) ([]string, error) {
	var reasons []string

	coreEvents := &corev1.EventList{}
	if err := k8sClient.List(ctx, coreEvents, client.InNamespace(scenario.Namespace),
		client.MatchingFields{"involvedObject.name": name}); err == nil {
		for _, ev := range coreEvents.Items {
			reasons = append(reasons, ev.Reason)
		}
	}

	return reasons, nil
}
