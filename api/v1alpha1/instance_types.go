/*
Copyright 2026.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Size is a t-shirt size that maps to a ResourceQuota and container resources.
// +kubebuilder:validation:Enum=small;medium;large
type Size string

// Ring is the upgrade ring an Instance belongs to.
// +kubebuilder:validation:Enum=internal;trial;prod
type Ring string

// ConnectivityMode selects the private connectivity flavour rendered into Terraform.
// +kubebuilder:validation:Enum=none;psc;privatelink
type ConnectivityMode string

// Phase is the coarse lifecycle phase of an Instance.
// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Deleting
type Phase string

const (
	SizeSmall  Size = "small"
	SizeMedium Size = "medium"
	SizeLarge  Size = "large"

	RingInternal Ring = "internal"
	RingTrial    Ring = "trial"
	RingProd     Ring = "prod"

	ConnectivityNone        ConnectivityMode = "none"
	ConnectivityPSC         ConnectivityMode = "psc"
	ConnectivityPrivateLink ConnectivityMode = "privatelink"

	PhasePending      Phase = "Pending"
	PhaseProvisioning Phase = "Provisioning"
	PhaseReady        Phase = "Ready"
	PhaseFailed       Phase = "Failed"
	PhaseDeleting     Phase = "Deleting"
)

// Condition types reported in status.conditions.
const (
	ConditionReady              = "Ready"
	ConditionKubernetesReady    = "KubernetesReady"
	ConditionTerraformRendered  = "TerraformRendered"
	ConditionTerraformValidated = "TerraformValidated"
)

// Connectivity configures private connectivity for an Instance.
// +kubebuilder:validation:XValidation:rule="self.mode == 'none' || (has(self.allowedProjects) && size(self.allowedProjects) > 0)",message="allowedProjects is required when mode is not none"
type Connectivity struct {
	// mode selects the private connectivity flavour.
	// +kubebuilder:default=none
	Mode ConnectivityMode `json:"mode,omitempty"`

	// allowedProjects lists the consumer projects/accounts allowed to connect.
	// +kubebuilder:validation:MaxItems=10
	// +optional
	AllowedProjects []string `json:"allowedProjects,omitempty"`
}

// InstanceSpec defines the desired state of Instance
type InstanceSpec struct {
	// tenant becomes the namespace inst-<tenant>. It is immutable.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tenant is immutable"
	Tenant string `json:"tenant"`

	// version is the podinfo image tag (semver, no prefix).
	// +required
	// +kubebuilder:validation:Pattern=`^\d+\.\d+\.\d+$`
	Version string `json:"version"`

	// size maps to ResourceQuota and container resources.
	// +kubebuilder:default=small
	// +optional
	Size Size `json:"size,omitempty"`

	// region is used only in rendered Terraform.
	// +kubebuilder:default=us-central1
	// +optional
	Region string `json:"region,omitempty"`

	// ring is the upgrade ring (used by the stretch rollout controller).
	// +kubebuilder:default=prod
	// +optional
	Ring Ring `json:"ring,omitempty"`

	// connectivity configures optional private connectivity.
	// +optional
	Connectivity *Connectivity `json:"connectivity,omitempty"`
}

// InstanceStatus defines the observed state of Instance.
type InstanceStatus struct {
	// phase is the coarse lifecycle phase.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// observedGeneration is the metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// terraformHash is the hash of the Terraform inputs last rendered.
	// +optional
	TerraformHash string `json:"terraformHash,omitempty"`

	// lastTaskName is the name of the most recent Terraform task Job.
	// +optional
	LastTaskName string `json:"lastTaskName,omitempty"`

	// conditions represent the current state of the Instance resource.
	// Types: Ready, KubernetesReady, TerraformRendered, TerraformValidated.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Tenant",type=string,JSONPath=`.spec.tenant`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Ring",type=string,JSONPath=`.spec.ring`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// Instance is the Schema for the instances API
type Instance struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Instance
	// +required
	Spec InstanceSpec `json:"spec"`

	// status defines the observed state of Instance
	// +optional
	Status InstanceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// InstanceList contains a list of Instance
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Instance{}, &InstanceList{})
		return nil
	})
}
