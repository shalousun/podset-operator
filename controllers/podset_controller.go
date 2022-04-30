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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dataclondv1 "podset-operator/api/v1"
)

var log = logf.Log.WithName("controller_podset")

// PodSetReconciler reconciles a PodSet object
type PodSetReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=data.clond.com.shalousun,resources=podsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=data.clond.com.shalousun,resources=podsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=v1,resources=pods,verbs=get;list;watch;create;update;patch;delete

func (r *PodSetReconciler) Reconcile(request ctrl.Request) (ctrl.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling PodSet")

	// Fetch the PodSet instance
	podSet := &dataclondv1.PodSet{}
	err := r.Get(context.TODO(), request.NamespacedName, podSet)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}
	replicas := podSet.Spec.Replicas
	if replicas%2 == 0 {
		replicas = replicas + 1
		podSet.Spec.Replicas = replicas
	}

	// Define a new service object
	service := newService(podSet)

	// Set PodSet instance as the owner and controller
	if err := controllerutil.SetControllerReference(podSet, service, r.Scheme); err != nil {
		return reconcile.Result{}, err
	}

	// Check if service already exists
	serviceFound := &corev1.Service{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, serviceFound)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a new service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
		err = r.Create(context.TODO(), service)
		if err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{Requeue: true}, nil
	}

	var expectPods []string
	for i := 0; i < int(replicas); i++ {
		podName := podSet.Name + "-" + strconv.Itoa(i)
		expectPods = append(expectPods, podName)
	}
	// List all pods owned by this PodSet instance
	existingPods, err := listPods(r, podSet)
	if err != nil {
		reqLogger.Error(err, "failed to list existing pods in the podSet")
		return reconcile.Result{}, err
	}
	var existingPodNames []string

	// Count the pods that are pending or running as available
	for _, pod := range existingPods {
		if pod.GetObjectMeta().GetDeletionTimestamp() != nil {
			continue
		}
		if pod.Status.Phase == corev1.PodPending || pod.Status.Phase == corev1.PodRunning {
			existingPodNames = append(existingPodNames, pod.GetObjectMeta().GetName())
		}
	}

	reqLogger.Info("Checking podset", "expected replicas", replicas, "Pod.Names", existingPodNames)

	// delete pod
	if podSet.Spec.Option == dataclondv1.DELETE {
		for _, pod := range existingPods {
			for _, name := range podSet.Spec.PodLists {
				if name == pod.Name {
					err = r.Delete(context.TODO(), &pod)
					if err != nil {
						reqLogger.Error(err, "failed to delete a pod %s", name)
						return reconcile.Result{}, err
					}
				}
			}
		}
	}

	// Scale Down Pods
	if int32(len(existingPodNames)) > replicas && podSet.Spec.Option != dataclondv1.DELETE {
		// delete a pod. Just one at a time (this reconciler will be called again afterwards)
		reqLogger.Info("Deleting a pod in the podset", "expected replicas", replicas, "Pod.Names", existingPodNames)
		pod := existingPods[0]
		err = r.Delete(context.TODO(), &pod)
		if err != nil {
			reqLogger.Error(err, "failed to delete a pod")
			return reconcile.Result{}, err
		}
	}

	// Scale Up Pods
	if int32(len(existingPodNames)) < replicas && podSet.Spec.Option != dataclondv1.DELETE {
		var diff = Difference(expectPods, existingPodNames)
		// create a new pod. Just one at a time (this reconciler will be called again afterwards)
		reqLogger.Info("Adding a pod in the podset", "expected replicas", replicas, "Pod.Names", existingPodNames)
		for _, podName := range diff {
			err := addPod(r, podSet, podName)
			if err != nil {
				reqLogger.Error(err, "failed to create a pod")
				return reconcile.Result{}, err
			}
		}
	}
	// Update the status for the PodSet, if needed.
	err = updateStatus(r, podSet, existingPods)
	if err != nil {
		reqLogger.Error(err, "failed to update the podSet")
		return reconcile.Result{}, err
	}
	return reconcile.Result{Requeue: true}, nil
}

func (r *PodSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dataclondv1.PodSet{}).
		Complete(r)
}

// addPod will create a new Pod based on the given PodSet.
func addPod(r *PodSetReconciler, cr *dataclondv1.PodSet, podName string) error {
	if cr.Spec.StorageClass != "" {
		// Check if pvc already exists
		pvc, err := newPVCForCR(cr, podName)
		if err != nil {
			return err
		}
		pvcFound := &corev1.PersistentVolumeClaim{}
		err = r.Get(context.TODO(), types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, pvcFound)
		if err != nil && errors.IsNotFound(err) {
			err = r.Create(context.TODO(), pvc)
			if err != nil {
				return err
			}
		}
	}

	pod := newPodForCR(cr, podName)
	err := controllerutil.SetControllerReference(cr, pod, r.Scheme)
	if err != nil {
		return err
	}
	return r.Create(context.TODO(), pod)
}

// generate pvc name
func newPVCForCR(cr *dataclondv1.PodSet, pvcName string) (*corev1.PersistentVolumeClaim, error) {
	storageSize, _ := resource.ParseQuantity(cr.Spec.StorageSize)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: cr.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
			StorageClassName: &cr.Spec.StorageClass,
		},
	}
	return pvc, nil

}
func newPodForCR(cr *dataclondv1.PodSet, podName string) *corev1.Pod {
	labels := labelsForPodSet(cr)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Hostname:  podName, // for headless svc
			Subdomain: cr.Name, // for headless svc
			Containers: []corev1.Container{
				{
					Name:  cr.Name,
					Image: dataclondv1.Image,
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: cr.Spec.Port,
							Protocol:      corev1.ProtocolTCP,
						},
					},
					Resources: cr.Spec.Resources,
					Env:       getENV(cr),
				},
			},
		},
	}
	if cr.Spec.StorageClass != "" {
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{Name: "data", MountPath: "/usr/share/nginx/html"},
		}
		pod.Spec.Volumes = []corev1.Volume{
			{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: podName, // user pod name as pvc name
					},
				},
			},
		}
	}
	return pod
}

func newService(cr *dataclondv1.PodSet) *corev1.Service {
	labels := map[string]string{"app": cr.Name}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Type:     corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Protocol:   corev1.ProtocolTCP,
					Port:       cr.Spec.Port,
					TargetPort: intstr.FromInt(int(cr.Spec.TargetPort)),
				},
			},
		},
	}
}

// defaultLabels returns the default set of labels for the PodSet.
func defaultLabels(cr *dataclondv1.PodSet) map[string]string {
	return map[string]string{
		"app":     cr.Name,
		"version": "v0.1",
	}
}

// labelsForPodSet returns the combined, set of labels for the PodSet.
func labelsForPodSet(cr *dataclondv1.PodSet) map[string]string {
	labels := defaultLabels(cr)
	for key, val := range cr.ObjectMeta.Labels {
		labels[key] = val
	}
	return labels
}

// listPods will return a slice containing the Pods owned by the Operator that
// do not have a DeletionTimestamp set.
func listPods(r *PodSetReconciler, cr *dataclondv1.PodSet) ([]corev1.Pod, error) {
	// List the pods for the given PodSet.
	podList := &corev1.PodList{}
	labelSelector := labels.SelectorFromSet(labelsForPodSet(cr))
	listOps := &client.ListOptions{Namespace: cr.Namespace, LabelSelector: labelSelector}
	err := r.List(context.TODO(), podList, listOps)
	if err != nil {
		return nil, err
	}
	// Filter out Pods with a DeletionTimestamp.
	pods := make([]corev1.Pod, 0)
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil {
			pods = append(pods, pod)
		}
	}
	return pods, nil
}

// updateStatus will update PodNames with the current list of Pods managed by the the PodSet.
func updateStatus(r *PodSetReconciler, cr *dataclondv1.PodSet, pods []corev1.Pod) error {
	podNames := make([]string, 0)
	for _, pod := range pods {
		podNames = append(podNames, pod.Name)
		annotations := pod.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["foo"] = time.Now().Format(time.RFC850)
		r.Log.Info("Update pod annotations", "podName", pod.Name)
		err := r.Update(context.TODO(), &pod)
		if err != nil {
			r.Log.Error(err, "Update Pod annotation error")
		}
	}
	r.Log.Info("Reconciling PodSet status", "podName", cr.Name, "PodNames", podNames, "StatusPodNames", cr.Status.PodNames)
	if reflect.DeepEqual(cr.Status.PodNames, podNames) {
		r.Log.Info("Not update PodSet status")
		return nil
	}
	status := dataclondv1.PodSetStatus{
		Replicas: int32(len(pods)),
		PodNames: podNames,
	}
	cr.Status = status
	annotations := cr.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["foo"] = "foo"
	cr.SetAnnotations(annotations)
	r.Log.Info("Reconciling PodSet annotation", "podName", cr.Name)
	return r.Update(context.TODO(), cr)
}
func addAnnotations(r *PodSetReconciler, cr *dataclondv1.PodSet) error {
	annotations := cr.GetAnnotations()
	annotations["foo"] = "foo"
	cr.SetAnnotations(annotations)
	return r.Update(context.TODO(), cr)
}

// get env
func getENV(cr *dataclondv1.PodSet) []corev1.EnvVar {
	envs := []corev1.EnvVar{{
		Name:  "SERVERS",
		Value: strconv.Itoa(int(cr.Spec.Replicas)),
	}}
	envs = append(envs, cr.Spec.Env...)
	return envs
}

// Difference of string arrays
func Difference(a, b []string) (diff []string) {
	m := make(map[string]bool)
	for _, item := range b {
		m[item] = true
	}
	for _, item := range a {
		if _, ok := m[item]; !ok {
			diff = append(diff, item)
		}
	}
	return
}
