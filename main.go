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

package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dataclondv1 "podset-operator/api/v1"
	"podset-operator/controllers"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = dataclondv1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var secureMetrics bool
	var enableLeaderElection bool
	var kubeAPIQPS int
	var kubeAPIBurst int
	var reclaimerConcurrency int
	flag.StringVar(&metricsAddr, "metrics-addr", ":8443", "The address the metric endpoint binds to.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"Serve metrics over HTTPS with Kubernetes authentication and authorization. "+
			"The manager needs create permission on tokenreviews and subjectaccessreviews.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&kubeAPIQPS, "kube-api-qps", 1000,
		"Maximum QPS for requests to the Kubernetes API. Zero keeps the client-go default (20).")
	flag.IntVar(&kubeAPIBurst, "kube-api-burst", 2000,
		"Maximum burst for requests to the Kubernetes API. Zero keeps the client-go default (30).")
	flag.IntVar(&reclaimerConcurrency, "reclaimer-concurrency", 50,
		"Number of concurrent Pod reclamation workers.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	cfg := ctrl.GetConfigOrDie()
	// The PodLoadTest controller is designed to saturate the API server; the
	// client-go default of 20 QPS would otherwise cap submission.
	if kubeAPIQPS > 0 {
		cfg.QPS = float32(kubeAPIQPS)
	}
	if kubeAPIBurst > 0 {
		cfg.Burst = kubeAPIBurst
	}

	metricsOptions := metricsserver.Options{BindAddress: metricsAddr}
	if secureMetrics {
		metricsOptions.SecureServing = true
		metricsOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:           scheme,
		Metrics:          metricsOptions,
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "dda390eb.com.shalousun",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.PodSetReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("PodSet"),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodSet")
		os.Exit(1)
	}
	if err = (&controllers.PodLoadTestReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodLoadTest")
		os.Exit(1)
	}
	if err = (&controllers.PodReclaimer{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Concurrency: reclaimerConcurrency,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodReclaimer")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
