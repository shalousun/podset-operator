/*


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

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	Image string = "nginx:alpine"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PodSetSpec defines the desired state of PodSet
type PodSetSpec struct {
	Replicas   int32    `json:"replicas"`
	Option     string   `json:"option"`
	PodLists   []string `json:"podLists"`
	Port       int32    `json:"port"`
	TargetPort int32    `json:"targetPort"`
	// 持久化存储类型
	StorageClass string `json:"storageClass,omitempty"`
	// 存储大小
	StorageSize string `json:"storageSize,omitempty"`

	Env []corev1.EnvVar `json:"env"`
}

// PodSetStatus defines the observed state of PodSet
type PodSetStatus struct {
	Replicas int32    `json:"replicas"`
	PodNames []string `json:"podNames"`
}

// +kubebuilder:object:root=true

// PodSet is the Schema for the podsets API
type PodSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodSetSpec   `json:"spec,omitempty"`
	Status PodSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodSetList contains a list of PodSet
type PodSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodSet{}, &PodSetList{})
}
