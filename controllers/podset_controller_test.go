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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dataclondv1 "podset-operator/api/v1"
)

var _ = Describe("PodSet controller", func() {
	It("reconciles the service, pods and status", func() {
		ctx := context.Background()
		key := types.NamespacedName{Name: "verify", Namespace: "default"}
		podSet := &dataclondv1.PodSet{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
			Spec:       dataclondv1.PodSetSpec{Replicas: 3, Port: 80, TargetPort: 80},
		}
		Expect(k8sClient.Create(ctx, podSet)).To(Succeed())

		r := &PodSetReconciler{Client: k8sClient, Log: logf.Log, Scheme: scheme.Scheme}

		// The first reconcile creates the Service and requeues; the second one
		// creates the missing Pods.
		for i := 0; i < 2; i++ {
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, key, svc)).To(Succeed())

		pods := &corev1.PodList{}
		Expect(k8sClient.List(ctx, pods, client.InNamespace(key.Namespace))).To(Succeed())
		Expect(pods.Items).To(HaveLen(3))

		// Simulate the kubelet reporting the Pods as running.
		for i := range pods.Items {
			pod := &pods.Items[i]
			pod.Status.Phase = corev1.PodRunning
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		}

		// A further reconcile records the Pods in the PodSet status.
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		updated := &dataclondv1.PodSet{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.Replicas).To(Equal(int32(3)))
		Expect(updated.Status.PodNames).To(ConsistOf("verify-0", "verify-1", "verify-2"))
	})
})
