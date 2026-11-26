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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kafka "github.com/scholzj/strimzi-go/pkg/apis/kafka.strimzi.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// TestHandleTLSCertExpiryAnnotationRollout verifies that the TLS expiry
// handler drives the Deployment rollout from the tlsVersion pod-template
// annotation: it patches when the annotation is stale — including when no
// renewal is due, which is the state left behind by a failed Deployment
// update — and does not roll spuriously once the annotation matches.
func TestHandleTLSCertExpiryAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-tls-expiry-annotation")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}
	cp := certprocessor.NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "test-sr", "default")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	caKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCAKeySuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1001",
		},
		Data: map[string][]byte{"ca.key": []byte(ca.CAKeyPEM)},
	}
	newTLSSecret := func(resourceVersion, caVersion string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + tlsSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: resourceVersion,
				Annotations:     map[string]string{CAVersionKey: caVersion},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"tls-keystore.jks":  keystore,
				"keystore_password": []byte(password),
				"key_password":      []byte(password),
			},
		}
	}
	newClient := func(caVersion, tlsVersion string) *StrimziSchemaRegistryReconciler {
		depAnnotations := map[string]string{}
		if tlsVersion != "" {
			depAnnotations[tlsVersionKey] = tlsVersion
		}
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy(), newTLSSecret("2000", caVersion),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, depAnnotations)).
			Build()
		return &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}
	}

	t.Run("does not patch when the annotation matches and the certificate is fresh", func(t *testing.T) {
		c := newClient("1000", "2000")
		result, recheck, err := c.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no immediate requeue when nothing changed, got %v", result.RequeueAfter)
		}
		if recheck != tlsCertRecheckInterval {
			t.Errorf("expected the standing recheck interval %v, got %v", tlsCertRecheckInterval, recheck)
		}
		if got := deploymentAnnotation(t, c.Client, depName, tlsVersionKey); got != "2000" {
			t.Errorf("expected the annotation to stay %q, got %q", "2000", got)
		}
	})

	t.Run("patches when the annotation is missing", func(t *testing.T) {
		// No renewal is due, but the Deployment does not record the secret
		// version at all — the shape of a deployment created before the
		// annotation-driven check existed, or whose marker was stripped. The
		// annotation check must record it and roll the pods once.
		c := newClient("1000", "")
		result, recheck, err := c.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the recovery patch")
		}
		if recheck != 0 {
			t.Errorf("expected no standing recheck alongside the immediate requeue, got %v", recheck)
		}
		if got := deploymentAnnotation(t, c.Client, depName, tlsVersionKey); got != "2000" {
			t.Errorf("expected the tlsVersion annotation %q, got %q", "2000", got)
		}
	})

	t.Run("patches after the cluster CA is rotated", func(t *testing.T) {
		// The -tls secret records the previous CA version, so the certificate
		// is renewed; the renewed secret's new ResourceVersion must be
		// recorded on the deployment to trigger the rollout.
		c := newClient("999", "2000")
		result, _, err := c.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the renewal rollout")
		}
		renewed := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, renewed); err != nil {
			t.Fatalf("failed to get the renewed -tls secret: %v", err)
		}
		if got := deploymentAnnotation(t, c.Client, depName, tlsVersionKey); got != renewed.ResourceVersion {
			t.Errorf("expected the tlsVersion annotation %q, got %q", renewed.ResourceVersion, got)
		}
	})
}

// TestRecreateMissingTLSSecretAnnotationRollout verifies that the missing
// -tls secret recreation rolls the Deployment through the tlsVersion
// pod-template annotation and stays a no-op before the Deployment exists.
func TestRecreateMissingTLSSecretAnnotationRollout(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-tls-recreate-annotation")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCASuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.crt": []byte(ca.CACertPEM)},
	}
	caKeySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cluster + clusterCAKeySuffix, Namespace: inst.Namespace},
		Data:       map[string][]byte{"ca.key": []byte(ca.CAKeyPEM)},
	}

	t.Run("patches the deployment after recreating the secret", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy(),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, nil)).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		result, err := r.recreateMissingTLSSecret(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter == 0 {
			t.Error("expected a requeue after the rollout patch")
		}
		recreated := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, recreated); err != nil {
			t.Fatalf("expected the -tls secret to be recreated: %v", err)
		}
		if got := deploymentAnnotation(t, c, depName, tlsVersionKey); got != recreated.ResourceVersion {
			t.Errorf("expected the tlsVersion annotation %q, got %q", recreated.ResourceVersion, got)
		}
	})

	t.Run("does nothing when the Deployment does not exist", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, caCertSecret.DeepCopy(), caKeySecret.DeepCopy()).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		result, err := r.recreateMissingTLSSecret(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no requeue before the Deployment exists, got %v", result.RequeueAfter)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, &corev1.Secret{}); err == nil {
			t.Error("expected no -tls secret to be created before the Deployment exists")
		}
	})
}

// TestHandleTLSCertExpiryNoKeystoreMaterial covers the skip branch for a -tls
// secret that cannot be expiry-checked: the keystore bytes or the keystore
// password are missing. The certificate must not be renewed and reconciliation
// must not fail; the standing recheck keeps the secret inspected so a later
// repair is picked up without a watch event.
func TestHandleTLSCertExpiryNoKeystoreMaterial(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	// The recorded CA version matches the cluster CA secret, so the CA-change
	// branch is not taken and the expiry branch is evaluated.
	caCertSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            cluster + clusterCASuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "1000",
		},
		Data: map[string][]byte{"ca.crt": []byte("stub")},
	}
	newTLSSecret := func(data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            inst.Name + tlsSecretSuffix,
				Namespace:       inst.Namespace,
				ResourceVersion: "2000",
				Annotations:     map[string]string{CAVersionKey: "1000"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
	}

	cases := []struct {
		name string
		data map[string][]byte
	}{
		{"tls-keystore.jks is missing", map[string][]byte{"keystore_password": []byte("pw")}},
		{"keystore_password is missing", map[string][]byte{"tls-keystore.jks": []byte("stub")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(inst, caCertSecret.DeepCopy(), newTLSSecret(tc.data),
					newAnnotationRolloutDeployment(depName.Name, depName.Namespace, map[string]string{tlsVersionKey: "2000"})).
				Build()
			r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

			result, recheck, err := r.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
			if err != nil {
				t.Fatalf("expected the missing keystore material to be skipped without an error, got %v", err)
			}
			if result.RequeueAfter != 0 {
				t.Errorf("expected no immediate requeue on the skip path, got %v", result.RequeueAfter)
			}
			if recheck != tlsCertRecheckInterval {
				t.Errorf("expected the standing recheck interval %v, got %v", tlsCertRecheckInterval, recheck)
			}

			untouched := &corev1.Secret{}
			if err := c.Get(context.Background(),
				types.NamespacedName{Name: inst.Name + tlsSecretSuffix, Namespace: inst.Namespace}, untouched); err != nil {
				t.Fatalf("failed to get the -tls secret: %v", err)
			}
			if untouched.ResourceVersion != "2000" {
				t.Errorf("expected the secret to be left untouched, got resourceVersion %s", untouched.ResourceVersion)
			}
		})
	}
}

// TestHandleTLSCertExpiryCASecretGetError covers the cluster CA Get failure in
// the CA-version renewal check: a generic API error must be returned so the
// reconcile retries. Treating it as "CA not rotated" would silently keep a
// certificate signed by a retired CA.
func TestHandleTLSCertExpiryCASecretGetError(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}

	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name + tlsSecretSuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "2000",
			Annotations:     map[string]string{CAVersionKey: "1000"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  []byte("stub"),
			"keystore_password": []byte("pw"),
		},
	}
	injected := errors.New("injected cluster CA secret get failure")

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(inst, tlsSecret).
		WithInterceptorFuncs(failGets(&corev1.Secret{}, cluster+clusterCASuffix, injected)).
		Build()
	r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

	result, recheck, err := r.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
	if !errors.Is(err, injected) {
		t.Fatalf("expected the cluster CA secret Get failure to be returned, got %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on the error path, got %v", result.RequeueAfter)
	}
	if recheck != 0 {
		t.Errorf("expected no standing recheck on the error path, got %v", recheck)
	}
}

// TestHandleTLSCertExpiryCASecretNotFound covers the missing cluster CA secret
// branch: without a CA version to compare, the handler skips only the
// CA-rotation check and still evaluates the certificate expiry. With a fresh
// certificate that is a clean no-op plus the standing recheck; when renewal is
// due the handler proceeds past the check and surfaces the missing signing
// material instead of silently skipping the renewal.
func TestHandleTLSCertExpiryCASecretNotFound(t *testing.T) {
	scheme := annotationRolloutScheme(t)

	ca, err := testutil.GenerateClusterCACert("test-tls-ca-notfound")
	if err != nil {
		t.Fatalf("failed to generate cluster CA: %v", err)
	}
	cp := certprocessor.NewCertProcessor(logr.Logger{})
	keystore, password, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "test-sr", "default")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	cluster := "test-cluster"
	inst := newTestInstance()
	inst.Spec.SecureHTTP = true
	inst.Spec.TLSSecretName = ""
	inst.Spec.SecurityProtocol = "PLAINTEXT"
	inst.Spec.Listener = "plaintext"
	inst.Labels = map[string]string{strimziClusterLabel: cluster}
	depName := types.NamespacedName{Name: inst.Name + deploySuffix, Namespace: inst.Namespace}

	// No cluster CA cert/key secrets exist at all.
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            inst.Name + tlsSecretSuffix,
			Namespace:       inst.Namespace,
			ResourceVersion: "2000",
			Annotations:     map[string]string{CAVersionKey: "1000"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  keystore,
			"keystore_password": []byte(password),
			"key_password":      []byte(password),
		},
	}

	t.Run("fresh certificate keeps the standing recheck instead of failing", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, tlsSecret.DeepCopy(),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, map[string]string{tlsVersionKey: "2000"})).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		result, recheck, err := r.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster)
		if err != nil {
			t.Fatalf("expected the missing CA secret to be skipped without an error, got %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Errorf("expected no immediate requeue for a fresh certificate, got %v", result.RequeueAfter)
		}
		if recheck != tlsCertRecheckInterval {
			t.Errorf("expected the standing recheck interval %v, got %v", tlsCertRecheckInterval, recheck)
		}
	})

	t.Run("renewal is attempted when the certificate is inside the window", func(t *testing.T) {
		originalThreshold := tlsCertRenewalThreshold
		tlsCertRenewalThreshold = 400 * 24 * time.Hour
		defer func() { tlsCertRenewalThreshold = originalThreshold }()

		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(inst, tlsSecret.DeepCopy(),
				newAnnotationRolloutDeployment(depName.Name, depName.Namespace, map[string]string{tlsVersionKey: "2000"})).
			Build()
		r := &StrimziSchemaRegistryReconciler{Client: c, Scheme: scheme}

		// The expiry branch is reached (past the skipped CA-version check) and
		// the renewal cannot proceed without the signing CA material, so the
		// failure is surfaced rather than silently swallowed.
		if _, _, err := r.handleTLSCertExpiry(context.Background(), inst, logr.Logger{}, cluster); err == nil {
			t.Fatal("expected the renewal to fail when the cluster CA material is missing")
		}
	})
}

// F2: the expiry-based renewal must also work through the full Reconcile entry
// point, not only when handleTLSCertExpiry is called directly. This spec pins
// the renewal threshold, reconciles a SecureHTTP CR with an operator-managed
// certificate, and asserts the secret is renewed, the Deployment rolls, and a
// restored (default) threshold makes the next pass a no-op that schedules the
// standing recheck interval.
var _ = Describe("Operator-managed REST API TLS expiry renewal (F2)", func() {
	const (
		registryName = "tls-expiry-sr"
		kafkaName    = "kafka-cluster"
	)

	ctx := context.Background()
	specCounter := 0

	var (
		namespace         *corev1.Namespace
		registryKey       types.NamespacedName
		originalThreshold time.Duration
	)

	BeforeEach(func() {
		specCounter++
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-tls-expiry-%d", specCounter),
		}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		registryKey = types.NamespacedName{Name: registryName, Namespace: namespace.Name}
		originalThreshold = tlsCertRenewalThreshold
		Expect(os.Setenv("STRIMZIREGISTRYOPERATOR_IMAGE", "ghcr.io/randsw/strimzi-schema-registry-operator")).To(Succeed())
	})

	AfterEach(func() {
		tlsCertRenewalThreshold = originalThreshold
		found := &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{}
		if err := k8sClient.Get(ctx, registryKey, found); err == nil {
			if len(found.Finalizers) > 0 {
				found.Finalizers = []string{}
				_ = k8sClient.Update(ctx, found)
			}
			_ = k8sClient.Delete(ctx, found)
		}
		_ = k8sClient.Delete(ctx, namespace)
		_ = os.Unsetenv("STRIMZIREGISTRYOPERATOR_IMAGE")
	})

	It("renews an expiring certificate and rolls the deployment through Reconcile", func() {
		clusterCA, err := testutil.GenerateClusterCACert("TLS-EXPIRY-ENVTEST")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: kafkaName + clusterCASuffix, Namespace: namespace.Name},
			Data:       map[string][]byte{"ca.crt": []byte(clusterCA.CACertPEM)},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: kafkaName + clusterCAKeySuffix, Namespace: namespace.Name},
			Data:       map[string][]byte{"ca.key": []byte(clusterCA.CAKeyPEM)},
		})).To(Succeed())

		By("creating the Kafka CR with a plaintext listener and its status")
		Expect(k8sClient.Create(ctx, &kafka.Kafka{
			ObjectMeta: metav1.ObjectMeta{Name: kafkaName, Namespace: namespace.Name},
			Spec: &kafka.KafkaSpec{Kafka: &kafka.KafkaClusterSpec{
				Listeners: []kafka.GenericKafkaListener{{
					Name: "plaintext",
					Port: 9092,
					Tls:  false,
					Type: kafka.INTERNAL_KAFKALISTENERTYPE,
				}},
				Version: "4.1.0",
			}},
		})).To(Succeed())
		currentKafka := &kafka.Kafka{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kafkaName, Namespace: namespace.Name}, currentKafka)).To(Succeed())
		currentKafka.Status = &kafka.KafkaStatus{
			ClusterId: "tls-expiry-cluster",
			Listeners: []kafka.ListenerStatus{{
				Name:             "plaintext",
				BootstrapServers: fmt.Sprintf("%s-kafka-bootstrap.%s.svc:9092", kafkaName, namespace.Name),
			}},
		}
		Expect(k8sClient.Status().Update(ctx, currentKafka)).To(Succeed())

		By("creating the SecureHTTP CR with an operator-managed TLS secret")
		Expect(k8sClient.Create(ctx, &strimziregistryoperatorv1alpha1.StrimziSchemaRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      registryName,
				Namespace: namespace.Name,
				Labels:    map[string]string{strimziClusterLabel: kafkaName},
			},
			Spec: strimziregistryoperatorv1alpha1.StrimziSchemaRegistrySpec{
				SecureHTTP:         true,
				Listener:           "plaintext",
				CompatibilityLevel: "forward",
				SecurityProtocol:   "PLAINTEXT",
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "test", Image: "confluentinc/cp-schema-registry:8.4.1"}},
				}},
			},
		})).To(Succeed())

		reconciler := &StrimziSchemaRegistryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		tlsSecretKey := types.NamespacedName{Name: registryName + tlsSecretSuffix, Namespace: namespace.Name}
		deploymentKey := types.NamespacedName{Name: registryName + deploySuffix, Namespace: namespace.Name}

		By("reconciling until the CR is fully settled")
		// Each create (Deployment, then Service) requeues, so keep driving
		// Reconcile until a pass performs no action and returns the standing
		// TLS recheck interval.
		Eventually(func(g Gomega) time.Duration {
			res, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
			g.Expect(reconcileErr).NotTo(HaveOccurred())
			svc := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: registryName, Namespace: namespace.Name}, svc)).To(Succeed())
			g.Expect(k8sClient.Get(ctx, tlsSecretKey, &corev1.Secret{})).To(Succeed())
			return res.RequeueAfter
		}, 30*time.Second, 250*time.Millisecond).Should(
			Equal(jitteredRecheckInterval(tlsCertRecheckInterval)))

		beforeSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, tlsSecretKey, beforeSecret)).To(Succeed())
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, deploymentKey, dep)).To(Succeed())
		Expect(dep.Spec.Template.Annotations).To(
			HaveKeyWithValue(tlsVersionKey, beforeSecret.ResourceVersion),
			"the tlsVersion rollout marker must be recorded at creation")

		cp := certprocessor.NewCertProcessor(logr.Logger{})
		notAfterBefore, err := cp.KeystoreCertExpiry(ctx, beforeSecret.Data["tls-keystore.jks"],
			string(beforeSecret.Data["keystore_password"]))
		Expect(err).NotTo(HaveOccurred())
		beforeVersion := beforeSecret.ResourceVersion

		By("pinning the renewal threshold so the fresh certificate counts as expiring soon")
		// The certificate timestamps have second precision: let the clock move
		// on so the renewed certificate carries a strictly later NotAfter.
		time.Sleep(1100 * time.Millisecond)
		tlsCertRenewalThreshold = time.Until(notAfterBefore) + time.Hour

		By("reconciling: the full entry point must renew and roll the deployment")
		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(time.Second),
			"the renewal pass must requeue so the expiry check re-runs")

		afterSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, tlsSecretKey, afterSecret)).To(Succeed())
		Expect(afterSecret.ResourceVersion).NotTo(Equal(beforeVersion),
			"the TLS secret must be renewed")
		notAfterAfter, err := cp.KeystoreCertExpiry(ctx, afterSecret.Data["tls-keystore.jks"],
			string(afterSecret.Data["keystore_password"]))
		Expect(err).NotTo(HaveOccurred())
		Expect(notAfterAfter.After(notAfterBefore)).To(BeTrue(),
			"the renewed certificate must expire later than the original")

		Expect(k8sClient.Get(ctx, deploymentKey, dep)).To(Succeed())
		Expect(dep.Spec.Template.Annotations).To(
			HaveKeyWithValue(tlsVersionKey, afterSecret.ResourceVersion),
			"the tlsVersion rollout marker must track the renewed secret")

		By("restoring the default threshold: the same Reconcile must not renew again")
		tlsCertRenewalThreshold = originalThreshold
		renewedVersion := afterSecret.ResourceVersion
		result, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: registryKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(jitteredRecheckInterval(tlsCertRecheckInterval)),
			"a fresh certificate must schedule the standing recheck interval")

		stable := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, tlsSecretKey, stable)).To(Succeed())
		Expect(stable.ResourceVersion).To(Equal(renewedVersion),
			"the certificate must not be renewed again outside the renewal window")
		Expect(k8sClient.Get(ctx, deploymentKey, dep)).To(Succeed())
		Expect(dep.Spec.Template.Annotations).To(HaveKeyWithValue(tlsVersionKey, renewedVersion))
	})
})
