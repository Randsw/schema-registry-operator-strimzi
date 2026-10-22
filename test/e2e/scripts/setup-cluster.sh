#!/usr/bin/env bash
# Deploys the e2e test infrastructure for one security-protocol scenario:
# Strimzi operator, a scaled-down Kafka cluster (1 KRaft controller + 1 broker)
# with only the listener the scenario needs, the registry-schemas topic and the
# confluent-schema-registry KafkaUser.
#
# Usage: setup-cluster.sh <ssl|sasl-ssl|plaintext|sasl-plaintext>
#
# Assumes an existing kind cluster and the kubectl/helm CLIs. The ssr-operator
# itself is installed separately (see .github/workflows/e2e.yaml) so that the
# image built from the commit under test can be used.

set -euo pipefail

SCENARIO="${1:?usage: setup-cluster.sh <ssl|sasl-ssl|plaintext|sasl-plaintext>}"
NAMESPACE="${E2E_NAMESPACE:-kafka}"
CLUSTER_NAME="${E2E_CLUSTER_NAME:-kafka-cluster}"
MANIFEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../manifests" && pwd)"

case "$SCENARIO" in
  ssl|sasl-ssl|plaintext|sasl-plaintext) ;;
  *)
    echo "ERROR: unknown scenario '$SCENARIO' (expected ssl, sasl-ssl, plaintext or sasl-plaintext)" >&2
    exit 1
    ;;
esac

echo ">>> Creating namespace $NAMESPACE"
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

echo ">>> Installing Strimzi operator"
helm upgrade --install --wait --timeout 15m --atomic \
  --namespace "$NAMESPACE" \
  strimzi-operator oci://quay.io/strimzi-helm/strimzi-kafka-operator \
  --set replicas=1

echo ">>> Deploying Kafka cluster for scenario '$SCENARIO'"
kubectl apply -n "$NAMESPACE" -f "$MANIFEST_DIR/kafka-$SCENARIO.yaml"

echo ">>> Waiting for Kafka cluster $CLUSTER_NAME to become ready"
kubectl wait "kafka/$CLUSTER_NAME" -n "$NAMESPACE" \
  --for=condition=Ready --timeout=900s

echo ">>> Deploying registry-schemas topic"
kubectl apply -n "$NAMESPACE" -f "$MANIFEST_DIR/kafkatopic.yaml"
kubectl wait kafkatopic/registry-schemas -n "$NAMESPACE" \
  --for=condition=Ready --timeout=300s

echo ">>> Deploying confluent-schema-registry KafkaUser"
case "$SCENARIO" in
  ssl)
    kubectl apply -n "$NAMESPACE" -f "$MANIFEST_DIR/kafkauser-schema-registry-tls.yaml"
    ;;
  sasl-ssl|sasl-plaintext)
    kubectl apply -n "$NAMESPACE" -f "$MANIFEST_DIR/kafkauser-schema-registry-scram.yaml"
    ;;
  plaintext)
    echo ">>> PLAINTEXT scenario needs no KafkaUser for the Schema Registry"
    ;;
esac

if [ "$SCENARIO" != "plaintext" ]; then
  echo ">>> Waiting for the KafkaUser to become ready"
  kubectl wait kafkauser/confluent-schema-registry -n "$NAMESPACE" \
    --for=condition=Ready --timeout=300s
fi

echo ">>> Waiting for the cluster CA secrets to be provisioned"
kubectl wait "secret/$CLUSTER_NAME-cluster-ca-cert" -n "$NAMESPACE" \
  --for=jsonpath='{.data.ca\.crt}' --timeout=300s
kubectl wait "secret/$CLUSTER_NAME-cluster-ca" -n "$NAMESPACE" \
  --for=jsonpath='{.data.ca\.key}' --timeout=300s

echo ">>> Scenario '$SCENARIO' infrastructure is ready"
