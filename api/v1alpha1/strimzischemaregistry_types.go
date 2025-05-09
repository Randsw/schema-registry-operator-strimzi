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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// StrimziSchemaRegistrySpec defines the desired state of StrimziSchemaRegistry
type StrimziSchemaRegistrySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Listener           string                 `json:"listener"`
	SecurityProtocol   string                 `json:"securityprotocol"`
	CompatibilityLevel string                 `json:"compatibilitylevel"`
	SecureHTTP         bool                   `json:"securehttp"`
	TLSSecretName      string                 `json:"tlssecretname,omitempty"`
	Template           corev1.PodTemplateSpec `json:"template" protobuf:"bytes,6,opt,name=template"`
}

// StrimziSchemaRegistryStatus defines the observed state of StrimziSchemaRegistry
type StrimziSchemaRegistryStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
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
	SchemeBuilder.Register(&StrimziSchemaRegistry{}, &StrimziSchemaRegistryList{})
}
