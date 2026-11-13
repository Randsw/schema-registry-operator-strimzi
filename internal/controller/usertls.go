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

	"github.com/go-logr/logr"
	strimziregistryoperatorv1alpha1 "github.com/randsw/schema-registry-operator-strimzi/api/v1alpha1"
	certprocessor "github.com/randsw/schema-registry-operator-strimzi/certProcessor"
	"github.com/randsw/schema-registry-operator-strimzi/metrics"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

// userSecretVersionKey annotates the derived <name>-<tlssecretname>-jks secret with
// the resourceVersion of the user secret it was converted from, so the
// conversion is regenerated only when the user secret actually changes
// (idempotent, no reconcile loops).
const userSecretVersionKey = keyPrefix + "/user-secret-resourceVersion"

// hasPEMTLSSecretKeys reports whether the secret carries the standard
// kubernetes.io/tls keys (tls.crt and tls.key), regardless of their values.
// It is used by validation to recognize a secret that is *intended* as a PEM
// secret so it can produce a precise error message for a partial shape.
func hasPEMTLSSecretKeys(secret *v1.Secret) bool {
	if secret == nil {
		return false
	}
	_, hasCert := secret.Data["tls.crt"]
	_, hasKey := secret.Data["tls.key"]
	return hasCert && hasKey
}

// isPEMTLSSecret reports whether the user TLS secret carries the standard
// kubernetes.io/tls shape (non-empty PEM tls.crt + tls.key) instead of a
// pre-built keystore. ca.crt is optional and does not affect detection.
func isPEMTLSSecret(secret *v1.Secret) bool {
	if secret == nil {
		return false
	}
	return len(secret.Data["tls.crt"]) > 0 && len(secret.Data["tls.key"]) > 0
}

// derivedJKSSecretName returns the name of the operator-managed secret that
// holds the JKS conversion of a PEM user TLS secret. The user secret is
// mounted read-only, so the conversion output cannot be written back into it.
// The name is scoped to the owning registry (<name>-<tlssecretname>-jks) so
// two registries referencing the same PEM secret each get their own derived
// secret: a shared, single-owner secret would be garbage-collected when the
// owning registry is deleted, breaking every other registry still mounting
// it.
func derivedJKSSecretName(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) string {
	return instance.Name + "-" + instance.Spec.TLSSecretName + jksSecretSuffix
}

// derivedSecretNameValid returns a non-empty message when the per-registry
// derived secret name would exceed the Kubernetes object-name limit
// (253 characters), or an empty string otherwise.
func derivedSecretNameValid(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry) string {
	derived := derivedJKSSecretName(instance)
	if len(derived) > 253 {
		return fmt.Sprintf("user-provided TLS secret %q would be converted into derived secret %q whose name exceeds the Kubernetes 253-character object-name limit; choose a shorter spec.tlssecretname or CR name",
			instance.Spec.TLSSecretName, derived)
	}
	return ""
}

// validateUserTLSSecret returns a non-empty message describing why the
// user-provided REST API TLS secret cannot be used, or an empty string when
// it is valid. It dispatches on the accepted secret shapes:
//   - a standard kubernetes.io/tls secret (tls.crt + tls.key PEM, ca.crt
//     optional): both keys must be non-empty and the derived
//     <name>-<tlssecretname>-jks name must fit the Kubernetes object-name
//     limit;
//   - a pre-built keystore: validated by userTLSSecretValidationError
//     following spec.keystoretype (tls-keystore.jks or tls-keystore.p12).
func validateUserTLSSecret(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, secret *v1.Secret) string {
	if secret == nil {
		return fmt.Sprintf("user-provided TLS secret %q not found", instance.Spec.TLSSecretName)
	}
	if hasPEMTLSSecretKeys(secret) {
		var missing []string
		for _, key := range []string{"tls.crt", "tls.key"} {
			if len(secret.Data[key]) == 0 {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return fmt.Sprintf("user-provided TLS secret %q looks like a kubernetes.io/tls secret but is missing required key(s) or has empty values: %s",
				instance.Spec.TLSSecretName, strings.Join(missing, ", "))
		}
		return derivedSecretNameValid(instance)
	}
	return userTLSSecretValidationError(instance.Spec.TLSSecretName, secret, effectiveKeystoreType(instance))
}

// buildDerivedJKSSecret converts the PEM user TLS secret into the JKS
// keystore contract and returns the desired derived secret. The user
// secret's resourceVersion is recorded in an annotation so the conversion is
// regenerated only when the user secret changes. ctx bounds the openssl and
// keytool child processes in the conversion (B5).
func (r *StrimziSchemaRegistryReconciler) buildDerivedJKSSecret(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	userSecret *v1.Secret,
	logger logr.Logger,
) (*v1.Secret, error) {
	cp := certprocessor.NewCertProcessor(logger)
	keystore, keystorePassword, err := cp.ConvertPEMToJKS(
		ctx,
		string(userSecret.Data["tls.crt"]),
		string(userSecret.Data["tls.key"]),
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to convert PEM user TLS secret %q to JKS: %w", userSecret.Name, err)
	}
	derived := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      derivedJKSSecretName(instance),
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"app":  "strimzi-schema-registry",
				"user": instance.Name,
			},
			Annotations: map[string]string{
				userSecretVersionKey: userSecret.ResourceVersion,
			},
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"tls-keystore.jks":  keystore,
			"keystore_password": []byte(keystorePassword),
			"key_password":      []byte(keystorePassword),
		},
	}
	if err := ctrl.SetControllerReference(instance, derived, r.Scheme); err != nil {
		logger.Error(err, "Failed to set StrimziSchemaRegistry instance as the owner and controller")
		return nil, err
	}
	return derived, nil
}

// ensureDerivedJKSSecret creates or updates the derived
// <name>-<tlssecretname>-jks secret for a PEM user TLS secret. It is a no-op
// when the derived secret exists, is controlled by this registry, and its
// user-secret-resourceVersion annotation matches the current user secret
// version. The secret is updated in place when stale so it is never absent
// for pods mounting it. An existing secret controlled by another
// StrimziSchemaRegistry — or by nothing — is never adopted or overwritten:
// overwriting another registry's conversion output would corrupt its REST
// API TLS material, and adopting an unowned secret would silently take over
// user-managed material.
func (r *StrimziSchemaRegistryReconciler) ensureDerivedJKSSecret(
	ctx context.Context,
	instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry,
	userSecret *v1.Secret,
	logger logr.Logger,
) error {
	name := derivedJKSSecretName(instance)
	existing := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: instance.Namespace}, existing)
	if err == nil {
		if owner := metav1.GetControllerOf(existing); owner == nil {
			return fmt.Errorf("derived TLS secret %q already exists but is not controlled by a StrimziSchemaRegistry; delete it so the operator can manage it", name)
		} else if owner.Kind != "StrimziSchemaRegistry" || owner.Name != instance.Name {
			return fmt.Errorf("derived TLS secret %q already exists and is controlled by %s %q; delete it or choose a different spec.tlssecretname", name, owner.Kind, owner.Name)
		}
		if existing.Annotations[userSecretVersionKey] == userSecret.ResourceVersion {
			logger.V(1).Info("Derived JKS secret is up-to-date", "Secret.Name", name)
			return nil
		}
		desired, err := r.buildDerivedJKSSecret(ctx, instance, userSecret, logger)
		if err != nil {
			return err
		}
		// Carry the ResourceVersion so the update applies in place.
		desired.ResourceVersion = existing.ResourceVersion
		if err := r.Update(ctx, desired); err != nil {
			logger.Error(err, "Failed to update derived JKS secret", "Secret.Name", name)
			return err
		}
		logger.Info("Regenerated derived JKS secret after user TLS secret change",
			"Secret.Name", name, "UserSecret.Name", userSecret.Name)
		return nil
	}
	if !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get derived JKS secret", "Secret.Name", name)
		return err
	}
	desired, err := r.buildDerivedJKSSecret(ctx, instance, userSecret, logger)
	if err != nil {
		return err
	}
	if err := r.Create(ctx, desired); err != nil {
		logger.Error(err, "Failed to create derived JKS secret", "Secret.Name", name)
		return err
	}
	metrics.StrimziSchemaRegistrySecretRotationTotal.Inc()
	logger.Info("Created derived JKS secret from PEM user TLS secret",
		"Secret.Name", name, "UserSecret.Name", userSecret.Name)
	return nil
}

// restAPITLSMountSecretName returns the name of the secret the Deployment
// should mount for the REST API TLS keystore: the derived
// <tlssecretname>-jks secret for PEM user secrets, the user secret itself
// for pre-built keystores, and the operator-managed <name>-tls secret when
// no user secret is set.
func restAPITLSMountSecretName(instance *strimziregistryoperatorv1alpha1.StrimziSchemaRegistry, userSecret *v1.Secret) string {
	if instance.Spec.TLSSecretName == "" {
		return instance.Name + tlsSecretSuffix
	}
	if isPEMTLSSecret(userSecret) {
		return derivedJKSSecretName(instance)
	}
	return instance.Spec.TLSSecretName
}

// restAPITLSVolumeSecretName returns the secret name the Deployment's
// rest-api-tls volume currently mounts, or an empty string when the volume
// is absent or not secret-backed.
func restAPITLSVolumeSecretName(dep *apps.Deployment) string {
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.Name == restAPITLSVolumeName && vol.Secret != nil {
			return vol.Secret.SecretName
		}
	}
	return ""
}

// fixRestAPITLSMount repoints the Deployment's REST API TLS material at the
// given secret: the rest-api-tls volume and the keystore/key password env
// references. It is used when the user TLS secret shape changes (pre-built
// keystore ↔ kubernetes.io/tls) so the mounts and env refs stay consistent
// with the secret the rollout loads.
func fixRestAPITLSMount(dep *apps.Deployment, mountSecretName string) {
	for i := range dep.Spec.Template.Spec.Volumes {
		vol := &dep.Spec.Template.Spec.Volumes[i]
		if vol.Name == restAPITLSVolumeName && vol.Secret != nil {
			vol.Secret.SecretName = mountSecretName
		}
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return
	}
	env := dep.Spec.Template.Spec.Containers[0].Env
	for i := range env {
		e := &env[i]
		if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			continue
		}
		if e.Name == sslKeystorePasswordEnv || e.Name == sslKeyPasswordEnv {
			e.ValueFrom.SecretKeyRef.Name = mountSecretName
		}
	}
}
