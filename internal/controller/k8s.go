package controller

import (
	"context"
	"encoding/json"
	go_err "errors"
	"fmt"
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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Resource name suffixes used throughout the operator.
const (
	jksSecretSuffix    = "-jks"
	tlsSecretSuffix    = "-tls"
	deploySuffix       = "-deploy"
	clusterCASuffix    = "-cluster-ca-cert"
	clusterCAKeySuffix = "-cluster-ca"
)

// labelsForStrimziSchemaRegistryOperator returns the standard set of labels
// for resources managed by the Schema Registry operator.
func labelsForStrimziSchemaRegistryOperator(name, image, kafkaClusterName string) map[string]string {
	return map[string]string{
		"app":                          name,
		"strimzi-schema-registry":      name,
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "strimzi-registry-operator",
		"app.kubernetes.io/name":       "strimzischemaregistry",
		"app.kubernetes.io/part-of":    name,
		"app.kubernetes.io/version": func() string {
			parts := strings.Split(image, ":")
			if len(parts) > 1 {
				last := parts[len(parts)-1]
				if !strings.Contains(last, "/") {
					return last
				}
			}
			return "latest"
		}(),
		"strimzi.io/cluster": kafkaClusterName,
	}
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
			err = r.Create(ctx, secret)
			if err != nil {
				logger.Error(err, "Failed to create secret", "Secret.Name", instance.Name+jksSecretSuffix)
				return nil, err
			}
			jksResourceVersion = secret.ResourceVersion
			logger.V(1).Info("Secret for Schema Registry KafkaStore TLS created successfully", "Secret.Name", secret.Name)
		} else {
			// createSecret returns the existing up-to-date secret instead of nil.
			jksResourceVersion = secret.ResourceVersion
		}
	}

	// Schema Registry REST API TLS secret
	var TLSSecretName string
	if instance.Spec.SecureHTTP {
		if instance.Spec.TLSSecretName == "" {
			TLSSecret, err := r.createTLSSecret(instance, ctx, logger, kafkaClusterName)
			if err != nil {
				logger.Error(err, "Failed to format TLS secret")
				return nil, err
			}
			if TLSSecret != nil {
				err = r.Create(ctx, TLSSecret)
				if err != nil {
					logger.Error(err, "Failed to create TLS secret")
					return nil, err
				}
				logger.Info("Secret for Schema Registry TLS created successfully", "Secret.Name", TLSSecret.Name)
			}
			TLSSecretName = TLSSecret.Name
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

	dep, configOverridden, err := r.buildDeploymentSpec(instance, kafkaBootstrapServer, kafkaClusterName, jksResourceVersion, saslUserResourceVersion, TLSSecretName, auth, logger)
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
	TLSSecretName string,
	auth kafkaAuth,
	logger logr.Logger,
) (*apps.Deployment, []string, error) {
	podSpec := instance.Spec.Template
	// Validate that at least one container exists
	if len(podSpec.Spec.Containers) == 0 {
		return nil, nil, fmt.Errorf("template must contain at least one container")
	}

	// Build pod environment, volumes, and probes
	podEnv, configOverridden := buildPodEnv(instance, kafkaBootstrapServer, TLSSecretName, auth)
	podVolume, containerVolumeMount := buildPodVolumes(instance, TLSSecretName, auth)

	listenerPort, scheme := listenerPortAndScheme(instance)
	readinessProbe, livenessProbe, startupProbe := buildProbes(listenerPort, scheme)

	podSpec.Spec.Containers[0].ReadinessProbe = readinessProbe
	podSpec.Spec.Containers[0].LivenessProbe = livenessProbe
	podSpec.Spec.Containers[0].StartupProbe = startupProbe

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
	podSpec.Spec.Containers[0].Env = podEnv
	podSpec.Spec.Containers[0].VolumeMounts = containerVolumeMount
	podSpec.Spec.Volumes = podVolume
	ls := labelsForStrimziSchemaRegistryOperator(instance.Name, instance.Spec.Template.Spec.Containers[0].Image, kafkaClusterName)
	podSpec.Labels = ls
	podSpec.Annotations = map[string]string{keyPrefix + "/jksVersion": jksResourceVersion}
	// Track the KafkaUser secret version for SASL modes so credential changes
	// trigger a rollout without rebuilding any JKS material.
	if auth.NeedsSASL {
		podSpec.Annotations[saslUserVersionKey] = saslUserResourceVersion
	}

	// Compute spec hash and store it in annotation to detect spec changes on subsequent reconciliations.
	specHash, err := computeSpecHash(instance)
	if err != nil {
		logger.Error(err, "Failed to calculate CR spec hash")
		return nil, nil, err
	}
	podSpec.Annotations[keyPrefix+"/specHash"] = specHash

	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + deploySuffix,
			Namespace: instance.Namespace,
			Labels:    ls,
			Annotations: map[string]string{
				keyPrefix + "/specHash": specHash,
			},
		},
		Spec: apps.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: podSpec,
		},
	}
	// Set StrimziSchemaRegistry instance as the owner and controller
	err = ctrl.SetControllerReference(instance, dep, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, nil, err
	}
	return dep, configOverridden, nil
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

// reservedConfigurationKeys are Confluent Schema Registry properties managed by
// the operator and therefore not settable through spec.configuration.
var reservedConfigurationKeys = map[string]struct{}{
	"kafkastore.bootstrap.servers":            {},
	"kafkastore.security.protocol":            {},
	"listeners":                               {},
	"host.name":                               {},
	"kafkastore.topic":                        {},
	"schema.registry.group.id":                {},
	"master.eligibility":                      {},
	"schema.compatibility.level":              {},
	"heap.opts":                               {},
	"schema.registry.inter.instance.protocol": {},
	"ssl.key.password":                        {},
}

// reservedConfigurationPrefixes are property key prefixes managed by the operator.
var reservedConfigurationPrefixes = []string{
	"kafkastore.ssl.",
	"kafkastore.sasl.",
	"ssl.keystore.",
}

// isReservedConfigurationKey reports whether the given Confluent property key is
// operator-managed and must be ignored when supplied through spec.configuration.
func isReservedConfigurationKey(key string) bool {
	if _, ok := reservedConfigurationKeys[key]; ok {
		return true
	}
	for _, prefix := range reservedConfigurationPrefixes {
		if strings.HasPrefix(key, prefix) {
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
		podEnv = append(podEnv,
			v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "https://0.0.0.0:8085"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL", Value: "https"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", Value: "/var/rest-api-tls/tls-keystore.jks"},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: TLSSecretName,
					},
					Key: "keystore_password",
				},
			}},
			v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEY_PASSWORD", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: TLSSecretName,
					},
					Key: "key_password",
				},
			}},
		)
	} else {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "http://0.0.0.0:8081"})
	}

	podEnv, overridden := appendConfigurationEnv(podEnv, instance.Spec.Configuration)
	return podEnv, overridden
}

// buildPodVolumes constructs the volumes and volume mounts for the deployment.
func buildPodVolumes(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, TLSSecretName string, auth kafkaAuth) ([]v1.Volume, []v1.VolumeMount) {
	var defaultMode int32 = 420
	var containerVolumeMount []v1.VolumeMount
	var podVolume []v1.Volume

	// KafkaStore JKS secret volume is only needed when TLS transport or mTLS
	// keystore material is required (SSL and SASL_SSL).
	if auth.NeedsTruststore || auth.NeedsKeystore {
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
			Name:      "rest-api-tls",
			MountPath: "/var/rest-api-tls",
			ReadOnly:  true,
		})
		podVolume = append(podVolume, v1.Volume{
			Name: "rest-api-tls",
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
// When SecureHTTP is enabled (scheme == HTTPS), readiness and startup probes use TCPSocket
// instead of HTTPGet. This avoids TLS certificate verification failures: Kubernetes kubelet
// does not support insecure-skip-verify for HTTPGet probes, so HTTPS probes against self-signed
// certificates (generated from the Strimzi cluster CA) would fail perpetually.
// The liveness probe already uses TCPSocket unconditionally for the same reason.
func buildProbes(listenerPort int32, scheme v1.URIScheme) (*v1.Probe, *v1.Probe, *v1.Probe) {
	var readinessHandler, startupHandler v1.ProbeHandler

	if scheme == v1.URISchemeHTTPS {
		// Self-signed cert: TCPSocket avoids TLS verification failures.
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

	startupProbe := &v1.Probe{
		ProbeHandler:        startupHandler,
		InitialDelaySeconds: 30,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}

	return readinessProbe, livenessProbe, startupProbe
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
	return "", "", go_err.New("cant find bootstrap address")
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
		return r.createSASLTruststoreSecret(instance, ctx, logger, clusterName, clusterCASecret)
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
// up-to-date according to isUpToDate. When it exists but is stale it is deleted
// so the caller can recreate it.
func (r *StrimziSchemaRegistryReconciler) existingJKSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, isUpToDate func(annotations map[string]string) bool) (*v1.Secret, bool, error) {
	jks_secret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + jksSecretSuffix, Namespace: instance.Namespace}, jks_secret)
	if err == nil {
		if isUpToDate(jks_secret.Annotations) {
			logger.V(1).Info("JKS secret is up-to-date")
			// Return the existing secret (not nil) so the caller has it for
			// updateDeployment without needing to re-fetch.
			return jks_secret, true, nil
		}
		logger.V(1).Info("About to delete JKS secret")
		if err := r.Delete(ctx, jks_secret); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get schema registry secret")
		return nil, false, err
	}
	return nil, false, nil
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

	jks_secret, upToDate, err := r.existingJKSSecret(instance, ctx, logger, func(annotations map[string]string) bool {
		return annotations[CAVersionKey] == clusterSecret.ResourceVersion &&
			annotations[userVersionKey] == userSecret.ResourceVersion
	})
	if err != nil {
		return nil, false, err
	}
	if upToDate {
		return jks_secret, false, nil
	}

	jks_secret_name := instance.Name + jksSecretSuffix
	logger.Info("Creating new keystore and truststore", "Secret Name", jks_secret_name)
	cp := certprocessor.NewCertProcessor(logger)
	truststore, truststore_password, err := cp.CreateTruststore(clusterCACert, "")
	if err != nil {
		return nil, false, err
	}
	keystore, keystore_password, err := cp.CreateKeystore(clientCACert, clientCert, clientKey, clientp12, userPassword)
	if err != nil {
		return nil, false, err
	}
	jks_secret = &v1.Secret{
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
	return jks_secret, true, nil
}

// createSASLTruststoreSecret creates or returns an up-to-date JKS secret
// containing only the truststore (built from the cluster CA) for SASL_SSL.
func (r *StrimziSchemaRegistryReconciler) createSASLTruststoreSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string, clusterCASecret *v1.Secret) (*v1.Secret, bool, error) {
	logger.Info("Creating truststore secret for schema registry Kafkastore SASL_SSL")

	clusterSecret, err := r.getClusterCASecret(instance, ctx, logger, clusterName, clusterCASecret)
	if err != nil {
		return nil, false, err
	}
	clusterCACert := string(clusterSecret.Data["ca.crt"])

	jks_secret, upToDate, err := r.existingJKSSecret(instance, ctx, logger, func(annotations map[string]string) bool {
		return annotations[CAVersionKey] == clusterSecret.ResourceVersion
	})
	if err != nil {
		return nil, false, err
	}
	if upToDate {
		return jks_secret, false, nil
	}

	jks_secret_name := instance.Name + jksSecretSuffix
	logger.Info("Creating new truststore", "Secret Name", jks_secret_name)
	cp := certprocessor.NewCertProcessor(logger)
	truststore, truststore_password, err := cp.CreateTruststore(clusterCACert, "")
	if err != nil {
		return nil, false, err
	}
	jks_secret = &v1.Secret{
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
	return jks_secret, true, nil
}

func (r *StrimziSchemaRegistryReconciler) createService(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, logger logr.Logger) (*v1.Service, error) {
	var port []v1.ServicePort
	logger.Info("Creating a new Service", "Service.Namespace", instance.Namespace, "Service.Name", instance.Name)
	if !instance.Spec.SecureHTTP {
		port = append(port, v1.ServicePort{Name: "http", Protocol: "TCP", Port: 80, TargetPort: intstr.IntOrString{IntVal: 8081}})
	} else {
		port = append(port, v1.ServicePort{Name: "https", Protocol: "TCP", Port: 443, TargetPort: intstr.IntOrString{IntVal: 8085}})
	}
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    instance.Labels,
			Annotations: map[string]string{
				"type": port[0].Name,
			},
		},
		Spec: v1.ServiceSpec{
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

func (r *StrimziSchemaRegistryReconciler) createTLSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, clusterName string) (*v1.Secret, error) {
	logger.Info("Creating secret for schema registry TLS")
	clusterCertSecret := &v1.Secret{}
	clusterKeySecret := &v1.Secret{}
	logger.V(1).Info("Searching for cluster CA cert secret", "Secret", clusterName+clusterCASuffix)
	err := r.Get(ctx, types.NamespacedName{Name: clusterName + clusterCASuffix, Namespace: instance.Namespace},
		clusterCertSecret)
	if err != nil {
		return nil, err
	}
	logger.V(1).Info("Searching for cluster CA key secret", "Secret", clusterName+clusterCAKeySuffix)
	err = r.Get(ctx, types.NamespacedName{Name: clusterName + clusterCAKeySuffix, Namespace: instance.Namespace},
		clusterKeySecret)
	if err != nil {
		return nil, err
	}
	clusterCert := string(clusterCertSecret.Data["ca.crt"])
	clusterKey := string(clusterKeySecret.Data["ca.key"])
	jksTLSSecret := &v1.Secret{}
	jksTLSSecretName := instance.Name + tlsSecretSuffix
	err = r.Get(ctx, types.NamespacedName{Name: jksTLSSecretName, Namespace: instance.Namespace}, jksTLSSecret)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get schema registry TLS secret")
		return nil, err
	}
	if err == nil {
		// Delete existing tls secret
		err = r.Delete(ctx, jksTLSSecret)
		if err != nil {
			return nil, err
		}
	}
	logger.Info("Creating keystore for TLS secret", "Secret Name", jksTLSSecretName)
	cp := certprocessor.NewCertProcessor(logger)
	TLSKeystore, TLSKeystorePassword, err := cp.GenerateTLSforHTTP(clusterCert, clusterKey, "",
		instance.Name+"."+instance.Namespace)
	if err != nil {
		return nil, err
	}
	jksTLSSecret = &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jksTLSSecretName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"app":  "strimzi-schema-registry",
				"user": instance.Name,
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  []byte(TLSKeystore),
			"keystore_password": []byte(TLSKeystorePassword),
			"key_password":      []byte(TLSKeystorePassword),
		},
	}
	err = ctrl.SetControllerReference(instance, jksTLSSecret, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, err
	}
	return jksTLSSecret, nil
}

func (r *StrimziSchemaRegistryReconciler) updateDeployment(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, jksSecret *v1.Secret) (*apps.Deployment, error) {
	// Update deployment
	dep := &apps.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, dep)
	if err != nil {
		logger.Error(err, "Failed to get deployment after user or cluster CA secret changed")
		return nil, err
	}
	if jksSecret == nil {
		jksSecret = &v1.Secret{}
		err = r.Get(ctx, types.NamespacedName{Name: instance.Name + jksSecretSuffix, Namespace: instance.Namespace}, jksSecret)
		if err != nil {
			logger.Error(err, "Failed to get jks secret after user or cluster CA secret changed")
			return nil, err
		}
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations[keyPrefix+"/jksVersion"] = jksSecret.ResourceVersion
	return dep, nil
}

// computeSpecHash computes a hash of the relevant spec fields to detect changes.
// The hash covers CompatibilityLevel, SecureHTTP, HeapOpts, Listener, SecurityProtocol,
// TLSSecretName, KafkastoreTopic, KafkastoreTopicReplicationFactor, SchemaRegistryGroupID,
// MasterEligibility, Configuration, and the full PodTemplateSpec — all fields that affect
// the deployment spec or service ports.
func computeSpecHash(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) (string, error) {
	h := fnv.New32a()

	if _, err := io.WriteString(h, string(instance.Spec.CompatibilityLevel)); err != nil {
		return "", fmt.Errorf("failed to write CompatibilityLevel to hash: %w", err)
	}

	if _, err := fmt.Fprintf(h, "%t", instance.Spec.SecureHTTP); err != nil {
		return "", fmt.Errorf("failed to write SecureHTTP to hash: %w", err)
	}

	if _, err := io.WriteString(h, instance.Spec.HeapOpts); err != nil {
		return "", fmt.Errorf("failed to write HeapOpts to hash: %w", err)
	}

	if _, err := io.WriteString(h, instance.Spec.Listener); err != nil {
		return "", fmt.Errorf("failed to write Listener to hash: %w", err)
	}

	if _, err := io.WriteString(h, instance.Spec.SecurityProtocol); err != nil {
		return "", fmt.Errorf("failed to write SecurityProtocol to hash: %w", err)
	}

	if _, err := io.WriteString(h, instance.Spec.TLSSecretName); err != nil {
		return "", fmt.Errorf("failed to write TLSSecretName to hash: %w", err)
	}

	// Storage and identity fields are hashed using the same effective/defaulted
	// values that buildPodEnv emits, so hashing stays deterministic and in sync.
	kafkastoreTopic := instance.Spec.KafkastoreTopic
	if kafkastoreTopic == "" {
		kafkastoreTopic = "registry-schemas"
	}
	if _, err := io.WriteString(h, kafkastoreTopic); err != nil {
		return "", fmt.Errorf("failed to write KafkastoreTopic to hash: %w", err)
	}

	if instance.Spec.KafkastoreTopicReplicationFactor != nil {
		if _, err := fmt.Fprintf(h, "%d", *instance.Spec.KafkastoreTopicReplicationFactor); err != nil {
			return "", fmt.Errorf("failed to write KafkastoreTopicReplicationFactor to hash: %w", err)
		}
	}

	schemaRegistryGroupID := instance.Spec.SchemaRegistryGroupID
	if schemaRegistryGroupID == "" {
		schemaRegistryGroupID = "schema-registry"
	}
	if _, err := io.WriteString(h, schemaRegistryGroupID); err != nil {
		return "", fmt.Errorf("failed to write SchemaRegistryGroupID to hash: %w", err)
	}

	masterEligibility := true
	if instance.Spec.MasterEligibility != nil {
		masterEligibility = *instance.Spec.MasterEligibility
	}
	if _, err := fmt.Fprintf(h, "%t", masterEligibility); err != nil {
		return "", fmt.Errorf("failed to write MasterEligibility to hash: %w", err)
	}

	// Configuration entries are hashed in sorted key order so the hash is
	// deterministic regardless of map iteration order.
	for _, key := range sortedConfigurationKeys(instance.Spec.Configuration) {
		if _, err := fmt.Fprintf(h, "%s=%s", key, instance.Spec.Configuration[key]); err != nil {
			return "", fmt.Errorf("failed to write configuration entry to hash: %w", err)
		}
	}

	// Include the full PodTemplateSpec so that container image, resources,
	// and other template changes trigger a deployment update.
	templateJSON, err := json.Marshal(instance.Spec.Template)
	if err != nil {
		return "", fmt.Errorf("failed to marshal Template to JSON for hash: %w", err)
	}
	if _, err := h.Write(templateJSON); err != nil {
		return "", fmt.Errorf("failed to write Template to hash: %w", err)
	}

	return fmt.Sprintf("%d", h.Sum32()), nil
}

// updateExistingDeployment updates an existing deployment when the spec has changed.
// It generates the desired deployment spec (without side effects), compares the spec hash
// annotation, and updates the deployment spec + triggers a rollout via annotation change
// if needed.
func (r *StrimziSchemaRegistryReconciler) updateExistingDeployment(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger, found *apps.Deployment,
) (*apps.Deployment, bool, error) {
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
	if auth.NeedsTruststore || auth.NeedsKeystore {
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
	if instance.Spec.SecureHTTP {
		if instance.Spec.TLSSecretName != "" {
			TLSSecretName = instance.Spec.TLSSecretName
		} else {
			TLSSecretName = instance.Name + tlsSecretSuffix
		}
	}

	// Generate desired deployment spec — NO side effects (no secret creation/deletion)
	desired, configOverridden, err := r.buildDeploymentSpec(instance, kafkaBootstrapServer, kafkaClusterName, jksResourceVersion, saslUserResourceVersion, TLSSecretName, auth, logger)
	if err != nil {
		return nil, false, err
	}
	r.emitConfigOverriddenEvents(instance, configOverridden)

	// Compute desired spec hash
	desiredHash, err := computeSpecHash(instance)
	if err != nil {
		return nil, false, err
	}

	// Get existing hash from annotation
	existingAnnotations := found.GetAnnotations()
	if existingAnnotations == nil {
		existingAnnotations = make(map[string]string)
	}
	existingHash := existingAnnotations[keyPrefix+"/specHash"]

	// If hash matches, no spec change detected
	if existingHash == desiredHash {
		return found, false, nil
	}

	logger.Info("Spec hash changed, updating deployment",
		"oldHash", existingHash, "newHash", desiredHash)

	// Update the deployment spec to match desired state
	found.Spec = desired.Spec

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

// mustParseQuantity parses a resource quantity string, panicking on failure.
// Used for compile-time known resource values (CPU/memory defaults).
func mustParseQuantity(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic("invalid resource quantity " + s + ": " + err.Error())
	}
	return q
}
