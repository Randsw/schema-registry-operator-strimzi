//go:build e2e

/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package e2e contains end-to-end tests for the Schema Registry operator
// against a real Kubernetes cluster (kind) running a Strimzi Kafka cluster.
//
// The tests are excluded from regular unit tests (make test filters out /e2e
// packages) and are only compiled with the "e2e" build tag:
//
//	go test -tags=e2e ./test/e2e/...
//
// The scenario under test is selected via environment variables set by
// .github/workflows/e2e.yaml:
//
//	E2E_SECURITY_PROTOCOL - StrimziSchemaRegistry spec.securityprotocol (SSL, SASL_SSL, PLAINTEXT, SASL_PLAINTEXT)
//	E2E_LISTENER         - StrimziSchemaRegistry spec.listener (tls, saslssl, plain, saslplain)
//	E2E_NAMESPACE        - namespace of the Kafka cluster and Schema Registry (default "kafka")
//	E2E_CLUSTER_NAME     - Strimzi Kafka cluster name (default "kafka-cluster")
//	E2E_REGISTRY_NAME    - StrimziSchemaRegistry CR name (default "confluent-schema-registry")
//
// The required infrastructure (Strimzi operator, Kafka cluster with the
// scenario listener, registry-schemas topic, KafkaUser) is deployed by
// test/e2e/scripts/setup-cluster.sh before the tests run.
package e2e

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
)

// scenarioConfig describes the e2e scenario under test.
type scenarioConfig struct {
	SecurityProtocol string
	Listener         string
	Namespace        string
	ClusterName      string
	RegistryName     string
}

var (
	k8sClient client.Client
	scenario  scenarioConfig
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Schema Registry Operator E2E Suite")
}

var _ = BeforeSuite(func() {
	scenario = scenarioConfig{
		SecurityProtocol: envOrDefault("E2E_SECURITY_PROTOCOL", "SSL"),
		Listener:         envOrDefault("E2E_LISTENER", "tls"),
		Namespace:        envOrDefault("E2E_NAMESPACE", "kafka"),
		ClusterName:      envOrDefault("E2E_CLUSTER_NAME", "kafka-cluster"),
		RegistryName:     envOrDefault("E2E_REGISTRY_NAME", "confluent-schema-registry"),
	}

	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(strimziregistryoperatorv1alpha1.AddToScheme(scheme)).To(Succeed())

	restConfig, err := config.GetConfig()
	Expect(err).NotTo(HaveOccurred(), "failed to load kubeconfig (is the kind cluster running?)")
	k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
})

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
