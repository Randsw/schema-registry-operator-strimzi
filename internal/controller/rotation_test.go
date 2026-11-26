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
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	strimziregistryoperatorv1beta1 "github.com/randsw/schema-registry-operator-strimzi/api/v1beta1"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// annotationRolloutScheme builds the scheme shared by the rotation rollout
// tests.
func annotationRolloutScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = strimziregistryoperatorv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	return scheme
}

// newAnnotationRolloutDeployment returns a minimal Deployment fixture carrying
// the given pod-template annotations (none when the map is empty), mirroring
// the shape the rotation handlers patch.
func newAnnotationRolloutDeployment(name, namespace string, annotations map[string]string) *apps.Deployment {
	dep := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: apps.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "confluent-sr"}}},
			},
		},
	}
	if len(annotations) > 0 {
		dep.Spec.Template.Annotations = annotations
	}
	return dep
}

// deploymentAnnotation fetches the Deployment and returns the value of the
// given pod-template annotation.
func deploymentAnnotation(t *testing.T, c client.Client, depName types.NamespacedName, key string) string {
	t.Helper()
	dep := &apps.Deployment{}
	if err := c.Get(context.Background(), depName, dep); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	return dep.Spec.Template.Annotations[key]
}

// TestRollDeploymentForSecretVersion covers the shared annotation-driven
// rollout helper: the Deployment is patched (triggering a rollout) only when
// the recorded pod-template annotation differs from the secret version, which
// keeps the rollout recoverable after a failed Deployment update and free of
// spurious rollouts once the annotation matches.
func TestRollDeploymentForSecretVersion(t *testing.T) {
	scheme := annotationRolloutScheme(t)
	inst := newTestInstance()
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}
	newClient := func(dep *apps.Deployment) client.Client {
		builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst)
		if dep != nil {
			builder = builder.WithObjects(dep)
		}
		return builder.Build()
	}

	t.Run("patches when the annotation is missing", func(t *testing.T) {
		c := newClient(newAnnotationRolloutDeployment(depName.Name, depName.Namespace, nil))
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
		patched, err := r.rollDeploymentForSecretVersion(context.Background(), inst, logr.Logger{}, jksVersionKey, "3000", "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !patched {
			t.Fatal("expected the deployment to be patched when the annotation is missing")
		}
		if got := deploymentAnnotation(t, c, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the jksVersion annotation %q, got %q", "3000", got)
		}
	})

	t.Run("patches when the annotation differs from the secret version", func(t *testing.T) {
		c := newClient(newAnnotationRolloutDeployment(depName.Name, depName.Namespace, map[string]string{jksVersionKey: "2999"}))
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
		patched, err := r.rollDeploymentForSecretVersion(context.Background(), inst, logr.Logger{}, jksVersionKey, "3000", "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !patched {
			t.Fatal("expected the deployment to be patched when the annotation is stale")
		}
		if got := deploymentAnnotation(t, c, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the jksVersion annotation %q, got %q", "3000", got)
		}
	})

	t.Run("does not patch when the annotation matches the secret version", func(t *testing.T) {
		c := newClient(newAnnotationRolloutDeployment(depName.Name, depName.Namespace, map[string]string{jksVersionKey: "3000"}))
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
		patched, err := r.rollDeploymentForSecretVersion(context.Background(), inst, logr.Logger{}, jksVersionKey, "3000", "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if patched {
			t.Fatal("expected no patch when the annotation already matches the secret version")
		}
		if got := deploymentAnnotation(t, c, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the annotation to stay %q, got %q", "3000", got)
		}
	})

	t.Run("does nothing when the Deployment does not exist", func(t *testing.T) {
		c := newClient(nil)
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
		patched, err := r.rollDeploymentForSecretVersion(context.Background(), inst, logr.Logger{}, jksVersionKey, "3000", "test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if patched {
			t.Fatal("expected no patch before the Deployment exists")
		}
	})
}

// TestHandleMTLSSecretRotationAnnotationRollout verifies that the mTLS
// rotation handler drives the Deployment rollout from the jksVersion
// pod-template annotation: it patches when the annotation is missing or
// stale — including when the -jks secret itself is already up-to-date, which
// is the state left behind by a failed Deployment update — patches again when
// the user secret is rotated, and does not roll spuriously once the
// annotation matches.
func TestHandleMTLSSecretRotationAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	user, err := testutil.GenerateTestUserCert(ca, "user-password")
	if err != nil {
		t.Fatalf("failed to generate user certificate: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	auth := kafkaAuth{Protocol: "SSL", Mode: authMTLS, NeedsTruststore: true, NeedsKeystore: true}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	clusterCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	newUserSecret := func(resourceVersion string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name,
				Namespace:       inst.Namespace,
				ResourceVersion: resourceVersion,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt":        []byte(ca.CACertPEM),
				"user.crt":      []byte(user.UserCertPEM),
				"user.key":      []byte(user.UserKeyPEM),
				"user.p12":      user.PKCS12Data,
				"user.password": []byte(user.Password),
			},
		}
	}
	// An up-to-date -jks secret: its change markers match the cluster CA and
	// user secret versions, so the handler's secret-churn detection reports
	// no change and the rollout decision must come from the annotation.
	newJKSSecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + jksSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: "3000",
				Annotations: map[string]string{
					CAVersionKey:   "1000",
					userVersionKey: "2000",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"truststore.jks": []byte("stub")},
		}
	}
	newClient := func(userVersion string, depAnnotations map[string]string) *StrimziSchemaRegistryReconciler {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, clusterCASecret.DeepCopy(), newUserSecret(userVersion), newJKSSecret(),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, depAnnotations)).
			Build()
		return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	}

	t.Run("patches the deployment when the jksVersion annotation is missing", func(t *testing.T) {
		c := newClient("2000", nil)
		result, err := c.handleMTLSSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rollout patch")
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the jksVersion annotation %q, got %q", "3000", got)
		}
	})

	t.Run("does not patch when the annotation matches the secret version", func(t *testing.T) {
		c := newClient("2000", map[string]string{jksVersionKey: "3000"})
		result, err := c.handleMTLSSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue when nothing changed, got %v", result.RequeueAfter)
		}
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got != "3000" {
			t.Errorf("expected the annotation to stay %q, got %q", "3000", got)
		}
	})

	t.Run("patches after the user secret is rotated", func(t *testing.T) {
		c := newClient("2001", map[string]string{jksVersionKey: "3000"})
		result, err := c.handleMTLSSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rotation rollout")
		}
		// The rebuilt secret carries a new ResourceVersion, so the recorded
		// annotation must have moved off the stale value.
		if got := deploymentAnnotation(t, c.Client, depName, jksVersionKey); got == "" || got == "3000" {
			t.Errorf("expected a fresh jksVersion annotation after the rotation, got %q", got)
		}
		rebuilt := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + jksSecretSuffix, Namespace: inst.Namespace}, rebuilt); err != nil {
			t.Fatalf("failed to get the rebuilt -jks secret: %v", err)
		}
		if rebuilt.Annotations[userVersionKey] != "2001" {
			t.Errorf("expected the rebuilt secret to record the rotated user version, got %q", rebuilt.Annotations[userVersionKey])
		}
	})

	t.Run("does not spuriously roll on the following reconcile", func(t *testing.T) {
		c := newClient("2000", nil)
		ctx := context.Background()
		if _, err := c.handleMTLSSecretRotation(ctx, inst, logr.Logger{}, cluster, auth); err != nil {
			t.Fatalf("unexpected error on the first reconcile: %v", err)
		}
		result, err := c.handleMTLSSecretRotation(ctx, inst, logr.Logger{}, cluster, auth)
		if err != nil {
			t.Fatalf("unexpected error on the second reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue once the annotation matches, got %v", result.RequeueAfter)
		}
	})
}

// TestHandleSecretRotationResolveAuthError covers the auth-resolution error
// branch of the rotation dispatcher: a security protocol that requires TLS
// client authentication paired with a SCRAM KafkaUser cannot be resolved, so
// handleSecretRotation must return the error before dispatching to any
// rotation handler (and before creating a -jks secret).
func TestHandleSecretRotationResolveAuthError(t *testing.T) {
	scheme := newErrorPathScheme()

	inst := newTestInstance()
	inst.Spec.SecurityProtocol = "SSL"
	inst.Spec.Listener = "tls"
	inst.Spec.TLSSecretName = ""
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}
	// A SCRAM user under SSL: resolveKafkaAuth rejects the combination.
	user := newTestKafkaUser(kafka.SCRAM_SHA_512_KAFKAUSERAUTHENTICATIONTYPE)

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, user,
			newAnnotationRolloutDeployment(depName.Name, depName.Namespace, nil)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	result, err := r.handleSecretRotation(context.Background(), inst, logr.Logger{}, "test-cluster")
	if err == nil {
		t.Fatal("expected the SSL + SCRAM auth resolution error to be returned")
	}
	if !strings.Contains(err.Error(), "requires a KafkaUser with tls authentication") {
		t.Errorf("expected the auth resolution error, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on the error path, got %v", result.RequeueAfter)
	}

	// No rotation may have run: the -jks secret must not exist.
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: inst.Name + jksSecretSuffix, Namespace: inst.Namespace}, &corev1.Secret{}); err == nil {
		t.Error("expected no -jks secret to be created before the auth mode resolves")
	}
}

// TestHandleMTLSSecretRotationGetErrors covers the three secret lookups of the
// mTLS rotation handler: a generic (non-NotFound) Get failure on the -jks
// secret, the KafkaUser secret or the cluster CA secret must be returned so
// the reconcile retries. A swallowed error would leave the keystore stale
// while reconciliation reported success.
func TestHandleMTLSSecretRotationGetErrors(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	auth := kafkaAuth{Protocol: "SSL", Mode: authMTLS, NeedsTruststore: true, NeedsKeystore: true}

	jksSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name + jksSecretSuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "3000",
		},
	}
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name,
			Namespace:       inst.Namespace,
			ResourceVersion: "2000",
		},
	}

	cases := []struct {
		name     string
		objects  []client.Object
		failName string
		injected error
	}{
		{
			name:     "-jks secret Get error",
			objects:  []client.Object{inst},
			failName: inst.Name + jksSecretSuffix,
		},
		{
			name:     "KafkaUser secret Get error",
			objects:  []client.Object{inst, jksSecret.DeepCopy()},
			failName: inst.Name,
		},
		{
			name:     "cluster CA secret Get error",
			objects:  []client.Object{inst, jksSecret.DeepCopy(), userSecret.DeepCopy()},
			failName: cluster + clusterCASuffix,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			injected := errors.New("injected secret get failure")
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(tc.objects...).
				WithInterceptorFuncs(failGets(&corev1.Secret{}, tc.failName, injected)).
				Build()
			r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

			result, err := r.handleMTLSSecretRotation(context.Background(), inst, logr.Logger{}, cluster, auth)
			if !errors.Is(err, injected) {
				t.Fatalf("expected the injected Get error to be returned, got %v", err)
			}
			if result.RequeueAfter != 0 {
				t.Errorf("expected no requeue on the error path, got %v", result.RequeueAfter)
			}
		})
	}
}

// TestEnsureClientAuthTruststoreSecretPersistError covers the persist failure
// branch: when the truststore is (re)built but storing it fails, the error
// must be returned instead of reporting a secret that is not on the cluster.
func TestEnsureClientAuthTruststoreSecretPersistError(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-clientauth-persist")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.ClientAuth = true
	inst.Spec.SecureHTTP = true
	inst.Labels = map[string]string{strimziClusterLabel: cluster}

	clusterCASecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	injected := errors.New("injected secret create failure")

	// No -jks secret exists, so the truststore is newly built (created=true)
	// and persistJKSSecret Creates it — the injected failure.
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, clusterCASecret).
		WithInterceptorFuncs(failCreates(&corev1.Secret{}, injected)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	secret, created, err := r.ensureClientAuthTruststoreSecret(inst, context.Background(), logr.Logger{}, cluster)
	if !errors.Is(err, injected) {
		t.Fatalf("expected the persist failure to be returned, got %v", err)
	}
	if created {
		t.Error("expected created=false on the failed persist")
	}
	if secret != nil {
		t.Errorf("expected no secret on the failed persist, got %v", secret)
	}
}

// TestRollDeploymentForSecretVersion_UpdateError covers the U-7 error branch:
// a Deployment Update failure (e.g. a 409 Conflict) must be returned to the
// caller so the next reconcile retries, and the pod-template annotation must
// stay unchanged — the recorded annotation is only written by a successful
// update, which is what keeps the rollout recoverable.
func TestRollDeploymentForSecretVersion_UpdateError(t *testing.T) {
	scheme := annotationRolloutScheme(t)
	inst := newTestInstance()
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}
	injected := errors.New("injected deployment update conflict")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, newAnnotationRolloutDeployment(depName.Name, depName.Namespace, map[string]string{jksVersionKey: "2999"})).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*apps.Deployment); ok {
					return injected
				}
				return c.Update(ctx, obj, opts...)
			},
		}).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	patched, err := r.rollDeploymentForSecretVersion(context.Background(), inst, logr.Logger{}, jksVersionKey, "3000", "test")
	if err == nil {
		t.Fatal("expected the Deployment Update failure to be returned")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected the injected error, got %v", err)
	}
	if patched {
		t.Error("expected patched=false on the failed update")
	}
	if got := deploymentAnnotation(t, c, depName, jksVersionKey); got != "2999" {
		t.Errorf("expected the annotation to stay %q after the failed update, got %q", "2999", got)
	}
}
