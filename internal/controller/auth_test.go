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
	"testing"
	"time"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newTestKafkaUser creates a minimal KafkaUser with the given authentication type.
func newTestKafkaUser(authType kafka.KafkaUserAuthenticationType) *kafka.KafkaUser {
	return &kafka.KafkaUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sr",
			Namespace: "default",
		},
		Spec: &kafka.KafkaUserSpec{
			Authentication: &kafka.KafkaUserAuthentication{Type: authType},
		},
	}
}

// envByName returns the env var with the given name, if present.
func envByName(env []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, e := range env {
		if e.Name == name {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

// volumeByName returns the volume with the given name, if present.
func volumeByName(volumes []corev1.Volume, name string) (corev1.Volume, bool) {
	for _, v := range volumes {
		if v.Name == name {
			return v, true
		}
	}
	return corev1.Volume{}, false
}

func requireEnvPresent(t *testing.T, env []corev1.EnvVar, name string) {
	t.Helper()
	if _, ok := envByName(env, name); !ok {
		t.Errorf("expected env var %q to be present", name)
	}
}

func requireEnvAbsent(t *testing.T, env []corev1.EnvVar, name string) {
	t.Helper()
	if _, ok := envByName(env, name); ok {
		t.Errorf("expected env var %q to be absent", name)
	}
}

func requireEnvValue(t *testing.T, env []corev1.EnvVar, name, want string) {
	t.Helper()
	e, ok := envByName(env, name)
	if !ok {
		t.Errorf("expected env var %q to be present", name)
		return
	}
	if e.Value != want {
		t.Errorf("expected env var %q to be %q, got %q", name, want, e.Value)
	}
}

func requireSecretRef(t *testing.T, e corev1.EnvVar, wantName, wantKey string) {
	t.Helper()
	if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Errorf("expected env var %q to reference a secret", e.Name)
		return
	}
	if e.ValueFrom.SecretKeyRef.Name != wantName {
		t.Errorf("expected env var %q secret name %q, got %q", e.Name, wantName, e.ValueFrom.SecretKeyRef.Name)
	}
	if e.ValueFrom.SecretKeyRef.Key != wantKey {
		t.Errorf("expected env var %q secret key %q, got %q", e.Name, wantKey, e.ValueFrom.SecretKeyRef.Key)
	}
}

func TestResolveKafkaAuth(t *testing.T) {
	tlsUser := newTestKafkaUser(kafka.TLS_KAFKAUSERAUTHENTICATIONTYPE)
	scramUser := newTestKafkaUser(kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE)

	tests := []struct {
		name          string
		protocol      string
		kafkaUser     *kafka.KafkaUser
		wantErr       bool
		wantMode      kafkaAuthMode
		wantProtocol  string
		wantTrust     bool
		wantKey       bool
		wantSASL      bool
		wantMechanism string
	}{
		{
			name:          "SSL with tls KafkaUser",
			protocol:      "SSL",
			kafkaUser:     tlsUser,
			wantMode:      authMTLS,
			wantProtocol:  "SSL",
			wantTrust:     true,
			wantKey:       true,
			wantSASL:      false,
			wantMechanism: "",
		},
		{
			name:          "empty protocol defaults to SSL",
			protocol:      "",
			kafkaUser:     tlsUser,
			wantMode:      authMTLS,
			wantProtocol:  "SSL",
			wantTrust:     true,
			wantKey:       true,
			wantSASL:      false,
			wantMechanism: "",
		},
		{
			name:      "SSL with no KafkaUser",
			protocol:  "SSL",
			kafkaUser: nil,
			wantErr:   true,
		},
		{
			name:      "SSL with scram KafkaUser",
			protocol:  "SSL",
			kafkaUser: scramUser,
			wantErr:   true,
		},
		{
			name:          "SASL_SSL with scram KafkaUser",
			protocol:      "SASL_SSL",
			kafkaUser:     scramUser,
			wantMode:      authSASLSSL,
			wantProtocol:  "SASL_SSL",
			wantTrust:     true,
			wantKey:       false,
			wantSASL:      true,
			wantMechanism: saslMechanismSCRAMSHA512,
		},
		{
			name:      "SASL_SSL with tls KafkaUser",
			protocol:  "SASL_SSL",
			kafkaUser: tlsUser,
			wantErr:   true,
		},
		{
			name:      "SASL_SSL with no KafkaUser",
			protocol:  "SASL_SSL",
			kafkaUser: nil,
			wantErr:   true,
		},
		{
			name:          "SASL_PLAINTEXT with scram KafkaUser",
			protocol:      "SASL_PLAINTEXT",
			kafkaUser:     scramUser,
			wantMode:      authSASLPlaintext,
			wantProtocol:  "SASL_PLAINTEXT",
			wantTrust:     false,
			wantKey:       false,
			wantSASL:      true,
			wantMechanism: saslMechanismSCRAMSHA512,
		},
		{
			name:      "SASL_PLAINTEXT with no KafkaUser",
			protocol:  "SASL_PLAINTEXT",
			kafkaUser: nil,
			wantErr:   true,
		},
		{
			name:      "SASL_PLAINTEXT with tls KafkaUser",
			protocol:  "SASL_PLAINTEXT",
			kafkaUser: tlsUser,
			wantErr:   true,
		},
		{
			name:          "PLAINTEXT with no KafkaUser",
			protocol:      "PLAINTEXT",
			kafkaUser:     nil,
			wantMode:      authPlaintext,
			wantProtocol:  "PLAINTEXT",
			wantTrust:     false,
			wantKey:       false,
			wantSASL:      false,
			wantMechanism: "",
		},
		{
			name:          "PLAINTEXT ignores a KafkaUser",
			protocol:      "PLAINTEXT",
			kafkaUser:     scramUser,
			wantMode:      authPlaintext,
			wantProtocol:  "PLAINTEXT",
			wantTrust:     false,
			wantKey:       false,
			wantSASL:      false,
			wantMechanism: "",
		},
		{
			name:      "unsupported protocol",
			protocol:  "OAUTH",
			kafkaUser: nil,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := newTestInstance()
			inst.Spec.SecurityProtocol = tt.protocol

			got, err := resolveKafkaAuth(inst, tt.kafkaUser)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.Mode != tt.wantMode {
				t.Errorf("expected mode %v, got %v", tt.wantMode, got.Mode)
			}
			if got.Protocol != tt.wantProtocol {
				t.Errorf("expected protocol %q, got %q", tt.wantProtocol, got.Protocol)
			}
			if got.NeedsTruststore != tt.wantTrust {
				t.Errorf("expected NeedsTruststore %v, got %v", tt.wantTrust, got.NeedsTruststore)
			}
			if got.NeedsKeystore != tt.wantKey {
				t.Errorf("expected NeedsKeystore %v, got %v", tt.wantKey, got.NeedsKeystore)
			}
			if got.NeedsSASL != tt.wantSASL {
				t.Errorf("expected NeedsSASL %v, got %v", tt.wantSASL, got.NeedsSASL)
			}
			if got.SASLMechanism != tt.wantMechanism {
				t.Errorf("expected SASLMechanism %q, got %q", tt.wantMechanism, got.SASLMechanism)
			}
		})
	}
}

func TestBuildPodEnvAuthModes(t *testing.T) {
	inst := newTestInstance()
	const bootstrap = "kafka-bootstrap:9092"

	t.Run("SSL", func(t *testing.T) {
		auth := kafkaAuth{Protocol: "SSL", Mode: authMTLS, NeedsTruststore: true, NeedsKeystore: true}
		env, _ := buildPodEnv(inst, bootstrap, "", auth)
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", "SSL")
		requireEnvPresent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
		requireEnvPresent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
	})

	t.Run("SASL_SSL", func(t *testing.T) {
		auth := kafkaAuth{Protocol: "SASL_SSL", Mode: authSASLSSL, NeedsTruststore: true, NeedsSASL: true, SASLMechanism: saslMechanismSCRAMSHA512}
		env, _ := buildPodEnv(inst, bootstrap, "", auth)
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", "SASL_SSL")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_PASSWORD")
		requireEnvPresent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
		requireEnvPresent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_PASSWORD")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM", "SCRAM-SHA-512")
		jaas, ok := envByName(env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
		if !ok {
			t.Fatal("expected SASL JAAS config env var to be present")
		}
		requireSecretRef(t, jaas, inst.Name, "sasl.jaas.config")
	})

	t.Run("SASL_PLAINTEXT", func(t *testing.T) {
		auth := kafkaAuth{Protocol: "SASL_PLAINTEXT", Mode: authSASLPlaintext, NeedsSASL: true, SASLMechanism: saslMechanismSCRAMSHA512}
		env, _ := buildPodEnv(inst, bootstrap, "", auth)
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", "SASL_PLAINTEXT")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_PASSWORD")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_PASSWORD")
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM", "SCRAM-SHA-512")
		jaas, ok := envByName(env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
		if !ok {
			t.Fatal("expected SASL JAAS config env var to be present")
		}
		requireSecretRef(t, jaas, inst.Name, "sasl.jaas.config")
	})

	t.Run("PLAINTEXT", func(t *testing.T) {
		auth := kafkaAuth{Protocol: "PLAINTEXT", Mode: authPlaintext}
		env, _ := buildPodEnv(inst, bootstrap, "", auth)
		requireEnvValue(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SECURITY_PROTOCOL", "PLAINTEXT")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_KEYSTORE_PASSWORD")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_LOCATION")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SSL_TRUSTSTORE_PASSWORD")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_MECHANISM")
		requireEnvAbsent(t, env, "SCHEMA_REGISTRY_KAFKASTORE_SASL_JAAS_CONFIG")
	})
}

func TestBuildPodVolumesAuthModes(t *testing.T) {
	inst := newTestInstance()

	t.Run("SSL mounts JKS secret volume", func(t *testing.T) {
		auth := kafkaAuth{Mode: authMTLS, NeedsTruststore: true, NeedsKeystore: true}
		volumes, mounts := buildPodVolumes(inst, "", auth)
		v, ok := volumeByName(volumes, "tls")
		if !ok {
			t.Fatal("expected tls volume to be present")
		}
		if v.Secret == nil || v.Secret.SecretName != inst.Name+jksSecretSuffix {
			t.Errorf("expected tls volume to reference secret %q", inst.Name+jksSecretSuffix)
		}
		if len(mounts) == 0 || mounts[0].MountPath != "/var/schemaregistry" {
			t.Errorf("expected /var/schemaregistry volume mount")
		}
	})

	t.Run("SASL_SSL mounts JKS secret volume", func(t *testing.T) {
		auth := kafkaAuth{Mode: authSASLSSL, NeedsTruststore: true}
		volumes, _ := buildPodVolumes(inst, "", auth)
		v, ok := volumeByName(volumes, "tls")
		if !ok {
			t.Fatal("expected tls volume to be present")
		}
		if v.Secret == nil || v.Secret.SecretName != inst.Name+jksSecretSuffix {
			t.Errorf("expected tls volume to reference secret %q", inst.Name+jksSecretSuffix)
		}
	})

	t.Run("SASL_PLAINTEXT has no JKS volume", func(t *testing.T) {
		auth := kafkaAuth{Mode: authSASLPlaintext, NeedsSASL: true}
		volumes, _ := buildPodVolumes(inst, "", auth)
		if _, ok := volumeByName(volumes, "tls"); ok {
			t.Error("expected no tls volume")
		}
	})

	t.Run("PLAINTEXT has no JKS volume", func(t *testing.T) {
		auth := kafkaAuth{Mode: authPlaintext}
		volumes, _ := buildPodVolumes(inst, "", auth)
		if _, ok := volumeByName(volumes, "tls"); ok {
			t.Error("expected no tls volume")
		}
	})
}

func TestCreateSecretAuthBranching(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	t.Run("PLAINTEXT produces no secret", func(t *testing.T) {
		r := &StrimziSchemaRegistryReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			Scheme: scheme,
		}
		inst := newTestInstance()
		secret, created, err := r.createSecret(inst, context.Background(), logr.Logger{}, "test-cluster", nil, nil,
			kafkaAuth{Mode: authPlaintext, Protocol: "PLAINTEXT"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if secret != nil {
			t.Error("expected nil secret for PLAINTEXT")
		}
		if created {
			t.Error("expected created=false for PLAINTEXT")
		}
	})

	t.Run("SASL_PLAINTEXT produces no secret", func(t *testing.T) {
		r := &StrimziSchemaRegistryReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			Scheme: scheme,
		}
		inst := newTestInstance()
		secret, created, err := r.createSecret(inst, context.Background(), logr.Logger{}, "test-cluster", nil, nil,
			kafkaAuth{Mode: authSASLPlaintext, Protocol: "SASL_PLAINTEXT", NeedsSASL: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if secret != nil {
			t.Error("expected nil secret for SASL_PLAINTEXT")
		}
		if created {
			t.Error("expected created=false for SASL_PLAINTEXT")
		}
	})

	t.Run("SASL_SSL produces truststore-only secret", func(t *testing.T) {
		clusterCA, err := testutil.GenerateClusterCACert("STIMZI-SR-SASL-SSL-UNIT")
		if err != nil {
			t.Fatalf("failed to generate cluster CA: %v", err)
		}
		clusterSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-cluster-cluster-ca-cert", Namespace: "default"},
			Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
		}
		r := &StrimziSchemaRegistryReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterSecret).Build(),
			Scheme: scheme,
		}
		inst := newTestInstance()
		secret, created, err := r.createSecret(inst, context.Background(), logr.Logger{}, "test-cluster", nil, nil,
			kafkaAuth{Mode: authSASLSSL, Protocol: "SASL_SSL", NeedsTruststore: true, NeedsSASL: true, SASLMechanism: saslMechanismSCRAMSHA512})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !created {
			t.Error("expected created=true for SASL_SSL")
		}
		if secret == nil {
			t.Fatal("expected non-nil secret for SASL_SSL")
		}
		if secret.Name != inst.Name+jksSecretSuffix {
			t.Errorf("expected secret name %q, got %q", inst.Name+jksSecretSuffix, secret.Name)
		}
		if _, ok := secret.Data["truststore.jks"]; !ok {
			t.Error("expected truststore.jks in SASL_SSL secret")
		}
		if _, ok := secret.Data["truststore_password"]; !ok {
			t.Error("expected truststore_password in SASL_SSL secret")
		}
		if _, ok := secret.Data["keystore.jks"]; ok {
			t.Error("expected no keystore.jks in SASL_SSL secret")
		}
		if _, ok := secret.Data["keystore_password"]; ok {
			t.Error("expected no keystore_password in SASL_SSL secret")
		}
	})
}

func TestHandleSecretRotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = kafka.AddToScheme(scheme)

	t.Run("PLAINTEXT is a no-op even when deployment exists", func(t *testing.T) {
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr-deploy", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "image:tag"}}},
				},
			},
		}
		r := &StrimziSchemaRegistryReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(dep).Build(),
			Scheme: scheme,
		}
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "PLAINTEXT"
		result, err := r.handleSecretRotation(context.Background(), inst, logr.Logger{}, "test-cluster")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue, got %v", result.RequeueAfter)
		}
	})

	t.Run("SASL_PLAINTEXT rolls deployment when KafkaUser secret changes", func(t *testing.T) {
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr-deploy", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{saslUserVersionKey: "1"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "test", Image: "image:tag"}},
					},
				},
			},
		}
		user := newTestKafkaUser(kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE)
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr", Namespace: "default", ResourceVersion: "2"},
			Data:       map[string][]byte{"password": []byte("pw"), "sasl.jaas.config": []byte("jaas")},
		}
		r := &StrimziSchemaRegistryReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(dep, user, userSecret).Build(),
			Scheme: scheme,
		}
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "SASL_PLAINTEXT"
		result, err := r.handleSecretRotation(context.Background(), inst, logr.Logger{}, "test-cluster")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != time.Second {
			t.Errorf("expected requeue after 1s, got %v", result.RequeueAfter)
		}

		updated := &appsv1.Deployment{}
		if err := r.Get(context.Background(), types.NamespacedName{Name: "test-sr-deploy", Namespace: "default"}, updated); err != nil {
			t.Fatalf("failed to get updated deployment: %v", err)
		}
		if got := updated.Spec.Template.Annotations[saslUserVersionKey]; got != "2" {
			t.Errorf("expected sasl user version annotation %q, got %q", "2", got)
		}
	})

	t.Run("SASL_PLAINTEXT does not roll when KafkaUser secret is unchanged", func(t *testing.T) {
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr-deploy", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{saslUserVersionKey: "2"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "test", Image: "image:tag"}},
					},
				},
			},
		}
		user := newTestKafkaUser(kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE)
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sr", Namespace: "default", ResourceVersion: "2"},
			Data:       map[string][]byte{"password": []byte("pw"), "sasl.jaas.config": []byte("jaas")},
		}
		r := &StrimziSchemaRegistryReconciler{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(dep, user, userSecret).Build(),
			Scheme: scheme,
		}
		inst := newTestInstance()
		inst.Spec.SecurityProtocol = "SASL_PLAINTEXT"
		result, err := r.handleSecretRotation(context.Background(), inst, logr.Logger{}, "test-cluster")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue, got %v", result.RequeueAfter)
		}
	})
}
