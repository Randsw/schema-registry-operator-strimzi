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

[Confluent Schema Registry](https://docs.confluent.io/platform/current/schema-registry/index.html) provides a serving layer for your metadata. It provides a RESTful interface for storing and retrieving your Avro®, JSON Schema, and Protobuf schemas.

It is an application that resides outside your Kafka cluster and handles the distribution of schemas to producers and consumers by storing a copy of the schema in its storage.

Confluent Schema Registry well integrated with Confluent Platform and Confluent Cloud. But not with Strimzi.

One of the instruments that integrates Confluent Schema Registry with a Strimzi Kafka cluster is [strimzi-registry-operator](https://github.com/lsst-sqre/strimzi-registry-operator). However, unfortunately, it is no longer maintained (the last commit was on Dec 21, 2022) and has several issues, like broken Kubernetes RBAC and only HTTP access to the Schema Registry REST API endpoint. Therefore I decided to rewrite it, focusing on using the Operator SDK in Golang and add some missing functionality.

## 2. Overview

***

- Once you deploy a `StrimziSchemaRegistry` resource, the operator creates a Kubernetes deployment of the Confluent Schema Registry, along with an associated Kubernetes service and secret.
- Works with Strimzi's TLS authentication and authorization by converting the TLS certificate associated with a KafkaUser into a JKS-formatted keystore and truststore that's used by Confluent Schema Registry.
- When Strimzi updates either the Kafka cluster's CA certificate or the KafkaUser's client certificates, the operator automatically recreates the JKS truststore/keystore secrets and triggers a rolling restart of the Schema Registry pods.
- Changes to the Strimzi `Kafka` CR (e.g. listener or bootstrap address changes) and to the `KafkaUser` CR (e.g. authentication type changes) are watched and trigger reconciliation automatically.
- You can add TLS support to the Schema Registry REST API endpoint by providing your own certificate or letting the operator create one for you, signed by the Kafka Cluster CA certificate. Operator-generated REST API certificates are renewed automatically before they expire (see [`tlssecretName`](#tlsecretname)).

### Supported versions

The operator deploys the Confluent Schema Registry image you specify in `spec.template` of the `StrimziSchemaRegistry` CR — it does not pin or override the image itself.

| Tested Schema Registry image (`confluentinc/cp-schema-registry`) | Notes |
|---|---|
| 7.6.x | Legacy Confluent Platform 7.x line |
| 8.4.x | Current recommended line (samples and tests use `8.4.1`) |

> **Caveat for CP 8.x:** Schema Registry 8.x requires Kafka broker protocol compatibility (Kafka 3.x+). Recent Strimzi versions are fine; if your Strimzi Kafka cluster runs an older broker, upgrade it (or stay on a 7.6.x Schema Registry image) before moving the Schema Registry to 8.x.

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
          image: confluentinc/cp-schema-registry:8.4.1
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
          image: confluentinc/cp-schema-registry:8.4.1
          imagePullPolicy: IfNotPresent
      restartPolicy: Always
```

### Schema Registry-related configurations

- `listener` is the **name** of the Kafka listener that the Schema Registry should use.
  You should set this value based on your `Kafka` resource.
  The ["In-detail: listener configuration"](#in-detail-listener-configuration) section, below, explains this in more detail.
  See also: Schema Registry [listeners](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#listeners) docs.

- `securityprotocol` is the security protocol for the Schema Registry to communicate with Kafka. Default is `SSL`. The authentication method is auto-detected from the referenced `KafkaUser` (`spec.authentication.type`). Supported values:
  
  - `SSL` — TLS transport with mTLS (client certificates). Requires a `KafkaUser` with `authentication.type: tls`. The operator builds a keystore and truststore from the KafkaUser secret.
  - `SASL_SSL` — TLS transport with SCRAM-SHA-512 authentication. Requires a `KafkaUser` with `authentication.type: scram-sha-512`. The operator builds only a truststore and reads the SCRAM credentials directly from the KafkaUser secret.
  - `SASL_PLAINTEXT` — plaintext transport with SCRAM-SHA-512 authentication. Requires a `KafkaUser` with `authentication.type: scram-sha-512`. No certificates or keystore are required.
  - `PLAINTEXT` — plaintext transport with no authentication. No `KafkaUser` secret or certificates are required.

  See also: Schema Registry [kafkastore.security.protocol](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#kafkastore-security-protocol) docs.

  > **Note**
  > When building keystores and truststores, the operator passes the generated
  > passwords to the `keytool`/`openssl` child processes through the process
  > environment, never as command-line arguments, so they are not readable from
  > `/proc/<pid>/cmdline` while the tools run.

- `compatibilityLevel` is the default schema compatibility level. Possible values:

  - `none`
  - `backward`
  - `backward_transitive`
  - `forward`
  - `forward_transitive`
  - `full`
  - `full_transitive`

  > **Note**
  > The operator deliberately defaults `compatibilitylevel` to `forward`. This
  > diverges from Confluent's own default of `backward`; set `compatibilitylevel`
  > explicitly if you need Confluent's default behavior.

  See also: Schema Registry [schema.compatibility.level](https://docs.confluent.io/platform/current/schema-registry/installation/config.html#schema-compatibility-level) docs.

- `securehttp` enable TLS on Schema Registry REST API endpoint.
  If `securehttp` is disabled the associated service points to `8081` port in Schema Registry pod. If enabled - to `8085` port.
- `clientauth` enables mutual TLS (mTLS) on the Schema Registry REST API endpoint. Default is `false`.
  It is only valid when `securehttp` is `true`: setting it without `securehttp` marks the CR
  `Ready=False` with reason `ClientAuthRequiresSecureHTTP` (status `InvalidSpec`) and skips reconciliation
  entirely, announced by `ClientAuthRequiresSecureHTTP` and `ReconciliationSkipped` Warning events.
  **Note:** while the combination is invalid, a previously created Deployment keeps running with its current
  configuration and is no longer reconciled — fix the spec to resume reconciliation. When enabled, clients must
  present a certificate signed by the Kafka cluster CA. The operator
  reuses the cluster-CA truststore and sets `SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION=REQUIRED` (the
  non-deprecated `ssl.client.authentication` property) together with
  `SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION` and `SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD`. For
  `PLAINTEXT`/`SASL_PLAINTEXT` security protocols (which do not otherwise need a truststore), the operator
  builds a truststore-only secret from the cluster CA certificate for this purpose.
- `tlssecretName` is name of secret that contains the TLS certificate and private key pair for the Schema Registry
  REST API. Must be in the same namespace as the `StrimziSchemaRegistry` CR.
  If this field is omitted or an empty string, the SSR operator automatically creates a TLS certificate and private key pair in JKS format, signs them with the Strimzi Kafka cluster CA(stored in the `<kafka-clustername>-cluster-ca` and `<kafka-clustername>-cluster-ca-cert` secrets)
  and mounts the secret to the Schema Registry pod.
  Operator-generated certificates are valid for one year and are renewed automatically when fewer than 30 days remain
  before expiry: the secret is updated in place, a `TLSCertificateRenewed` event is emitted and the Schema Registry
  pods are rolled so the new keystore is loaded. Certificates you provide yourself via `tlssecretName` are never
  touched by the operator.
  If the operator-generated `<name>-tls` secret or the operator-managed `<name>-jks` secret is deleted manually,
  the operator recreates it automatically on the next reconciliation and rolls the Schema Registry pods, so a
  deleted secret no longer breaks the next pod restart.

  The secret must match one of the following three shapes (all keys with non-empty values):

  1. **JKS keystore** (the default contract, used when `keystoretype` is `JKS` or unset):

     |Key               |Value                                            |
     |------------------|-------------------------------------------------|
     |tls-keystore.jks  |JKS keystore with the certificate and private key|
     |keystore_password |Keystore password                                |
     |key_password      |Private key password                             |

  2. **PKCS12 keystore** (when `keystoretype` is `PKCS12`):

     |Key               |Value                                            |
     |------------------|-------------------------------------------------|
     |tls-keystore.p12  |PKCS12 keystore with the certificate and key     |
     |keystore_password |Keystore password                                |
     |key_password      |Private key password                             |

  3. **Standard `kubernetes.io/tls` secret** (PEM, as issued by cert-manager):

     |Key    |Value                                                        |
     |-------|-------------------------------------------------------------|
     |tls.crt|PEM-encoded certificate (required)                           |
     |tls.key|PEM-encoded private key (required)                           |
     |ca.crt |PEM-encoded issuing CA (optional)                            |

  The operator validates the secret before rendering the Deployment. A missing secret, a shape mismatch, or
  missing/empty keys set `Ready=False` with reason `InvalidTLSSecret` on the CR status and emit a `Warning` event;
  no Deployment is created until the secret is fixed, and reconciliation resumes automatically once the secret is
  valid. Rotating the secret (any update that changes its `resourceVersion`) is detected by the operator and the
  Schema Registry pods are rolled automatically so the JVM reloads the keystore.

  Create a JKS-keystore secret with `kubectl`:

  ```bash
  kubectl create secret generic schema-registry-tls \
    --from-file=tls-keystore.jks=/path/to/tls-keystore.jks \
    --from-literal=keystore_password='<keystore-password>' \
    --from-literal=key_password='<key-password>'
  ```

  **cert-manager note:** a plain `kubernetes.io/tls` secret (shape 3) now works without re-packing. The operator
  converts the PEM certificate and key into a JKS keystore itself and stores the result in an operator-managed
  **derived secret** named `<tlssecretname>-jks`, which is what the Schema Registry pod mounts. The derived secret
  is regenerated automatically whenever the user secret changes (tracked via the user secret's `resourceVersion`),
  and the pod rollout keeps following the *user* secret's `resourceVersion`, so a cert-manager renewal rolls the
  pods exactly once. The name `<tlssecretname>-jks` must not collide with the operator-managed `<name>-jks` or
  `<name>-tls` secrets (i.e. `tlssecretname` must differ from the CR name); otherwise validation fails with
  `InvalidTLSSecret`. If you prefer to manage the keystore yourself, re-pack cert-manager's output into the JKS
  contract instead:

  ```bash
  kubectl get secret <cert-manager-secret> -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/tls.crt
  kubectl get secret <cert-manager-secret> -o jsonpath='{.data.tls\.key}' | base64 -d > /tmp/tls.key
  openssl pkcs12 -export -in /tmp/tls.crt -inkey /tmp/tls.key -name schema-registry \
    -passout pass:<keystore-password> -out /tmp/keystore.p12
  keytool -importkeystore -srckeystore /tmp/keystore.p12 -srcstoretype PKCS12 -srcstorepass <keystore-password> \
    -destkeystore /tmp/tls-keystore.jks -deststoretype JKS -deststorepass <keystore-password> -noprompt
  kubectl create secret generic schema-registry-tls \
    --from-file=tls-keystore.jks=/tmp/tls-keystore.jks \
    --from-literal=keystore_password='<keystore-password>' \
    --from-literal=key_password='<keystore-password>'
  ```

  [Java Generate Keys Tutorial](https://docs.oracle.com/javase/tutorial/security/toolsign/step3.html)

- `keystoretype` selects the keystore format of the user-provided TLS secret (`tlssecretName`). Valid values:
  `JKS` (the default, including when unset) and `PKCS12`. With `PKCS12` the secret must carry the keystore under
  the `tls-keystore.p12` key (shape 2 above) and the operator sets `SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE=PKCS12` with
  `ssl.keystore.location=/var/rest-api-tls/tls-keystore.p12`; with `JKS` the current behavior is unchanged
  (`tls-keystore.jks`, Confluent's built-in default type). The field has no effect for standard
  `kubernetes.io/tls` (PEM) secrets, which are always converted to JKS, and no effect when `tlssecretName` is
  empty (the operator-managed secret is always JKS).

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

  The operator **merges** your template with its own managed values instead of
  overwriting them. Custom container `env` vars, `volumeMounts`, pod `volumes`,
  pod `labels` and pod `annotations` are preserved; the operator-managed values
  always win on identity conflicts:

  | Field            | Conflict key                     | Winner                                   |
  |------------------|----------------------------------|------------------------------------------|
  | `env`            | variable name                    | operator                                 |
  | `volumes`        | volume name                      | operator                                 |
  | `volumeMounts`   | mount name or mount path         | operator                                 |
  | pod `labels`     | label key                        | operator                                 |
  | pod `annotations`| annotation key                   | operator rollout markers                 |

  User-only entries are appended after the operator's in a deterministic order.
  The `Deployment` selector continues to match only the operator's labels, so
  user labels never affect pod selection.

- `service.type` is the Kubernetes Service type. Valid values: `ClusterIP` (default), `NodePort`, `LoadBalancer`.
- `service.port` overrides the Service port. Defaults to `80` for plain HTTP and `443` for TLS REST endpoints.
- `service.nodeport` pins the node port when `service.type` is `NodePort` or `LoadBalancer`. When unset,
  Kubernetes allocates one automatically.
- `service.annotations` and `service.labels` are passed through to the managed Kubernetes Service.
  They are useful for exposing custom metadata such as Prometheus scraping annotations (`prometheus.io/scrape`),
  without editing the Service by hand. The operator always preserves its own `type` annotation and merges
  the CR labels into the Service.

  ```yaml
  spec:
    # ...
    service:
      type: LoadBalancer
      port: 8443
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8081"
      labels:
        monitoring.example.com/team: schemas
  ```

- `networkpolicy.enabled` (default `false`) creates a `NetworkPolicy` for the Schema Registry pods.
  When enabled, the policy allows ingress on the Schema Registry listener port from within the namespace
  and egress to the Kafka bootstrap service (plus DNS resolution via kube-dns). Enable it to restrict pod
  traffic in namespaces that enforce a default-deny policy. The DNS egress rule targets the standard
  kube-dns pods (`k8s-app=kube-dns`) in the `kube-system` namespace; clusters running a custom DNS service
  (different namespace or pod labels) need the policy adjusted manually.
- `networkpolicy.allowcrossnamespaceingress` (default `false`) allows ingress to the Schema Registry from
  all namespaces instead of only the namespace of the `StrimziSchemaRegistry` resource. Only meaningful
  when `networkpolicy.enabled` is `true`.

  ```yaml
  spec:
    # ...
    networkpolicy:
      enabled: true
      allowcrossnamespaceingress: true
  ```

### CR status

The operator reports the CR as `Ready=True` (`status.status: "Ok"`) only once the Schema Registry Deployment
has converged on its desired replica count: exactly the desired number of replicas exist and all of them are
ready. While the Deployment is starting, unavailable, or still converging (e.g. during a scale-down, while
surplus replicas terminate), the CR shows `Ready=False` with reason `NotReady`.

`status.status` is one of:

| Value             | Meaning                                                                    |
|-------------------|----------------------------------------------------------------------------|
| `Ok`              | The Deployment is ready.                                                    |
| `NotReady`        | The Deployment is starting, unavailable or not yet converged.               |
| `InvalidSpec`     | The spec combination is invalid (e.g. `clientauth` without `securehttp`).   |
| `InvalidTLSSecret`| The user-provided TLS secret is missing or does not match the contract.     |
| `ReconcileError`  | The last reconciliation failed; the `Ready` condition message has the cause.|

Upgrading the operator may trigger a one-time rollout of existing Schema Registry deployments even when the
spec is unchanged: the spec hash recorded in the `strimziregistryoperator.randsw.code/specHash` annotation
was upgraded from a 32-bit to a 64-bit FNV digest, rewriting every stored value once.

### Behavior changes

- Operator-managed REST API TLS certificates (`securehttp: true` without
  `tlssecretname`) are now re-checked periodically (every 12 hours, sooner when
  the renewal window opens sooner) even when no Kubernetes event fires, so they
  renew reliably instead of expiring silently.
- `ConfigKeyOverridden` Warning events for reserved `spec.configuration` keys
  are emitted only when the Deployment spec changes, not on every reconcile.
- Custom `env`, `volumeMounts`, `volumes`, pod labels and annotations supplied
  in `spec.template` are merged into the Deployment instead of being discarded
  (see the `template` bullet above).
- Stale JKS secrets are updated in place during rotation; they are no longer
  deleted and recreated, closing the window in which restarting pods could not
  mount them.

Phase 3 fixes:

- Manual `kubectl scale` edits to the Deployment are preserved: a spec change
  no longer resets the replica count to 1 (the CR exposes no `replicas` field,
  so the operator never desires a specific count).
- Reconcile errors (missing KafkaUser, missing bootstrap listener, missing
  cluster CA secret, ...) are surfaced on the CR as `Ready=False` with reason
  `ReconcileError` and the error message, instead of leaving the `Ready`
  condition stale while reconciliation retries with backoff.
- The `Ready` condition now converges on the Deployment's desired replica
  count, so a scale-down in progress no longer reports `Ready` prematurely.
  `status.status` is validated against an enum (`Ok`, `NotReady`,
  `InvalidSpec`, `InvalidTLSSecret`, `ReconcileError`).
- NetworkPolicy annotations set by users are preserved on updates instead of
  being wiped.
- The bootstrap server lookup error is actionable: it names the requested
  listener, the Kafka cluster and the CR, and lists the listeners the Kafka CR
  exposes.
- Secret watch events are pre-filtered: events for operator-owned secrets
  (`<name>-jks`, `<name>-tls`, derived `<tlssecretname>-jks`) no longer
  trigger CR list calls, and cluster CA cert events skip the
  `spec.tlssecretname` lookup.
- The instance-count metric is now correctly spelled
  `strimzi_schema_registry_instance_current_count`; the old misspelled
  `strimzi_schema_register_instance_current_count` metric was **removed** —
  update dashboards and alerts (see [Operator metrics](#operator-metrics)).
- REST API certificates are back-dated by a 5 minute clock-skew margin
  (`NotBefore`), so clients with slightly fast clocks do not reject fresh
  certificates. The RSA key size for generated certificates is a
  package-level variable in the operator source (`certprocessor.rsaKeyBits`,
  default 2048 for fast renewals) instead of a hardcoded 4096.
- Generated certificates no longer carry the non-standard `<cn>.svc.cluster`
  DNS SAN.
- The Schema Registry listener port (8081/http or 8085/https) is now declared
  in the container spec.

### Operator metrics

The operator exposes the following Prometheus metrics on its metrics endpoint:

| Metric                                              | Type    | Description                                             |
|-----------------------------------------------------|---------|---------------------------------------------------------|
| `strimzi_schema_registry_instance_current_count`    | Gauge   | Current number of StrimziSchemaRegistry instances.      |
| `strimzi_schema_registry_reconcile_errors_total`    | Counter | Total number of reconciliation errors.                  |
| `strimzi_schema_registry_secret_rotation_total`     | Counter | Total number of secret rotation events.                 |
| `strimzi_schema_registry_deployment_update_total`   | Counter | Total number of deployment updates.                     |

The former misspelled instance-count metric
`strimzi_schema_register_instance_current_count` was removed in this release;
update dashboards and alerts to
`strimzi_schema_registry_instance_current_count`.

### Single replica support

Only a **single replica** is supported. Running multiple replicas with `securehttp: true` is not supported
because the generated REST TLS certificate SANs are DNS-only (the Service DNS name) while the Schema Registry
`host.name` is set to the pod IP, so inter-instance HTTPS forwarding between replicas breaks.
This limit is structurally enforced: the CRD exposes no `replicas` field and the operator always renders a
single-replica Deployment.

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
`spec.configuration` (their managed value wins). Each overridden key is logged,
and surfaced as a `Warning` event (`ConfigKeyOverridden`) on the CR when the key
is first applied and again whenever the Deployment spec changes — steady-state
reconciles do not repeat the events; reconciliation still succeeds.

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
| `inter.instance.protocol`                  | Derived from `spec.securehttp` (`https` when enabled)       |
| `schema.registry.inter.instance.protocol`  | Deprecated spelling, kept reserved until a release note     |
| `ssl.client.authentication`                | `REQUIRED` when `spec.clientauth` is enabled                |
| `kafkastore.ssl.*`                         | KafkaStore TLS keystore/truststore                          |
| `kafkastore.sasl.*`                        | KafkaStore SASL (SCRAM) credentials                         |
| `ssl.keystore.*` (incl. `ssl.keystore.type`)| REST API TLS keystore (type follows `spec.keystoretype`)    |
| `ssl.key.password`                         | REST API TLS key password                                   |

`spec.heapopts` accepts only plain digit heap sizes (e.g. `-Xms512M -Xmx512M`); hex-letter sizes such as
`12a` are rejected by CRD validation.

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
          image: confluentinc/cp-schema-registry:8.4.1
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

## 7. End-to-end tests

The operator has end-to-end tests that run in GitHub Actions on kind clusters
as part of the `test-release.yaml` workflow. They run when a pull request is
opened or updated and when a branch is merged/pushed to `main`. Each
`securityprotocol` (`SSL`, `SASL_SSL`, `PLAINTEXT`, `SASL_PLAINTEXT`) runs in
its own kind cluster with a scaled-down
Kafka (1 KRaft controller + 1 broker, replication factor 1) and only the
listener it needs. Inside each job the Schema Registry is tested first with
`clientauth: false` and then with `clientauth: true` (mutual TLS on the REST
API), plus the `clientauth`-without-`securehttp` validation path.

The verification is a REST round-trip (list subjects, register a JSON schema,
fetch it back, check the compatibility level). Because schemas are persisted
to the `registry-schemas` Kafka topic, a successful round-trip proves the
Kafka connection works with the scenario's security protocol.

To run a single scenario locally against an existing kind cluster:

```bash
# 1. Create a kind cluster and deploy the scenario infrastructure
#    (Strimzi operator, Kafka with the scenario listener, topic, KafkaUser)
kind create cluster --name e2e --config test/e2e/kind-config.yaml
./test/e2e/scripts/setup-cluster.sh ssl   # or sasl-ssl, plaintext, sasl-plaintext

# 2. Build the operator image, load it into kind and install the helm chart
docker build -t ssr-operator:e2e .
kind load docker-image ssr-operator:e2e --name e2e
helm install --wait --timeout 10m --namespace kafka --create-namespace \
  ssr-operator ./helm-chart/ssr-operator \
  --set image.repository=ssr-operator --set image.tag=e2e \
  --set image.pullPolicy=IfNotPresent

# 3. Run the tests (E2E_SECURITY_PROTOCOL: SSL, SASL_SSL, PLAINTEXT, SASL_PLAINTEXT)
E2E_SECURITY_PROTOCOL=SSL E2E_LISTENER=tls make e2e
```

## 8. Example

You can find example of using the schema registry in my repo - `https://github.com/Randsw/strimzi-kafka-cluster`

## 9. Special thanks

I want to thank all the people who worked on  [lsst-sqre/strimzi-registry-operator](https://github.com/lsst-sqre/strimzi-registry-operator)
You create a great instrument!

## 10. Conclusion

There are many Schema Registry functionalities that not implemented in this operator because i don't need them. If you need some of that functionality create an issue and i see what i can do
