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

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	monitoring "github.com/randsw/schema-registry-operator-strimzi/metrics"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// StrimziSchemaRegistryReconciler reconciles a StrimziSchemaRegistry object
type StrimziSchemaRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const finalizer = "metrics.strimziregistryoperator.randsw.code/finalizer"

// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=strimziregistryoperator.randsw.code,resources=strimzischemaregistries/finalizers,verbs=update

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
	_ = log.FromContext(ctx)
	logger := log.FromContext(ctx).WithValues("StrimziSchemaRegistry", req.NamespacedName)

	logger.Info("Reconciling StrimziSchemaRegistry", "Request name", req.Name, "request namespace", req.Namespace)

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
		//TODO Secret renew
		return ctrl.Result{}, err
	}
	// Add finalizer for metrics
	if !controllerutil.ContainsFinalizer(instance, finalizer) {
		logger.Info("Adding Finalizer for CascadeAutoOperator")
		controllerutil.AddFinalizer(instance, finalizer)
		if err = r.Update(ctx, instance); err != nil {
			logger.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}
	isApplicationMarkedToBeDeleted := instance.GetDeletionTimestamp() != nil
	if isApplicationMarkedToBeDeleted {
		if controllerutil.ContainsFinalizer(instance, finalizer) {
			r.finalizeApplication(ctx, instance)
			controllerutil.RemoveFinalizer(instance, finalizer)
			err := r.Update(ctx, instance)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	// Check if the Deployment already exists, if not create a new one
	found := &apps.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name + "-deploy", Namespace: instance.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Deployment
		deployment := r.createDeployment(instance, ctx, &logger)
		// Increment instance count
		monitoring.StrimziSchemaRegisterCurrentInstanceCount.Inc()
		logger.Info("Creating a new Deployment", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
		err = r.Create(ctx, deployment)
		if err != nil {
			logger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *StrimziSchemaRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}).
		Owns(&apps.Deployment{}).
		Watches(
			&corev1.Secret{}, // Watch the Busybox CR
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				// Check if the Busybox resource has the label 'backup-needed: "true"'
				if _, ok := obj.GetLabels()["strimzi.io/component-type"]; ok {
					return []reconcile.Request{
						{
							NamespacedName: types.NamespacedName{
								Name:      obj.GetName(),      // Reconcile the associated BackupBusybox resource
								Namespace: obj.GetNamespace(), // Use the namespace of the changed Busybox
							},
						},
					}
				}
				if _, ok := obj.GetLabels()["strimzi.io/component-type"]; ok {
					return []reconcile.Request{
						{
							NamespacedName: types.NamespacedName{
								Name:      obj.GetName(),      // Reconcile the associated BackupBusybox resource
								Namespace: obj.GetNamespace(), // Use the namespace of the changed Busybox
							},
						},
					}
				}
				// If the label is not present or doesn't match, don't trigger reconciliation
				return []reconcile.Request{}
			}),
		).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}

func (reconciler *StrimziSchemaRegistryReconciler) finalizeApplication(ctx context.Context, application *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) {
	monitoring.StrimziSchemaRegisterCurrentInstanceCount.Dec()
}

func (r *StrimziSchemaRegistryReconciler) createDeployment(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, ctx context.Context, logger *logr.Logger) *apps.Deployment {
	ls := labelsForCascadeAutoOperator(instance.Name, instance.Name)

	var podSpec = instance.Spec.Template

	podSpec.Labels = ls

	podSpec.Spec.Volumes[0].ConfigMap.Name = instance.Name + "-cm"
	// Use special service account for cascade scenarion controller. SA created by heml-chart
	podSpec.Spec.ServiceAccountName = "cascade-scenario"

	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-deploy",
			Namespace: instance.Namespace,
			Labels:    instance.Labels,
		},
		Spec: apps.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: podSpec, // PodSec
		}, // Spec
	} // Deployment

	// Set CascadeAutoOperator instance as the owner and controller
	err := ctrl.SetControllerReference(instance, dep, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set CascadeAutoOperator instance as the owner and controller")
	}
	return dep
}

func labelsForCascadeAutoOperator(name_app string, name_cr string) map[string]string {
	return map[string]string{"app": name_app, "strimzi-schema-registry": name_cr}
}
