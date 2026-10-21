<h1 align="center" style="border-bottom: none;">SSR Operator</h1>
<h3 align="center">Confluent Schema Registry for Strimzi Kafka operator(SSR operator)</h3>
</p>
<p align="center">
<a href="" alt="GoVersion">
        <img src="https://img.shields.io/github/go-mod/go-version/randsw/schema-registry-operator-strimzi" /></a>
<a href="" alt="Release">
        <img src="https://img.shields.io/github/v/release/randsw/schema-registry-operator-strimzi" /></a>
<a href="" alt="LastTag">
        <img src="https://img.shields.io/github/v/tag/randsw/schema-registry-operator-strimzi" /></a>
<a href="" alt="LastCommit">
        <img src="https://img.shields.io/github/last-commit/randsw/schema-registry-operator-strimzi" /></a>
<a href="" alt="Build">
        <img src="https://img.shields.io/github/actions/workflow/status/randsw/schema-registry-operator-strimzi/build.yaml" /></a>
<a href="" alt="Build">
        <img src="https://img.shields.io/github/actions/workflow/status/randsw/schema-registry-operator-strimzi/test-release.yaml?label=tests" /></a>
<a href="" alt="Build">
        <img src="https://img.shields.io/github/actions/workflow/status/randsw/schema-registry-operator-strimzi/helm-chart.yaml?label=helm-chart-test" /></a></p>

## 1. Motivation

***
[Strimzi](https://strimzi.io/) is one of the best and easiest way to deploy [Kafka](https://kafka.apache.org/) cluster on Kubernetes. It provides a simple way of managing users, topics, security, etc.

[Confluent Schema Registry](https://docs.confluent.io/platform/6.2/schema-registry/index.html) provides a serving layer for your metadata. It provides a RESTful interface for storing and retrieving your Avro®, JSON Schema, and Protobuf schemas.

It is an application that resides outside your Kafka cluster and handles the distribution of schemas to producers and consumers by storing a copy of the schema in its storage.

Confluent Schema Registry well integrated with Confluent Platform and Confluent Cloud. But not with Strimzi.

One of the instruments that integrates Confluent Schema Registry with a Strimzi Kafka cluster is [strimzi-registry-operator](https://github.com/lsst-sqre/strimzi-registry-operator). However, unfortunately, it is no longer maintained (the last commit was on Dec 21, 2022) and has several issues, like broken Kubernetes RBAC and only HTTP access to the Schema Registry REST API endpoint. Therefore I decided to rewrite it, focusing on using the Operator SDK in Golang and add some missing functionality.

## 2. Overview

***

- Once you deploy a `StrimziSchemaRegistry` resource, the operator creates a Kubernetes deployment of the Confluent Schema Registry, along with an associated Kubernetes service and secret.
- Works with Strimzi's TLS authentication and authorization by converting the TLS certificate associated with a KafkaUser into a JKS-formatted keystore and truststore that's used by Confluent Schema Registry.
- When Strimzi updates either the Kafka cluster's CA certificate or the KafkaUser's client certificates, the operator automatically recreates the JKS truststore/keystore secrets and triggers a rolling restart of the Schema Registry pods.
- You can add TLS support to the Schema Registry REST API endpoint by providing your own certificate or letting the operator create one for you, signed by the Kafka Cluster CA certificate.

## 3. Deploy operator

***

### With Helm

A Helm chart is available for strimzi-registry-operator on GitHub at [randsw/schema-registry-operator-strimzi](https://github.com/Randsw/schema-registry-operator-strimzi/tree/main/helm-chart/ssr-operator)

```bash
helm repo add ssr-operator https://randsw.github.io/schema-registry-operator-strimzi/
helm repo update
helm install ssr ssr-operator/ssr-operator --namespace <desired_namespace> --create-namespace
```

## 4. Deploy a Schema Registry

***

### Step 1. Deploy a KafkaTopic

:warning: Don't forget specify namespace

:warning: Pay attention to the label `strimzi.io/cluster`. It should match the Kafka cluster name (the name of the `Kafka` CR).

Deploy a `KafkaTopic` that the Schema Registry will use as its primary storage.

```yaml
apiVersion: kafka.strimzi.io/v1
kind: KafkaTopic
metadata:
  name: registry-schemas
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  partitions: 1
  replicas: 3
  config:
    # http://kafka.apache.org/documentation/#topicconfigs
    cleanup.policy: compact
```

> **Note**
> The default topic name is `registry-schemas`. It can now be overridden with `spec.kafkastoretopic`.
> The name `_schemas` (Confluent's own default) isn't used because it isn't convenient to create with `KafkaTopic` resources.
> For auto-creation, the replication factor follows `spec.kafkastoretopicreplicationfactor`; leave it unset to use Confluent's built-in default (`3`), or set `1` for small or single-node clusters.

### Step 2. Deploy a KafkaUser

Deploy a `KafkaUser` for the Schema Registry that gives the Schema Registry sufficient permissions:

```yaml
apiVersion: kafka.strimzi.io/v1
kind: KafkaUser
metadata:
  name: confluent-schema-registry
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  authentication:
    type: tls
  authorization:
    # Official docs on authorizations required for the Schema Registry:
    # https://docs.confluent.io/current/schema-registry/security/index.html#authorizing-access-to-the-schemas-topic
    type: simple
    acls:
      # Allow all operations on the registry-schemas topic
      # Read, Write, and DescribeConfigs are known to be required
      - resource:
          type: topic
          name: registry-schemas
          patternType: literal
        operations:
          - All
        type: allow
        host: "*"
      # Allow all operations on the schema-registry* group
      - resource:
          type: group
          name: schema-registry
          patternType: prefix
        operations:
          - All
        type: allow
        host: "*"
      # Allow Describe on the __consumer_offsets topic
      - resource:
          type: topic
          name: __consumer_offsets
          patternType: literal
        operations:
          - Describe
        type: allow
        host: "*"
```

### Step 3. Deploy the StrimziSchemaRegistry

Now that there is a topic and a user, you can deploy the Schema Registry itself.
The strimzi-schema-registry operator deploys the Schema Registry given a `StrimziSchemaRegistry` resource:

```yaml
apiVersion: strimziregistryoperator.randsw.code/v1alpha1
kind: StrimziSchemaRegistry
metadata:
  name: confluent-schema-registry
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  securehttp:         true
  listener:           "tls"
  compatibilitylevel: "forward"
  securityprotocol:   "SSL"
  template:
    spec:
      containers:
        - name: confluent-sr
          image: confluentinc/cp-schema-registry:7.6.5
          imagePullPolicy: IfNotPresent
      restartPolicy: Always
```

## 5. Access to StrimziSchemaRegistry

Schema Registry can be accessed using service in namespace where StrimziSchemaRegistry deployed. The name of the service matches the name of the CR.

## 6. StrimziSchemaRegistry configuration properties

These configurations can be set as fields of the StrimziSchemaRegistry's spec field:

```yaml
apiVersion: strimziregistryoperator.randsw.code/v1alpha1
kind: StrimziSchemaRegistry
metadata:
  name: confluent-schema-registry
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  securehttp:         true
  clientauth:         false
  listener:           "tls"
  compatibilitylevel: "forward"
  securityprotocol:   "SSL"
  tlssecretName:      ""
  kafkastoretopic: "registry-schemas"
  kafkastoretopicreplicationfactor: 1
  schemaregistrygroupid: "schema-registry"
  mastereligibility: true
  template:
    spec:
      containers:
        - name: confluent-sr
          image: confluentinc/cp-schema-registry:7.6.5
          imagePullPolicy: IfNotPresent
      restartPolicy: Always
```

### Schema Registry-related configurations

- `listener` is the **name** of the Kafka listener that the Schema Registry should use.
  You should set this value based on your `Kafka` resource.
  The ["In-detail: listener configuration"](#in-detail-listener-configuration) section, below, explains this in more detail.
  See also: Schema Registry [listeners](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#listeners) docs.

- `securityProtocol` is the security protocol for the Schema Registry to communicate with Kafka. Default is `SSL`. The authentication method is auto-detected from the referenced `KafkaUser` (`spec.authentication.type`). Supported values:
  
  - `SSL` — TLS transport with mTLS (client certificates). Requires a `KafkaUser` with `authentication.type: tls`. The operator builds a keystore and truststore from the KafkaUser secret.
  - `SASL_SSL` — TLS transport with SCRAM-SHA-512 authentication. Requires a `KafkaUser` with `authentication.type: scram-sha-512`. The operator builds only a truststore and reads the SCRAM credentials directly from the KafkaUser secret.
  - `SASL_PLAINTEXT` — plaintext transport with SCRAM-SHA-512 authentication. Requires a `KafkaUser` with `authentication.type: scram-sha-512`. No certificates or keystore are required.
  - `PLAINTEXT` — plaintext transport with no authentication. No `KafkaUser` secret or certificates are required.

  See also: Schema Registry [kafkastore.security.protocol](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#kafkastore-security-protocol) docs.

- `compatibilityLevel` is the default schema compatibility level. Possible values:

  - `none`
  - `backward`
  - `backward_transitive`
  - `forward`
  - `forward_transitive`
  - `full`
  - `full_transitive`

  See also: Schema Registry [schema.compatibility.level](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#schema-compatibility-level) docs.

- `securehttp` enable TLS on Schema Registry REST API endpoint.
  If `securehttp` is disabled the associated service points to `8081` port in Schema Registry pod. If enabled - to `8085` port.
- `clientauth` enables mutual TLS (mTLS) on the Schema Registry REST API endpoint. Default is `false`.
  It is only valid when `securehttp` is `true`: setting it without `securehttp` marks the CR
  `Ready=False` with reason `ClientAuthRequiresSecureHTTP` and emits a `Warning` event, while reconciliation
  still succeeds. When enabled, clients must present a certificate signed by the Kafka cluster CA. The operator
  reuses the cluster-CA truststore and sets `SCHEMA_REGISTRY_SSL_CLIENT_AUTH=true` together with
  `SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION` and `SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD`. For
  `PLAINTEXT`/`SASL_PLAINTEXT` security protocols (which do not otherwise need a truststore), the operator
  builds a truststore-only secret from the cluster CA certificate for this purpose.
- `tlssecretName` is name of secret that contain TLS certificate and private key pair in JKS format. Must be in same
  namespace  with `StrimziSchemaRegistry` CR
  If this field is omitted or an empty string, the SSR operator automatically creates a TLS certificate and private key pair in JKS format, signs them with the Strimzi Kafka cluster CA(stored in the `<kafka-clustername>-cluster-ca` and `<kafka-clustername>-cluster-ca-cert` secrets)
  and mounts the secret to the Schema Registry pod.
  Secret data format:

  |Key               |Value            |
  |------------------|-----------------|
  |tls-keystore.jks  |Keystore         |
  |keystore_password |Keystore password|
  |key_password      |Key password     |
  
  [Java Generate Keys Tutorial](https://docs.oracle.com/javase/tutorial/security/toolsign/step3.html)

  See also: Schema Registry [Configuring the REST API for HTTP or HTTPS](https://docs.confluent.io/platform/current/schema-registry/security/index.html#configuring-the-rest-api-for-http-or-https)

- `kafkastoretopic` is the name of the Kafka topic Schema Registry uses to store schemas. Default is `registry-schemas`.

  See also: Schema Registry [kafkastore.topic](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#kafkastore-topic) docs.

- `kafkastoretopicreplicationfactor` is the replication factor used when Schema Registry creates the storage topic.
  It has no default: leave it unset to preserve Confluent's built-in default (`3`), or set `1` for small or single-node clusters. The value must be `>= 1`.

  See also: Schema Registry [kafkastore.topic.replication.factor](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#kafkastore-topic-replication-factor) docs.

- `schemaregistrygroupid` is the Kafka consumer group id used by Schema Registry. Default is `schema-registry`.

  See also: Schema Registry [schema.registry.group.id](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#schema-registry-group-id) docs.

- `mastereligibility` controls whether this Schema Registry instance is eligible to be elected master. Default is `true`.

  See also: Schema Registry [master.eligibility](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#master-eligibility) docs.

- `template` is a standart Kubernetes template for pod. you can configure it as you want according to the [pod specification](https://dev-k8sref-io.web.app/docs/workloads/podtemplate-v1/)

### Arbitrary Schema Registry configuration

Use `spec.configuration` to pass arbitrary Confluent Schema Registry properties
that are not exposed as dedicated spec fields. Keys are the dotted Confluent
property names and values are strings:

```yaml
spec:
  # ...
  configuration:
    access.control.allow.origin: "*"
    authentication.realm: "Basic"
    debug: "true"
```

Each key is converted into a `SCHEMA_REGISTRY_*` environment variable with these
rules:

- The key is uppercased.
- `.` becomes `_`.
- `-` becomes `_`.
- The result is prefixed with `SCHEMA_REGISTRY_`.

Examples:

| Confluent property            | Environment variable                              |
|-------------------------------|---------------------------------------------------|
| `access.control.allow.origin` | `SCHEMA_REGISTRY_ACCESS_CONTROL_ALLOW_ORIGIN`      |
| `authentication.realm`        | `SCHEMA_REGISTRY_AUTHENTICATION_REALM`             |
| `debug`                       | `SCHEMA_REGISTRY_DEBUG`                            |
| `kafkastore.timeout.ms`       | `SCHEMA_REGISTRY_KAFKASTORE_TIMEOUT_MS`            |

The following keys are operator-managed and are **ignored** when supplied through
`spec.configuration` (their managed value wins). Each overridden key is logged
and surfaced as a `Warning` event (`ConfigKeyOverridden`) on the CR; reconciliation
still succeeds.

| Reserved key                               | Operator-managed setting                                    |
|--------------------------------------------|-------------------------------------------------------------|
| `kafkastore.bootstrap.servers`             | Kafka bootstrap servers (from the Strimzi listener)         |
| `kafkastore.security.protocol`             | `spec.securityprotocol`                                     |
| `listeners`                                | REST listener (`http`/`https` based on `spec.securehttp`)   |
| `host.name`                                | Pod IP                                                      |
| `kafkastore.topic`                         | `spec.kafkastoretopic`                                      |
| `schema.registry.group.id`                 | `spec.schemaregistrygroupid`                                |
| `master.eligibility`                       | `spec.mastereligibility`                                    |
| `schema.compatibility.level`               | `spec.compatibilitylevel`                                   |
| `heap.opts`                                | `spec.heapopts`                                             |
| `schema.registry.inter.instance.protocol`  | Derived from `spec.securehttp`                              |
| `kafkastore.ssl.*`                         | KafkaStore TLS keystore/truststore                          |
| `kafkastore.sasl.*`                        | KafkaStore SASL (SCRAM) credentials                         |
| `ssl.keystore.*`                           | REST API TLS keystore                                       |
| `ssl.key.password`                         | REST API TLS key password                                   |

### BASIC/RBAC authentication on the REST API

REST API authentication is not exposed as a dedicated spec field. Instead, combine
the `configuration` escape hatch with a JAAS configuration mounted through
`spec.template`. For example, to enable HTTP Basic authentication:

```yaml
spec:
  securehttp: true
  configuration:
    authentication.method: BASIC
    authentication.realm: SchemaRegistry-Props
    authentication.roles: admins,developers
  template:
    spec:
      containers:
        - name: confluent-sr
          image: confluentinc/cp-schema-registry:7.6.5
          env:
            - name: SCHEMA_REGISTRY_AUTHENTICATION_JAAS_CONFIG
              value: /etc/schema-registry/auth/jaas.conf
          volumeMounts:
            - name: auth-jaas
              mountPath: /etc/schema-registry/auth
              readOnly: true
      volumes:
        - name: auth-jaas
          secret:
            secretName: schema-registry-basic-auth
```

Create the `schema-registry-basic-auth` secret containing the JAAS configuration
file (for example `jaas.conf`). For more details see the Confluent documentation on
[Schema Registry authentication](https://docs.confluent.io/platform/current/schema-registry/security/index.html#authentication)
and [authentication.roles](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#authentication-roles).

### In detail: listener configuration

The `spec.listener` field in the `StrimziSchemaRegistry` resource specifies the Kafka broker listener that the Schema Registry uses.
These listeners are configured in the `Kafka` resource you created with Strimzi.

Consider a `Kafka` resource:

```yaml
apiVersion: kafka.strimzi.io/v1
kind: Kafka
metadata:
  name: my-cluster
spec:
  kafka:
    #...
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false
      - name: tls
        port: 9093
        type: internal
        tls: true
        authentication:
          type: tls
      - name: external1
        port: 9094
        type: route
        tls: true
      - name: external2
        port: 9095
        type: ingress
        tls: true
        authentication:
          type: tls
        configuration:
          bootstrap:
            host: bootstrap.myingress.com
          brokers:
          - broker: 0
            host: broker-0.myingress.com
          - broker: 1
            host: broker-1.myingress.com
          - broker: 2
            host: broker-2.myingress.com
    #...
```

To use the encrypted internal listener, the `spec.listener` field in your `StrimziSchemaRegistry` resource should be `tls`:

```yaml
apiVersion: strimziregistryoperator.randsw.code/v1alpha1
kind: StrimziSchemaRegistry
metadata:
  name: confluent-schema-registry
spec:
  listener: tls
```

To use the unencrypted internal listener instead, the `spec.listener` field in your `StrimziSchemaRegistry` resource should be `plain` instead:

```yaml
apiVersion: strimziregistryoperator.randsw.code/v1alpha1
kind: StrimziSchemaRegistry
metadata:
  name: confluent-schema-registry
spec:
  listener: plain
```

## 7. Example

You can find example of using the schema registry in my repo - `https://github.com/Randsw/strimzi-kafka-cluster`

## 8. Special thanks

I want to thank all the people who worked on  [lsst-sqre/strimzi-registry-operator](https://github.com/lsst-sqre/strimzi-registry-operator)
You create a great instrument!

## 9. Conclusion

There are many Schema Registry functionalities that not implemented in this operator because i don't need them. If you need some of that functionality create an issue and i see what i can do
