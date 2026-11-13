# ssr-operator Helm chart

Deploys the **Schema Registry operator** (`strimzi-schema-registry-operator`)
for Strimzi Kafka.

## What this chart deploys — and what it does not

This chart deploys the **operator only**. The operator image is
`ghcr.io/randsw/strimzi-schema-registry-operator` (see
[`values.yaml`](values.yaml)); the chart contains **no** Confluent Schema
Registry image and creates no Schema Registry workload itself.

The Schema Registry **image is supplied by the user** through
`spec.template` of the `StrimziSchemaRegistry` custom resource, e.g.:

```yaml
apiVersion: strimziregistryoperator.randsw.code/v1alpha1
kind: StrimziSchemaRegistry
metadata:
  name: confluent-schema-registry
  labels:
    strimzi.io/cluster: kafka-cluster
spec:
  template:
    spec:
      containers:
        - name: confluent-sr
          image: confluentinc/cp-schema-registry:8.4.1
```

After installing the chart, create a `StrimziSchemaRegistry` CR pointing at a
Strimzi Kafka cluster (see the
[operator README](../../README.md) for the full CR reference, examples and
supported versions).

## What the chart installs

- The operator `Deployment` (image `ghcr.io/randsw/strimzi-schema-registry-operator`).
- The `StrimziSchemaRegistry` CRD.
- RBAC for the operator (see the scoping option below).
- A leader-election `Role`/`RoleBinding` in the release namespace.
- Optionally a metrics `Service` (`metrics.enabled`, default `true`).

## CRD lifecycle

The CRD lives in the chart's [`crds/`](crds/) directory, not under
`templates/`. Helm installs files from `crds/` before rendering templates and
never upgrades or deletes them: uninstalling or upgrading the release leaves
the CRD — and every `StrimziSchemaRegistry` custom resource — in place.

- **Uninstall is safe.** `helm uninstall` removes the operator Deployment,
  RBAC and Service, but the CRD and all CRs survive. Delete the CRD manually
  (`kubectl delete crd strimzischemaregistries.strimziregistryoperator.randsw.code`)
  only when you also want to remove every registry CR cluster-wide.
- **CRD schema upgrades are manual.** When a chart release ships a newer CRD
  schema, apply it yourself before upgrading the operator:

  ```console
  kubectl apply -f helm-chart/ssr-operator/crds/strimziregistryoperator.randsw.code_strimzischemaregistries.yaml
  ```

- **Migrating from chart versions that shipped the CRD under `templates/`**
  (chart ≤ 0.0.30): on those releases the CRD was a release resource, so
  `helm uninstall` deleted the CRD and all CRs. To move an existing release
  to the new chart safely:

  1. Back up every `StrimziSchemaRegistry` CR:
     `kubectl get strimzischemaregistries -A -o yaml > cr-backup.yaml`
  2. `helm uninstall <release>` (this deletes the old CRD and the CRs).
  3. Install the new chart release (it creates the CRD from `crds/`).
  4. Re-apply the CR backup: `kubectl apply -f cr-backup.yaml`. The operator
     recreates its owned Deployments, Services and secrets from the CRs.

  Alternatively, to avoid any CR downtime window, keep the old release until
  you have re-applied the CRD from the new chart with
  `kubectl apply --server-side --field-manager=<something-other-than-helm>`,
  confirmed the CRD no longer belongs to the release
  (`helm get manifest <release>` must not list it), and only then upgrade.

## RBAC scope

By default the operator runs **cluster-scoped**: the manager RBAC is rendered
as `ClusterRole`/`ClusterRoleBinding` and the operator watches every
namespace.

Set `rbac.namespaceScoped=true` to run the operator **namespace-scoped**:

- the manager RBAC is rendered as a namespaced `Role`/`RoleBinding` in the
  release namespace, and
- the operator watches only its own namespace (the chart injects a
  `WATCH_NAMESPACE` environment variable, which the operator maps onto its
  manager cache).

The proxy role (`TokenReview`/`SubjectAccessReview`) and the metrics-reader
role (non-resource `/metrics` access) stay cluster-scoped in both modes:
those operations are not namespaced, so a namespaced `Role` could not
authorize them.

```console
helm install ssr-operator ./helm-chart/ssr-operator --set rbac.namespaceScoped=true
```

In namespace-scoped mode, `StrimziSchemaRegistry` CRs (and the Strimzi Kafka
cluster they reference) must live in the release namespace.

## Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Operator replicas. |
| `image.repository` | `ghcr.io/randsw/strimzi-schema-registry-operator` | Operator image. |
| `image.tag` | *(unset)* | Operator image tag. Defaults to the chart `appVersion` (the operator version the chart was released with); override only to pin a different build, and avoid mutable tags such as `latest`. |
| `metrics.enabled` | `true` | Create a `Service` for the operator metrics endpoint. |
| `metrics.secure` | `false` | Serve metrics over HTTPS with authn/authz. |
| `metrics.port` | `8080` | Port the metrics endpoint binds to. |
| `rbac.namespaceScoped` | `false` | `false`: cluster-wide `ClusterRole`/`ClusterRoleBinding`, watch all namespaces. `true`: namespaced `Role`/`RoleBinding` in the release namespace, watch only that namespace. |
| `serviceAccount.create` / `name` | `true` / `strimzi-schema-registry-operator` | Service account for the operator. |
| `cluster_roles` / `roles` | see `values.yaml` | Data-driven RBAC rule sets rendered by the chart. |
