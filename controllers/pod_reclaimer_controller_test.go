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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dataclondv1 "podset-operator/api/v1"
)

var _ = Describe("PodReclaimer", func() {
	It("deletes a completed pod and its PVC", func() {
		ctx := context.Background()
		cr := &dataclondv1.PodLoadTest{
			ObjectMeta: metav1.ObjectMeta{Name: "loadtest-reclaim", Namespace: "default"},
			Spec: dataclondv1.PodLoadTestSpec{
				Template: podLoadTestTemplate(),
				// The reclaimer only removes PVCs when the run declares storage.
				Storage: &dataclondv1.PodLoadTestStorage{},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		podKey := types.NamespacedName{Name: "reclaim-done", Namespace: "default"}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podKey.Name,
				Namespace: podKey.Namespace,
				Labels: map[string]string{
					podLoadTestUIDLabel: string(cr.UID),
					managedByLabel:      managedByValue,
				},
				Annotations: map[string]string{podLoadTestNameAnnotation: cr.Name},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers:    []corev1.Container{{Name: "worker", Image: "busybox:1.36"}},
			},
		}
		Expect(controllerutil.SetControllerReference(cr, pod, scheme.Scheme)).To(Succeed())
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())

		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podKey.Name,
				Namespace: podKey.Namespace,
				Labels:    map[string]string{podLoadTestUIDLabel: string(cr.UID)},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pvc)).To(Succeed())

		pod.Status.Phase = corev1.PodSucceeded
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		r := &PodReclaimer{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: podKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(apierrors.IsNotFound(
			k8sClient.Get(ctx, podKey, &corev1.Pod{}),
		)).To(BeTrue())

		// envtest's apiserver adds the pvc-protection finalizer but no
		// controller removes it, so accept either fully deleted or terminating.
		storedPVC := &corev1.PersistentVolumeClaim{}
		err = k8sClient.Get(ctx, podKey, storedPVC)
		if err == nil {
			Expect(storedPVC.DeletionTimestamp).NotTo(BeNil())
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	It("ignores pods that are not managed by a PodLoadTest", func() {
		ctx := context.Background()
		podKey := types.NamespacedName{Name: "unmanaged-pod", Namespace: "default"}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers:    []corev1.Container{{Name: "worker", Image: "busybox:1.36"}},
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		pod.Status.Phase = corev1.PodSucceeded
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		r := &PodReclaimer{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: podKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, podKey, &corev1.Pod{})).To(Succeed())
	})
})
