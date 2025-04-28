package controller

import (
	"context"
	go_err "errors"
	"strings"

	kafka "github.com/RedHatInsights/strimzi-client-go/apis/kafka.strimzi.io/v1beta2"
	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func labelsForStrimziSchemaRegistryOperator(name_app string, name_cr string,
	image string, kakfkaClusterName string) map[string]string {
	return map[string]string{"app": name_app, "strimzi-schema-registry": name_cr,
		"app.kubernetes.io/instance":   name_app,
		"app.kubernetes.io/managed-by": "strimzi-registry-operator",
		"app.kubernetes.io/name":       "strimzischemaregistry",
		"app.kubernetes.io/part-of":    name_app,
		"app.kubernetes.io/version":    strings.Split(image, ":")[1],
		"strimzi.io/cluster":           kakfkaClusterName} //schema-registry image tag
}

func (r *StrimziSchemaRegistryReconciler) createDeployment(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger *logr.Logger) *apps.Deployment {
	logger.Info("Creating a new Deployment", "Deployment.Namespace", instance.Namespace, "Deployment.Name", instance.Name+"-deploy")
	var defaultMode int32 = 420
	podSpec := instance.Spec.Template
	// Create Schema registry configuration
	var podEnv []v1.EnvVar
	var podVolume []v1.Volume
	var containerVolumeMount []v1.VolumeMount

	// Get Kafka Bootstrap Server address
	kafkaBootstrapServer, kafkaClusterName, err := r.getKafkaBootstrapServers(instance, ctx, *logger)
	if err != nil {
		logger.Error(err, "Fail to get Kafka bootstrap servers")
		return nil
	}
	// Create secret for Kafkastore
	secret, err := r.createSecret(instance, ctx, logger, kafkaClusterName, nil, nil)
	if err != nil {
		logger.Error(err, "Failed to format secret", "Secret.Name", instance.Name+"-jks")
	}
	if secret != nil {
		err = r.Create(ctx, secret)
		if err != nil {
			logger.Error(err, "Failed to create secret", "Secret.Name", instance.Name+"-jks")
		}
		logger.V(1).Info("Secret for Schema Registry KafkaStore TLS created successfully", "Secret.Name", secret.Name)
	} else {
		err := r.Get(ctx, types.NamespacedName{Name: instance.Name + "-jks", Namespace: instance.Namespace}, secret)
		if err != nil {
			logger.Error(err, "Failed to get secret", "Secret.Name", instance.Name+"-jks")
		}
	}
	// Formation of the pod ENV
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
				Name: instance.Name + "-jks",
			},
			Key: "keystore_password",
		},
	},
	})
	podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
		SecretKeyRef: &v1.SecretKeySelector{
			LocalObjectReference: v1.LocalObjectReference{
				Name: instance.Name + "-jks",
			},
			Key: "truststore_password",
		},
	},
	})
	// Mount KafkaStore secret volume to container
	containerVolumeMount = append(containerVolumeMount, v1.VolumeMount{
		Name:      "tls",
		MountPath: "/var/schemaregistry",
		ReadOnly:  true,
	})
	// Add KafkaStore secret as volumes
	podVolume = append(podVolume, v1.Volume{Name: "tls",
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName:  instance.Name + "-jks",
				DefaultMode: &defaultMode,
			}},
	})

	// Schema Registry REST API TLS secret
	var TLSSecretName string
	if instance.Spec.SecureHTTP {
		//TODO Trustore if client use tls auth to schema registry
		//podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_TRUSTSTORE_LOCATION", Value: "/var/tls/truststore.jks"})
		// podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_TRUSTSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
		// 	SecretKeyRef: &v1.SecretKeySelector{
		// 		LocalObjectReference: v1.LocalObjectReference{
		// 			Name: instance.Name + "-tls",
		// 		},
		// 		Key: "truststore_password",
		// 	},
		// },
		// })
		if instance.Spec.TLSSecretName == "" {
			TLSSecret, err := r.createTLSSecret(instance, ctx, logger, kafkaClusterName)
			if err != nil {
				logger.Error(err, "Failed to format TLS secret")
			}
			if TLSSecret != nil {
				err = r.Create(ctx, TLSSecret)
				if err != nil {
					logger.Error(err, "Failed to create TLS secret")
				}
				logger.Info("Secret for Schema Registry TLS created successfully", "Secret.Name", TLSSecret.Name)
			}
			TLSSecretName = TLSSecret.Name
		} else {
			TLSSecretName = instance.Spec.TLSSecretName
		}
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "https://0.0.0.0:8085"})
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SCHEMA_REGISTRY_INTER_INSTANCE_PROTOCOL", Value: "https"})
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_LOCATION", Value: "/var/rest-api-tls/tls-keystore.jks"})
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEYSTORE_PASSWORD", ValueFrom: &v1.EnvVarSource{
			SecretKeyRef: &v1.SecretKeySelector{
				LocalObjectReference: v1.LocalObjectReference{
					Name: TLSSecretName,
				},
				Key: "keystore_password",
			},
		},
		})
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_SSL_KEY_PASSWORD", ValueFrom: &v1.EnvVarSource{
			SecretKeyRef: &v1.SecretKeySelector{
				LocalObjectReference: v1.LocalObjectReference{
					Name: TLSSecretName,
				},
				Key: "key_password",
			},
		},
		})
		// Mount REST API TLS secret volume to container
		containerVolumeMount = append(containerVolumeMount, v1.VolumeMount{
			Name:      "rest-api-tls",
			MountPath: "/var/rest-api-tls",
			ReadOnly:  true,
		})
		// Add REST API TLS secret as volumes
		podVolume = append(podVolume, v1.Volume{Name: "rest-api-tls",
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName:  TLSSecretName,
					DefaultMode: &defaultMode,
				}},
		})
	} else {
		podEnv = append(podEnv, v1.EnvVar{Name: "SCHEMA_REGISTRY_LISTENERS", Value: "http://0.0.0.0:8081"})
	}
	podSpec.Spec.Containers[0].Env = podEnv
	podSpec.Spec.Containers[0].VolumeMounts = containerVolumeMount
	podSpec.Spec.Volumes = podVolume
	ls := labelsForStrimziSchemaRegistryOperator(instance.Name, instance.Name, instance.Spec.Template.Spec.Containers[0].Image, kafkaClusterName)
	podSpec.Labels = ls
	podSpec.Annotations = map[string]string{keyPrefix + "/jksVersion": secret.ResourceVersion}

	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-deploy",
			Namespace: instance.Namespace,
			Labels:    ls,
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

func (r *StrimziSchemaRegistryReconciler) getKafkaBootstrapServers(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger logr.Logger) (string, string, error) {
	// Get Kafka cluster name. Finding KafkaUser CR with name equal to our SchemaRegistry
	// and read name from it
	kafkaUsers := &kafka.KafkaUserList{}
	var kafkaClusterName string
	err := r.List(ctx, kafkaUsers)
	if err != nil {
		return "", "", err
	}
	logger.V(1).Info("Got Kafka User", "Number of Kafka User", len(kafkaUsers.Items))
	for _, kafkaUser := range kafkaUsers.Items {
		if kafkaUser.Name == instance.Name {
			kafkaClusterName = kafkaUser.Labels["strimzi.io/cluster"]
		}
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
		logger.V(1).Info("Found kafka listeners.", "Listener", *listener.Name)
		if *listener.Name == kafkaListener {
			kafkaBootstrapServer = *listener.BootstrapServers
			logger.V(1).Info("Found specified kafka cluster listeners.", "Listener", kafkaListener, "kafkaBootstap", kafkaBootstrapServer)
			logger.V(1).Info("KafkaBootstap", "Address", kafkaBootstrapServer)
			return kafkaBootstrapServer, kafkaClusterName, nil
		}
	}
	logger.V(1).Info("No listeners found. Check CR config", "Listener", kafkaListener)
	return "", "", go_err.New("cant find bootstrap address")
}

func (r *StrimziSchemaRegistryReconciler) createSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger *logr.Logger, clusterName string, clusterCASecret *v1.Secret,
	userCASecret *v1.Secret) (*v1.Secret, error) {
	logger.Info("Creating secret for schema registry Kafkastore TLS")
	clusterSecret := &v1.Secret{}
	userSecret := &v1.Secret{}
	// Get cluster secret
	if clusterCASecret == nil {
		logger.V(1).Info("Searching for cluster CA secret", "Secret", clusterName+"-cluster-ca-cert")
		err := r.Get(ctx, types.NamespacedName{Name: clusterName + "-cluster-ca-cert", Namespace: instance.Namespace}, clusterSecret)
		if err != nil {
			return nil, err
		}
	} else {
		clusterSecret = clusterCASecret
	}
	logger.V(1).Info("Cluster CA certificate version", "Version", clusterSecret.ResourceVersion)
	clusterCACert := string(clusterSecret.Data["ca.crt"])
	if userCASecret == nil {
		logger.Info("Searching for user CA secret", "Secret", instance.Name)
		err := r.Get(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}, userSecret)
		if err != nil {
			return nil, err
		}
	} else {
		userSecret = userCASecret
	}
	logger.V(1).Info("Client certification version", "Version", userSecret.ResourceVersion)
	clientCACert := string(userSecret.Data["ca.crt"])
	clientCert := string(userSecret.Data["user.crt"])
	clientKey := string(userSecret.Data["user.key"])
	userPassword := string(userSecret.Data["user.password"])
	clientp12 := string(userSecret.Data["user.p12"])

	jks_secret := &v1.Secret{}
	jks_secret_name := instance.Name + "-jks"
	err := r.Get(ctx, types.NamespacedName{Name: jks_secret_name, Namespace: instance.Namespace}, jks_secret)
	if err == nil {
		if jks_secret.Annotations[CAVersionKey] == clusterSecret.ResourceVersion &&
			jks_secret.Annotations[userVersionKey] == userSecret.ResourceVersion {
			logger.V(1).Info("JKS secret is up-to-date")
			return nil, nil
		}
		logger.V(1).Info("About to delete JKS secret")
		err = r.Delete(ctx, jks_secret)
		if err != nil {
			return nil, err
		}
	}
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get schema registry secret")
		return nil, err
	}
	logger.Info("Creating new keystore and trustore", "Secret Name", jks_secret_name)
	cp := certprocessor.NewCertProcessor(logger)
	truststore, truststore_password, err := cp.CreateTruststore(clusterCACert, "")
	if err != nil {
		return nil, err
	}
	keystore, keystore_password, err := cp.CreateKeystore(clientCACert, clientCert, clientKey, clientp12, userPassword)
	if err != nil {
		return nil, err
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
	}
	return jks_secret, nil
}

func (r *StrimziSchemaRegistryReconciler) createService(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, logger *logr.Logger) *v1.Service {
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
	}
	return svc
}

func (r *StrimziSchemaRegistryReconciler) createTLSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger *logr.Logger, clusterName string) (*v1.Secret, error) {
	logger.Info("Creating secret for schema registry TLS")
	clusterCertSecret := &v1.Secret{}
	clusterKeySecret := &v1.Secret{}
	logger.V(1).Info("Searching for cluster CA cert secret", "Secret", clusterName+"-cluster-ca-cert")
	err := r.Get(ctx, types.NamespacedName{Name: clusterName + "-cluster-ca-cert", Namespace: instance.Namespace},
		clusterCertSecret)
	if err != nil {
		return nil, err
	}
	logger.V(1).Info("Searching for cluster CA key secret", "Secret", clusterName+"-cluster-ca")
	err = r.Get(ctx, types.NamespacedName{Name: clusterName + "-cluster-ca", Namespace: instance.Namespace},
		clusterKeySecret)
	if err != nil {
		return nil, err
	}
	clusterCert := string(clusterCertSecret.Data["ca.crt"])
	clusterKey := string(clusterKeySecret.Data["ca.key"])
	jksTLSSecret := &v1.Secret{}
	jksTLSSecretName := instance.Name + "-tls"
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
	}
	return jksTLSSecret, nil
}

func (r *StrimziSchemaRegistryReconciler) updateDeployment(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	ctx context.Context, logger *logr.Logger) (*apps.Deployment, error) {
	// Read created secret to get his ResourceVersion
	readSecret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: instance.Name + "-jks", Namespace: instance.Namespace}, readSecret)
	if err != nil {
		logger.Error(err, "Failed to get new jks secret after user or cluster CA secret changed")
		return nil, err
	}
	// Update deployment
	dep := &apps.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.Name + "-deploy", Namespace: instance.Namespace}, dep)
	if err != nil {
		logger.Error(err, "Failed to get deployment after user or cluster CA secret changed")
		return nil, err
	}
	dep.Spec.Template.Annotations[keyPrefix+"/jksVersion"] = readSecret.ResourceVersion
	return dep, nil
}
