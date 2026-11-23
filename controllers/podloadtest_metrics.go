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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const metricsNamespace = "podloadtest"

var (
	podsCreatedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "pods_created_total",
		Help:      "Number of Pods successfully submitted to the API server.",
	}, []string{"namespace", "podloadtest"})

	podCreateErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "pod_create_errors_total",
		Help:      "Number of Pod create requests that failed.",
	}, []string{"namespace", "podloadtest"})

	podCreateDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "pod_create_duration_seconds",
		Help:      "Latency of Pod create API requests.",
		Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"namespace", "podloadtest"})

	pvcsCreatedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "pvcs_created_total",
		Help:      "Number of PersistentVolumeClaims successfully created for test Pods.",
	}, []string{"namespace", "podloadtest"})

	podsInFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "pods_in_flight",
		Help:      "Number of submitted Pods that have not completed yet.",
	}, []string{"namespace", "podloadtest"})

	submissionRateGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "submission_rate_per_second",
		Help:      "Observed Pod submission rate over the last 10 seconds.",
	}, []string{"namespace", "podloadtest"})

	podsReclaimedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "pods_reclaimed_total",
		Help:      "Number of Pods deleted by the reclaimer.",
	}, []string{"namespace", "podloadtest"})

	podReclaimErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "pod_reclaim_errors_total",
		Help:      "Number of errors encountered while reclaiming Pods.",
	}, []string{"namespace", "podloadtest"})

	podLifetimeSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "pod_lifetime_seconds",
		Help:      "Lifetime of completed Pods from creation to reclamation.",
		Buckets:   []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"namespace", "podloadtest"})

	podCompletionSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "pod_completion_duration_seconds",
		Help:      "Duration from Pod creation to reaching Succeeded/Failed, excluding reclaimer queue time.",
		Buckets:   []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 600},
	}, []string{"namespace", "podloadtest"})

	podDeleteDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "pod_delete_duration_seconds",
		Help:      "Latency of Pod delete API requests issued by the reclaimer.",
		Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"namespace", "podloadtest"})
)

func init() {
	metrics.Registry.MustRegister(
		podsCreatedTotal,
		podCreateErrorsTotal,
		podCreateDurationSeconds,
		pvcsCreatedTotal,
		podsInFlight,
		submissionRateGauge,
		podsReclaimedTotal,
		podReclaimErrorsTotal,
		podLifetimeSeconds,
		podCompletionSeconds,
		podDeleteDurationSeconds,
	)
}

// forgetPodLoadTestMetrics removes the per-run metric series when the
// PodLoadTest object is deleted.
func forgetPodLoadTestMetrics(namespace, name string) {
	podsCreatedTotal.DeleteLabelValues(namespace, name)
	podCreateErrorsTotal.DeleteLabelValues(namespace, name)
	podCreateDurationSeconds.DeleteLabelValues(namespace, name)
	pvcsCreatedTotal.DeleteLabelValues(namespace, name)
	podsInFlight.DeleteLabelValues(namespace, name)
	submissionRateGauge.DeleteLabelValues(namespace, name)
	podsReclaimedTotal.DeleteLabelValues(namespace, name)
	podReclaimErrorsTotal.DeleteLabelValues(namespace, name)
	podLifetimeSeconds.DeleteLabelValues(namespace, name)
	podCompletionSeconds.DeleteLabelValues(namespace, name)
	podDeleteDurationSeconds.DeleteLabelValues(namespace, name)
}
