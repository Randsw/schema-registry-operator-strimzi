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

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	monitoring "github.com/randsw/schema-registry-operator-strimzi/metrics"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"
)

// StrimziSchemaRegistryReconciler reconciles a StrimziSchemaRegistry object
type StrimziSchemaRegistryReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder recorder.EventRecorder
}

const finalizer = "strimziregistryoperator.randsw.code/finalizer"
const legacyFinalizer = "metrics.strimziregistryoperator.randsw.code/finalizer"
const keyPrefix = "strimziregistryoperator.randsw.code"
const CAVersionKey = keyPrefix + "/caSecretVersion"
const userVersionKey = keyPrefix + "/clientSecretVersion"
const saslUserVersionKey = keyPrefix + "/saslUserSecretVersion"
const tlsVersionKey = keyPrefix + "/tlsVersion"

// tlsCertRenewalThreshold is how close to expiry the operator-managed REST
// API TLS certificate must be before it is renewed. It is a variable so
// tests can force renewal with normally-signed certificates.
var tlsCertRenewalThreshold = 30 * 24 * time.Hour

// strimziClusterLabel is the label key used by Strimzi to identify the Kafka cluster name.
const strimziClusterLabel = "strimzi.io/cluster"

// getStrimziClusterName extracts the strimzi.io/cluster label value from the instance.
// Returns an error if labels are nil or the required label is missing.
func getStrimziClusterName(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) (string, error) {
	labels := instance.GetLabels()
	if labels == nil {
		return "", fmt.Errorf("StrimziSchemaRegistry %s/%s has no labels; required label %q is missing",
			instance.Namespace, instance.Name, strimziClusterLabel)
	}
	clusterName, ok := labels[strimziClusterLabel]
	if !ok || clusterName == "" {
		return "", fmt.Errorf("StrimziSchemaRegistry %s/%s is missing required label %q",
			instance.Namespace, instance.Name, strimziClusterLabel)
	}
	return clusterName, nil
}

// clientAuthValidationError returns a non-empty message describing why the
// ClientAuth spec is invalid, or an empty string when it is valid.
func clientAuthValidationError(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) string {
	if instance.Spec.ClientAuth && !instance.Spec.SecureHTTP {
		return "spec.clientauth is only valid when spec.securehttp is true"
	}
	return ""
}

// userTLSSecretValidationError returns a non-empty message describing why the
// user-provided REST API TLS secret (spec.tlssecretname) cannot be used, or an
// empty string when it is valid. A nil secret means the secret does not exist.
// The secret must carry a keystore plus both passwords with non-empty values;
// the Schema Registry container references these exact keys. The required
// keystore key follows the keystore type: tls-keystore.jks for JKS (the
// default) and tls-keystore.p12 for PKCS12.
func userTLSSecretValidationError(secretName string, secret *v1.Secret, keystoreType string) string {
	if secret == nil {
		return fmt.Sprintf("user-provided TLS secret %q not found", secretName)
	}
	keystoreKey := "tls-keystore.jks"
	if keystoreType == keystoreTypePKCS12 {
		keystoreKey = "tls-keystore.p12"
	}
	var missing []string
	for _, key := range []string{keystoreKey, "keystore_password", "key_password"} {
		if len(secret.Data[key]) == 0 {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("user-provided TLS secret %q is missing required key(s) or has empty values: %s",
			secretName, strings.Join(missing, ", "))
	}
	return ""
}

// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kafka.strimzi.io,resources=kafkas,verbs=get;list;watch
// +kubebuilder:rbac:groups=kafka.strimzi.io,resources=kafkausers,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *StrimziSchemaRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Reconcile watch deployment, StrimziSchemaRegistry and secret managed by Strimzi operator
	logger.Info("Reconciling StrimziSchemaRegistry")
	instance := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			logger.Info("Resource not found. Ignoring since object must be deleted.")
			r.updateInstanceCountMetric(ctx)
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get StrimziSchemaRegistry. May be it is a Secret")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}
	// Snapshot the original status so we only write it back when it actually
	// changes. Writing status unconditionally bumps resourceVersion, which
	// re-triggers this reconcile forever because the controller watches its
	// own CR via For().
	origStatus := instance.Status.DeepCopy()
	// Migrate the finalizer from the legacy value to the current value, adding
	// or removing finalizers in a single update so existing objects do not end
	// up with a dangling legacy finalizer.
	if err = r.migrateFinalizer(ctx, instance, logger); err != nil {
		logger.Error(err, "Failed to migrate finalizer")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}
	isApplicationMarkedToBeDeleted := instance.GetDeletionTimestamp() != nil
	if isApplicationMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(instance, finalizer) {
			controllerutil.RemoveFinalizer(instance, finalizer)
			err := r.Update(ctx, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		r.updateInstanceCountMetric(ctx)
		return ctrl.Result{}, nil
	}

	// Validate that ClientAuth is only enabled when SecureHTTP is enabled.
	if msg := clientAuthValidationError(instance); msg != "" {
		logger.Info("Invalid ClientAuth configuration; skipping reconciliation")
		meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ClientAuthRequiresSecureHTTP",
			Message:            msg,
			ObservedGeneration: instance.Generation,
		})
		instance.Status.Status = "InvalidSpec"
		if !equality.Semantic.DeepEqual(origStatus, instance.Status) {
			if err := r.Status().Update(ctx, instance); err != nil {
				logger.Error(err, "Failed to update CR status")
				monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
				return ctrl.Result{}, err
			}
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(instance, nil, v1.EventTypeWarning, "ClientAuthRequiresSecureHTTP", "",
				"spec.clientauth is set but spec.securehttp is false; mutual TLS on the REST API will not be enabled")
		}
		return ctrl.Result{}, nil
	}

	// Validate the user-provided REST API TLS secret before building or
	// updating the Deployment. A missing secret or missing/empty required keys
	// would render a Deployment whose pods can never start (stuck in
	// CreateContainerConfigError or a Schema Registry crash loop) with no
	// operator-visible signal. Surface the failure on the CR status and via a
	// Warning event, and skip reconciliation until the secret is fixed: the
	// Secret watch re-triggers reconciliation when the user updates it.
	if instance.Spec.SecureHTTP && instance.Spec.TLSSecretName != "" {
		userTLSSecret := &v1.Secret{}
		err = r.Get(ctx, types.NamespacedName{Name: instance.Spec.TLSSecretName, Namespace: instance.Namespace}, userTLSSecret)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get user-provided TLS secret", "Secret.Name", instance.Spec.TLSSecretName)
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		var userTLSSecretObj *v1.Secret
		if err == nil {
			userTLSSecretObj = userTLSSecret
		}
		if msg := validateUserTLSSecret(instance, userTLSSecretObj); msg != "" {
			logger.Info("Invalid user-provided TLS secret; skipping reconciliation", "Secret.Name", instance.Spec.TLSSecretName, "Reason", msg)
			meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidTLSSecret",
				Message:            msg,
				ObservedGeneration: instance.Generation,
			})
			instance.Status.Status = "InvalidTLSSecret"
			if !equality.Semantic.DeepEqual(origStatus, instance.Status) {
				if err := r.Status().Update(ctx, instance); err != nil {
					logger.Error(err, "Failed to update CR status")
					monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
					return ctrl.Result{}, err
				}
			}
			if r.Recorder != nil {
				r.Recorder.Eventf(instance, nil, v1.EventTypeWarning, "InvalidTLSSecret", "", "%s", msg)
			}
			// The Secret watch re-enqueues when the user fixes the secret;
			// the requeue is only a safety net.
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}

	// Get the strimzi cluster name from the CR labels
	strimziClusterName, err := getStrimziClusterName(instance)
	if err != nil {
		logger.Error(err, "Failed to get strimzi cluster name from labels")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}

	// Handle secret rotation if user or cluster CA secrets have changed.
	result, err := r.handleSecretRotation(ctx, instance, logger, strimziClusterName)
	if err != nil {
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return result, err
	}
	if result.RequeueAfter > 0 {
		return result, nil
	}

	// Renew the operator-managed REST API TLS certificate when it is close to
	// expiry, so the Schema Registry never serves an expired certificate.
	result, err = r.handleTLSCertExpiry(ctx, instance, logger, strimziClusterName)
	if err != nil {
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return result, err
	}
	if result.RequeueAfter > 0 {
		return result, nil
	}

	// Roll the Deployment when the user-provided REST API TLS secret was
	// rotated, so the Schema Registry JVM reloads the JKS keystore.
	result, err = r.handleUserTLSSecretRotation(ctx, instance, logger)
	if err != nil {
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return result, err
	}
	if result.RequeueAfter > 0 {
		return result, nil
	}

	// Check if the Deployment already exists, if not create a new one
	found := &apps.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Deployment
		deployment, err := r.createDeployment(instance, ctx, logger)
		if err != nil {
			logger.Error(err, "Failed to create deployment")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		err = r.Create(ctx, deployment)
		if err != nil {
			logger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		logger.Info("Deployment created successfully", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Deployment")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}

	// Deployment exists — reconcile spec changes
	updatedDep, specChanged, err := r.updateExistingDeployment(instance, ctx, logger, found)
	if err != nil {
		logger.Error(err, "Failed to reconcile deployment spec changes")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}
	if specChanged {
		err = r.Update(ctx, updatedDep)
		if err != nil {
			logger.Error(err, "Failed to update deployment after spec change")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		monitoring.StrimziSchemaRegistryDeploymentUpdateTotal.Inc()
		logger.Info("Deployment updated after spec change", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	condition := readyCondition(found.Status.Replicas, found.Status.ReadyReplicas, instance.Generation)
	meta.SetStatusCondition(&instance.Status.Conditions, condition)
	if condition.Status == metav1.ConditionTrue {
		instance.Status.Status = "Ok"
	} else {
		instance.Status.Status = "NotReady"
	}
	if !equality.Semantic.DeepEqual(origStatus, instance.Status) {
		err = r.Status().Update(ctx, instance)
		if err != nil {
			logger.Error(err, "Failed to update CR Status")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
	}
	// Creating service for deployment
	foundSvc := &v1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, foundSvc)
	if err != nil && errors.IsNotFound(err) {
		svc, err := r.createService(instance, logger)
		if err != nil {
			logger.Error(err, "Failed to create service")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		err = r.Create(ctx, svc)
		if err != nil {
			logger.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		// Service created successfully - return and requeue
		logger.Info("Service created successfully", "Service.Name", svc.Name, "Service.Namespace", svc.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Service")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}

	// Service exists — reconcile spec changes (e.g., SecureHTTP changed)
	desiredSvc, err := r.createService(instance, logger)
	if err != nil {
		logger.Error(err, "Failed to create desired service spec")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}

	// Compare service type, ports, labels, and annotations to detect changes.
	serviceChanged := foundSvc.Spec.Type != desiredSvc.Spec.Type
	if len(foundSvc.Spec.Ports) != len(desiredSvc.Spec.Ports) {
		serviceChanged = true
	} else {
		for i, desiredPort := range desiredSvc.Spec.Ports {
			existingPort := foundSvc.Spec.Ports[i]
			// NodePort is compared only when the desired spec pins one; when
			// unset, the API server allocates a node port into the existing
			// spec and that allocation must not be treated as drift.
			nodePortChanged := desiredPort.NodePort != 0 && existingPort.NodePort != desiredPort.NodePort
			if existingPort.Port != desiredPort.Port ||
				existingPort.TargetPort.IntVal != desiredPort.TargetPort.IntVal ||
				existingPort.Name != desiredPort.Name ||
				nodePortChanged {
				serviceChanged = true
				break
			}
		}
	}
	if !equality.Semantic.DeepEqual(foundSvc.Labels, desiredSvc.Labels) {
		serviceChanged = true
	}
	if !equality.Semantic.DeepEqual(foundSvc.Annotations, desiredSvc.Annotations) {
		serviceChanged = true
	}

	if serviceChanged {
		logger.Info("Service changed, updating service",
			"Service.Name", instance.Name, "Service.Namespace", instance.Namespace)
		// Preserve API-server-allocated node ports when the desired spec does
		// not pin one and the target type still uses node ports, so updating
		// the service does not drop the allocation.
		foundSvc.Spec.Ports = mergeServicePorts(desiredSvc.Spec.Type, desiredSvc.Spec.Ports, foundSvc.Spec.Ports)
		foundSvc.Spec.Type = desiredSvc.Spec.Type
		foundSvc.Labels = desiredSvc.Labels
		foundSvc.Annotations = desiredSvc.Annotations
		err = r.Update(ctx, foundSvc)
		if err != nil {
			logger.Error(err, "Failed to update service after spec change")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		logger.Info("Service updated after spec change", "Service.Name", instance.Name, "Service.Namespace", instance.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Reconcile the optional NetworkPolicy for the Schema Registry pods.
	if err = r.reconcileNetworkPolicy(ctx, instance, logger); err != nil {
		logger.Error(err, "Failed to reconcile NetworkPolicy")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}

	r.updateInstanceCountMetric(ctx)
	return ctrl.Result{}, nil
}

// readyCondition builds the Ready status condition for the Schema Registry
// Deployment. The Deployment is only Ready when it has at least one replica
// and all of them are ready: immediately after creation Replicas and
// ReadyReplicas are both 0, which must not be reported as Ready.
func readyCondition(replicas, readyReplicas int32, generation int64) metav1.Condition {
	if replicas > 0 && readyReplicas == replicas {
		return metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Ready",
			Message:            fmt.Sprintf("Schema Registry is ready with %d replicas", readyReplicas),
			ObservedGeneration: generation,
		}
	}
	return metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "NotReady",
		Message:            fmt.Sprintf("Schema Registry deployment is not ready: %d/%d replicas ready", readyReplicas, replicas),
		ObservedGeneration: generation,
	}
}

// handleSecretRotation detects changes to the user secret or cluster CA secret,
// and triggers secret recreation plus deployment update when needed.
// The rotation behavior depends on the resolved Kafka auth mode:
//   - SSL (mTLS): rebuild the JKS keystore + truststore when the KafkaUser or
//     cluster CA secret changes.
//   - SASL_SSL: rebuild only the truststore when the cluster CA changes, and
//     roll the deployment when the KafkaUser (SCRAM credential) secret changes.
//   - SASL_PLAINTEXT: roll the deployment when the KafkaUser secret changes.
//   - PLAINTEXT: no secret material, so there is nothing to rotate.
func (r *StrimziSchemaRegistryReconciler) handleSecretRotation(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
) (ctrl.Result, error) {
	// Nothing to rotate before the Deployment exists: createDeployment
	// bootstraps all dependent secrets and annotations.
	deployment := &apps.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, deployment)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get deployment during secret rotation")
		return ctrl.Result{}, err
	}

	// Resolve the Kafka auth mode once so the rotation path knows which
	// secrets to track and how to react to changes.
	kafkaUser, err := r.getKafkaUser(instance, ctx)
	if err != nil {
		logger.Error(err, "Failed to get KafkaUser for secret rotation")
		return ctrl.Result{}, err
	}
	auth, err := resolveKafkaAuth(instance, kafkaUser)
	if err != nil {
		logger.Error(err, "Failed to resolve Kafka auth mode for secret rotation")
		return ctrl.Result{}, err
	}

	switch auth.Mode {
	case authPlaintext:
		if clientAuthNeedsTruststoreSecret(instance, auth) {
			return r.handleClientAuthTruststoreRotation(ctx, instance, logger, strimziClusterName)
		}
		// PLAINTEXT needs no KafkaUser secret and no JKS secret.
		return ctrl.Result{}, nil
	case authSASLPlaintext:
		if clientAuthNeedsTruststoreSecret(instance, auth) {
			if result, err := r.handleClientAuthTruststoreRotation(ctx, instance, logger, strimziClusterName); err != nil || result.RequeueAfter > 0 {
				return result, err
			}
		}
		return r.handleSASLSecretRotation(ctx, instance, logger, strimziClusterName, auth)
	case authSASLSSL:
		return r.handleSASLSecretRotation(ctx, instance, logger, strimziClusterName, auth)
	default:
		return r.handleMTLSSecretRotation(ctx, instance, logger, strimziClusterName, auth)
	}
}

// handleMTLSSecretRotation rebuilds the JKS keystore + truststore secret and
// rolls the deployment when the KafkaUser or cluster CA secret changes (SSL).
func (r *StrimziSchemaRegistryReconciler) handleMTLSSecretRotation(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
	auth kafkaAuth,
) (ctrl.Result, error) {
	userSecretChanged := false
	clusterCASecretChanged := false

	curr_secret := &v1.Secret{}
	CAsecret := &v1.Secret{}
	userSecret := &v1.Secret{}

	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + jksSecretSuffix, Namespace: instance.Namespace}, curr_secret)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get StrimziSchemaRegistry user jks secret.")
		return ctrl.Result{}, err
	} else if errors.IsNotFound(err) {
		// handleSecretRotation only dispatches here once the Deployment
		// exists, so a missing -jks secret means it was deleted manually:
		// the next pod restart would fail to mount it. Rebuild it the same
		// way a rotation does (createSecret fetches the user and cluster CA
		// secrets itself) and roll the deployment.
		logger.Info("Jks secret not found but the Deployment exists; recreating it", "Secret.Name", instance.Name+jksSecretSuffix)
		newSecret, created, err := r.createSecret(instance, ctx, logger, strimziClusterName, nil, nil, auth)
		if err != nil {
			logger.Error(err, "Failed to rebuild missing jks secret")
			return ctrl.Result{}, err
		}
		if created && newSecret != nil {
			if err := r.Create(ctx, newSecret); err != nil {
				logger.Error(err, "Failed to create rebuilt jks secret")
				return ctrl.Result{}, err
			}
		}
		monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
		dep, err := r.updateDeployment(instance, ctx, logger, newSecret)
		if err != nil {
			logger.Error(err, "Failed to get deployment for missing jks secret recreation")
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, dep); err != nil {
			logger.Error(err, "Failed to update deployment after jks secret recreation")
			return ctrl.Result{}, err
		}
		logger.Info("Deployment updated after jks secret recreation", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Get user secret
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, userSecret)
	if err != nil {
		logger.Error(err, "Failed to get StrimziSchemaRegistry user secret.")
		return ctrl.Result{}, err
	}
	if userSecret.ResourceVersion != curr_secret.Annotations[userVersionKey] {
		logger.Info("User secret for ssr KafkaStore is changed")
		userSecretChanged = true
	}
	// Get cluster CA secret
	err = r.Get(ctx, types.NamespacedName{Name: strimziClusterName + clusterCASuffix,
		Namespace: instance.Namespace}, CAsecret)
	if err != nil {
		logger.Error(err, "Failed to get StrimziSchemaRegistry cluster ca secret.")
		return ctrl.Result{}, err
	}
	if CAsecret.ResourceVersion != curr_secret.Annotations[CAVersionKey] {
		logger.Info("Kafka cluster CA secret is changed")
		clusterCASecretChanged = true
	}

	if !userSecretChanged && !clusterCASecretChanged {
		return ctrl.Result{}, nil
	}

	var newSecret *v1.Secret
	var newSecretCreated bool
	if userSecretChanged && clusterCASecretChanged {
		newSecret, newSecretCreated, err = r.createSecret(instance, ctx, logger, strimziClusterName,
			CAsecret, userSecret, auth)
		if err != nil {
			logger.Error(err, "Failed to create jks secret after user and cluster CA secret changed")
			return ctrl.Result{}, err
		}
		// Renew REST API TLS secret after cluster CA secret changed
		if err = r.renewTLSSecret(instance, ctx, logger); err != nil {
			return ctrl.Result{}, err
		}
	} else if userSecretChanged && !clusterCASecretChanged {
		newSecret, newSecretCreated, err = r.createSecret(instance, ctx, logger, strimziClusterName,
			nil, userSecret, auth)
		if err != nil {
			logger.Error(err, "Failed to create jks secret after user secret changed")
			return ctrl.Result{}, err
		}
	} else {
		newSecret, newSecretCreated, err = r.createSecret(instance, ctx, logger, strimziClusterName,
			CAsecret, nil, auth)
		if err != nil {
			logger.Error(err, "Failed to create jks secret after cluster CA secret changed")
			return ctrl.Result{}, err
		}
		// Renew REST API TLS secret after cluster CA secret changed
		if err = r.renewTLSSecret(instance, ctx, logger); err != nil {
			return ctrl.Result{}, err
		}
	}
	// Only create the secret if createSecret indicated it was newly generated.
	if newSecretCreated {
		err = r.Create(ctx, newSecret)
		if err != nil {
			logger.Error(err, "Failed to create new jks secret after user or cluster CA secret changed")
			return ctrl.Result{}, err
		}
	}
	monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
	logger.Info("Creating new jks secret after user or cluster CA secret changed was successful")
	dep, err := r.updateDeployment(instance, ctx, logger, newSecret)
	if err != nil {
		logger.Error(err, "Failed to update deployment", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
		return ctrl.Result{}, err
	}
	err = r.Update(ctx, dep)
	if err != nil {
		logger.Error(err, "Failed to update deployment after user or cluster CA secret changed")
		return ctrl.Result{}, err
	}
	logger.Info("Deployment updated after secret change", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// handleSASLSecretRotation handles secret rotation for SASL_SSL and
// SASL_PLAINTEXT. It never rebuilds a keystore:
//   - SASL_SSL rebuilds only the truststore when the cluster CA changes.
//   - Both modes roll the deployment (annotation-based) when the KafkaUser
//     SCRAM credential secret changes.
func (r *StrimziSchemaRegistryReconciler) handleSASLSecretRotation(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
	auth kafkaAuth,
) (ctrl.Result, error) {
	// The KafkaUser secret (named after the instance) carries the SCRAM
	// credentials (sasl.jaas.config / password).
	userSecret := &v1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, userSecret); err != nil {
		logger.Error(err, "Failed to get KafkaUser secret for SASL rotation")
		return ctrl.Result{}, err
	}

	// Detect a cluster CA change for SASL_SSL (truststore-only rebuild).
	var CAsecret *v1.Secret
	clusterCASecretChanged := false
	if auth.NeedsTruststore {
		CAsecret = &v1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: strimziClusterName + clusterCASuffix, Namespace: instance.Namespace}, CAsecret); err != nil {
			logger.Error(err, "Failed to get cluster CA secret for SASL_SSL rotation")
			return ctrl.Result{}, err
		}
		curr_secret := &v1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: instance.Name + jksSecretSuffix, Namespace: instance.Namespace}, curr_secret)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get jks secret for SASL_SSL rotation")
			return ctrl.Result{}, err
		}
		if errors.IsNotFound(err) || curr_secret.Annotations[CAVersionKey] != CAsecret.ResourceVersion {
			clusterCASecretChanged = true
		}
	}

	if clusterCASecretChanged {
		newSecret, newSecretCreated, err := r.createSecret(instance, ctx, logger, strimziClusterName, CAsecret, nil, auth)
		if err != nil {
			logger.Error(err, "Failed to create truststore secret after cluster CA secret changed")
			return ctrl.Result{}, err
		}
		if newSecretCreated {
			if err := r.Create(ctx, newSecret); err != nil {
				logger.Error(err, "Failed to create new truststore secret after cluster CA secret changed")
				return ctrl.Result{}, err
			}
		}
		monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
		dep, err := r.updateDeployment(instance, ctx, logger, newSecret)
		if err != nil {
			logger.Error(err, "Failed to update deployment", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, dep); err != nil {
			logger.Error(err, "Failed to update deployment after cluster CA secret changed")
			return ctrl.Result{}, err
		}
		logger.Info("Deployment updated after cluster CA secret changed", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Roll the deployment when the KafkaUser SCRAM credential secret changes.
	userSecretChanged, err := r.saslUserSecretChanged(ctx, instance, userSecret)
	if err != nil {
		logger.Error(err, "Failed to detect KafkaUser secret change")
		return ctrl.Result{}, err
	}
	if userSecretChanged {
		dep, err := r.rollDeploymentForSASLUserSecret(instance, ctx, logger, userSecret)
		if err != nil {
			logger.Error(err, "Failed to update deployment after KafkaUser secret changed")
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, dep); err != nil {
			logger.Error(err, "Failed to update deployment after KafkaUser secret changed")
			return ctrl.Result{}, err
		}
		logger.Info("Deployment updated after KafkaUser secret changed", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// handleClientAuthTruststoreRotation ensures the cluster-CA truststore secret
// used for REST API mutual TLS exists and is up to date for PLAINTEXT and
// SASL_PLAINTEXT modes (where the Kafka auth path does not already build it).
// When the secret is newly created it records its version on the deployment to
// trigger a rollout.
func (r *StrimziSchemaRegistryReconciler) handleClientAuthTruststoreRotation(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
) (ctrl.Result, error) {
	secret, created, err := r.ensureClientAuthTruststoreSecret(instance, ctx, logger, strimziClusterName)
	if err != nil {
		logger.Error(err, "Failed to ensure client-auth truststore secret")
		return ctrl.Result{}, err
	}
	if !created {
		return ctrl.Result{}, nil
	}
	monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
	dep, err := r.updateDeployment(instance, ctx, logger, secret)
	if err != nil {
		logger.Error(err, "Failed to update deployment after client-auth truststore change")
		return ctrl.Result{}, err
	}
	if err := r.Update(ctx, dep); err != nil {
		logger.Error(err, "Failed to update deployment after client-auth truststore change")
		return ctrl.Result{}, err
	}
	logger.Info("Deployment updated after client-auth truststore change", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// saslUserSecretChanged reports whether the KafkaUser secret version recorded on
// the deployment differs from the current secret, indicating that the SASL
// credentials have changed and the pods need to be rolled.
func (r *StrimziSchemaRegistryReconciler) saslUserSecretChanged(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	userSecret *v1.Secret,
) (bool, error) {
	dep := &apps.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, dep); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	recorded := ""
	if dep.Spec.Template.Annotations != nil {
		recorded = dep.Spec.Template.Annotations[saslUserVersionKey]
	}
	return recorded != userSecret.ResourceVersion, nil
}

// rollDeploymentForSASLUserSecret records the current KafkaUser secret version
// on the deployment pod template, triggering a rollout so pods pick up the new
// SASL credentials.
func (r *StrimziSchemaRegistryReconciler) rollDeploymentForSASLUserSecret(
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context,
	logger logr.Logger,
	userSecret *v1.Secret,
) (*apps.Deployment, error) {
	dep := &apps.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, dep); err != nil {
		logger.Error(err, "Failed to get deployment after KafkaUser secret changed")
		return nil, err
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations[saslUserVersionKey] = userSecret.ResourceVersion
	return dep, nil
}

// mapStrimziResourceToRequests enqueues reconcile requests for the
// StrimziSchemaRegistry CRs attached to the same Kafka cluster as the watched
// object, matched via the strimzi.io/cluster label. It backs the Kafka and
// KafkaUser watches so listener/bootstrap changes on the Kafka CR and
// authentication changes on the KafkaUser CR trigger reconciliation.
// When requireNameMatch is true (KafkaUser), only the CR whose name equals
// the watched object's name is enqueued, mirroring getKafkaUser.
func (r *StrimziSchemaRegistryReconciler) mapStrimziResourceToRequests(ctx context.Context, obj client.Object, requireNameMatch bool) []reconcile.Request {
	clusterName := obj.GetLabels()[strimziClusterLabel]
	if clusterName == "" {
		return nil
	}
	list := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistryList{}
	if err := r.List(ctx, list,
		client.InNamespace(obj.GetNamespace()),
		client.MatchingLabels{strimziClusterLabel: clusterName},
	); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		if requireNameMatch && list.Items[i].Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      list.Items[i].Name,
				Namespace: list.Items[i].Namespace,
			},
		})
	}
	return requests
}

// mapSecretToRequests enqueues reconcile requests for the StrimziSchemaRegistry
// CRs affected by a Secret event. Two matching paths:
//  1. Label fast-path (unchanged): secrets carrying the strimzi.io/cluster
//     label are matched against CRs of the same Kafka cluster — the CR-named
//     user secret and the cluster CA cert secret.
//  2. Name path: CRs whose spec.tlssecretname equals the secret's name.
//     User-provided REST API TLS secrets (created with kubectl or cert-manager)
//     typically carry no Strimzi label, so they are matched by name instead.
//
// A CR matching both paths is enqueued once.
func (r *StrimziSchemaRegistryReconciler) mapSecretToRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	requests := make([]reconcile.Request, 0, 1)
	seen := make(map[types.NamespacedName]struct{}, 1)
	add := func(name, namespace string) {
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if _, ok := seen[nn]; ok {
			return
		}
		seen[nn] = struct{}{}
		requests = append(requests, reconcile.Request{NamespacedName: nn})
	}

	// Label fast-path: all Strimzi secrets we care about (user secrets, cluster
	// CA cert secrets) carry the strimzi.io/cluster label. List only CRs that
	// belong to the same Kafka cluster, using a label selector to minimize the
	// List scope (typically 1 CR per cluster).
	secretClusterName := obj.GetLabels()[strimziClusterLabel]
	if secretClusterName != "" {
		attachedStrimziRegistryOperators := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistryList{}
		if err := r.List(ctx, attachedStrimziRegistryOperators,
			client.InNamespace(obj.GetNamespace()),
			client.MatchingLabels{strimziClusterLabel: secretClusterName},
		); err == nil {
			for i := range attachedStrimziRegistryOperators.Items {
				item := &attachedStrimziRegistryOperators.Items[i]
				// User secret: the secret name matches the CR name (both are the
				// KafkaUser name).
				if obj.GetName() == item.Name {
					add(item.Name, item.Namespace)
					continue
				}
				// Cluster CA cert secret: name ends with -cluster-ca-cert.
				if strings.HasSuffix(obj.GetName(), clusterCASuffix) {
					add(item.Name, item.Namespace)
				}
			}
		}
	}

	// Name path: user-provided REST API TLS secrets referenced via
	// spec.tlssecretname. Namespace-scoped so unrelated secrets only pay the
	// cost of listing the (typically few) CRs in their own namespace.
	tlsReferencing := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistryList{}
	if err := r.List(ctx, tlsReferencing, client.InNamespace(obj.GetNamespace())); err == nil {
		for i := range tlsReferencing.Items {
			item := &tlsReferencing.Items[i]
			if item.Spec.TLSSecretName != "" && item.Spec.TLSSecretName == obj.GetName() {
				add(item.Name, item.Namespace)
			}
		}
	}

	if len(requests) == 0 {
		return nil
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *StrimziSchemaRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}).
		Watches(
			&v1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapSecretToRequests),
		).
		Watches(
			&kafka.Kafka{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				return r.mapStrimziResourceToRequests(ctx, obj, false)
			}),
		).
		Watches(
			&kafka.KafkaUser{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				return r.mapStrimziResourceToRequests(ctx, obj, true)
			}),
		).
		Owns(&apps.Deployment{}).
		Complete(r)
}

// renewTLSSecret creates and applies a new TLS secret for Schema Registry REST API.
// It is a no-op when SecureHTTP is disabled or a custom TLSSecretName is provided.
// Uses controllerutil.CreateOrUpdate to handle AlreadyExists gracefully.
func (r *StrimziSchemaRegistryReconciler) renewTLSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, ctx context.Context, logger logr.Logger) error {
	if !instance.Spec.SecureHTTP || instance.Spec.TLSSecretName != "" {
		return nil
	}
	strimziClusterName, err := getStrimziClusterName(instance)
	if err != nil {
		logger.Error(err, "Failed to get strimzi cluster name from labels")
		return err
	}
	TLSSecret, exists, err := r.createTLSSecret(instance, ctx, logger, strimziClusterName)
	if err != nil {
		logger.Error(err, "Failed to format TLS secret")
		return err
	}
	// Update in place when the secret already exists so it is never absent
	// for pods mounting it; createTLSSecret carries the existing
	// ResourceVersion to make the Update valid.
	if exists {
		err = r.Update(ctx, TLSSecret)
	} else {
		err = r.Create(ctx, TLSSecret)
	}
	if err != nil {
		logger.Error(err, "Failed to create or update TLS secret")
		return err
	}
	logger.Info("Secret for Schema Registry TLS created/updated successfully", "Secret.Name", TLSSecret.Name)
	return nil
}

// updateInstanceCountMetric recomputes the instance count gauge from the list
// of StrimziSchemaRegistry CRs. Recomputing (instead of inc/dec bookkeeping)
// keeps the metric correct across operator restarts, CR deletions and missed
// events. It is called on every reconcile completion and on the deletion
// paths; a List error is logged and ignored so it never fails reconciliation.
func (r *StrimziSchemaRegistryReconciler) updateInstanceCountMetric(ctx context.Context) {
	list := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistryList{}
	if err := r.List(ctx, list); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list StrimziSchemaRegistry CRs for the instance count metric")
		return
	}
	monitoring.StrimziSchemaRegistryCurrentInstanceCount.Set(float64(len(list.Items)))
}

// migrateFinalizer ensures the object carries the current finalizer and drops
// the legacy finalizer. When nothing changes it performs no write, avoiding a
// reconcile hot loop. The legacy->current swap happens in a single update.
func (r *StrimziSchemaRegistryReconciler) migrateFinalizer(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
) error {
	changed := false
	if controllerutil.ContainsFinalizer(instance, legacyFinalizer) {
		controllerutil.RemoveFinalizer(instance, legacyFinalizer)
		changed = true
	}
	if !controllerutil.ContainsFinalizer(instance, finalizer) {
		controllerutil.AddFinalizer(instance, finalizer)
		changed = true
	}
	if !changed {
		return nil
	}
	logger.V(1).Info("Migrating finalizers", "Finalizers", instance.Finalizers)
	return r.Update(ctx, instance)
}

// handleTLSCertExpiry renews the operator-managed REST API TLS certificate
// (spec.securehttp with no custom tlssecretname) when it expires within
// tlsCertRenewalThreshold. The renewed secret is updated in place and the
// deployment is rolled via a tlsVersion pod-template annotation so the JVM
// reloads the keystore. Custom TLS secrets are user-managed and never
// touched.
func (r *StrimziSchemaRegistryReconciler) handleTLSCertExpiry(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
) (ctrl.Result, error) {
	if !instance.Spec.SecureHTTP || instance.Spec.TLSSecretName != "" {
		return ctrl.Result{}, nil
	}
	tlsSecret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + tlsSecretSuffix, Namespace: instance.Namespace}, tlsSecret)
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get REST API TLS secret for expiry check")
			return ctrl.Result{}, err
		}
		// createDeployment bootstraps the TLS secret on first reconcile, but
		// it never runs again once the Deployment exists. Recreate a manually
		// deleted secret (and roll the deployment) or the next pod restart
		// fails to mount it.
		return r.recreateMissingTLSSecret(ctx, instance, logger, strimziClusterName)
	}
	keystore := tlsSecret.Data["tls-keystore.jks"]
	password, ok := tlsSecret.Data["keystore_password"]
	if len(keystore) == 0 || !ok {
		logger.Info("REST API TLS secret has no keystore material; skipping expiry check")
		return ctrl.Result{}, nil
	}

	cp := certprocessor.NewCertProcessor(logger)
	notAfter, err := cp.KeystoreCertExpiry(keystore, string(password))
	if err != nil {
		// A malformed keystore must not block reconciliation; log and skip.
		logger.Error(err, "Failed to read REST API TLS certificate expiry; skipping renewal check")
		return ctrl.Result{}, nil
	}
	if time.Until(notAfter) > tlsCertRenewalThreshold {
		return ctrl.Result{}, nil
	}

	logger.Info("REST API TLS certificate is close to expiry, renewing",
		"NotAfter", notAfter.Format(time.RFC3339), "Threshold", tlsCertRenewalThreshold.String())
	newSecret, exists, err := r.createTLSSecret(instance, ctx, logger, strimziClusterName)
	if err != nil {
		logger.Error(err, "Failed to build renewed REST API TLS secret")
		return ctrl.Result{}, err
	}
	if exists {
		err = r.Update(ctx, newSecret)
	} else {
		err = r.Create(ctx, newSecret)
	}
	if err != nil {
		logger.Error(err, "Failed to persist renewed REST API TLS secret")
		return ctrl.Result{}, err
	}
	monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
	if r.Recorder != nil {
		r.Recorder.Eventf(instance, nil, v1.EventTypeNormal, "TLSCertificateRenewed", "",
			"REST API TLS certificate expires at %s; renewed it and rolling the deployment",
			notAfter.Format(time.RFC3339))
	}

	// Roll the deployment so the JVM reloads the renewed keystore. The
	// tlsVersion pod-template annotation changes on every renewal, forcing
	// a rollout even though the JKS secret itself did not change.
	dep, err := r.updateDeployment(instance, ctx, logger, nil)
	if err != nil {
		logger.Error(err, "Failed to get deployment for TLS certificate renewal rollout")
		return ctrl.Result{}, err
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations[tlsVersionKey] = newSecret.ResourceVersion
	if err := r.Update(ctx, dep); err != nil {
		logger.Error(err, "Failed to update deployment after TLS certificate renewal")
		return ctrl.Result{}, err
	}
	logger.Info("Deployment updated after TLS certificate renewal", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// recreateMissingTLSSecret rebuilds a deleted operator-managed REST API TLS
// secret and rolls the deployment so the JVM reloads the recreated keystore.
// It is a no-op before the Deployment exists: createDeployment bootstraps the
// secret on first reconcile.
func (r *StrimziSchemaRegistryReconciler) recreateMissingTLSSecret(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
) (ctrl.Result, error) {
	dep := &apps.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, dep); err != nil {
		if errors.IsNotFound(err) {
			// createDeployment bootstraps the TLS secret on first reconcile.
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get deployment for TLS secret recreation")
		return ctrl.Result{}, err
	}
	logger.Info("REST API TLS secret not found but the Deployment exists; recreating it", "Secret.Name", instance.Name+tlsSecretSuffix)
	newSecret, exists, err := r.createTLSSecret(instance, ctx, logger, strimziClusterName)
	if err != nil {
		logger.Error(err, "Failed to build replacement REST API TLS secret")
		return ctrl.Result{}, err
	}
	if exists {
		err = r.Update(ctx, newSecret)
	} else {
		err = r.Create(ctx, newSecret)
	}
	if err != nil {
		logger.Error(err, "Failed to persist replacement REST API TLS secret")
		return ctrl.Result{}, err
	}
	monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
	if r.Recorder != nil {
		r.Recorder.Eventf(instance, nil, v1.EventTypeNormal, "TLSSecretRecreated", "",
			"REST API TLS secret %s was deleted; recreated it and rolling the deployment", instance.Name+tlsSecretSuffix)
	}
	// Roll the deployment so the JVM reloads the recreated keystore.
	dep, err = r.updateDeployment(instance, ctx, logger, nil)
	if err != nil {
		logger.Error(err, "Failed to get deployment for TLS secret recreation rollout")
		return ctrl.Result{}, err
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations[tlsVersionKey] = newSecret.ResourceVersion
	if err := r.Update(ctx, dep); err != nil {
		logger.Error(err, "Failed to update deployment after TLS secret recreation")
		return ctrl.Result{}, err
	}
	logger.Info("Deployment updated after TLS secret recreation", "Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace)
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// handleUserTLSSecretRotation rolls the Deployment when the user-provided REST
// API TLS secret (spec.tlssecretname) has been rotated. The Schema Registry JVM
// reads the JKS keystore once at startup, so a rotated secret must trigger a
// pod rollout: the secret's resourceVersion is recorded in the tlsVersion
// pod-template annotation at deployment create/update time and compared here.
// The handler is idempotent — once the annotation matches the current secret
// version, subsequent reconciles are no-ops. It is a no-op when the secret or
// the Deployment does not exist (the validation step reports a missing secret;
// createDeployment records the version at creation time).
func (r *StrimziSchemaRegistryReconciler) handleUserTLSSecretRotation(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !instance.Spec.SecureHTTP || instance.Spec.TLSSecretName == "" {
		return ctrl.Result{}, nil
	}
	userSecret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Spec.TLSSecretName, Namespace: instance.Namespace}, userSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get user-provided TLS secret for rotation check")
		return ctrl.Result{}, err
	}
	dep := &apps.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name + deploySuffix, Namespace: instance.Namespace}, dep)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get deployment for user TLS secret rotation")
		return ctrl.Result{}, err
	}
	// Regenerate the derived JKS secret when the user secret is a PEM
	// (kubernetes.io/tls) secret so the mounted material matches; the
	// tlsVersion rollout annotation continues to track the USER secret's
	// resourceVersion (roll once, then stable).
	if isPEMTLSSecret(userSecret) {
		if err := r.ensureDerivedJKSSecret(ctx, instance, userSecret, logger); err != nil {
			logger.Error(err, "Failed to regenerate derived JKS secret after user TLS secret rotation")
			return ctrl.Result{}, err
		}
	}
	current := ""
	if dep.Spec.Template.Annotations != nil {
		current = dep.Spec.Template.Annotations[tlsVersionKey]
	}
	if current == userSecret.ResourceVersion {
		return ctrl.Result{}, nil
	}
	// When the user secret shape changed (pre-built keystore ↔
	// kubernetes.io/tls) the Deployment mounts the wrong secret; repoint the
	// mounts and env refs at the expected one in the same update that rolls
	// the pods, so the rollout never runs with stale mounts.
	if expectedMount := restAPITLSMountSecretName(instance, userSecret); restAPITLSVolumeSecretName(dep) != expectedMount {
		fixRestAPITLSMount(dep, expectedMount)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = make(map[string]string)
	}
	dep.Spec.Template.Annotations[tlsVersionKey] = userSecret.ResourceVersion
	if err := r.Update(ctx, dep); err != nil {
		logger.Error(err, "Failed to roll deployment after user TLS secret rotation")
		return ctrl.Result{}, err
	}
	monitoring.StrimziSchemaRegistrySecretRotationTotal.Inc()
	logger.Info("Deployment rolled after user-provided TLS secret rotation",
		"Deployment.Name", instance.Name+deploySuffix, "Deployment.Namespace", instance.Namespace,
		"Secret.Name", instance.Spec.TLSSecretName)
	return ctrl.Result{RequeueAfter: time.Second}, nil
}

// emitConfigOverriddenEvents records a Warning event for each spec.configuration
// key that was skipped because it is operator-managed.
func (r *StrimziSchemaRegistryReconciler) emitConfigOverriddenEvents(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, overridden []string) {
	if r.Recorder == nil {
		return
	}
	for _, key := range overridden {
		r.Recorder.Eventf(instance, nil, v1.EventTypeWarning, "ConfigKeyOverridden", "",
			"configuration key %q is operator-managed and was ignored", key)
	}
}
