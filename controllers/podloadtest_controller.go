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
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dataclondv1 "podset-operator/api/v1"
)

const (
	podLoadTestUIDLabel       = "podloadtest.data.clond.com/uid"
	podLoadTestNameAnnotation = "podloadtest.data.clond.com/name"

	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "podloadtest-operator"

	storageVolumeName  = "podloadtest-data"
	defaultMountPath   = "/data"
	defaultStorageSize = "1Gi"

	defaultMaxInFlight       = 100
	defaultCreateConcurrency = 10
	maxPodsPerReconcile      = 500

	submissionRateWindow = 10 * time.Second
)

var podLoadTestLog = logf.Log.WithName("podloadtest")

// PodLoadTestReconciler submits Pods for PodLoadTest objects. It never deletes
// Pods: reclamation is handled by PodReclaimer so that submission throughput
// and cleanup do not block each other.
type PodLoadTestReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	storesMu sync.Mutex
	limiters *tokenBucketStore
	windows  *rateWindowStore
}

// +kubebuilder:rbac:groups=data.clond.com.shalousun,resources=podloadtests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=data.clond.com.shalousun,resources=podloadtests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;delete

func (r *PodLoadTestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.ensureStores()
	key := req.NamespacedName.String()
	logger := podLoadTestLog.WithValues("podloadtest", req.NamespacedName)

	cr := &dataclondv1.PodLoadTest{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		if apierrors.IsNotFound(err) {
			r.limiters.forget(key)
			r.windows.forget(key)
			forgetPodLoadTestMetrics(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !cr.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	now := time.Now()
	startTime := cr.Status.StartTime
	if startTime == nil {
		startTime = &metav1.Time{Time: now}
	}

	if len(cr.Spec.Template.Spec.Containers) == 0 {
		logger.Info("invalid PodLoadTest spec: no containers")
		return ctrl.Result{}, r.updateStatus(ctx, cr, statusUpdate{
			phase:     dataclondv1.PodLoadTestPhaseFailed,
			message:   "spec.template.spec.containers must not be empty",
			startTime: startTime,
		})
	}

	inFlight, err := r.countInFlight(ctx, cr)
	if err != nil {
		return ctrl.Result{}, err
	}

	rate := r.windows.recordAndRate(key, now, 0, submissionRateWindow)
	podsInFlight.WithLabelValues(cr.Namespace, cr.Name).Set(float64(inFlight))
	submissionRateGauge.WithLabelValues(cr.Namespace, cr.Name).Set(rate)

	durationElapsed := cr.Spec.DurationSeconds > 0 &&
		now.Sub(startTime.Time) >= time.Duration(cr.Spec.DurationSeconds)*time.Second
	submissionDone := (cr.Spec.Total > 0 && cr.Status.Submitted >= cr.Spec.Total) || durationElapsed

	// Nothing more to submit: update status and keep polling until the Pods
	// created by this run have drained.
	if cr.Spec.Suspend || submissionDone {
		phase := dataclondv1.PodLoadTestPhaseRunning
		if cr.Spec.Suspend {
			phase = dataclondv1.PodLoadTestPhaseSuspended
		}
		if submissionDone {
			phase = dataclondv1.PodLoadTestPhaseCompleted
		}
		var completionTime *metav1.Time
		if submissionDone && cr.Status.CompletionTime == nil {
			completionTime = &metav1.Time{Time: now}
		}
		if err := r.updateStatus(ctx, cr, statusUpdate{
			phase:          phase,
			message:        "",
			startTime:      startTime,
			completionTime: completionTime,
			inFlight:       inFlight,
			rate:           rateAsInt32(rate),
		}); err != nil {
			return ctrl.Result{}, err
		}
		if submissionDone && inFlight > 0 {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, nil
	}

	maxInFlight := cr.Spec.MaxInFlight
	if maxInFlight <= 0 {
		maxInFlight = defaultMaxInFlight
	}
	capacity := int(maxInFlight) - int(inFlight)
	if capacity < 0 {
		capacity = 0
	}
	limit := maxPodsPerReconcile
	if capacity < limit {
		limit = capacity
	}
	if cr.Spec.Total > 0 {
		remaining := cr.Spec.Total - cr.Status.Submitted
		if remaining < 0 {
			remaining = 0
		}
		if remaining < int64(limit) {
			limit = int(remaining)
		}
	}
	if limit > 0 {
		limit = r.limiters.take(key, &cr.Spec, now, limit)
	}

	created := 0
	var createErr error
	if limit > 0 {
		concurrency := int(cr.Spec.CreateConcurrency)
		if concurrency <= 0 {
			concurrency = defaultCreateConcurrency
		}
		created, createErr = r.submitPods(ctx, cr, limit, concurrency)
	}

	submitted := cr.Status.Submitted + int64(created)
	completedNow := (cr.Spec.Total > 0 && submitted >= cr.Spec.Total) || durationElapsed
	phase := dataclondv1.PodLoadTestPhaseRunning
	if completedNow {
		phase = dataclondv1.PodLoadTestPhaseCompleted
	}
	var completionTime *metav1.Time
	if completedNow && cr.Status.CompletionTime == nil {
		completionTime = &metav1.Time{Time: now}
	}
	message := ""
	if createErr != nil {
		message = createErr.Error()
	}
	rate = r.windows.recordAndRate(key, now, created, submissionRateWindow)
	newInFlight := inFlight + int32(created)
	if newInFlight < 0 {
		newInFlight = 0
	}
	podsInFlight.WithLabelValues(cr.Namespace, cr.Name).Set(float64(newInFlight))
	submissionRateGauge.WithLabelValues(cr.Namespace, cr.Name).Set(rate)

	if err := r.updateStatus(ctx, cr, statusUpdate{
		phase:          phase,
		message:        message,
		startTime:      startTime,
		completionTime: completionTime,
		inFlight:       newInFlight,
		rate:           rateAsInt32(rate),
		submittedDelta: int64(created),
	}); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("submitted pods",
		"created", created,
		"submitted", submitted,
		"inFlight", newInFlight,
		"ratePerSecond", rate,
		"maxInFlight", maxInFlight,
		"submitRatePerSecond", cr.Spec.SubmitRatePerSecond,
	)

	switch {
	case completedNow && newInFlight > 0:
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	case completedNow:
		return ctrl.Result{}, nil
	case createErr != nil:
		return ctrl.Result{RequeueAfter: time.Second}, nil
	case capacity <= created:
		// maxInFlight reached; wait for Pods to complete and be reclaimed.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	case cr.Spec.SubmitRatePerSecond > 0:
		return ctrl.Result{RequeueAfter: requeueDelay(cr.Spec.SubmitRatePerSecond)}, nil
	default:
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}
}

// countInFlight counts submitted Pods that have not reached a terminal phase.
// It uses the manager cache: at high Pod counts an uncached List is a large
// API round trip on every reconcile and becomes the submission bottleneck.
// The cache may briefly lag behind Create, allowing a small overshoot of
// maxInFlight; that is acceptable for a load test.
func (r *PodLoadTestReconciler) countInFlight(ctx context.Context, cr *dataclondv1.PodLoadTest) (int32, error) {
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList,
		client.InNamespace(cr.Namespace),
		client.MatchingLabels(map[string]string{podLoadTestUIDLabel: string(cr.UID)}),
	); err != nil {
		return 0, err
	}
	inFlight := int32(0)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		inFlight++
	}
	return inFlight, nil
}

// submitPods creates n Pods using the given worker count. Pods are created
// with unique names so a retried reconcile cannot create duplicates.
func (r *PodLoadTestReconciler) submitPods(ctx context.Context, cr *dataclondv1.PodLoadTest, n, concurrency int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	if concurrency > n {
		concurrency = n
	}
	if concurrency < 1 {
		concurrency = 1
	}

	names := make(chan string)
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range names {
				results <- r.createPod(ctx, cr, name)
			}
		}()
	}

	nameList := make([]string, n)
	for i := range nameList {
		nameList[i] = newPodName(cr)
	}
	go func() {
		for _, name := range nameList {
			names <- name
		}
		close(names)
		wg.Wait()
		close(results)
	}()

	success := 0
	var firstErr error
	for err := range results {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success++
	}
	return success, firstErr
}

func (r *PodLoadTestReconciler) createPod(ctx context.Context, cr *dataclondv1.PodLoadTest, name string) error {
	start := time.Now()
	metricLabels := []string{cr.Namespace, cr.Name}

	if cr.Spec.Storage != nil {
		pvc := newPVCForPodLoadTest(cr, name)
		if err := controllerutil.SetControllerReference(cr, pvc, r.Scheme); err != nil {
			podCreateErrorsTotal.WithLabelValues(metricLabels...).Inc()
			return fmt.Errorf("set owner reference on pvc %q: %w", name, err)
		}
		switch err := r.Create(ctx, pvc); {
		case err == nil:
			pvcsCreatedTotal.WithLabelValues(metricLabels...).Inc()
		case apierrors.IsAlreadyExists(err):
			// The PVC survived an earlier failed attempt; reuse it.
		default:
			podCreateErrorsTotal.WithLabelValues(metricLabels...).Inc()
			return fmt.Errorf("create pvc %q: %w", name, err)
		}
	}

	pod := newPodForPodLoadTest(cr, name)
	if err := controllerutil.SetControllerReference(cr, pod, r.Scheme); err != nil {
		podCreateErrorsTotal.WithLabelValues(metricLabels...).Inc()
		return fmt.Errorf("set owner reference on pod %q: %w", name, err)
	}
	err := r.Create(ctx, pod)
	podCreateDurationSeconds.WithLabelValues(metricLabels...).Observe(time.Since(start).Seconds())
	if err != nil {
		podCreateErrorsTotal.WithLabelValues(metricLabels...).Inc()
		if cr.Spec.Storage != nil {
			r.deletePVCBestEffort(ctx, cr, name)
		}
		return fmt.Errorf("create pod %q: %w", name, err)
	}
	podsCreatedTotal.WithLabelValues(metricLabels...).Inc()
	return nil
}

func (r *PodLoadTestReconciler) deletePVCBestEffort(ctx context.Context, cr *dataclondv1.PodLoadTest, name string) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cr.Namespace},
	}
	if err := r.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		podLoadTestLog.Error(err, "failed to clean up PVC after Pod create failure",
			"namespace", cr.Namespace, "pvc", name)
	}
}

// newPodName builds a unique Pod/PVC name. The base name is truncated so the
// generated name stays within the 253 character DNS subdomain limit.
func newPodName(cr *dataclondv1.PodLoadTest) string {
	const suffixLen = 6 // "-" plus 5 random characters
	base := cr.Name
	if len(base) > 253-suffixLen {
		base = strings.TrimRight(base[:253-suffixLen], "-.")
	}
	return base + "-" + rand.String(5)
}

func newPodForPodLoadTest(cr *dataclondv1.PodLoadTest, name string) *corev1.Pod {
	spec := cr.Spec.Template.Spec.DeepCopy()
	if spec.RestartPolicy == "" {
		// Without this a Pod defaults to Always and never reaches Succeeded,
		// so it would never be reclaimed.
		spec.RestartPolicy = corev1.RestartPolicyNever
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cr.Namespace,
			Labels:      podLoadTestLabels(cr),
			Annotations: podLoadTestAnnotations(cr),
		},
		Spec: *spec,
	}

	if cr.Spec.Storage != nil {
		mountPath := cr.Spec.Storage.MountPath
		if mountPath == "" {
			mountPath = defaultMountPath
		}
		volumeName := uniqueVolumeName(pod)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name},
			},
		})
		for i := range pod.Spec.Containers {
			pod.Spec.Containers[i].VolumeMounts = append(pod.Spec.Containers[i].VolumeMounts,
				corev1.VolumeMount{Name: volumeName, MountPath: mountPath})
		}
	}
	return pod
}

// uniqueVolumeName avoids colliding with a volume already present in the
// user-provided template.
func uniqueVolumeName(pod *corev1.Pod) string {
	used := make(map[string]struct{}, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		used[volume.Name] = struct{}{}
	}
	name := storageVolumeName
	for i := 1; ; i++ {
		if _, ok := used[name]; !ok {
			return name
		}
		name = fmt.Sprintf("%s-%d", storageVolumeName, i)
	}
}

func newPVCForPodLoadTest(cr *dataclondv1.PodLoadTest, name string) *corev1.PersistentVolumeClaim {
	storage := cr.Spec.Storage
	size := storage.Size
	if size.IsZero() {
		size = resource.MustParse(defaultStorageSize)
	}
	accessModes := storage.AccessModes
	if len(accessModes) == 0 {
		accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cr.Namespace,
			Labels:      podLoadTestLabels(cr),
			Annotations: podLoadTestAnnotations(cr),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: accessModes,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}
	if storage.StorageClassName != "" {
		storageClassName := storage.StorageClassName
		pvc.Spec.StorageClassName = &storageClassName
	}
	return pvc
}

func podLoadTestLabels(cr *dataclondv1.PodLoadTest) map[string]string {
	labels := make(map[string]string, len(cr.Spec.Template.Labels)+2)
	for k, v := range cr.Spec.Template.Labels {
		labels[k] = v
	}
	labels[managedByLabel] = managedByValue
	labels[podLoadTestUIDLabel] = string(cr.UID)
	return labels
}

func podLoadTestAnnotations(cr *dataclondv1.PodLoadTest) map[string]string {
	annotations := make(map[string]string, len(cr.Spec.Template.Annotations)+1)
	for k, v := range cr.Spec.Template.Annotations {
		annotations[k] = v
	}
	annotations[podLoadTestNameAnnotation] = cr.Name
	return annotations
}

// requeueDelay returns how long to wait before the token bucket is expected to
// hold at least one token. The lower bound prevents a hot reconcile loop and
// limits how often the (uncached) Pod list is issued.
func requeueDelay(ratePerSecond int32) time.Duration {
	if ratePerSecond <= 0 {
		return 100 * time.Millisecond
	}
	delay := time.Second / time.Duration(ratePerSecond)
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	if delay > time.Second {
		delay = time.Second
	}
	return delay
}

// rateAsInt32 rounds the observed rate for the status field. The precise
// float value is exposed through the submission rate metric.
func rateAsInt32(rate float64) int32 {
	if rate <= 0 {
		return 0
	}
	return int32(math.Round(rate))
}

func (r *PodLoadTestReconciler) ensureStores() {
	r.storesMu.Lock()
	defer r.storesMu.Unlock()
	if r.limiters == nil {
		r.limiters = newTokenBucketStore()
	}
	if r.windows == nil {
		r.windows = newRateWindowStore()
	}
}

// statusUpdate carries the fields written by a reconcile. submittedDelta is
// applied to the latest status so that a conflict caused by a concurrent spec
// edit does not lose the number of Pods created by this reconcile.
type statusUpdate struct {
	submittedDelta int64
	inFlight       int32
	rate           int32
	phase          string
	message        string
	startTime      *metav1.Time
	completionTime *metav1.Time
}

func (r *PodLoadTestReconciler) updateStatus(ctx context.Context, cr *dataclondv1.PodLoadTest, u statusUpdate) error {
	key := types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &dataclondv1.PodLoadTest{}
		if err := r.Get(ctx, key, fresh); err != nil {
			return err
		}
		if fresh.UID != cr.UID {
			// The object was deleted and recreated while this reconcile was
			// running; the Pods created for the old object are left to GC.
			return nil
		}
		status := &fresh.Status
		if status.StartTime == nil && u.startTime != nil {
			status.StartTime = u.startTime
		}
		status.Submitted += u.submittedDelta
		status.InFlight = u.inFlight
		status.SubmissionRate = u.rate
		status.ObservedGeneration = fresh.Generation
		if u.phase != "" {
			status.Phase = u.phase
		}
		status.Message = u.message
		if u.completionTime != nil && status.CompletionTime == nil {
			status.CompletionTime = u.completionTime
		}
		return r.Status().Update(ctx, fresh)
	})
}

func (r *PodLoadTestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("podloadtest").
		For(&dataclondv1.PodLoadTest{}).
		Complete(r)
}
