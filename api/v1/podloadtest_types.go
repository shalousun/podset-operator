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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PodLoadTestPhasePending means the run has not submitted any Pod yet.
	PodLoadTestPhasePending = "Pending"
	// PodLoadTestPhaseRunning means the run is submitting Pods.
	PodLoadTestPhaseRunning = "Running"
	// PodLoadTestPhaseSuspended means submission is paused by spec.suspend.
	PodLoadTestPhaseSuspended = "Suspended"
	// PodLoadTestPhaseCompleted means the run has submitted all requested Pods
	// or its duration has elapsed. Cleanup keeps running.
	PodLoadTestPhaseCompleted = "Completed"
	// PodLoadTestPhaseFailed means the spec is invalid and nothing will be submitted.
	PodLoadTestPhaseFailed = "Failed"
)

// PodLoadTestSpec defines the desired state of PodLoadTest
type PodLoadTestSpec struct {
	// Total is the total number of Pods to submit. Zero means no limit:
	// submission continues until durationSeconds elapses, the run is
	// suspended, or the object is deleted.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Total int64 `json:"total,omitempty"`

	// MaxInFlight is the maximum number of submitted Pods that have not
	// completed yet. Once reached, submission pauses until Pods complete and
	// are reclaimed.
	// +optional
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	MaxInFlight int32 `json:"maxInFlight,omitempty"`

	// SubmitRatePerSecond limits Pod submission (client-side token bucket).
	// Zero disables the client-side limit, leaving only maxInFlight as
	// backpressure.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SubmitRatePerSecond int32 `json:"submitRatePerSecond,omitempty"`

	// SubmitBurst is the token bucket burst size, i.e. how many Pods may be
	// submitted back-to-back. Defaults to submitRatePerSecond when set,
	// otherwise to maxInFlight.
	// +optional
	// +kubebuilder:validation:Minimum=1
	SubmitBurst int32 `json:"submitBurst,omitempty"`

	// CreateConcurrency is the number of Pod create requests that may run in
	// parallel. Increase it when the API server can handle more concurrency.
	// +optional
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	CreateConcurrency int32 `json:"createConcurrency,omitempty"`

	// DurationSeconds stops submission after this many seconds. Zero means no
	// time limit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DurationSeconds int64 `json:"durationSeconds,omitempty"`

	// Suspend pauses Pod submission while true. Reclamation continues.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Template is the Pod template used for every submitted Pod.
	// RestartPolicy defaults to Never so that Pods can reach Succeeded and be
	// reclaimed.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Template corev1.PodTemplateSpec `json:"template"`

	// Storage enables a dedicated PVC for every Pod when set.
	// +optional
	Storage *PodLoadTestStorage `json:"storage,omitempty"`

	// Cleanup controls how completed Pods are reclaimed.
	// +optional
	Cleanup PodLoadTestCleanup `json:"cleanup,omitempty"`

	// PodTimeoutSeconds deletes Pods that have not completed within this many
	// seconds. Zero disables the timeout. This prevents a broken image or an
	// unschedulable Pod from stalling the run.
	// +optional
	// +kubebuilder:validation:Minimum=0
	PodTimeoutSeconds int32 `json:"podTimeoutSeconds,omitempty"`
}

// PodLoadTestStorage configures the optional per-Pod PVC.
type PodLoadTestStorage struct {
	// StorageClassName is the storage class for each PVC. Empty uses the
	// cluster default storage class.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// Size is the requested storage size for each PVC. Defaults to 1Gi.
	// +optional
	Size resource.Quantity `json:"size,omitempty"`

	// AccessModes for each PVC. Defaults to ReadWriteOnce.
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// MountPath is where the volume is mounted in every container.
	// Defaults to /data.
	// +optional
	MountPath string `json:"mountPath,omitempty"`
}

// PodLoadTestCleanup controls reclamation of completed Pods.
type PodLoadTestCleanup struct {
	// DeleteCompletedPods deletes Pods as soon as they reach Succeeded or
	// Failed. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	DeleteCompletedPods *bool `json:"deleteCompletedPods,omitempty"`

	// DeletePVCs deletes the PVC created for a Pod when that Pod is reclaimed.
	// Defaults to true.
	// +optional
	// +kubebuilder:default=true
	DeletePVCs *bool `json:"deletePVCs,omitempty"`

	// TTLSecondsAfterFinished delays Pod deletion by this many seconds after
	// completion. Defaults to 0 (delete immediately).
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// PodLoadTestStatus defines the observed state of PodLoadTest
type PodLoadTestStatus struct {
	// ObservedGeneration is the generation last processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is Pending, Running, Suspended, Completed or Failed.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Submitted is the number of Pod create requests that succeeded.
	// +optional
	Submitted int64 `json:"submitted,omitempty"`

	// InFlight is the number of submitted Pods that have not completed yet.
	// +optional
	InFlight int32 `json:"inFlight,omitempty"`

	// SubmissionRate is the observed submission rate in whole Pods per
	// second, measured over a 10 second window. Use the
	// podloadtest_submission_rate_per_second metric for the precise value.
	// +optional
	SubmissionRate int32 `json:"submissionRate,omitempty"`

	// StartTime is when the run started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when submission finished or the duration elapsed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message contains the last submission error or failure reason.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=plt
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=.status.phase
// +kubebuilder:printcolumn:name="Submitted",type=integer,JSONPath=.status.submitted
// +kubebuilder:printcolumn:name="InFlight",type=integer,JSONPath=.status.inFlight
// +kubebuilder:printcolumn:name="Rate/s",type=integer,JSONPath=.status.submissionRate
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=.metadata.creationTimestamp

// PodLoadTest is the Schema for the podloadtests API
type PodLoadTest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodLoadTestSpec   `json:"spec,omitempty"`
	Status PodLoadTestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodLoadTestList contains a list of PodLoadTest
type PodLoadTestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodLoadTest `json:"items"`
}
