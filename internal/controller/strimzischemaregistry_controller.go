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
	"encoding/base64"
	"strings"

	kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	monitoring "github.com/randsw/schema-registry-operator-strimzi/metrics"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
		logger.Info("Deployment created successfully", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	foundSvc := &v1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, foundSvc)
	if err != nil && errors.IsNotFound(err) {
		svc := r.createService(instance, &logger)
		logger.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		err = r.Create(ctx, svc)
		if err != nil {
			logger.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}
		// Service created successfully - return and requeue
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
		Owns(&apps.Deployment{}).
		Watches(
			&v1.Secret{}, // Watch the Busybox CR
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
	logger.Info("Creating a new Deployment", "Deployment.Namespace", instance.Namespace, "Service.Name", instance.Name+"-deploy")
	keyPrefix := "strimziregistryoperator.randsw.code"

	ls := labelsForCascadeAutoOperator(instance.Name, instance.Name, instance.Spec.Template.Spec.Containers[0].Image)

	var podSpec = instance.Spec.Template

	podSpec.Labels = ls

	// Use special service account for cascade scenarion controller. SA created by heml-chart
	podSpec.Spec.ServiceAccountName = "schema-registry-operator"

	// Create Schema registry configuration
	var podEnv []v1.EnvVar
	var podVolume []v1.Volume
	var containerVolumeMount []v1.VolumeMount

	kafkaBootstrapServer, kafkaClusterName, err := r.getKafkaBootstrapServers(instance, ctx, *logger)
	if err != nil {
		logger.Error(err, "Fail to get Kafka bootstrap servers")
		return nil
	}
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_BOOTSTRAP_SERVERS", Value: kafkaBootstrapServer})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_HOST_NAME", ValueFrom: &v1.EnvVarSource{
		FieldRef: &v1.ObjectFieldSelector{
			FieldPath: "status.podIP",
		},
	},
	})
	// Default
	if instance.Spec.CompatibilityLevel == "" {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL", Value: "forward"})
	} else {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SCHEMA_COMPATIBILITY_LEVEL", Value: instance.Spec.CompatibilityLevel})
	}
	// Default
	if instance.Spec.SecurityProtocol == "" {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", Value: "SSL"})
	} else {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", Value: instance.Spec.SecurityProtocol})
	}
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_MASTER_ELIGIBILITY", Value: "true"})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_HEAP_OPTS", Value: "-Xms512M -Xmx512M"})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_TOPIC", Value: "registry-schemas"})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION", Value: "/var/schemaregistry/keystore.jks"})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION", Value: "/var/schemaregistry/truststore.jks"})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
		SecretKeyRef: &v1.SecretKeySelector{
			LocalObjectReference: v1.LocalObjectReference{
				Name: instance.Name + "jks",
			},
			Key: "keystore_password",
		},
	},
	})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
		SecretKeyRef: &v1.SecretKeySelector{
			LocalObjectReference: v1.LocalObjectReference{
				Name: instance.Name + "jks",
			},
			Key: "truststore_password",
		},
	},
	})

	// Mount secret to container
	containerVolumeMount = append(containerVolumeMount, v1.VolumeMount{
		Name:      "tls",
		MountPath: "/var/schemaregistry",
		ReadOnly:  true,
	})

	if instance.Spec.SecureHTTP {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "https://0.0.0.0:8085"})
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION", Value: "/var/schemaregistry/truststore.jks"})
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
			SecretKeyRef: &v1.SecretKeySelector{
				LocalObjectReference: v1.LocalObjectReference{
					Name: instance.Name + "jks",
				},
				Key: "truststore_password",
			},
		},
		})
		if instance.Spec.TLSSecretName == "" {
			//TODO Create own server key and cert and sign with kafka-cluster-ca-cert. Keystore and key are same!!!!!
			//TODO Mount as volume
			//TODO keystore env add
		} else {
			podVolume = append(podVolume, v1.Volume{Name: "http-tls",
				VolumeSource: v1.VolumeSource{
					Secret: &v1.SecretVolumeSource{
						SecretName: instance.Name + "jks",
					}},
			})
			podSpec.Spec.Volumes[0].Secret.SecretName = instance.Spec.TLSSecretName
			podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					LocalObjectReference: v1.LocalObjectReference{
						Name: instance.Name + "jks",
					},
					Key: "truststore_password",
				},
			},
			})
		}
	} else {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "http://0.0.0.0:8081"})
	}
	var defaultMode int32 = 420
	// Mount secret as volumes
	podVolume = append(podVolume, v1.Volume{Name: "tls",
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName:  instance.Name + "jks",
				DefaultMode: &defaultMode,
			}},
	})

	podSpec.Spec.Containers[0].Env = podEnv
	podSpec.Spec.Volumes = podVolume
	podSpec.Spec.Containers[0].VolumeMounts = containerVolumeMount
	podSpec.Spec.ServiceAccountName = instance.Name

	secret, err := r.createSecret(instance, ctx, logger, kafkaClusterName, nil, nil)
	if err != nil {
		logger.Error(err, "Failed to format secret")
	}

	err = r.Create(ctx, secret)
	if err != nil {
		logger.Error(err, "Failed to create secret")
	}

	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.Name + "-deploy",
			Namespace:   instance.Namespace,
			Labels:      instance.Labels,
			Annotations: map[string]string{keyPrefix + "/jksVersion": secret.ResourceVersion},
		},
		Spec: apps.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: podSpec, // PodSec
		},
	}
	// Set StrimziSchemaRegistry instance as the owner and controller
	err = ctrl.SetControllerReference(instance, dep, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
	}
	return dep
}

func labelsForCascadeAutoOperator(name_app string, name_cr string, image string) map[string]string {
	return map[string]string{"app": name_app, "strimzi-schema-registry": name_cr,
		"app.kubernetes.io/instance":   name_app,
		"app.kubernetes.io/managed-by": "strimzi-registry-operator",
		"app.kubernetes.io/name":       "strimzischemaregistry",
		"app.kubernetes.io/part-of":    name_app,
		"app.kubernetes.io/version":    strings.Split(image, ":")[1]} //schema-registry image tag
}

func (r *StrimziSchemaRegistryReconciler) getKafkaBootstrapServers(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, ctx context.Context, logger logr.Logger) (string, string, error) {

	// Get Kafka cluster name. Finding KafkaUser CR with name equal to our SchemaRegistry
	// and read name from it
	kafkaUsers := &kafka.KafkaUserList{}
	var kafkaClusterName string
	err := r.List(ctx, kafkaUsers)
	if err != nil {
		return "", "", err
	}
	logger.Info("Got Kafka User", "Number", len(kafkaUsers.Items))
	for _, kafkaUser := range kafkaUsers.Items {
		if kafkaUser.Name == instance.Name {
			kafkaClusterName = kafkaUser.Labels["strimzi.io/cluster"]
		}
	}
	logger.Info("Found kafka cluster CR", "Name", kafkaClusterName)
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
		logger.Info("Found kafka listeners.", "Listener", *listener.Name)
		if *listener.Name == kafkaListener {
			kafkaBootstrapServer = *listener.BootstrapServers
			logger.Info("Found specified kafka cluster listeners.", "Listener", kafkaListener, "kafkaBootstap", kafkaBootstrapServer)
		}
	}
	logger.Info("KafkaBootstap", "Address", kafkaBootstrapServer)
	return kafkaBootstrapServer, kafkaClusterName, nil
}

func (r *StrimziSchemaRegistryReconciler) createSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, ctx context.Context, logger *logr.Logger,
	clusterName string, clusterCASecret *v1.Secret, userCASecret *v1.Secret) (*v1.Secret, error) {
	keyPrefix := "strimziregistryoperator.randsw.code"
	CAVersionKey := keyPrefix + "/caSecretVersion"
	userVersionKey := keyPrefix + "/clientSecretVersion"
	clusterSecret := &v1.Secret{}
	userSecret := &v1.Secret{}
	// Get cluster secret
	if clusterCASecret == nil {
		logger.Info("Searching for secret", "Secret", clusterName+"-cluster-ca-cert")
		err := r.Get(ctx, types.NamespacedName{Name: clusterName + "-cluster-ca-cert", Namespace: instance.Namespace}, clusterSecret)
		if err != nil {
			return nil, err
		}
	} else {
		clusterSecret = clusterCASecret
	}
	logger.Info("Cluster CA certificate version", "Version", clusterSecret.ResourceVersion)
	clusterCACert, err := certprocessor.Decode_secret_field(string(clusterSecret.Data["ca.crt"]))
	if err != nil {
		return nil, err
	}

	if userCASecret == nil {
		logger.Info("Searching for secret", "Secret", instance.Name+"-cluster-ca-cert")
		err = r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, userSecret)
		if err != nil {
			return nil, err
		}
	} else {
		userSecret = userCASecret
	}
	logger.Info("Client certification version", "Version", userSecret.ResourceVersion)
	clientCACert, err := certprocessor.Decode_secret_field(string(userSecret.Data["ca.crt"]))
	if err != nil {
		return nil, err
	}
	clientCert, err := certprocessor.Decode_secret_field(string(userSecret.Data["user.crt"]))
	if err != nil {
		return nil, err
	}
	clientKey, err := certprocessor.Decode_secret_field(string(userSecret.Data["user.key"]))
	if err != nil {
		return nil, err
	}

	userPassword, err := certprocessor.Decode_secret_field(string(userSecret.Data["user.password"]))
	if err != nil {
		return nil, err
	}

	clientp12 := string(userSecret.Data["user.p12"])

	jks_secret := &v1.Secret{}
	jks_secret_name := instance.Name + "-jks"
	err = r.Get(ctx, types.NamespacedName{Name: jks_secret_name, Namespace: instance.Namespace}, jks_secret)
	if err == nil {
		if jks_secret.Annotations[CAVersionKey] == clusterSecret.ResourceVersion &&
			jks_secret.Annotations[userVersionKey] == userSecret.ResourceVersion {
			logger.Info("JKS secret is up-to-date")
			return nil, nil
		}

		logger.Info("About to delete JKS secret")

		err = r.Delete(ctx, jks_secret)
		if err != nil {
			return nil, err
		}
	}
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get Deployment")
		return nil, err
	}
	logger.Info("Creating new JKS secret", "Secret Name", jks_secret_name)

	cp := certprocessor.NewCertProcessor(logger)
	truststore, truststore_password, err := cp.CreateTruststore(clusterCACert, "")
	if err != nil {
		return nil, err
	}
	keystore, keystore_password, err := cp.CreateKeystore(clientCACert, clientCert, clientKey, clientp12, userPassword)
	if err != nil {
		return nil, err
	}
	logger.Info("Creating Secret", "Name", jks_secret_name)
	jks_secret = &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jks_secret_name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"app":  "strimzi-chema-registry",
				"user": instance.Name,
			},
			Annotations: map[string]string{
				CAVersionKey:   clusterSecret.ResourceVersion,
				userVersionKey: userSecret.ResourceVersion,
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"truststore.jks":      []byte(base64.StdEncoding.EncodeToString([]byte(truststore))),
			"keystore.jks":        []byte(base64.StdEncoding.EncodeToString([]byte(keystore))),
			"truststore_password": []byte(base64.StdEncoding.EncodeToString([]byte(truststore_password))),
			"keystore_password":   []byte(base64.StdEncoding.EncodeToString([]byte(keystore_password))),
		},
	}

	err = ctrl.SetControllerReference(instance, jks_secret, r.Scheme)
	if err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
	}
	return jks_secret, nil
}

// Create service for scenario controller
func (r *StrimziSchemaRegistryReconciler) createService(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, logger *logr.Logger) *v1.Service {
	var source string
	var port []v1.ServicePort
	if instance.Spec.SecureHTTP {
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
				"source": source,
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
		logger.Error(err, "Failed to set CascadeAutoOperator instance as the owner and controller for service")
	}
	return svc
}
