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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// podSetManagedByValue is the managed-by label value for Pods created by the
// PodSet controller.
const podSetManagedByValue = "podset-operator"

// ManagedPodLabelSelector matches Pods created by the PodSet and PodLoadTest
// controllers. The manager cache uses it so Pods from unrelated workloads on
// a large shared cluster are not copied into the operator's informer cache.
func ManagedPodLabelSelector() (labels.Selector, error) {
	requirement, err := labels.NewRequirement(
		managedByLabel,
		selection.In,
		[]string{managedByValue, podSetManagedByValue},
	)
	if err != nil {
		return nil, err
	}
	return labels.NewSelector().Add(*requirement), nil
}
