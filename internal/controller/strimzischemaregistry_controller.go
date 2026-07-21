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
	monitoring "github.com/randsw/schema-registry-operator-strimzi/metrics"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// StrimziSchemaRegistryReconciler reconciles a StrimziSchemaRegistry object
type StrimziSchemaRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const finalizer = "metrics.strimziregistryoperator.randsw.code/finalizer"
const keyPrefix = "strimziregistryoperator.randsw.code"
const CAVersionKey = keyPrefix + "/caSecretVersion"
const userVersionKey = keyPrefix + "/clientSecretVersion"

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

// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

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
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get StrimziSchemaRegistry. May be it is a Secret")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
	}
	// Add finalizer for metrics
	if !controllerutil.ContainsFinalizer(instance, finalizer) {
		logger.V(1).Info("Adding Finalizer for StrimziSchemaRegistry for correct metrics calculation")
		controllerutil.AddFinalizer(instance, finalizer)
		if err = r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
	}
	isApplicationMarkedToBeDeleted := instance.GetDeletionTimestamp() != nil
	if isApplicationMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(instance, finalizer) {
			r.finalizeApplication()
			controllerutil.RemoveFinalizer(instance, finalizer)
			err := r.Update(ctx, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
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
		// Increment instance count
		monitoring.StrimziSchemaRegistryCurrentInstanceCount.Inc()
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
	conditionType := "Ready"
	if found.Status.ReadyReplicas == found.Status.Replicas {
		meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:    conditionType,
			Status:  metav1.ConditionTrue,
			Reason:  "Ready",
			Message: fmt.Sprintf("Schema Registry is ready with %d replicas", found.Status.ReadyReplicas),
		})
	} else {
		meta.SetStatusCondition(&instance.Status.Conditions, metav1.Condition{
			Type:    conditionType,
			Status:  metav1.ConditionFalse,
			Reason:  "Not Ready",
			Message: fmt.Sprintf("Schema Registry deployment is no ready: %d/%d replicas ready", found.Status.ReadyReplicas, found.Status.Replicas),
		})
	}
	err = r.Status().Update(ctx, instance)
	if err != nil {
		logger.Error(err, "Failed to update CR Status")
		monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
		return ctrl.Result{}, err
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

	// Compare service ports to detect changes
	serviceChanged := false
	if len(foundSvc.Spec.Ports) != len(desiredSvc.Spec.Ports) {
		serviceChanged = true
	} else {
		for i, desiredPort := range desiredSvc.Spec.Ports {
			existingPort := foundSvc.Spec.Ports[i]
			if existingPort.Port != desiredPort.Port ||
				existingPort.TargetPort.IntVal != desiredPort.TargetPort.IntVal ||
				existingPort.Name != desiredPort.Name {
				serviceChanged = true
				break
			}
		}
	}

	if serviceChanged {
		logger.Info("Service ports changed, updating service",
			"Service.Name", instance.Name, "Service.Namespace", instance.Namespace)
		foundSvc.Spec.Ports = desiredSvc.Spec.Ports
		if foundSvc.Annotations == nil {
			foundSvc.Annotations = make(map[string]string)
		}
		foundSvc.Annotations["type"] = desiredSvc.Spec.Ports[0].Name
		err = r.Update(ctx, foundSvc)
		if err != nil {
			logger.Error(err, "Failed to update service after spec change")
			monitoring.StrimziSchemaRegistryReconcileErrorsTotal.Inc()
			return ctrl.Result{}, err
		}
		logger.Info("Service updated after spec change", "Service.Name", instance.Name, "Service.Namespace", instance.Namespace)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// handleSecretRotation detects changes to the user secret or cluster CA secret,
// and triggers secret recreation plus deployment update when needed.
// It encapsulates the secret-change detection and re-creation logic that was
// previously inline in Reconcile.
func (r *StrimziSchemaRegistryReconciler) handleSecretRotation(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	logger logr.Logger,
	strimziClusterName string,
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
		logger.Error(err, "Jks secret not found. Maybe first reconcile")
		return ctrl.Result{}, nil
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

	if userSecretChanged || clusterCASecretChanged {
		var newSecret *v1.Secret
		var newSecretCreated bool
		if userSecretChanged && clusterCASecretChanged {
			newSecret, newSecretCreated, err = r.createSecret(instance, ctx, logger, strimziClusterName,
				CAsecret, userSecret)
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
				nil, userSecret)
			if err != nil {
				logger.Error(err, "Failed to create jks secret after user secret changed")
				return ctrl.Result{}, err
			}
		} else if !userSecretChanged && clusterCASecretChanged {
			newSecret, newSecretCreated, err = r.createSecret(instance, ctx, logger, strimziClusterName,
				CAsecret, nil)
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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *StrimziSchemaRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}).
		Watches(
			&v1.Secret{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// Fast path: all secrets we care about (user secrets, cluster CA cert
				// secrets) carry the strimzi.io/cluster label. If the secret lacks this
				// label, it cannot be relevant — return immediately without listing CRs.
				// This avoids O(N²) fan-out where every Secret change triggered a List
				// of every StrimziSchemaRegistry CR in the namespace.
				secretLabels := obj.GetLabels()
				if secretLabels == nil {
					return nil
				}
				secretClusterName := secretLabels[strimziClusterLabel]
				if secretClusterName == "" {
					return nil
				}

				// List only CRs that belong to the same Kafka cluster, using label
				// selector to minimize the List scope (typically 1 CR per cluster).
				attachedStrimziRegistryOperators := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistryList{}
				err := r.List(ctx, attachedStrimziRegistryOperators,
					client.InNamespace(obj.GetNamespace()),
					client.MatchingLabels{strimziClusterLabel: secretClusterName},
				)
				if err != nil {
					return nil
				}

				requests := make([]reconcile.Request, 0, len(attachedStrimziRegistryOperators.Items))
				for _, item := range attachedStrimziRegistryOperators.Items {
					// User secret: the secret name matches the CR name (both are the
					// KafkaUser name).
					if obj.GetName() == item.GetName() {
						requests = append(requests, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      item.GetName(),
								Namespace: item.GetNamespace(),
							},
						})
						continue
					}
					// Cluster CA cert secret: name ends with -cluster-ca-cert.
					if strings.HasSuffix(obj.GetName(), clusterCASuffix) {
						requests = append(requests, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      item.GetName(),
								Namespace: item.GetNamespace(),
							},
						})
					}
				}
				return requests
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
	TLSSecret, err := r.createTLSSecret(instance, ctx, logger, strimziClusterName)
	if err != nil {
		logger.Error(err, "Failed to format TLS secret")
		return err
	}
	if TLSSecret == nil {
		return nil
	}
	// Use CreateOrUpdate to handle both initial creation and concurrent reconciliation
	// where another reconcile loop may have already created the secret.
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, TLSSecret, func() error {
		// The mutate function is called for updates; the secret data is already set
		// by createTLSSecret, so no mutation is needed here.
		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to create or update TLS secret")
		return err
	}
	logger.Info("Secret for Schema Registry TLS created/updated successfully", "Secret.Name", TLSSecret.Name)
	return nil
}

func (reconciler *StrimziSchemaRegistryReconciler) finalizeApplication() {
	monitoring.StrimziSchemaRegistryCurrentInstanceCount.Dec()
}
