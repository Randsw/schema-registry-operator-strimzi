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
	"time"

	"strings"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	monitoring "github.com/randsw/schema-registry-operator-strimzi/metrics"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"

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

// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the StrimziSchemaRegistry object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *StrimziSchemaRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	userSecretChanged := false
	clusterCASecretChanged := false
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
		return ctrl.Result{}, err
	}
	// Add finalizer for metrics
	if !controllerutil.ContainsFinalizer(instance, finalizer) {
		logger.V(1).Info("Adding Finalizer for StrimziSchemaRegistry for correct metrics calculation")
		controllerutil.AddFinalizer(instance, finalizer)
		if err = r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
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
	// Check if reconcile triggered by secret change and renew secret
	curr_secret := &v1.Secret{}
	CAsecret := &v1.Secret{}
	userSecret := &v1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: req.Name + "-jks", Namespace: req.Namespace}, curr_secret)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get StrimziSchemaRegistry user jks secret.")
		return ctrl.Result{}, err
	} else if errors.IsNotFound(err) {
		logger.Error(err, "Jks secret not found. Maybe first reconcile")
	} else {
		// Get user secret
		err = r.Get(ctx, req.NamespacedName, userSecret)
		if err != nil {
			logger.Error(err, "Failed to get StrimziSchemaRegistry user secret.")
			return ctrl.Result{}, err
		}
		if userSecret.ResourceVersion != curr_secret.Annotations["strimziregistryoperator.randsw.code/clientSecretVersion"] {
			logger.Info("User secret for ssr KafkaStore is changed")
			// Renew cluster secret
			userSecretChanged = true
		}
		// Get cluster CA secret
		err = r.Get(ctx, types.NamespacedName{Name: instance.GetLabels()["strimzi.io/cluster"] + "-cluster-ca-cert",
			Namespace: req.Namespace}, CAsecret)
		if err != nil {
			logger.Error(err, "Failed to get StrimziSchemaRegistry cluster ca secret.")
			return ctrl.Result{}, err
		}
		if CAsecret.ResourceVersion != curr_secret.Annotations["strimziregistryoperator.randsw.code/caSecretVersion"] {
			logger.Info("Kafka cluster CA secret is changed")
			// Renew cluster secret
			clusterCASecretChanged = true
		}
	}
	if userSecretChanged || clusterCASecretChanged {
		newSecret := &v1.Secret{}
		if userSecretChanged && clusterCASecretChanged {
			newSecret, err = r.createSecret(instance, ctx, &logger, instance.GetLabels()["strimzi.io/cluster"],
				CAsecret, userSecret)
			if err != nil {
				logger.Error(err, "Failed to create jks secret after user and cluster CA secret changed")
				return ctrl.Result{}, err
			}
		} else if userSecretChanged && !clusterCASecretChanged {
			newSecret, err = r.createSecret(instance, ctx, &logger, instance.GetLabels()["strimzi.io/cluster"],
				nil, userSecret)
			if err != nil {
				logger.Error(err, "Failed to create jks secret after user secret changed")
				return ctrl.Result{}, err
			}
		} else if !userSecretChanged && clusterCASecretChanged {
			newSecret, err = r.createSecret(instance, ctx, &logger, instance.GetLabels()["strimzi.io/cluster"],
				CAsecret, nil)
			if err != nil {
				logger.Error(err, "Failed to create jks secret after user secret changed")
				return ctrl.Result{}, err
			}
		}
		err = r.Create(ctx, newSecret)
		if err != nil {
			logger.Error(err, "Failed to create new jks secret after user or cluster CA secret changed")
			return ctrl.Result{}, err
		}
		logger.Info("Creating new jks secret after user or cluster CA secret changed was successfull")
		time.Sleep(50 * time.Millisecond) //Pause for KubeApi write new secret to etcd so we can get it ResourceVersion
		dep, err := r.updateDeployment(instance, ctx, &logger)
		if err != nil {
			logger.Error(err, "Failed to update deployment", "Deployment.Name", instance.Name+"-deploy", "Deployment.Namespace", instance.Namespace)
			return ctrl.Result{}, err
		}
		err = r.Update(ctx, dep)
		if err != nil {
			logger.Error(err, "Failed to update deployment after user or cluster CA secret changed")
			return ctrl.Result{}, err
		}
		logger.Info("Deployment updated after secret change", "Deployment.Name", instance.Name+"-deploy", "Deployment.Namespace", instance.Namespace)
		return ctrl.Result{Requeue: true}, nil
	}
	// Check if the Deployment already exists, if not create a new one
	found := &apps.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name + "-deploy", Namespace: instance.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Deployment
		deployment := r.createDeployment(instance, ctx, &logger)
		// Increment instance count
		monitoring.StrimziSchemaRegisterCurrentInstanceCount.Inc()
		err = r.Create(ctx, deployment)
		if err != nil {
			logger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		logger.Info("Deployment created successfully", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}
	// Creating service for deployment
	foundSvc := &v1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, foundSvc)
	if err != nil && errors.IsNotFound(err) {
		svc := r.createService(instance, &logger)
		err = r.Create(ctx, svc)
		if err != nil {
			logger.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}
		// Service created successfully - return and requeue
		logger.Info("Service created successfully", "Service.Name", svc.Name, "Service.Namespace", svc.Namespace)
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *StrimziSchemaRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}).
		Watches(
			&v1.Secret{}, // Watch the secret
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				attachedStrimziRegistryOperators := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistryList{}
				err := r.List(ctx, attachedStrimziRegistryOperators)
				if err != nil {
					return []reconcile.Request{}
				}
				requests := []reconcile.Request{}
				for _, item := range attachedStrimziRegistryOperators.Items {
					// Check if the Secret resource has the label 'strimzi.io/cluster'
					// Get user secret
					if obj.GetName() == item.GetName() {
						if obj.GetLabels()["strimzi.io/cluster"] == item.GetLabels()["strimzi.io/cluster"] {
							requests = append(requests, reconcile.Request{
								NamespacedName: types.NamespacedName{
									Name:      item.GetName(),
									Namespace: item.GetNamespace(),
								},
							})
						}
					}
					// Get cluster ca secret
					if obj.GetLabels()["strimzi.io/cluster"] == item.GetLabels()["strimzi.io/cluster"] && strings.HasSuffix(obj.GetName(), "-cluster-ca-cert") {
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

func (reconciler *StrimziSchemaRegistryReconciler) finalizeApplication() {
	monitoring.StrimziSchemaRegisterCurrentInstanceCount.Dec()
}
