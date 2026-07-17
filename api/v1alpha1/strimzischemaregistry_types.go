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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StrimziSchemaRegistry manages Confluent Schema Registry deployment
// alongside Strimzi Kafka clusters, handling TLS certificates and
// keystore/truststore generation automatically.

// StrimziSchemaRegistrySpec defines the desired state of StrimziSchemaRegistry
type StrimziSchemaRegistrySpec struct {
	// Listener name for Kafka cluster (defaults to "tls")
	// +kubebuilder:default="tls"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern="^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,253}[a-zA-Z0-9])?$"
	Listener string `json:"listener,omitempty"`

	// SecurityProtocol defines the Kafka security protocol to use.
	// +kubebuilder:default="SSL"
	// Valid values: SSL, SASL_SSL, PLAINTEXT, SASL_PLAINTEXT.
	// +kubebuilder:validation:Enum=SSL;SASL_SSL;PLAINTEXT;SASL_PLAINTEXT
	SecurityProtocol string `json:"securityprotocol,omitempty"`

	// CompatibilityLevel defines the schema compatibility level for Schema Registry.
	// Valid values: none, backward, backward_transitive, forward, forward_transitive, full, full_transitive.
	// +kubebuilder:default="forward"
	// +kubebuilder:validation:Enum=none;backward;backward_transitive;forward;forward_transitive;full;full_transitive
	CompatibilityLevel string `json:"compatibilitylevel,omitempty"`

	SecureHTTP bool `json:"securehttp"`

	// TLSSecretName is the name of the Kubernetes secret containing TLS certificates.
	// Must be a valid Kubernetes resource name (DNS subdomain).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$"
	TLSSecretName string `json:"tlssecretname,omitempty"`

	// HeapOpts sets the JVM heap options for Schema Registry (defaults to "-Xms512M -Xmx512M")
	// +kubebuilder:validation:Pattern="^(-Xms[a-fA-F0-9]+(m|M|g|G) -Xmx[a-fA-F0-9]+(m|M|g|G))?$"
	HeapOpts string `json:"heapopts,omitempty"`

	Template corev1.PodTemplateSpec `json:"template"`
}

// StrimziSchemaRegistryStatus defines the observed state of StrimziSchemaRegistry
type StrimziSchemaRegistryStatus struct {
	// Status of the Schema Registry deployment (Ok, Not Ready, etc.)
	Status string `json:"status"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.status",description="The status of Schema Registry"
// StrimziSchemaRegistry is the Schema for the strimzischemaregistries API
type StrimziSchemaRegistry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StrimziSchemaRegistrySpec   `json:"spec,omitempty"`
	Status StrimziSchemaRegistryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// StrimziSchemaRegistryList contains a list of StrimziSchemaRegistry
type StrimziSchemaRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StrimziSchemaRegistry `json:"items"`
}

func init() {
	SchemeBuilder.Register(addKnownTypes)
}
