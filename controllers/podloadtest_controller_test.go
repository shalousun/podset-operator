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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dataclondv1 "podset-operator/api/v1"
)

func podLoadTestTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    "worker",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", "echo done"},
			}},
		},
	}
}

func listPodsForRun(ctx context.Context, cr *dataclondv1.PodLoadTest) []corev1.Pod {
	pods := &corev1.PodList{}
	Expect(k8sClient.List(ctx, pods,
		client.InNamespace(cr.Namespace),
		client.MatchingLabels(map[string]string{podLoadTestUIDLabel: string(cr.UID)}),
	)).To(Succeed())
	return pods.Items
}

var _ = Describe("PodLoadTest controller", func() {
	It("registers the submission and reclamation controllers on one manager", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect((&PodLoadTestReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)).To(Succeed())
		Expect((&PodReclaimer{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)).To(Succeed())
	})

	It("submits the requested number of pods and completes", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "loadtest-create", Namespace: "default"}
		cr := &dataclondv1.PodLoadTest{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: dataclondv1.PodLoadTestSpec{
				Total:             5,
				MaxInFlight:       5,
				CreateConcurrency: 2,
				Template:          podLoadTestTemplate(),
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		r := &PodLoadTestReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		pods := listPodsForRun(ctx, cr)
		Expect(pods).To(HaveLen(5))
		for i := range pods {
			pod := &pods[i]
			// The template does not set a restart policy; the controller must
			// default it to Never or the Pods would never complete.
			Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyNever))
			Expect(metav1.IsControlledBy(pod, cr)).To(BeTrue())
			Expect(pod.Annotations[podLoadTestNameAnnotation]).To(Equal(cr.Name))
			Expect(pod.Labels[podLoadTestUIDLabel]).To(Equal(string(cr.UID)))
		}

		updated := &dataclondv1.PodLoadTest{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Submitted).To(Equal(int64(5)))
		Expect(updated.Status.InFlight).To(Equal(int32(5)))
		Expect(updated.Status.Phase).To(Equal(dataclondv1.PodLoadTestPhaseCompleted))
	})

	It("creates and mounts a PVC for every pod", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "loadtest-storage", Namespace: "default"}
		size := resource.MustParse("1Gi")
		cr := &dataclondv1.PodLoadTest{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec: dataclondv1.PodLoadTestSpec{
				Total:             2,
				MaxInFlight:       2,
				CreateConcurrency: 2,
				Template:          podLoadTestTemplate(),
				Storage: &dataclondv1.PodLoadTestStorage{
					StorageClassName: "standard",
					Size:             size,
					MountPath:        "/cache",
				},
			},
		}
		Expect(k8sClient.Create(ctx, cr)).To(Succeed())

		r := &PodLoadTestReconciler{Client: k8sClient, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		pods := listPodsForRun(ctx, cr)
		Expect(pods).To(HaveLen(2))
		for i := range pods {
			pod := &pods[i]
			Expect(pod.Spec.Volumes).To(HaveLen(1))
			claim := pod.Spec.Volumes[0].PersistentVolumeClaim
			Expect(claim).NotTo(BeNil())
			Expect(claim.ClaimName).To(Equal(pod.Name))
			Expect(pod.Spec.Containers[0].VolumeMounts).To(ContainElement(
				corev1.VolumeMount{Name: storageVolumeName, MountPath: "/cache"},
			))

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pvc)).To(Succeed())
			Expect(pvc.Labels[podLoadTestUIDLabel]).To(Equal(string(cr.UID)))
			Expect(pvc.Spec.StorageClassName).NotTo(BeNil())
			Expect(*pvc.Spec.StorageClassName).To(Equal("standard"))
		}
	})
})
