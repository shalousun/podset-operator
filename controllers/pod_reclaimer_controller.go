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

package controllers

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	dataclondv1 "podset-operator/api/v1"
)

// PodReclaimer deletes completed (or timed out) Pods created by PodLoadTest.
// It runs in a separate controller/workqueue from the submitter so that
// reclamation cannot block submission, and vice versa.
type PodReclaimer struct {
	client.Client
	Scheme *runtime.Scheme
	// Concurrency is the number of reclaimer workers. Zero uses the default.
	Concurrency int
}

// +kubebuilder:rbac:groups=data.clond.com.shalousun,resources=podloadtests,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;delete

func (r *PodReclaimer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := podLoadTestLog.WithValues("pod", req.NamespacedName)

	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !pod.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	uid, managed := pod.Labels[podLoadTestUIDLabel]
	if !managed || pod.Labels[managedByLabel] != managedByValue {
		return ctrl.Result{}, nil
	}

	runName := podLoadTestRunName(pod)
	policy := defaultPodCleanupPolicy()
	if owner := metav1.GetControllerOf(pod); owner != nil && owner.Kind == "PodLoadTest" {
		cr := &dataclondv1.PodLoadTest{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, cr); err == nil {
			policy = podCleanupPolicyFromSpec(cr.Spec)
			runName = cr.Name
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	finished := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	var finishedAt time.Time
	if finished {
		finishedAt = podFinishedAt(pod)
		if !policy.deleteCompleted {
			return ctrl.Result{}, nil
		}
		if policy.ttl > 0 {
			if wait := time.Until(finishedAt.Add(policy.ttl)); wait > 0 {
				return ctrl.Result{RequeueAfter: wait}, nil
			}
		}
	} else if policy.timeout > 0 {
		if wait := time.Until(pod.CreationTimestamp.Add(policy.timeout)); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	logger.Info("reclaiming pod", "phase", pod.Status.Phase, "podloadtest", runName)
	deleteStart := time.Now()
	err := r.Delete(ctx, pod)
	podDeleteDurationSeconds.WithLabelValues(pod.Namespace, runName).
		Observe(time.Since(deleteStart).Seconds())
	if err != nil && !apierrors.IsNotFound(err) {
		podReclaimErrorsTotal.WithLabelValues(pod.Namespace, runName).Inc()
		return ctrl.Result{}, err
	}
	if finished {
		podLifetimeSeconds.WithLabelValues(pod.Namespace, runName).
			Observe(time.Since(pod.CreationTimestamp.Time).Seconds())
		completion := finishedAt.Sub(pod.CreationTimestamp.Time)
		if completion < 0 {
			completion = 0
		}
		podCompletionSeconds.WithLabelValues(pod.Namespace, runName).
			Observe(completion.Seconds())
	}
	podsReclaimedTotal.WithLabelValues(pod.Namespace, runName).Inc()

	if policy.deletePVCs {
		r.deletePVC(ctx, pod, uid, runName)
	}
	return ctrl.Result{}, nil
}

func (r *PodReclaimer) deletePVC(ctx context.Context, pod *corev1.Pod, uid, runName string) {
	key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, key, pvc); err != nil {
		if !apierrors.IsNotFound(err) {
			podReclaimErrorsTotal.WithLabelValues(pod.Namespace, runName).Inc()
			podLoadTestLog.Error(err, "failed to get PVC for reclaimed pod", "pvc", key)
		}
		return
	}
	if pvc.Labels[podLoadTestUIDLabel] != uid {
		// Not created by this run; leave it alone.
		return
	}
	if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		podReclaimErrorsTotal.WithLabelValues(pod.Namespace, runName).Inc()
		podLoadTestLog.Error(err, "failed to delete PVC for reclaimed pod", "pvc", key)
	}
}

type podCleanupPolicy struct {
	deleteCompleted bool
	deletePVCs      bool
	ttl             time.Duration
	timeout         time.Duration
}

func defaultPodCleanupPolicy() podCleanupPolicy {
	return podCleanupPolicy{deleteCompleted: true, deletePVCs: true}
}

func podCleanupPolicyFromSpec(spec dataclondv1.PodLoadTestSpec) podCleanupPolicy {
	policy := defaultPodCleanupPolicy()
	if spec.Cleanup.DeleteCompletedPods != nil {
		policy.deleteCompleted = *spec.Cleanup.DeleteCompletedPods
	}
	if spec.Cleanup.DeletePVCs != nil {
		policy.deletePVCs = *spec.Cleanup.DeletePVCs
	}
	policy.ttl = time.Duration(spec.Cleanup.TTLSecondsAfterFinished) * time.Second
	policy.timeout = time.Duration(spec.PodTimeoutSeconds) * time.Second
	// Only Pods from a run with storage have a per-Pod PVC. Skipping the PVC
	// lookup otherwise avoids a cache miss and API GET for every reclaimed Pod.
	policy.deletePVCs = policy.deletePVCs && spec.Storage != nil
	return policy
}

// podFinishedAt returns the best available completion timestamp. Pods do not
// have status.completionTime, so the latest container termination time is
// used. If kubelet never reported a terminated container the TTL is measured
// from now, which still guarantees eventual reclamation.
func podFinishedAt(pod *corev1.Pod) time.Time {
	var latest time.Time
	for _, status := range pod.Status.ContainerStatuses {
		if terminated := status.State.Terminated; terminated != nil && terminated.FinishedAt.After(latest) {
			latest = terminated.FinishedAt.Time
		}
	}
	if !latest.IsZero() {
		return latest
	}
	return time.Now()
}

func podLoadTestRunName(pod *corev1.Pod) string {
	if name := pod.Annotations[podLoadTestNameAnnotation]; name != "" {
		return name
	}
	if owner := metav1.GetControllerOf(pod); owner != nil && owner.Kind == "PodLoadTest" {
		return owner.Name
	}
	return "unknown"
}

func (r *PodReclaimer) SetupWithManager(mgr ctrl.Manager) error {
	// High-churn load tests complete Pods faster than a single worker can
	// delete them; use a larger worker pool so reclamation keeps up with
	// submission and does not accumulate Pod metadata.
	concurrency := r.Concurrency
	if concurrency <= 0 {
		concurrency = 50
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("pod-reclaimer").
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		For(&corev1.Pod{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			labels := obj.GetLabels()
			return labels[managedByLabel] == managedByValue && labels[podLoadTestUIDLabel] != ""
		})).
		Complete(r)
}
