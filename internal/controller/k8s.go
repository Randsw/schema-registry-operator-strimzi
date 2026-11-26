package controller

import (
	"context"
	"encoding/json"
	go_err "errors"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resource name suffixes and fixed names used throughout the operator.
const (
	jksSecretSuffix     = "-jks"
	tlsSecretSuffix     = "-tls"
	deploySuffix        = "-deploy"
	clusterCASuffix     = "-cluster-ca-cert"
	clusterCAKeySuffix  = "-cluster-ca"
	networkPolicySuffix = "-network-policy"

	// operatorAppLabelKey/operatorAppLabelValue label every secret the
	// operator creates. The orphaned-secret GC selects on this label so it
	// never lists (and never has to reason about) the far larger set of
	// unrelated secrets in the namespace.
	operatorAppLabelKey   = "app"
	operatorAppLabelValue = "strimzi-schema-registry"

	// REST API TLS volume and env names, shared by the deployment builders
	// and the user TLS secret mount fixup.
	restAPITLSVolumeName   = "rest-api-tls"
	sslKeystorePasswordEnv = "SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD"
	sslKeyPasswordEnv      = "SCHEMA_REGISTRY_SSL_KEY_PASSWORD"

	// bootstrapServersEnv is the operator-managed env var carrying the Kafka
	// bootstrap address. Its value comes from the Kafka CR's status, not from
	// the CR spec, so it is invisible to computeSpecHash and must be compared
	// separately when detecting deployment drift.
	bootstrapServersEnv = "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS"

	// DNS egress peer assumed by the rendered NetworkPolicy: the standard
	// kube-dns pods in the kube-system namespace. Clusters running a custom
	// DNS service (different namespace or pod labels, e.g. some CoreDNS
	// deployments) need the policy adjusted — this assumption is documented
	// in the README NetworkPolicy section.
	dnsNamespaceLabel = "kubernetes.io/metadata.name"
	dnsNamespaceName  = "kube-system"
	dnsPodLabel       = "k8s-app"
	dnsPodValue       = "kube-dns"
)

// selectorLabelsForStrimziSchemaRegistryOperator returns the stable identity
// label subset used as the Deployment selector. The Deployment selector field
// is immutable at the API server, so it must never contain labels that can
// change over the lifetime of the resource: app.kubernetes.io/version is
// derived from the container image tag and strimzi.io/cluster from the CR
// label, so both are excluded (they remain on the Deployment and pod labels,
// which are mutable). The returned subset is always contained in
// labelsForStrimziSchemaRegistryOperator, so the selector keeps matching the
// pods.
func selectorLabelsForStrimziSchemaRegistryOperator(name string) map[string]string {
	return map[string]string{
		"app":                          name,
		"strimzi-schema-registry":      name,
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/name":       "strimzischemaregistry",
		"app.kubernetes.io/managed-by": "strimzi-registry-operator",
		"app.kubernetes.io/part-of":    name,
	}
}

// maxLabelValueLength is the Kubernetes limit for label values: a valid
// label value must be 63 characters or fewer.
const maxLabelValueLength = 63

// imageVersionLabel derives the app.kubernetes.io/version label value from
// the container image reference: the substring after the last ':' when it is
// a tag rather than part of a registry host, "latest" when the image carries
// no tag. A digest-pinned image (image@sha256:<64 hex chars>) yields a
// 64-character value that exceeds the 63-character Kubernetes label limit,
// so the digest is truncated (F3). The label is no longer part of the
// Deployment selector (F1) but remains a pod label and must stay valid.
func imageVersionLabel(image string) string {
	parts := strings.Split(image, ":")
	if len(parts) > 1 {
		last := parts[len(parts)-1]
		if !strings.Contains(last, "/") {
			if len(last) > maxLabelValueLength {
				return last[:maxLabelValueLength]
			}
			return last
		}
	}
	return "latest"
}

// labelsForStrimziSchemaRegistryOperator returns the standard set of labels
// for resources managed by the Schema Registry operator. It extends the
// stable selector subset with the mutable app.kubernetes.io/version (derived
// from the image tag) and strimzi.io/cluster (from the CR label); those two
// must never be used in a Deployment selector (F1).
func labelsForStrimziSchemaRegistryOperator(name, image, kafkaClusterName string) map[string]string {
	ls := selectorLabelsForStrimziSchemaRegistryOperator(name)
	ls["app.kubernetes.io/version"] = imageVersionLabel(image)
	ls["strimzi.io/cluster"] = kafkaClusterName
	return ls
}

// createDeployment creates a new Deployment along with its dependent secrets.
// It handles both initial deployment creation and secret bootstrapping. For
// spec-only updates where secrets should NOT be recreated, use updateExistingDeployment
// instead (which calls buildDeploymentSpec directly, bypassing secret creation).
func (r *StrimziSchemaRegistryReconciler) createDeployment(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger) (*apps.Deployment, error) {
	logger.Info("Creating a new Deployment", "Deployment.Namespace", instance.Namespace, "Deployment.Name", instance.Name+deploySuffix)

	// Get Kafka Bootstrap Server address
	kafkaBootstrapServer, kafkaClusterName, err := r.getKafkaBootstrapServers(instance, ctx, logger)
	if err != nil {
		logger.Error(err, "Fail to get Kafka bootstrap servers")
		return nil, err
	}

	// Resolve the Kafka auth mode once and reuse it for secret creation and the
	// deployment builders.
	kafkaUser, err := r.getKafkaUser(instance, ctx)
	if err != nil {
		logger.Error(err, "Failed to get KafkaUser")
		return nil, err
	}
	auth, err := resolveKafkaAuth(instance, kafkaUser)
	if err != nil {
		logger.Error(err, "Failed to resolve Kafka auth mode")
		return nil, err
	}

	// Create secret for Kafkastore
	secret, created, err := r.createSecret(instance, ctx, logger, kafkaClusterName, nil, nil, auth)
	if err != nil {
		logger.Error(err, "Failed to format secret", "Secret.Name", instance.Name+jksSecretSuffix)
		return nil, err
	}

	var jksResourceVersion string
	if secret != nil {
		if created {
			// A rebuilt secret may carry the ResourceVersion of a stale
			// existing secret (the Deployment was deleted but the Secret
			// kept): persistJKSSecret updates it in place instead of
			// creating a duplicate (B4).
			err = r.persistJKSSecret(ctx, secret)
			if err != nil {
				logger.Error(err, "Failed to create/update secret", "Secret.Name", instance.Name+jksSecretSuffix)
				return nil, err
			}
			jksResourceVersion = secret.ResourceVersion
			logger.V(1).Info("Secret for Schema Registry KafkaStore TLS created successfully", "Secret.Name", secret.Name)
		} else {
			// createSecret returns the existing up-to-date secret instead of nil.
			jksResourceVersion = secret.ResourceVersion
		}
	}

	// REST API mutual TLS requires a cluster-CA truststore. When the Kafka auth
	// mode does not already produce one (PLAINTEXT/SASL_PLAINTEXT), build it now
	// so the deployment can mount it.
	if clientAuthNeedsTruststoreSecret(instance, auth) {
		clientAuthSecret, _, err := r.ensureClientAuthTruststoreSecret(instance, ctx, logger, kafkaClusterName)
		if err != nil {
			logger.Error(err, "Failed to ensure client-auth truststore secret")
			return nil, err
		}
		if clientAuthSecret != nil {
			jksResourceVersion = clientAuthSecret.ResourceVersion
		}
	}

	// Schema Registry REST API TLS secret
	var TLSSecretName string
	var tlsResourceVersion string
	if instance.Spec.SecureHTTP {
		if instance.Spec.TLSSecretName == "" {
			TLSSecret, exists, err := r.createTLSSecret(instance, ctx, logger, kafkaClusterName)
			if err != nil {
				logger.Error(err, "Failed to format TLS secret")
				return nil, err
			}
			if exists {
				err = r.Update(ctx, TLSSecret)
			} else {
				err = r.Create(ctx, TLSSecret)
			}
			if err != nil {
				logger.Error(err, "Failed to persist TLS secret")
				return nil, err
			}
			logger.Info("Secret for Schema Registry TLS created/updated successfully", "Secret.Name", TLSSecret.Name)
			TLSSecretName = TLSSecret.Name
			// Record the operator-managed REST API TLS secret version so the
			// tlsVersion rollout annotation is present from creation: the
			// annotation-driven renewal check (handleTLSCertExpiry) then
			// compares against a recorded value instead of rolling a freshly
			// created deployment one extra time.
			tlsResourceVersion = TLSSecret.ResourceVersion
		} else {
			TLSSecretName = instance.Spec.TLSSecretName
		}
	}

	// Track the KafkaUser secret version for SASL modes so credential changes
	// trigger a rollout without rebuilding any JKS material.
	saslUserResourceVersion := ""
	if auth.NeedsSASL {
		saslUserResourceVersion, err = r.getSASLUserSecretVersion(instance, ctx, logger)
		if err != nil {
			logger.Error(err, "Failed to get KafkaUser secret version for SASL")
			return nil, err
		}
	}

	// Track the user-provided REST API TLS secret version so rotating the
	// secret (e.g. a cert-manager reissue) triggers a rollout: the Schema
	// Registry JVM reads the JKS keystore once at startup.
	if instance.Spec.SecureHTTP && instance.Spec.TLSSecretName != "" {
		userTLSSecret := &v1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: instance.Namespace}, userTLSSecret); err != nil {
			logger.Error(err, "Failed to get user-provided TLS secret", "Secret.Name", TLSSecretName)
			return nil, err
		}
		tlsResourceVersion = userTLSSecret.ResourceVersion
		// A standard kubernetes.io/tls (PEM) secret is converted to a JKS
		// keystore in an operator-managed derived secret, which is what the
		// pod mounts and references. The tlsVersion rollout annotation keeps
		// tracking the USER secret's resourceVersion.
		if isPEMTLSSecret(userTLSSecret) {
			if err := r.ensureDerivedJKSSecret(ctx, instance, userTLSSecret, logger); err != nil {
				logger.Error(err, "Failed to ensure derived JKS secret for PEM user TLS secret", "Secret.Name", TLSSecretName)
				return nil, err
			}
			TLSSecretName = derivedJKSSecretName(instance)
		}
	}

	dep, configOverridden, err := r.buildDeploymentSpec(instance, kafkaBootstrapServer, kafkaClusterName, jksResourceVersion, saslUserResourceVersion, tlsResourceVersion, TLSSecretName, auth, logger)
	if err != nil {
		logger.Error(err, "Failed to build deployment spec")
		return nil, err
	}
	r.emitConfigOverriddenEvents(instance, configOverridden)
	return dep, nil
}

// buildDeploymentSpec builds the Deployment spec from the CR spec and pre-fetched external inputs.
// It is a pure function that produces the desired deployment — it does NOT create or mutate any
// Kubernetes resources (no side effects). All secret creation and external reads must happen before
// calling this function.
func (r *StrimziSchemaRegistryReconciler) buildDeploymentSpec(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	kafkaBootstrapServer string,
	kafkaClusterName string,
	jksResourceVersion string,
	saslUserResourceVersion string,
	tlsResourceVersion string,
	TLSSecretName string,
	auth kafkaAuth,
	logger logr.Logger,
) (*apps.Deployment, []string, error) {
	// Deep-copy the pod template: instance comes from the controller-runtime
	// cache, and a plain struct copy shares the Containers slice backing array
	// (and resource maps) with the cached CR object. The mutations below (env,
	// resources, probes) would then corrupt the cached object observed by
	// subsequent reconciles.
	podSpec := *instance.Spec.Template.DeepCopy()
	// Validate that at least one container exists
	if len(podSpec.Spec.Containers) == 0 {
		return nil, nil, fmt.Errorf("template must contain at least one container")
	}

	// Build pod environment, volumes, and probes
	podEnv, configOverridden := buildPodEnv(instance, kafkaBootstrapServer, TLSSecretName, auth)
	podVolume, containerVolumeMount := buildPodVolumes(instance, TLSSecretName, auth)

	listenerPort, scheme := listenerPortAndScheme(instance)
	readinessProbe, livenessProbe, startupProbe := buildProbes(listenerPort, scheme, authenticationMethodConfigured(instance.Spec.Configuration))

	podSpec.Spec.Containers[0].ReadinessProbe = readinessProbe
	podSpec.Spec.Containers[0].LivenessProbe = livenessProbe
	podSpec.Spec.Containers[0].StartupProbe = startupProbe

	// Declare the Schema Registry listener port explicitly (B19): the Service
	// targetPort and the probes reference the numeric port, but the pod spec
	// never declared it, leaving the port invisible to tooling
	// (NetworkPolicies, service meshes, kubectl describe). User-declared
	// ports from spec.template are preserved; an entry for the listener port
	// itself is replaced by the operator-managed one.
	portName := "http"
	if scheme == v1.URISchemeHTTPS {
		portName = "https"
	}
	listenerPorts := []v1.ContainerPort{{
		Name:          portName,
		ContainerPort: listenerPort,
		Protocol:      v1.ProtocolTCP,
	}}
	podSpec.Spec.Containers[0].Ports = mergeContainerPorts(listenerPorts, podSpec.Spec.Containers[0].Ports)

	resources := &podSpec.Spec.Containers[0].Resources
	if resources.Requests == nil {
		resources.Requests = v1.ResourceList{}
	}
	if _, ok := resources.Requests[v1.ResourceCPU]; !ok {
		resources.Requests[v1.ResourceCPU] = mustParseQuantity("250m")
	}
	if _, ok := resources.Requests[v1.ResourceMemory]; !ok {
		resources.Requests[v1.ResourceMemory] = mustParseQuantity("512Mi")
	}
	if resources.Limits == nil {
		resources.Limits = v1.ResourceList{}
	}
	if _, ok := resources.Limits[v1.ResourceCPU]; !ok {
		resources.Limits[v1.ResourceCPU] = mustParseQuantity("1000m")
	}
	if _, ok := resources.Limits[v1.ResourceMemory]; !ok {
		resources.Limits[v1.ResourceMemory] = mustParseQuantity("1Gi")
	}
	// Merge the operator-managed values with whatever the user supplied in
	// spec.template so custom env vars, volumes, mounts, labels and
	// annotations survive reconciliation instead of being discarded. The
	// operator always wins on identity conflicts (env name, volume name,
	// mount path, label/annotation key); user extras are appended/preserved.
	podSpec.Spec.Containers[0].Env = mergeEnvVars(podEnv, podSpec.Spec.Containers[0].Env)
	podSpec.Spec.Containers[0].VolumeMounts = mergeVolumeMounts(containerVolumeMount, podSpec.Spec.Containers[0].VolumeMounts)
	podSpec.Spec.Volumes = mergeVolumes(podVolume, podSpec.Spec.Volumes)
	ls := labelsForStrimziSchemaRegistryOperator(instance.Name, instance.Spec.Template.Spec.Containers[0].Image, kafkaClusterName)
	// Operator labels override user labels on conflicting keys; the
	// Deployment selector uses the stable identity subset only, so user
	// labels never narrow or break the selector.
	podSpec.Labels = mergeStringMaps(podSpec.Labels, ls)
	// Start from the user annotations so they are preserved, then set the
	// operator-managed rollout markers (which override conflicting keys).
	podSpec.Annotations = mergeStringMaps(podSpec.Annotations, map[string]string{jksVersionKey: jksResourceVersion})
	// Track the KafkaUser secret version for SASL modes so credential changes
	// trigger a rollout without rebuilding any JKS material.
	if auth.NeedsSASL {
		podSpec.Annotations[saslUserVersionKey] = saslUserResourceVersion
	}

	// Track the REST API TLS secret version so rotating the secret triggers
	// a rollout: user-provided secrets record the user secret's version,
	// operator-managed secrets the <name>-tls secret's version. Recording
	// the operator-managed version at build time keeps the annotation
	// present from creation, so the annotation-driven renewal check
	// (handleTLSCertExpiry) compares against a recorded value instead of
	// rolling a freshly created deployment one extra time, and a spec
	// update never drops the marker the rotation handlers maintain.
	if tlsResourceVersion != "" {
		podSpec.Annotations[tlsVersionKey] = tlsResourceVersion
	}

	// Compute spec hash and store it in annotation to detect spec changes on subsequent reconciliations.
	podSpec.Annotations[keyPrefix+"/specHash"] = computeSpecHash(instance)

	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + deploySuffix,
			Namespace: instance.Namespace,
			Labels:    ls,
			Annotations: map[string]string{
				keyPrefix + "/specHash": computeSpecHash(instance),
			},
		},
		Spec: apps.DeploymentSpec{
			// The desired replica count comes from spec.replicas (B20); an
			// unset value defaults to 1, matching the Deployment API default
			// so CRs created before the field existed behave unchanged.
			Replicas: ptr.To(effectiveReplicas(instance)),
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabelsForStrimziSchemaRegistryOperator(instance.Name),
			},
			Template: podSpec,
		},
	}
	// Set StrimziSchemaRegistry instance as the owner and controller
	err := ctrl.SetControllerReference(instance, dep, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, nil, err
	}
	return dep, configOverridden, nil
}

// effectiveReplicas returns the desired number of Schema Registry replicas
// (B20). An unset spec.replicas defaults to 1, matching the Deployment API
// default so CRs created before the field existed keep working unchanged.
func effectiveReplicas(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) int32 {
	if instance.Spec.Replicas != nil {
		return *instance.Spec.Replicas
	}
	return 1
}

// listenerPortAndScheme returns the container port and URI scheme based on SecureHTTP.
func listenerPortAndScheme(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) (int32, v1.URIScheme) {
	if instance.Spec.SecureHTTP {
		return 8085, v1.URISchemeHTTPS
	}
	return 8081, v1.URISchemeHTTP
}

// confluentPropertyToEnvName converts a Confluent Schema Registry property key
// into the SCHEMA_REGISTRY_* environment variable name. Dots and dashes become
// underscores and the result is uppercased.
func confluentPropertyToEnvName(key string) string {
	return "SCHEMA_REGISTRY_" + confluentPropertyEnvReplacer.Replace(strings.ToUpper(key))
}

var confluentPropertyEnvReplacer = strings.NewReplacer(".", "_", "-", "_")

// Keystore types supported for user-provided REST API TLS secrets.
const (
	keystoreTypeJKS    = "JKS"
	keystoreTypePKCS12 = "PKCS12"
)

// effectiveKeystoreType returns the keystore format of the user-provided REST
// API TLS secret (spec.tlssecretname). An unset value defaults to JKS
// (Confluent's built-in default), so CRs created before the keystoretype
// field existed keep working unchanged.
func effectiveKeystoreType(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) string {
	if instance.Spec.KeystoreType == keystoreTypePKCS12 {
		return keystoreTypePKCS12
	}
	return keystoreTypeJKS
}

// restAPIKeystoreLocation returns the in-pod path of the REST API TLS
// keystore for the given keystore type.
func restAPIKeystoreLocation(keystoreType string) string {
	if keystoreType == keystoreTypePKCS12 {
		return "/var/rest-api-tls/tls-keystore.p12"
	}
	return "/var/rest-api-tls/tls-keystore.jks"
}

// reservedConfigurationKeys are Confluent Schema Registry properties managed by
// the operator and therefore not settable through spec.configuration.
//
// The deprecated spellings (schema.registry.inter.instance.protocol,
// ssl.client.auth) stay reserved until a release note announces their removal;
// the modern equivalents (ssl.client.authentication, inter.instance.protocol)
// are operator-managed from the start. ssl.keystore.type is operator-managed
// because it follows spec.keystoretype, and
// kafkastore.topic.replication.factor follows
// spec.kafkastoretopicreplicationfactor — the operator omits that env var
// entirely when the spec field is unset, so a user-supplied value must not
// leak through and override Confluent's built-in default.
var reservedConfigurationKeys = map[string]struct{}{
	"kafkastore.bootstrap.servers":            {},
	"kafkastore.security.protocol":            {},
	"listeners":                               {},
	"host.name":                               {},
	"kafkastore.topic":                        {},
	"kafkastore.topic.replication.factor":     {},
	"schema.registry.group.id":                {},
	"master.eligibility":                      {},
	"schema.compatibility.level":              {},
	"heap.opts":                               {},
	"schema.registry.inter.instance.protocol": {},
	"inter.instance.protocol":                 {},
	"ssl.client.authentication":               {},
	"ssl.client.auth":                         {},
	"ssl.keystore.type":                       {},
	"ssl.key.password":                        {},
}

// reservedConfigurationPrefixes are property key prefixes managed by the
// operator. ssl.truststore.* is reserved because the REST API TLS setup is
// rendered by the operator (and ssl.truststore.type must follow the keystore
// type when REST API mutual TLS is enabled), so user-supplied truststore
// properties must not bypass the operator-managed env vars.
var reservedConfigurationPrefixes = []string{
	"kafkastore.ssl.",
	"kafkastore.sasl.",
	"ssl.keystore.",
	"ssl.truststore.",
}

// isReservedConfigurationKey reports whether the given Confluent property key is
// operator-managed and must be ignored when supplied through spec.configuration.
//
// The key is normalized before matching: Confluent property names are
// case-insensitive and tolerate dashes in place of dots (both spellings
// convert to the same SCHEMA_REGISTRY_* env var), so inputs like
// SSL-CLIENT-AUTH or Ssl.ClientAuth must be caught exactly like the canonical
// lowercase dot-separated form.
func isReservedConfigurationKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "."))
	if _, ok := reservedConfigurationKeys[normalized]; ok {
		return true
	}
	for _, prefix := range reservedConfigurationPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// sortedConfigurationKeys returns the keys of configuration in sorted order so
// rendering and hashing are deterministic.
func sortedConfigurationKeys(configuration map[string]string) []string {
	keys := make([]string, 0, len(configuration))
	for key := range configuration {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// appendConfigurationEnv appends user-supplied configuration properties as
// SCHEMA_REGISTRY_* env vars. Reserved keys and keys whose converted env name
// collides with an operator-managed env var are skipped. It returns the updated
// env slice and the list of skipped keys in deterministic sorted order.
func appendConfigurationEnv(podEnv []v1.EnvVar, configuration map[string]string) ([]v1.EnvVar, []string) {
	managed := make(map[string]struct{}, len(podEnv))
	for _, env := range podEnv {
		managed[env.Name] = struct{}{}
	}

	var overridden []string
	for _, key := range sortedConfigurationKeys(configuration) {
		envName := confluentPropertyToEnvName(key)
		if isReservedConfigurationKey(key) {
			overridden = append(overridden, key)
			continue
		}
		if _, exists := managed[envName]; exists {
			overridden = append(overridden, key)
			continue
		}
		podEnv = append(podEnv, v1.EnvVar{Name: envName, Value: configuration[key]})
	}
	return podEnv, overridden
}

// buildPodEnv constructs the environment variables for the Schema Registry
// container and returns them along with the spec.configuration keys that were
// skipped because they are operator-managed.
func buildPodEnv(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, kafkaBootstrapServer, TLSSecretName string, auth kafkaAuth) ([]v1.EnvVar, []string) {
	podEnv := []v1.EnvVar{
		{Name: "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS", Value: kafkaBootstrapServer},
		{Name: "SCHEMA_REGISTRY_HOST_NAME", ValueFrom: &v1.EnvVarSource{
			FieldRef: &v1.ObjectFieldSelector{
				FieldPath: "status.podIP",
			},
		}},
	}

	// Compatibility level
	compatLevel := instance.Spec.CompatibilityLevel
	if compatLevel == "" {
		compatLevel = "forward"
	}
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL", Value: compatLevel})

	// Security protocol (already defaulted by resolveKafkaAuth)
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", Value: auth.Protocol})

	// Heap opts
	heapOpts := instance.Spec.HeapOpts
	if heapOpts == "" {
		heapOpts = "-Xms512M -Xmx512M"
	}

	// Storage and identity settings. Empty/nil values fall back to the documented
	// defaults so CRs created before CRD defaults existed keep working unchanged.
	kafkastoreTopic := instance.Spec.KafkastoreTopic
	if kafkastoreTopic == "" {
		kafkastoreTopic = "registry-schemas"
	}

	schemaRegistryGroupID := instance.Spec.SchemaRegistryGroupID
	if schemaRegistryGroupID == "" {
		schemaRegistryGroupID = "schema-registry"
	}

	masterEligibility := true
	if instance.Spec.MasterEligibility != nil {
		masterEligibility = *instance.Spec.MasterEligibility
	}

	podEnv = append(podEnv,
		v1.EnvVar{Name: "SCHEMA_REGISTRY_MASTER_ELIGIBILITY", Value: strconv.FormatBool(masterEligibility)},
		v1.EnvVar{Name: "SCHEMA_REGISTRY_HEAP_OPTS", Value: heapOpts},
		v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", Value: kafkastoreTopic},
		v1.EnvVar{Name: "SCHEMA_REGISTRY_SCHEMA_REGISTRY_GROUP_ID", Value: schemaRegistryGroupID},
	)

	// Only emit the replication factor when explicitly set so Confluent's
	// built-in default (3) is preserved for unset values.
	if instance.Spec.KafkastoreTopicReplicationFactor != nil {
		podEnv = append(podEnv, v1.EnvVar{
			Name:  "SCHEMA_REGISTRY_KAFKASTORE_TOPIC_REPLICATION_FACTOR",
			Value: strconv.FormatInt(int64(*instance.Spec.KafkastoreTopicReplicationFactor), 10),
		})
	}

	// KafkaStore TLS environment. The keystore is only needed for mTLS (SSL);
	// the truststore is needed for TLS transport (SSL and SASL_SSL).
	if auth.NeedsKeystore {
		podEnv = append(podEnv,
			v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION", Value: "/var/schemaregistry/keystore.jks"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: instance.Name + jksSecretSuffix,
					},
					Key: "keystore_password",
				},
			}},
		)
	}
	if auth.NeedsTruststore {
		podEnv = append(podEnv,
			v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION", Value: "/var/schemaregistry/truststore.jks"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: instance.Name + jksSecretSuffix,
					},
					Key: "truststore_password",
				},
			}},
		)
	}

	// KafkaStore SASL environment for SASL_SSL and SASL_PLAINTEXT. Strimzi
	// writes the SCRAM JAAS config into the KafkaUser secret (named after the CR).
	if auth.NeedsSASL {
		podEnv = append(podEnv,
			v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM", Value: auth.SASLMechanism},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: instance.Name,
					},
					Key: "sasl.jaas.config",
				},
			}},
		)
	}

	// REST API TLS configuration
	if instance.Spec.SecureHTTP {
		keystoreType := effectiveKeystoreType(instance)
		restAPIEnv := []v1.EnvVar{
			{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "https://0.0.0.0:8085"},
			// inter.instance.protocol replaces the deprecated
			// schema.registry.inter.instance.protocol spelling.
			{Name: "SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL", Value: "https"},
			{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", Value: restAPIKeystoreLocation(keystoreType)},
			{Name: sslKeystorePasswordEnv, ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: TLSSecretName,
					},
					Key: "keystore_password",
				},
			}},
			{Name: sslKeyPasswordEnv, ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: TLSSecretName,
					},
					Key: "key_password",
				},
			}},
		}
		// Confluent defaults ssl.keystore.type to JKS, so the property is only
		// emitted for the non-default PKCS12 keystore type.
		if keystoreType == keystoreTypePKCS12 {
			restAPIEnv = append(restAPIEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE", Value: keystoreType})
		}
		podEnv = append(podEnv, restAPIEnv...)
	} else {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "http://0.0.0.0:8081"})
	}

	// REST API mutual TLS: require client certificates signed by the cluster CA.
	// ssl.client.authentication replaces the deprecated ssl.client.auth=true
	// spelling (NONE/REQUESTED/REQUIRED).
	if instance.Spec.ClientAuth && instance.Spec.SecureHTTP {
		podEnv = append(podEnv,
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_CLIENT_AUTHENTICATION", Value: "REQUIRED"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION", Value: "/var/schemaregistry/truststore.jks"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: instance.Name + jksSecretSuffix,
					},
					Key: "truststore_password",
				},
			}},
		)
	}

	podEnv, overridden := appendConfigurationEnv(podEnv, instance.Spec.Configuration)
	return podEnv, overridden
}

// buildPodVolumes constructs the volumes and volume mounts for the deployment.
func buildPodVolumes(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, TLSSecretName string, auth kafkaAuth) ([]v1.Volume, []v1.VolumeMount) {
	var defaultMode int32 = 420
	var containerVolumeMount []v1.VolumeMount
	var podVolume []v1.Volume

	// KafkaStore JKS secret volume is needed when TLS transport or mTLS
	// keystore material is required (SSL and SASL_SSL), and also when REST API
	// mutual TLS is enabled (the cluster-CA truststore is reused for it).
	if auth.NeedsTruststore || auth.NeedsKeystore || (instance.Spec.ClientAuth && instance.Spec.SecureHTTP) {
		containerVolumeMount = append(containerVolumeMount, v1.VolumeMount{
			Name:      "tls",
			MountPath: "/var/schemaregistry",
			ReadOnly:  true,
		})
		podVolume = append(podVolume, v1.Volume{
			Name: "tls",
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName:  instance.Name + jksSecretSuffix,
					DefaultMode: &defaultMode,
				},
			},
		})
	}

	// REST API TLS secret volume
	if instance.Spec.SecureHTTP {
		containerVolumeMount = append(containerVolumeMount, v1.VolumeMount{
			Name:      restAPITLSVolumeName,
			MountPath: "/var/rest-api-tls",
			ReadOnly:  true,
		})
		podVolume = append(podVolume, v1.Volume{
			Name: restAPITLSVolumeName,
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName:  TLSSecretName,
					DefaultMode: &defaultMode,
				},
			},
		})
	}

	return podVolume, containerVolumeMount
}

// buildProbes constructs readiness, liveness, and startup probes for the Schema Registry container.
//
// Readiness and startup probes use TCPSocket instead of HTTPGet in two cases:
//
//   - When SecureHTTP is enabled (scheme == HTTPS). This avoids TLS
//     certificate verification failures: the Kubernetes kubelet does not
//     support insecure-skip-verify for HTTPGet probes, so HTTPS probes against
//     self-signed certificates (generated from the Strimzi cluster CA) would
//     fail perpetually.
//   - When authentication.method is configured in spec.configuration. An
//     authenticated listener rejects unauthenticated HTTP GET requests with
//     401 Unauthorized, so the probes would fail perpetually; a TCP connect
//     probe works regardless of authentication.
//
// Without authentication the HTTP probe behavior is kept. The liveness probe
// uses TCPSocket unconditionally for the same reasons.
func buildProbes(listenerPort int32, scheme v1.URIScheme, authConfigured bool) (*v1.Probe, *v1.Probe, *v1.Probe) {
	var readinessHandler, startupHandler v1.ProbeHandler

	if scheme == v1.URISchemeHTTPS || authConfigured {
		// Self-signed cert or authenticated listener: TCPSocket avoids TLS
		// verification failures and 401 responses.
		readinessHandler = v1.ProbeHandler{
			TCPSocket: &v1.TCPSocketAction{
				Port: intstr.IntOrString{IntVal: listenerPort},
			},
		}
		startupHandler = v1.ProbeHandler{
			TCPSocket: &v1.TCPSocketAction{
				Port: intstr.IntOrString{IntVal: listenerPort},
			},
		}
	} else {
		readinessHandler = v1.ProbeHandler{
			HTTPGet: &v1.HTTPGetAction{
				Path:   "/",
				Port:   intstr.IntOrString{IntVal: listenerPort},
				Scheme: v1.URISchemeHTTP,
			},
		}
		startupHandler = v1.ProbeHandler{
			HTTPGet: &v1.HTTPGetAction{
				Path:   "/",
				Port:   intstr.IntOrString{IntVal: listenerPort},
				Scheme: v1.URISchemeHTTP,
			},
		}
	}

	readinessProbe := &v1.Probe{
		ProbeHandler:     readinessHandler,
		PeriodSeconds:    10,
		TimeoutSeconds:   5,
		FailureThreshold: 3,
	}

	livenessProbe := &v1.Probe{
		ProbeHandler: v1.ProbeHandler{
			TCPSocket: &v1.TCPSocketAction{
				Port: intstr.IntOrString{IntVal: listenerPort},
			},
		},
		PeriodSeconds:    15,
		TimeoutSeconds:   5,
		FailureThreshold: 3,
	}

	// The startup budget (10s delay + 12 x 10s = 130s) leaves room for JVM
	// startup plus the first Kafka topic initialization, which the former
	// 30s + 3 x 10s = 60s budget could exhaust.
	startupProbe := &v1.Probe{
		ProbeHandler:        startupHandler,
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		FailureThreshold:    12,
	}

	return readinessProbe, livenessProbe, startupProbe
}

// authenticationMethodConfigured reports whether the user configured a
// non-empty authentication.method property in spec.configuration. The lookup
// normalizes dash and case variants (authentication-method,
// AUTHENTICATION.METHOD) the same way Confluent property spellings convert to
// the same SCHEMA_REGISTRY_* env var.
func authenticationMethodConfigured(configuration map[string]string) bool {
	for key, value := range configuration {
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", "."))
		if normalized == "authentication.method" && value != "" {
			return true
		}
	}
	return false
}

// clientAuthNeedsTruststoreSecret reports whether REST API mutual TLS requires
// an operator-managed cluster-CA truststore secret that is not already produced
// for the Kafka auth mode (i.e. PLAINTEXT and SASL_PLAINTEXT).
func clientAuthNeedsTruststoreSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, auth kafkaAuth) bool {
	return instance.Spec.ClientAuth && instance.Spec.SecureHTTP && !auth.NeedsTruststore
}

func (r *StrimziSchemaRegistryReconciler) getKafkaBootstrapServers(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger) (string, string, error) {
	// Get Kafka cluster name from the StrimziSchemaRegistry labels. This keeps
	// bootstrap server discovery independent of the KafkaUser so that PLAINTEXT
	// (which requires no KafkaUser) can still resolve the bootstrap address.
	kafkaClusterName, err := getStrimziClusterName(instance)
	if err != nil {
		return "", "", err
	}
	logger.V(1).Info("Found kafka cluster CR", "Name", kafkaClusterName)
	// Find bootstap server address
	var kafkaBootstrapServer string
	kafkaCluster := &kafka.Kafka{}
	err = r.Get(ctx, types.NamespacedName{Name: kafkaClusterName, Namespace: instance.Namespace}, kafkaCluster)
	if err != nil {
		return "", "", err
	}
	kafkaListener := instance.Spec.Listener
	if kafkaListener == "" {
		kafkaListener = "tls"
	}
	for _, listener := range kafkaCluster.Status.Listeners {
		logger.V(1).Info("Found kafka listeners.", "Listener", listener.Name)
		if strings.EqualFold(listener.Name, kafkaListener) {
			kafkaBootstrapServer = listener.BootstrapServers
			logger.V(1).Info("Found specified kafka cluster listeners.", "Listener", kafkaListener, "kafkaBootstrap", kafkaBootstrapServer)
			logger.V(1).Info("KafkaBootstrap", "Address", kafkaBootstrapServer)
			return kafkaBootstrapServer, kafkaClusterName, nil
		}
	}
	logger.V(1).Info("No listeners found. Check CR config", "Listener", kafkaListener)
	// Actionable error (B10): name the requested listener, the Kafka cluster
	// and the CR that requested it, and the listeners the Kafka CR actually
	// exposes, so the operator log and the CR status (B7) say what to fix.
	available := make([]string, 0, len(kafkaCluster.Status.Listeners))
	for _, listener := range kafkaCluster.Status.Listeners {
		available = append(available, listener.Name)
	}
	if len(available) == 0 {
		return "", "", fmt.Errorf(
			"listener %q not found on Kafka cluster %s/%s: the Kafka CR exposes no status.listeners yet; requested by StrimziSchemaRegistry %s/%s",
			kafkaListener, kafkaCluster.Name, kafkaCluster.Namespace, instance.Name, instance.Namespace)
	}
	return "", "", fmt.Errorf(
		"listener %q not found on Kafka cluster %s/%s: available listeners are [%s]; check spec.listener on StrimziSchemaRegistry %s/%s",
		kafkaListener, kafkaCluster.Name, kafkaCluster.Namespace, strings.Join(available, ", "), instance.Name, instance.Namespace)
}

// createSecret creates or returns an up-to-date JKS secret for the Schema Registry.
// Returns the secret, a bool indicating whether a new secret was created (as opposed
// to being already up-to-date), and any error encountered.
//
// The produced secret depends on the resolved Kafka auth mode:
//   - mTLS (SSL): truststore + keystore.
//   - SASL_SSL: truststore only.
//   - SASL_PLAINTEXT / PLAINTEXT: no JKS secret is produced (returns nil, false, nil).
func (r *StrimziSchemaRegistryReconciler) createSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string, clusterCASecret *v1.Secret,
	userCASecret *v1.Secret, auth kafkaAuth) (*v1.Secret, bool, error) {
	switch auth.Mode {
	case authSASLPlaintext, authPlaintext:
		logger.V(1).Info("No JKS secret is required for the selected auth mode", "SecurityProtocol", auth.Protocol)
		return nil, false, nil
	case authSASLSSL:
		return r.createTruststoreSecret(instance, ctx, logger, clusterName, clusterCASecret)
	default:
		return r.createMTLSSecret(instance, ctx, logger, clusterName, clusterCASecret, userCASecret)
	}
}

// getClusterCASecret returns the cluster CA cert secret, fetching it when the
// provided secret is nil.
func (r *StrimziSchemaRegistryReconciler) getClusterCASecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string, clusterCASecret *v1.Secret) (*v1.Secret, error) {
	if clusterCASecret != nil {
		return clusterCASecret, nil
	}
	logger.V(1).Info("Searching for cluster CA secret", "Secret", clusterName+clusterCASuffix)
	clusterSecret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: clusterName + clusterCASuffix, Namespace: instance.Namespace}, clusterSecret)
	if err != nil {
		return nil, err
	}
	logger.V(1).Info("Cluster CA certificate version", "Version", clusterSecret.ResourceVersion)
	return clusterSecret, nil
}

// getKafkaUserSecret returns the KafkaUser secret (named after the instance),
// fetching it when the provided secret is nil.
func (r *StrimziSchemaRegistryReconciler) getKafkaUserSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, userCASecret *v1.Secret) (*v1.Secret, error) {
	if userCASecret != nil {
		return userCASecret, nil
	}
	logger.Info("Searching for user CA secret", "Secret", instance.Name)
	userSecret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, userSecret)
	if err != nil {
		return nil, err
	}
	logger.V(1).Info("Client certification version", "Version", userSecret.ResourceVersion)
	return userSecret, nil
}

// getSASLUserSecretVersion returns the ResourceVersion of the KafkaUser secret
// that carries the SCRAM credentials for SASL modes. This version is stored on
// the deployment pod template so credential changes can trigger a rollout.
func (r *StrimziSchemaRegistryReconciler) getSASLUserSecretVersion(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context,
	logger logr.Logger,
) (string, error) {
	userSecret := &v1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, userSecret); err != nil {
		return "", err
	}
	logger.V(1).Info("SASL KafkaUser secret version", "Version", userSecret.ResourceVersion)
	return userSecret.ResourceVersion, nil
}

// existingJKSSecret fetches the current -jks secret and reports whether it is
// up-to-date according to isUpToDate. When it exists but is stale it is
// returned as-is (NOT deleted) so the caller can rebuild the material and
// update the secret in place, carrying the existing ResourceVersion (B4).
// Delete-then-recreate left a window in which a pod restarting in between
// failed to mount the secret — the same failure mode already fixed for the
// REST TLS secret path in createTLSSecret.
func (r *StrimziSchemaRegistryReconciler) existingJKSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, isUpToDate func(annotations map[string]string) bool) (*v1.Secret, bool, error) {
	jks_secret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + jksSecretSuffix, Namespace: instance.Namespace}, jks_secret)
	if err == nil {
		if isUpToDate(jks_secret.Annotations) {
			logger.V(1).Info("JKS secret is up-to-date")
			// Return the existing secret (not nil) so the caller has its current
			// ResourceVersion for the rollout annotation without re-fetching.
			return jks_secret, true, nil
		}
		logger.V(1).Info("JKS secret is stale; caller will update it in place")
		// Return the stale secret so the caller can carry its
		// ResourceVersion into the rebuilt one for an in-place Update.
		return jks_secret, false, nil
	}
	if !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get schema registry secret")
		return nil, false, err
	}
	return nil, false, nil
}

// persistJKSSecret stores a (re)built JKS secret: secrets carrying a
// ResourceVersion (a stale existing secret) are updated in place so they are
// never absent for pods mounting them; new secrets are created.
func (r *StrimziSchemaRegistryReconciler) persistJKSSecret(ctx context.Context, secret *v1.Secret) error {
	if secret.ResourceVersion != "" {
		return r.Update(ctx, secret)
	}
	return r.Create(ctx, secret)
}

// gcOrphanedSecrets deletes operator-owned secrets that no longer correspond
// to a live CR or the CR's current spec, so CR deletion and
// spec.tlssecretname transitions do not leave stale TLS material behind:
//   - a secret whose controller owner reference points to a
//     StrimziSchemaRegistry that no longer exists (the CR was deleted) —
//     covers the <name>-jks, <name>-tls and derived
//     <name>-<tlssecretname>-jks outputs;
//   - a derived <name>-<old>-jks secret of a live CR whose middle segment no
//     longer matches spec.tlssecretname (the user TLS secret was renamed).
//
// The collection is deliberately conservative. The List is scoped to secrets
// carrying the operator's app label, so unrelated secrets in the namespace are
// never even fetched. Within that set, only secrets carrying a controller owner
// reference to a StrimziSchemaRegistry are ever considered — user-provided TLS
// secrets, Strimzi CA/user secrets and every uncontrolled secret are never
// touched. A live CR's <name>-jks and <name>-tls outputs are never deleted
// either: they are recreated on demand and deleting them would leave pods
// mounting a missing secret. Errors are logged and skipped so secret hygiene
// can never fail reconciliation.
//
// Deleting an orphan emits a Secret Delete event; mapSecretToRequests maps it
// back to the CR named in the owner reference. For a deleted owner the
// reconcile finds the CR gone and returns without error (no loop); for a live
// owner the extra reconcile observes the secret is gone and converges.
func (r *StrimziSchemaRegistryReconciler) gcOrphanedSecrets(ctx context.Context, instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, logger logr.Logger) {
	secretList := &v1.SecretList{}
	if err := r.List(ctx, secretList,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{operatorAppLabelKey: operatorAppLabelValue},
	); err != nil {
		logger.Error(err, "Failed to list secrets for orphaned-secret garbage collection")
		return
	}
	for i := range secretList.Items {
		secret := &secretList.Items[i]
		owner := metav1.GetControllerOf(secret)
		if owner == nil || owner.Kind != "StrimziSchemaRegistry" {
			// Uncontrolled (user TLS, Strimzi CA/user secrets) or foreign-
			// controlled: never touched.
			continue
		}
		if owner.Name != instance.Name {
			ownerCR := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
			err := r.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: instance.Namespace}, ownerCR)
			if err == nil {
				continue // another live CR's secret
			}
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to check the owning CR of a controlled secret during garbage collection; skipping it",
					"Secret.Name", secret.Name, "Owner.Name", owner.Name)
				continue
			}
			// The owning CR no longer exists: the secret is orphaned.
		} else if !isStaleDerivedJKSSecret(instance, secret.Name) {
			// This live CR's secret: only a stale derived <name>-<old>-jks
			// secret is clearly orphaned; the <name>-jks and <name>-tls
			// outputs are recreated on demand and never garbage-collected
			// while the CR lives.
			continue
		}
		if err := r.Delete(ctx, secret); err != nil {
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete an orphaned operator-owned secret", "Secret.Name", secret.Name)
			}
			continue
		}
		logger.Info("Garbage-collected orphaned operator-owned secret",
			"Secret.Name", secret.Name, "Secret.Namespace", secret.Namespace)
	}
}

// isStaleDerivedJKSSecret reports whether secretName is a derived
// <name>-<tlssecretname>-jks secret of the given CR whose middle segment no
// longer matches the CR's current spec.tlssecretname — i.e. a conversion
// output left behind by a spec.tlssecretname transition. The mTLS keystore
// (<name>-jks, empty middle segment) and the REST API TLS secret
// (<name>-tls, not -jks suffixed) never match.
func isStaleDerivedJKSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, secretName string) bool {
	prefix := instance.Name + "-"
	if !strings.HasPrefix(secretName, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(secretName, prefix)
	// The suffix must actually be present in the remainder: the mTLS
	// keystore <name>-jks leaves the remainder "jks", which does not end
	// with "-jks", so it never matches.
	if !strings.HasSuffix(remainder, jksSecretSuffix) {
		return false
	}
	middle := strings.TrimSuffix(remainder, jksSecretSuffix)
	if middle == "" {
		return false
	}
	return middle != instance.Spec.TLSSecretName
}

// createMTLSSecret creates or returns an up-to-date JKS secret containing both
// the truststore (built from the cluster CA) and the keystore (built from the
// KafkaUser client certificate) for mTLS authentication.
func (r *StrimziSchemaRegistryReconciler) createMTLSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string, clusterCASecret *v1.Secret,
	userCASecret *v1.Secret) (*v1.Secret, bool, error) {
	logger.Info("Creating secret for schema registry Kafkastore TLS")

	clusterSecret, err := r.getClusterCASecret(instance, ctx, logger, clusterName, clusterCASecret)
	if err != nil {
		return nil, false, err
	}
	clusterCACert := string(clusterSecret.Data["ca.crt"])

	userSecret, err := r.getKafkaUserSecret(instance, ctx, logger, userCASecret)
	if err != nil {
		return nil, false, err
	}
	clientCACert := string(userSecret.Data["ca.crt"])
	clientCert := string(userSecret.Data["user.crt"])
	clientKey := string(userSecret.Data["user.key"])
	clientp12 := string(userSecret.Data["user.p12"])
	userPasswordData, ok := userSecret.Data["user.password"]
	if !ok {
		return nil, false, go_err.New("user.password field is missing from KafkaUser secret; this field is required for keystore creation")
	}
	userPassword := string(userPasswordData)

	// A pre-built user.p12 is encrypted with the KafkaUser's user.password:
	// the PKCS12 decode unlocks the source store with that password, so an
	// empty value cannot be replaced by a generated one (the generated
	// password would not open the p12 and the decode fails). Reject it up
	// front with a clear validation error instead of a broken keystore
	// generation downstream. On the PEM path (no user.p12) an empty password
	// is fine: the keystore is sealed with the generated password itself.
	if clientp12 != "" && len(userPasswordData) == 0 {
		return nil, false, go_err.New("user.password field is empty in the KafkaUser secret; it is required as the PKCS12 keystore password when user.p12 is present")
	}

	// Validate the certificate material the keystore pipeline dereferences.
	// A pre-built user.p12 is used directly and makes the PEM fields
	// unnecessary; otherwise the PEM path needs all three fields (ca.crt for
	// the issuing chain, user.crt for the end-entity certificate, user.key
	// for the private key), and a missing key would only surface as a
	// "no certificate matches the provided private key" failure.
	if clientp12 == "" {
		var missing []string
		for _, key := range []string{"ca.crt", "user.crt", "user.key"} {
			if len(userSecret.Data[key]) == 0 {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return nil, false, fmt.Errorf("%s field(s) missing from KafkaUser secret; this field is required for keystore creation", strings.Join(missing, ", "))
		}
	}

	existing, upToDate, err := r.existingJKSSecret(instance, ctx, logger, func(annotations map[string]string) bool {
		return annotations[CAVersionKey] == clusterSecret.ResourceVersion &&
			annotations[userVersionKey] == userSecret.ResourceVersion
	})
	if err != nil {
		return nil, false, err
	}
	if upToDate {
		return existing, false, nil
	}

	jks_secret_name := instance.Name + jksSecretSuffix
	logger.Info("Rebuilding keystore and truststore", "Secret Name", jks_secret_name)
	cp := certprocessor.NewCertProcessor(logger)
	truststore, truststore_password, err := cp.CreateTruststore(ctx, clusterCACert, "")
	if err != nil {
		return nil, false, err
	}
	keystore, keystore_password, err := cp.CreateKeystore(ctx, clientCACert, clientCert, clientKey, clientp12, userPassword)
	if err != nil {
		return nil, false, err
	}
	jks_secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jks_secret_name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"app":  "strimzi-schema-registry",
				"user": instance.Name,
			},
			Annotations: map[string]string{
				CAVersionKey:   clusterSecret.ResourceVersion,
				userVersionKey: userSecret.ResourceVersion,
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"truststore.jks":      []byte(truststore),
			"keystore.jks":        []byte(keystore),
			"truststore_password": []byte(truststore_password),
			"keystore_password":   []byte(keystore_password),
		},
	}
	err = ctrl.SetControllerReference(instance, jks_secret, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, false, err
	}
	if existing != nil {
		// Carry the ResourceVersion so the caller Updates in place instead
		// of deleting and recreating the secret (B4).
		jks_secret.ResourceVersion = existing.ResourceVersion
	}
	return jks_secret, true, nil
}

// createTruststoreSecret creates or returns an up-to-date JKS secret
// containing only the truststore (built from the cluster CA). It is used for
// SASL_SSL and for REST API mutual TLS in PLAINTEXT/SASL_PLAINTEXT modes.
func (r *StrimziSchemaRegistryReconciler) createTruststoreSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string, clusterCASecret *v1.Secret) (*v1.Secret, bool, error) {
	logger.Info("Creating truststore secret for schema registry")

	clusterSecret, err := r.getClusterCASecret(instance, ctx, logger, clusterName, clusterCASecret)
	if err != nil {
		return nil, false, err
	}
	clusterCACert := string(clusterSecret.Data["ca.crt"])

	existing, upToDate, err := r.existingJKSSecret(instance, ctx, logger, func(annotations map[string]string) bool {
		return annotations[CAVersionKey] == clusterSecret.ResourceVersion
	})
	if err != nil {
		return nil, false, err
	}
	if upToDate {
		return existing, false, nil
	}

	jks_secret_name := instance.Name + jksSecretSuffix
	logger.Info("Rebuilding truststore", "Secret Name", jks_secret_name)
	cp := certprocessor.NewCertProcessor(logger)
	truststore, truststore_password, err := cp.CreateTruststore(ctx, clusterCACert, "")
	if err != nil {
		return nil, false, err
	}
	jks_secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jks_secret_name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"app":  "strimzi-schema-registry",
				"user": instance.Name,
			},
			Annotations: map[string]string{
				CAVersionKey: clusterSecret.ResourceVersion,
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"truststore.jks":      []byte(truststore),
			"truststore_password": []byte(truststore_password),
		},
	}
	err = ctrl.SetControllerReference(instance, jks_secret, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, false, err
	}
	if existing != nil {
		// Carry the ResourceVersion so the caller Updates in place instead
		// of deleting and recreating the secret (B4).
		jks_secret.ResourceVersion = existing.ResourceVersion
	}
	return jks_secret, true, nil
}

// ensureClientAuthTruststoreSecret ensures an operator-managed cluster-CA
// truststore secret exists for REST API mutual TLS. It is used when the Kafka
// auth mode does not already produce a truststore secret (PLAINTEXT and
// SASL_PLAINTEXT). It returns the secret and whether it was (re)built (as
// opposed to already up-to-date). A rebuilt secret is persisted in place when
// a stale secret existed (B4).
func (r *StrimziSchemaRegistryReconciler) ensureClientAuthTruststoreSecret(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context,
	logger logr.Logger,
	clusterName string,
) (*v1.Secret, bool, error) {
	secret, created, err := r.createTruststoreSecret(instance, ctx, logger, clusterName, nil)
	if err != nil {
		return nil, false, err
	}
	if !created {
		return secret, false, nil
	}
	if err := r.persistJKSSecret(ctx, secret); err != nil {
		logger.Error(err, "Failed to create/update client-auth truststore secret")
		return nil, false, err
	}
	return secret, true, nil
}

func (r *StrimziSchemaRegistryReconciler) createService(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, logger logr.Logger) (*v1.Service, error) {
	logger.Info("Creating a new Service", "Service.Namespace", instance.Namespace, "Service.Name", instance.Name)

	// Defaults: ClusterIP on port 80 (http) or 443 (https), overridable via
	// spec.service.type / port / nodeport.
	serviceType := v1.ServiceTypeClusterIP
	var servicePort int32
	var portName string
	var targetPort int32
	if instance.Spec.SecureHTTP {
		servicePort, portName, targetPort = 443, "https", 8085
	} else {
		servicePort, portName, targetPort = 80, "http", 8081
	}
	var nodePort int32
	if instance.Spec.Service != nil {
		if instance.Spec.Service.Type != "" {
			serviceType = v1.ServiceType(instance.Spec.Service.Type)
		}
		if instance.Spec.Service.Port != nil {
			servicePort = *instance.Spec.Service.Port
		}
		if instance.Spec.Service.NodePort != nil {
			nodePort = *instance.Spec.Service.NodePort
		}
	}

	sp := v1.ServicePort{
		Name:       portName,
		Protocol:   "TCP",
		Port:       servicePort,
		TargetPort: intstr.IntOrString{IntVal: targetPort},
	}
	if nodePort != 0 {
		sp.NodePort = nodePort
	}
	port := []v1.ServicePort{sp}

	// Start from the CR labels (existing behavior) and overlay the optional
	// spec.service.labels passthrough.
	labels := mergeStringMaps(instance.Labels, nil)
	annotations := map[string]string{"type": port[0].Name}
	if instance.Spec.Service != nil {
		labels = mergeStringMaps(labels, instance.Spec.Service.Labels)
		annotations = mergeStringMaps(annotations, instance.Spec.Service.Annotations)
	}

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.Name,
			Namespace:   instance.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: v1.ServiceSpec{
			Type:  serviceType,
			Ports: port,
			Selector: map[string]string{
				"app": instance.Name,
			},
		},
	}
	err := ctrl.SetControllerReference(instance, svc, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistryOperator instance as the owner and controller for service")
		return nil, err
	}
	return svc, nil
}

// mergeServiceAnnotations merges the desired operator-managed Service
// annotations over the existing ones without deleting keys the operator does
// not currently manage. Foreign annotations (set by users or other
// controllers, e.g. cloud LoadBalancer or ExternalDNS) are preserved;
// operator-managed keys (the "type" marker and the spec.service.annotations
// passthrough) are added or updated in place.
//
// Known limitation: a passthrough annotation that the user later removes from
// spec.service.annotations lingers on the Service — without tracking state
// the operator cannot distinguish it from a foreign key, and it must never
// delete keys it does not currently manage.
func mergeServiceAnnotations(existing, desired map[string]string) map[string]string {
	return mergeStringMaps(existing, desired)
}

// serviceAnnotationsDrifted reports whether any operator-managed annotation —
// every key present in the desired set — differs on the existing Service.
// Foreign annotations are deliberately ignored: they are not operator-managed,
// must be preserved on update, and must never trigger an update loop.
func serviceAnnotationsDrifted(existing, desired map[string]string) bool {
	for key, want := range desired {
		if existing[key] != want {
			return true
		}
	}
	return false
}

// mergeServicePorts returns the desired service ports with API-server-allocated
// node ports preserved from the existing service. The allocation is carried
// over only when the target service type still uses node ports (NodePort or
// LoadBalancer): copying it into e.g. a ClusterIP service makes the API server
// reject the update ("nodePort: Forbidden"), deadlocking reconciliation.
func mergeServicePorts(desiredType v1.ServiceType, desiredPorts, existingPorts []v1.ServicePort) []v1.ServicePort {
	ports := make([]v1.ServicePort, len(desiredPorts))
	for i, p := range desiredPorts {
		if p.NodePort == 0 && i < len(existingPorts) &&
			(desiredType == v1.ServiceTypeNodePort || desiredType == v1.ServiceTypeLoadBalancer) {
			p.NodePort = existingPorts[i].NodePort
		}
		ports[i] = p
	}
	return ports
}

// mergeStringMaps returns a new map containing base overlaid with override.
// The result is never nil, which keeps hashing and diffing deterministic.
func mergeStringMaps(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

// mergeEnvVars combines the operator-managed environment with the user's
// template environment (B3). The operator values come first and win on name
// conflicts; user-only variables are appended in their original order, so the
// result is deterministic and contains no duplicate names. Nil slices merge
// into nil, keeping a template without env vars unchanged.
func mergeEnvVars(operator, user []v1.EnvVar) []v1.EnvVar {
	if len(user) == 0 {
		return operator
	}
	taken := make(map[string]struct{}, len(operator))
	for _, e := range operator {
		taken[e.Name] = struct{}{}
	}
	merged := append([]v1.EnvVar{}, operator...)
	for _, e := range user {
		if _, exists := taken[e.Name]; exists {
			continue
		}
		taken[e.Name] = struct{}{}
		merged = append(merged, e)
	}
	return merged
}

// mergeVolumes combines the operator-managed volumes with the user's template
// volumes (B3). Operator volumes win on name conflicts and come first; user
// volumes are appended in their original order so the output is deterministic.
func mergeVolumes(operator, user []v1.Volume) []v1.Volume {
	if len(user) == 0 {
		return operator
	}
	taken := make(map[string]struct{}, len(operator))
	for _, vol := range operator {
		taken[vol.Name] = struct{}{}
	}
	merged := append([]v1.Volume{}, operator...)
	for _, vol := range user {
		if _, exists := taken[vol.Name]; exists {
			continue
		}
		taken[vol.Name] = struct{}{}
		merged = append(merged, vol)
	}
	return merged
}

// mergeVolumeMounts combines the operator-managed container mounts with the
// user's template mounts (B3). Operator mounts win: on a conflicting volume
// name or mount path the operator entry is kept and the user entry dropped.
// User-only mounts are appended in their original order.
func mergeVolumeMounts(operator, user []v1.VolumeMount) []v1.VolumeMount {
	if len(user) == 0 {
		return operator
	}
	takenNames := make(map[string]struct{}, len(operator))
	takenPaths := make(map[string]struct{}, len(operator))
	for _, m := range operator {
		takenNames[m.Name] = struct{}{}
		takenPaths[m.MountPath] = struct{}{}
	}
	merged := append([]v1.VolumeMount{}, operator...)
	for _, m := range user {
		if _, exists := takenNames[m.Name]; exists {
			continue
		}
		if _, exists := takenPaths[m.MountPath]; exists {
			continue
		}
		takenNames[m.Name] = struct{}{}
		takenPaths[m.MountPath] = struct{}{}
		merged = append(merged, m)
	}
	return merged
}

// mergeContainerPorts merges operator-managed container ports with
// user-declared ones: the operator wins on conflicting containerPort numbers
// and names, user-only ports are appended in their original order.
func mergeContainerPorts(operator, user []v1.ContainerPort) []v1.ContainerPort {
	if len(operator) == 0 {
		return user
	}
	takenNumbers := make(map[int32]struct{}, len(operator))
	takenNames := make(map[string]struct{}, len(operator))
	for _, p := range operator {
		takenNumbers[p.ContainerPort] = struct{}{}
		if p.Name != "" {
			takenNames[p.Name] = struct{}{}
		}
	}
	merged := append([]v1.ContainerPort{}, operator...)
	for _, p := range user {
		if _, exists := takenNumbers[p.ContainerPort]; exists {
			continue
		}
		if p.Name != "" {
			if _, exists := takenNames[p.Name]; exists {
				continue
			}
			takenNames[p.Name] = struct{}{}
		}
		takenNumbers[p.ContainerPort] = struct{}{}
		merged = append(merged, p)
	}
	return merged
}

// networkPolicyEnabled reports whether the optional NetworkPolicy should be
// created for the Schema Registry pods.
func networkPolicyEnabled(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) bool {
	return instance.Spec.NetworkPolicy != nil && instance.Spec.NetworkPolicy.Enabled
}

// buildNetworkPolicy constructs the desired NetworkPolicy for the Schema Registry
// pods. It allows ingress on the SR listener port from within the namespace and
// egress to the Kafka bootstrap service (identified by the Strimzi cluster label).
func buildNetworkPolicy(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, kafkaClusterName, kafkaBootstrapServer string) (*networkingv1.NetworkPolicy, error) {
	listenerPort, _ := listenerPortAndScheme(instance)
	kafkaPort, err := portFromBootstrapServer(kafkaBootstrapServer)
	if err != nil {
		return nil, err
	}

	image := ""
	if len(instance.Spec.Template.Spec.Containers) > 0 {
		image = instance.Spec.Template.Spec.Containers[0].Image
	}

	tcp := v1.ProtocolTCP
	udp := v1.ProtocolUDP
	listenerPortTarget := intstr.IntOrString{Type: intstr.Int, IntVal: listenerPort}
	kafkaPortTarget := intstr.IntOrString{Type: intstr.Int, IntVal: kafkaPort}
	dnsPortTarget := intstr.IntOrString{Type: intstr.Int, IntVal: 53}

	// Ingress sources: by default only pods in the CR's namespace; with
	// spec.networkpolicy.allowcrossnamespaceingress the empty From list
	// allows ingress from all namespaces.
	var ingressFrom []networkingv1.NetworkPolicyPeer
	if instance.Spec.NetworkPolicy != nil && instance.Spec.NetworkPolicy.AllowCrossNamespaceIngress {
		ingressFrom = nil
	} else {
		ingressFrom = []networkingv1.NetworkPolicyPeer{
			{
				// Empty pod selector matches all pods in the same namespace.
				PodSelector: &metav1.LabelSelector{},
			},
		}
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + networkPolicySuffix,
			Namespace: instance.Namespace,
			Labels:    labelsForStrimziSchemaRegistryOperator(instance.Name, image, kafkaClusterName),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": instance.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: ingressFrom,
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &listenerPortTarget},
					},
				},
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"strimzi.io/cluster": kafkaClusterName,
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &kafkaPortTarget},
					},
				},
				{
					// DNS resolution: the Schema Registry resolves the Kafka
					// bootstrap service name via kube-dns. Without this rule
					// the policy breaks name resolution in default-deny
					// namespaces.
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									dnsNamespaceLabel: dnsNamespaceName,
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									dnsPodLabel: dnsPodValue,
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udp, Port: &dnsPortTarget},
						{Protocol: &tcp, Port: &dnsPortTarget},
					},
				},
				{
					// Inter-instance traffic: every replica advertises its own
					// pod IP as host.name (downward API), so a replica
					// forwarding a request to a peer connects to the peer pod
					// directly on the listener port. Without this rule a
					// multi-replica registry with the policy enabled cannot
					// forward requests between instances.
					To: []networkingv1.NetworkPolicyPeer{
						{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": instance.Name,
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &listenerPortTarget},
					},
				},
			},
		},
	}, nil
}

// portFromBootstrapServer extracts the TCP port from a Kafka bootstrap server
// address of the form host:port.
func portFromBootstrapServer(bootstrap string) (int32, error) {
	idx := strings.LastIndex(bootstrap, ":")
	if idx == -1 || idx == len(bootstrap)-1 {
		return 0, fmt.Errorf("invalid Kafka bootstrap server %q: missing port", bootstrap)
	}
	port, err := strconv.Atoi(bootstrap[idx+1:])
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("invalid Kafka bootstrap server %q: bad port", bootstrap)
	}
	return int32(port), nil
}

// networkPoliciesEqual reports whether the existing NetworkPolicy matches the
// desired state (spec and labels only). Annotations are user-managed — the
// operator never sets any and preserves the existing ones on update (B8) —
// so they are deliberately excluded from the comparison.
func networkPoliciesEqual(existing, desired *networkingv1.NetworkPolicy) bool {
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return false
	}
	return equality.Semantic.DeepEqual(existing.Labels, desired.Labels)
}

// reconcileNetworkPolicy creates, updates, or deletes the NetworkPolicy for the
// Schema Registry pods according to spec.networkpolicy.enabled.
func (r *StrimziSchemaRegistryReconciler) reconcileNetworkPolicy(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
) error {
	name := types.NamespacedName{Name: instance.Name + networkPolicySuffix, Namespace: instance.Namespace}
	existing := &networkingv1.NetworkPolicy{}
	getErr := r.Get(ctx, name, existing)

	if !networkPolicyEnabled(instance) {
		if errors.IsNotFound(getErr) {
			return nil
		}
		if getErr != nil {
			return getErr
		}
		logger.V(1).Info("NetworkPolicy disabled, deleting existing policy", "NetworkPolicy.Name", name.Name)
		return r.Delete(ctx, existing)
	}

	// Resolve the Kafka bootstrap server to derive the egress port.
	kafkaBootstrapServer, kafkaClusterName, err := r.getKafkaBootstrapServers(instance, ctx, logger)
	if err != nil {
		return err
	}
	desired, err := buildNetworkPolicy(instance, kafkaClusterName, kafkaBootstrapServer)
	if err != nil {
		return err
	}
	if err := ctrl.SetControllerReference(instance, desired, r.Scheme); err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller for NetworkPolicy")
		return err
	}

	if errors.IsNotFound(getErr) {
		logger.Info("Creating NetworkPolicy", "NetworkPolicy.Name", name.Name)
		return r.Create(ctx, desired)
	}
	if getErr != nil {
		return getErr
	}

	if !networkPoliciesEqual(existing, desired) {
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		// Annotations are user-managed and preserved: assigning the desired
		// (always nil) annotations wiped any annotations set on the existing
		// policy (B8).
		logger.Info("NetworkPolicy changed, updating", "NetworkPolicy.Name", name.Name)
		return r.Update(ctx, existing)
	}

	return nil
}

// createTLSSecret builds the JKS keystore secret for the Schema Registry REST
// API TLS endpoint. It returns the desired secret and a bool indicating
// whether the secret already exists (callers must Update instead of Create in
// that case). The existing secret is never deleted: updating in place keeps
// the secret present for pods mounting it, avoiding the window where a
// delete-then-create left the secret absent and pod mounts failed.
func (r *StrimziSchemaRegistryReconciler) createTLSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string) (*v1.Secret, bool, error) {
	logger.Info("Creating secret for schema registry TLS")
	clusterCertSecret := &v1.Secret{}
	clusterKeySecret := &v1.Secret{}
	logger.V(1).Info("Searching for cluster CA cert secret", "Secret", clusterName+clusterCASuffix)
	err := r.Get(ctx, types.NamespacedName{Name: clusterName + clusterCASuffix, Namespace: instance.Namespace},
		clusterCertSecret)
	if err != nil {
		return nil, false, err
	}
	logger.V(1).Info("Searching for cluster CA key secret", "Secret", clusterName+clusterCAKeySuffix)
	err = r.Get(ctx, types.NamespacedName{Name: clusterName + clusterCAKeySuffix, Namespace: instance.Namespace},
		clusterKeySecret)
	if err != nil {
		return nil, false, err
	}
	clusterCert := string(clusterCertSecret.Data["ca.crt"])
	clusterKey := string(clusterKeySecret.Data["ca.key"])
	jksTLSSecretName := instance.Name + tlsSecretSuffix
	existing := &v1.Secret{}
	exists := false
	err = r.Get(ctx, types.NamespacedName{Name: jksTLSSecretName, Namespace: instance.Namespace}, existing)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get schema registry TLS secret")
		return nil, false, err
	}
	if err == nil {
		exists = true
	}
	logger.Info("Creating keystore for TLS secret", "Secret Name", jksTLSSecretName)
	cp := certprocessor.NewCertProcessor(logger)
	TLSKeystore, TLSKeystorePassword, err := cp.GenerateTLSforHTTP(ctx, clusterCert, clusterKey, "",
		instance.Name, instance.Namespace)
	if err != nil {
		return nil, false, err
	}
	// F2: record the signing cluster CA cert secret's resourceVersion on the
	// -tls secret. handleTLSCertExpiry compares it with the current CA
	// secret's version so a cluster CA rotation renews the REST API
	// certificate in every auth mode, not only mTLS.
	jksTLSSecret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jksTLSSecretName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"app":  "strimzi-schema-registry",
				"user": instance.Name,
			},
			Annotations: map[string]string{
				CAVersionKey: clusterCertSecret.ResourceVersion,
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  []byte(TLSKeystore),
			"keystore_password": []byte(TLSKeystorePassword),
			"key_password":      []byte(TLSKeystorePassword),
		},
	}
	if exists {
		// Carry the ResourceVersion so callers can Update the secret in place.
		jksTLSSecret.ResourceVersion = existing.ResourceVersion
	}
	err = ctrl.SetControllerReference(instance, jksTLSSecret, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, false, err
	}
	return jksTLSSecret, exists, nil
}

// computeSpecHash computes a hash of the relevant spec fields to detect changes.
// The hash covers CompatibilityLevel, SecureHTTP, ClientAuth, HeapOpts, Listener,
// SecurityProtocol, TLSSecretName, KafkastoreTopic, KafkastoreTopicReplicationFactor,
// SchemaRegistryGroupID, MasterEligibility, Configuration, and the full PodTemplateSpec —
// all fields that affect the deployment spec or service ports.
//
// Service passthrough annotations/labels and NetworkPolicy enablement are
// deliberately excluded: they change the Service or NetworkPolicy objects but
// never the Schema Registry pods, and both are reconciled independently, so
// including them would cause full pod restarts on unrelated metadata edits.
//
// FNV-64a is used instead of the former FNV-32 to reduce the collision
// probability across spec versions (64-bit vs 32-bit digest). Changing the
// algorithm rewrites every stored hash value once, which triggers a one-time
// rollout of existing Schema Registry deployments on operator upgrade.
// writeHash appends s to the fnv hash. Writes to an in-memory fnv hash
// cannot fail, so the former per-write error branches are gone.
func writeHash(h hash.Hash64, s string) {
	_, _ = io.WriteString(h, s)
}

// writeHashf appends the formatted value to the fnv hash.
func writeHashf(h hash.Hash64, format string, args ...any) {
	_, _ = fmt.Fprintf(h, format, args...)
}

// computeSpecHash computes a hash of the relevant spec fields to detect changes.
// The hash covers CompatibilityLevel, SecureHTTP, ClientAuth, HeapOpts, Listener,
// SecurityProtocol, TLSSecretName, KafkastoreTopic, KafkastoreTopicReplicationFactor,
// SchemaRegistryGroupID, MasterEligibility, Configuration, and the full PodTemplateSpec —
// all fields that affect the deployment spec or service ports.
//
// Service passthrough annotations/labels and NetworkPolicy enablement are
// deliberately excluded: they change the Service or NetworkPolicy objects but
// never the Schema Registry pods, and both are reconciled independently, so
// including them would cause full pod restarts on unrelated metadata edits.
//
// FNV-64a is used instead of the former FNV-32 to reduce the collision
// probability across spec versions (64-bit vs 32-bit digest). Changing the
// algorithm rewrites every stored hash value once, which triggers a one-time
// rollout of existing Schema Registry deployments on operator upgrade.
func computeSpecHash(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) string {
	h := fnv.New64a()

	writeHash(h, string(instance.Spec.CompatibilityLevel))

	writeHashf(h, "%t", instance.Spec.SecureHTTP)

	writeHashf(h, "%t", instance.Spec.ClientAuth)

	writeHash(h, instance.Spec.HeapOpts)

	writeHash(h, instance.Spec.Listener)

	writeHash(h, instance.Spec.SecurityProtocol)

	writeHash(h, instance.Spec.TLSSecretName)

	// The keystore type changes the rendered keystore location and the
	// SCHEMA_REGISTRY_SSL_KEYSTORE_TYPE env var, so it participates in the
	// hash using the same effective value buildPodEnv emits.
	writeHash(h, effectiveKeystoreType(instance))

	// Storage and identity fields are hashed using the same effective/defaulted
	// values that buildPodEnv emits, so hashing stays deterministic and in sync.
	kafkastoreTopic := instance.Spec.KafkastoreTopic
	if kafkastoreTopic == "" {
		kafkastoreTopic = "registry-schemas"
	}
	writeHash(h, kafkastoreTopic)

	if instance.Spec.KafkastoreTopicReplicationFactor != nil {
		writeHashf(h, "%d", *instance.Spec.KafkastoreTopicReplicationFactor)
	}

	schemaRegistryGroupID := instance.Spec.SchemaRegistryGroupID
	if schemaRegistryGroupID == "" {
		schemaRegistryGroupID = "schema-registry"
	}
	writeHash(h, schemaRegistryGroupID)

	masterEligibility := true
	if instance.Spec.MasterEligibility != nil {
		masterEligibility = *instance.Spec.MasterEligibility
	}
	writeHashf(h, "%t", masterEligibility)

	// The replica count is part of the desired Deployment spec (B20), so
	// changing spec.replicas must trigger a deployment update. Hashed using
	// the same effective value buildDeploymentSpec emits, so an explicit 1
	// and an unset replicas (both meaning 1) hash identically and never
	// cause a spurious rollout.
	writeHashf(h, "%d", effectiveReplicas(instance))

	// Configuration entries are hashed in sorted key order so the hash is
	// deterministic regardless of map iteration order.
	for _, key := range sortedConfigurationKeys(instance.Spec.Configuration) {
		writeHashf(h, "%s=%s", key, instance.Spec.Configuration[key])
	}

	// Include the full PodTemplateSpec so that container image, resources,
	// and other template changes trigger a deployment update.
	templateJSON, err := json.Marshal(instance.Spec.Template)
	if err != nil {
		// json.Marshal of a PodTemplateSpec cannot fail (no channels,
		// funcs or cycles); fall back to the zero hash rather than
		// propagating an impossible error.
		return "0"
	}
	_, _ = h.Write(templateJSON)

	return fmt.Sprintf("%d", h.Sum64())
}

// updateExistingDeployment updates an existing deployment when the spec has changed.
// It generates the desired deployment spec (without side effects), compares the spec hash
// annotation, and updates the deployment spec + triggers a rollout via annotation change
// if needed.
func (r *StrimziSchemaRegistryReconciler) updateExistingDeployment(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, found *apps.Deployment,
) (*apps.Deployment, bool, error) {
	// F1: the Deployment selector is immutable at the API server. A
	// Deployment created before the stable-selector fix carries mutable
	// labels (app.kubernetes.io/version, strimzi.io/cluster) in its selector,
	// so any image or cluster-label change makes the desired selector differ
	// and the in-place update is rejected with "spec.selector: field is
	// immutable". The selector cannot be rewritten: delete and re-create the
	// Deployment in this reconcile instead. The check runs before the
	// spec-hash short-circuit so the migration happens even in steady state.
	if !deploymentSelectorMatches(found, instance.Name) {
		return r.recreateDeploymentForLegacySelector(instance, ctx, logger, found)
	}

	// Get Kafka bootstrap server and cluster name (read-only, no side effects)
	kafkaBootstrapServer, kafkaClusterName, err := r.getKafkaBootstrapServers(instance, ctx, logger)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get Kafka bootstrap servers: %w", err)
	}

	// Resolve the Kafka auth mode once (read-only, no side effects)
	kafkaUser, err := r.getKafkaUser(instance, ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get KafkaUser: %w", err)
	}
	auth, err := resolveKafkaAuth(instance, kafkaUser)
	if err != nil {
		return nil, false, fmt.Errorf("failed to resolve Kafka auth mode: %w", err)
	}

	// Read the existing JKS secret ResourceVersion only when TLS material is
	// required (read-only, no side effects).
	jksResourceVersion := ""
	if auth.NeedsTruststore || auth.NeedsKeystore || clientAuthNeedsTruststoreSecret(instance, auth) {
		existingJKS := &v1.Secret{}
		err = r.Get(ctx, types.NamespacedName{Name: instance.Name + jksSecretSuffix, Namespace: instance.Namespace}, existingJKS)
		if err != nil && !errors.IsNotFound(err) {
			return nil, false, fmt.Errorf("failed to get existing JKS secret: %w", err)
		}
		if err == nil {
			jksResourceVersion = existingJKS.ResourceVersion
		}
	}

	// Read the KafkaUser secret ResourceVersion for SASL modes so the
	// annotation-based rollout marker is preserved across spec updates.
	saslUserResourceVersion := ""
	if auth.NeedsSASL {
		saslUserResourceVersion, err = r.getSASLUserSecretVersion(instance, ctx, logger)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get KafkaUser secret version: %w", err)
		}
	}

	// Determine TLSSecretName without creating any secrets
	TLSSecretName := ""
	tlsResourceVersion := ""
	if instance.Spec.SecureHTTP {
		if instance.Spec.TLSSecretName != "" {
			TLSSecretName = instance.Spec.TLSSecretName
			// Track the user-provided REST API TLS secret version so rotating
			// the secret triggers a rollout. Reconcile validates the secret
			// before this point, so it must exist here.
			userTLSSecret := &v1.Secret{}
			if err := r.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: instance.Namespace}, userTLSSecret); err != nil {
				return nil, false, fmt.Errorf("failed to get user-provided TLS secret: %w", err)
			}
			tlsResourceVersion = userTLSSecret.ResourceVersion
			// A standard kubernetes.io/tls (PEM) secret is converted to a JKS
			// keystore in an operator-managed derived secret, which is what
			// the pod mounts and references. This also self-heals a deleted
			// or stale derived secret during normal reconciles.
			if isPEMTLSSecret(userTLSSecret) {
				if err := r.ensureDerivedJKSSecret(ctx, instance, userTLSSecret, logger); err != nil {
					return nil, false, fmt.Errorf("failed to ensure derived JKS secret: %w", err)
				}
				TLSSecretName = derivedJKSSecretName(instance)
			}
		} else {
			TLSSecretName = instance.Name + tlsSecretSuffix
			// Record the operator-managed REST API TLS secret version so the
			// tlsVersion rollout annotation survives spec updates (instead of
			// being dropped, which would make the next renewal check see a
			// missing marker and roll the pods again) and the annotation-
			// driven renewal check compares against a recorded value. A
			// missing secret (recreation pending) records nothing.
			tlsSecret := &v1.Secret{}
			if err := r.Get(ctx, types.NamespacedName{Name: TLSSecretName, Namespace: instance.Namespace}, tlsSecret); err != nil && !errors.IsNotFound(err) {
				return nil, false, fmt.Errorf("failed to get operator-managed TLS secret: %w", err)
			} else if err == nil {
				tlsResourceVersion = tlsSecret.ResourceVersion
			}
		}
	}

	// Generate desired deployment spec — NO side effects (no secret creation/deletion)
	desired, configOverridden, err := r.buildDeploymentSpec(instance, kafkaBootstrapServer, kafkaClusterName, jksResourceVersion, saslUserResourceVersion, tlsResourceVersion, TLSSecretName, auth, logger)
	if err != nil {
		return nil, false, err
	}

	// Preserve the operator-managed rollout markers the desired build cannot
	// determine because their secret is currently missing (recreation
	// pending in the rotation handlers). buildDeploymentSpec records the
	// secret's ResourceVersion whenever the secret exists, so a spec patch
	// would otherwise drop the marker — and the next rotation check would
	// see it missing and roll the pods a second time. When the secret
	// exists, the desired value (the current ResourceVersion) is fresher and
	// wins, so a pending rollout is never suppressed.
	if instance.Spec.SecureHTTP && instance.Spec.TLSSecretName == "" && tlsResourceVersion == "" {
		preservePodTemplateAnnotation(found, desired, tlsVersionKey)
	}
	if jksResourceVersion == "" &&
		(auth.NeedsTruststore || auth.NeedsKeystore || clientAuthNeedsTruststoreSecret(instance, auth)) {
		preservePodTemplateAnnotation(found, desired, jksVersionKey)
	}

	// Compute desired spec hash
	desiredHash := computeSpecHash(instance)

	// Get existing hash from annotation
	existingAnnotations := found.GetAnnotations()
	if existingAnnotations == nil {
		existingAnnotations = make(map[string]string)
	}
	existingHash := existingAnnotations[keyPrefix+"/specHash"]

	// If hash matches, no spec change is detected from the CR alone. The
	// rendered deployment also depends on live cluster inputs the spec hash
	// cannot see: the Kafka bootstrap address comes from the Kafka CR's
	// status. When the listener is rebuilt (for example the Kafka cluster was
	// recreated with a different bootstrap service or port), only this env
	// var changes; without the comparison below the pods would keep the
	// stale bootstrap address forever, because the Kafka watch re-enqueues
	// reconciliation but every pass short-circuits on the unchanged hash.
	// The Deployment labels are compared too: they carry the image-derived
	// version and the strimzi.io/cluster label, neither of which
	// participates in the spec hash, so a CR label change must still
	// propagate to the Deployment metadata.
	bootstrapDrift := false
	if current := deploymentBootstrapServers(found); current != "" && current != kafkaBootstrapServer {
		// The comparison only applies when the deployment actually carries
		// the operator-managed env var: an absent variable (a deployment
		// predating the check, or stripped by hand) must not be treated as
		// drift, which would roll pods on the very first reconcile after an
		// operator upgrade.
		bootstrapDrift = true
	}
	labelsDrifted := !equality.Semantic.DeepEqual(found.Labels, desired.Labels)
	// The live spec is compared against the desired spec on every reconcile —
	// even when the hash matches — so manual edits to operator-managed fields
	// (image, env, resources, probes, volumes, labels, annotations) are
	// reverted instead of being ignored until the next CR change.
	pinReplicas := instance.Spec.Replicas != nil
	specDrift := deploymentSpecDrifted(found, desired, pinReplicas)

	if existingHash == desiredHash && !bootstrapDrift && !labelsDrifted && !specDrift {
		// Steady state: do NOT emit ConfigKeyOverridden events here. The
		// desired spec is rebuilt on every reconcile, so emitting on each
		// pass would spam a Warning event per reserved configuration key
		// per reconcile. The user was warned when the key was first
		// applied (createDeployment and spec-change paths below).
		return found, false, nil
	}
	if existingHash != desiredHash {
		logger.Info(deploymentUpdateReason(existingHash, desiredHash),
			"oldHash", existingHash, "newHash", desiredHash)
	} else if bootstrapDrift {
		logger.Info(deploymentUpdateReason(existingHash, desiredHash),
			"oldBootstrapServers", deploymentBootstrapServers(found), "newBootstrapServers", kafkaBootstrapServer)
	} else if specDrift {
		logger.Info("Deployment spec drifted from the desired spec; updating")
	} else {
		logger.Info("Deployment labels drifted from the desired labels; updating")
	}

	// The CR spec actually changed: warn about operator-managed keys that
	// were ignored while building the desired spec (B2). A labels-only,
	// bootstrap-only or manual-edit drift does not touch the CR
	// configuration, so the events are not repeated for it.
	if existingHash != desiredHash || bootstrapDrift {
		r.emitConfigOverriddenEvents(instance, configOverridden)
	}

	// Patch only the drifted fields instead of replacing the whole spec.
	// When the CR sets spec.replicas (B20), the desired replica count wins
	// and a manual `kubectl scale` is reverted on the next spec change. When
	// the CR leaves replicas unset, the operator still never desires a
	// specific replica count: the live Spec.Replicas — set by manual
	// `kubectl scale` edits — must survive the update instead of being reset
	// (B6). The pod template is replaced only when it drifted; the rollout
	// markers the desired build could not determine were preserved into it
	// above, so the patch never clobbers or spuriously resets them.
	if pinReplicas && !equality.Semantic.DeepEqual(found.Spec.Replicas, desired.Spec.Replicas) {
		found.Spec.Replicas = desired.Spec.Replicas
	}
	if !podTemplateEqual(&found.Spec.Template, &desired.Spec.Template) {
		found.Spec.Template = *desired.Spec.Template.DeepCopy()
	}

	// Sync the Deployment labels so CR label changes (the strimzi.io/cluster
	// label and the image-derived version label) propagate to the
	// Deployment metadata instead of lingering stale forever.
	found.Labels = desired.Labels

	// Update annotations to trigger rollout
	if found.Spec.Template.Annotations == nil {
		found.Spec.Template.Annotations = make(map[string]string)
	}
	found.Spec.Template.Annotations[keyPrefix+"/specHash"] = desiredHash

	// Also update top-level annotations
	if found.Annotations == nil {
		found.Annotations = make(map[string]string)
	}
	found.Annotations[keyPrefix+"/specHash"] = desiredHash

	return found, true, nil
}

// preservePodTemplateAnnotation carries an existing pod-template annotation
// value from the live Deployment into the desired spec, so a spec patch does
// not drop a rollout marker the desired build could not determine.
func preservePodTemplateAnnotation(found, desired *apps.Deployment, key string) {
	if found.Spec.Template.Annotations == nil {
		return
	}
	value, ok := found.Spec.Template.Annotations[key]
	if !ok || value == "" {
		return
	}
	if desired.Spec.Template.Annotations == nil {
		desired.Spec.Template.Annotations = make(map[string]string)
	}
	desired.Spec.Template.Annotations[key] = value
}

// deploymentSpecDrifted reports whether the live Deployment spec differs from
// the desired spec in fields the operator manages: the replica count (only
// when the CR pins it — an unpinned CR never desires a specific count, so a
// manual `kubectl scale` survives, B6) and the pod template. API-server
// defaults for fields the desired build leaves unset are ignored, so a
// freshly defaulted Deployment never reads as drifted (which would update it
// on every reconcile).
func deploymentSpecDrifted(found, desired *apps.Deployment, pinReplicas bool) bool {
	if pinReplicas && !equality.Semantic.DeepEqual(found.Spec.Replicas, desired.Spec.Replicas) {
		return true
	}
	return !podTemplateEqual(&found.Spec.Template, &desired.Spec.Template)
}

// podTemplateEqual compares the live pod template against the desired one,
// clearing API-server defaults on a copy of the live template wherever the
// desired template leaves the field unset, so the comparison only reports
// drift in fields the operator actually manages.
func podTemplateEqual(found, desired *v1.PodTemplateSpec) bool {
	live := found.DeepCopy()
	normalizePodSpecForCompare(&live.Spec, &desired.Spec)
	return equality.Semantic.DeepEqual(*live, *desired)
}

// normalizePodSpecForCompare clears API-server defaults on the live pod spec
// (modified in place; callers pass a deep copy) wherever the desired spec
// leaves the field unset.
func normalizePodSpecForCompare(live, desired *v1.PodSpec) {
	if desired.RestartPolicy == "" {
		live.RestartPolicy = ""
	}
	if desired.TerminationGracePeriodSeconds == nil {
		live.TerminationGracePeriodSeconds = nil
	}
	if desired.DNSPolicy == "" {
		live.DNSPolicy = ""
	}
	if desired.SchedulerName == "" {
		live.SchedulerName = ""
	}
	if desired.EnableServiceLinks == nil {
		live.EnableServiceLinks = nil
	}
	if desired.PreemptionPolicy == nil {
		live.PreemptionPolicy = nil
	}
	if desired.ServiceAccountName == "" {
		live.ServiceAccountName = ""
	}
	// The API server materializes an empty (non-nil) pod security context on
	// stored pod templates; the desired build leaves it nil unless the user
	// template sets one.
	if desired.SecurityContext == nil {
		live.SecurityContext = nil
	}
	normalizeContainersForCompare(live.Containers, desired.Containers)
	normalizeContainersForCompare(live.InitContainers, desired.InitContainers)
	normalizeVolumesForCompare(live.Volumes, desired.Volumes)
}

// normalizeContainersForCompare clears the API-server container defaults
// (termination message path/policy, image pull policy, probe thresholds,
// port protocols) wherever the desired container leaves them unset. A
// container count difference is genuine drift and needs no normalization.
func normalizeContainersForCompare(live, desired []v1.Container) {
	if len(live) != len(desired) {
		return
	}
	for i := range desired {
		d := &desired[i]
		l := &live[i]
		if d.TerminationMessagePath == "" {
			l.TerminationMessagePath = ""
		}
		if d.TerminationMessagePolicy == "" {
			l.TerminationMessagePolicy = ""
		}
		if d.ImagePullPolicy == "" {
			l.ImagePullPolicy = ""
		}
		normalizeProbeForCompare(l.ReadinessProbe, d.ReadinessProbe)
		normalizeProbeForCompare(l.LivenessProbe, d.LivenessProbe)
		normalizeProbeForCompare(l.StartupProbe, d.StartupProbe)
		for j := range d.Ports {
			if j < len(l.Ports) && d.Ports[j].Protocol == "" && l.Ports[j].Protocol == v1.ProtocolTCP {
				l.Ports[j].Protocol = ""
			}
		}
		// The API server defaults a fieldRef env source's apiVersion to "v1"
		// (the operator's SCHEMA_REGISTRY_HOST_NAME uses one).
		for j := range d.Env {
			if j >= len(l.Env) || d.Env[j].ValueFrom == nil || l.Env[j].ValueFrom == nil {
				continue
			}
			if d.Env[j].ValueFrom.FieldRef != nil && l.Env[j].ValueFrom.FieldRef != nil &&
				d.Env[j].ValueFrom.FieldRef.APIVersion == "" {
				l.Env[j].ValueFrom.FieldRef.APIVersion = ""
			}
		}
	}
}

// normalizeProbeForCompare clears the API-server probe defaults (success
// threshold 1, timeout 1s, period 10s, failure threshold 3) wherever the
// desired probe leaves them unset.
func normalizeProbeForCompare(live, desired *v1.Probe) {
	if live == nil || desired == nil {
		return
	}
	if desired.SuccessThreshold == 0 {
		live.SuccessThreshold = 0
	}
	if desired.TimeoutSeconds == 0 {
		live.TimeoutSeconds = 0
	}
	if desired.PeriodSeconds == 0 {
		live.PeriodSeconds = 0
	}
	if desired.FailureThreshold == 0 {
		live.FailureThreshold = 0
	}
	if desired.InitialDelaySeconds == 0 {
		live.InitialDelaySeconds = 0
	}
}

// normalizeVolumesForCompare clears the API-server secret volume defaultMode
// (420) wherever the desired volume leaves it unset, for the volume source
// types the API server defaults.
func normalizeVolumesForCompare(live, desired []v1.Volume) {
	if len(live) != len(desired) {
		return
	}
	for i := range desired {
		lv := &live[i].VolumeSource
		dv := &desired[i].VolumeSource
		if lv.Secret != nil && dv.Secret != nil && dv.Secret.DefaultMode == nil {
			lv.Secret.DefaultMode = nil
		}
		if lv.ConfigMap != nil && dv.ConfigMap != nil && dv.ConfigMap.DefaultMode == nil {
			lv.ConfigMap.DefaultMode = nil
		}
		if lv.Projected != nil && dv.Projected != nil && dv.Projected.DefaultMode == nil {
			lv.Projected.DefaultMode = nil
		}
		if lv.DownwardAPI != nil && dv.DownwardAPI != nil && dv.DownwardAPI.DefaultMode == nil {
			lv.DownwardAPI.DefaultMode = nil
		}
	}
}

// deploymentSelectorMatches reports whether the existing Deployment's
// selector equals the stable identity selector the operator desires. A
// missing selector (a hand-crafted object) counts as a legacy selector and
// triggers the migration.
func deploymentSelectorMatches(found *apps.Deployment, name string) bool {
	existing := map[string]string{}
	if found.Spec.Selector != nil && found.Spec.Selector.MatchLabels != nil {
		existing = found.Spec.Selector.MatchLabels
	}
	return equality.Semantic.DeepEqual(existing, selectorLabelsForStrimziSchemaRegistryOperator(name))
}

// recreateDeploymentForLegacySelector migrates a Deployment whose selector
// differs from the desired stable identity selector. The selector field is
// immutable, so the migration cannot be an in-place update: the Deployment is
// deleted and re-created in the same reconcile via the createDeployment path,
// which bootstraps all dependent secrets exactly like a first reconcile.
// Schema Registry is stateless (its state lives in the Kafka topic), so the
// one-time rollout is safe. Returns the re-created Deployment with
// specChanged=false: the object already exists in its desired form and needs
// no further update this pass.
func (r *StrimziSchemaRegistryReconciler) recreateDeploymentForLegacySelector(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context,
	logger logr.Logger,
	found *apps.Deployment,
) (*apps.Deployment, bool, error) {
	logger.Info("Deployment selector differs from the desired stable identity selector; recreating the Deployment (one-time migration, the selector field is immutable)",
		"Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace,
		"ExistingSelector", found.Spec.Selector.MatchLabels)

	// Background propagation removes the Deployment immediately while the
	// garbage collector cleans up the ReplicaSet and pods asynchronously, so
	// the re-create below does not race the delete.
	if err := r.Delete(ctx, found, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to delete the Deployment with a legacy selector")
		return nil, false, err
	}
	dep, err := r.createDeployment(instance, ctx, logger)
	if err != nil {
		logger.Error(err, "Failed to build the replacement Deployment during the selector migration")
		return nil, false, err
	}
	if err := r.Create(ctx, dep); err != nil {
		logger.Error(err, "Failed to create the replacement Deployment during the selector migration")
		return nil, false, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(instance, nil, v1.EventTypeNormal, "DeploymentRecreated", "RecreateDeployment",
			"Deployment %s had a selector containing mutable labels (e.g. app.kubernetes.io/version, strimzi.io/cluster); the selector field is immutable, so it was deleted and re-created with the stable identity selector as a one-time migration (Schema Registry is stateless)", dep.Name)
	}
	return dep, false, nil
}

// deploymentUpdateReason returns the log message describing why an existing
// Deployment is being updated. The CR spec hash is the primary change
// detector; when it is unchanged the update is driven by the Kafka bootstrap
// address drift the hash cannot see (the address is rendered from the Kafka
// CR's status). Branching the message keeps the log truthful: logging a hash
// change with equal old/new hashes would be misleading (F4).
func deploymentUpdateReason(existingHash, desiredHash string) string {
	if existingHash != desiredHash {
		return "Spec hash changed, updating deployment"
	}
	return "Kafka bootstrap address changed, updating deployment"
}

// deploymentBootstrapServers returns the value of the operator-managed
// SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS env var on the deployment's
// first container, or an empty string when the container or the variable is
// absent. It is used to detect bootstrap drift the spec hash cannot see.
func deploymentBootstrapServers(dep *apps.Deployment) string {
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == bootstrapServersEnv {
			return e.Value
		}
	}
	return ""
}

// mustParseQuantity parses a resource quantity string, panicking on failure.
// Used for compile-time known resource values (CPU/memory defaults).
func mustParseQuantity(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic("invalid resource quantity " + s + ": " + err.Error())
	}
	return q
}
