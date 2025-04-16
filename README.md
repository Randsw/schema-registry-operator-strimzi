# Confluent Schema Registry operator for Strimzi Kafka clusters (SSR operator)

## 1. Motivation

***
[Strimzi](https://strimzi.io/) is one of the best and easiest way to deploy [Kafka](https://kafka.apache.org/) cluster on Kubernetes. It provide simple way of managing user, topic, security etc.

[Confluent Schema Registry](https://docs.confluent.io/platform/6.2/schema-registry/index.html) provides a serving layer for your metadata. It provides a RESTful interface for storing and retrieving your Avro®, JSON Schema, and Protobuf schemas.

 It is an application that resides outside of your Kafka cluster and handles the distribution of schemas to the producer and consumer by storing a copy of schema in its.

Confluent Schema Registry well integrated with Confluent Platform and Confluent Cloud. But no with Strimzi.

One of the instrument that integrate Confluent Schema Registry with Strimzi Kafka cluster is [strimzi-registry-operator](https://github.com/lsst-sqre/strimzi-registry-operator). But, unfortunatly is no longer mantained(last commit was on on Dec 21, 2022) and have some issue like brocken Kubernetes RBAC and only http access to Schema Registry REST API endpoint. So I decide rewrite it concent usin Operator SDK on Golang. And add TLS support for REST API endpoint.

## 2. Overview

***

- Once you deploy a `StrimziSchemaRegistry` resource, the operator creates a Kubernetes deployment of the Confluent Schema Registry, along with an associated Kubernetes service and secret.
- Works with Strimzi's TLS authentication and authorization by converting the TLS certificate associated with a KafkaUser into a JKS-formatted keystore and truststore that's used by Confluence Schema Registry.
- When Strimzi updates either the Kafka cluster's CA certificate or the KafkaUser's client certificates, the operator automatically recreates the JKS truststore/keystore secrets and triggers a rolling restart of the Schema Registry pods.
- You can add TLS support to Schema Registry REST API endpoint by providing you own certificate or operator create it for you signed by Kafka Cluster CA certificate.

## 3. Deploy operator

***

### With Helm

A Helm chart is available for strimzi-registry-operator on GitHub at [randsw/schema-registry-operator-strimzi](https://github.com/Randsw/schema-registry-operator-strimzi/tree/main/helm-chart/ssr-operator)

```bash
helm repo add ssr-operator https://randsw.github.io/schema-registry-operator-strimzi/
helm repo update
helm install ssr-operator/ssr-operator --name ssr --namespace <desired_namespace> --create-namespace
```

