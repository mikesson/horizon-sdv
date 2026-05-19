// Copyright (c) 2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/acn-horizon-sdv/workflow-namespace-drain/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		argocdNamespace      string
		gitopsRootAppNames   string
		rootFinalizer        string
		workflowsNamespace   string
		gracefulTimeout      time.Duration
		workflowListPageSize int64
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address for metrics.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for health probes.")
	flag.StringVar(&argocdNamespace, "argocd-namespace", "argocd", "Namespace where the root ArgoCD Application exists.")
	flag.StringVar(&gitopsRootAppNames, "gitops-root-app-name", "",
		"Comma-separated names of root Argo CD app-of-apps Applications (e.g. prefix+horizon-sdv). Falls back to GITOPS_ROOT_APP_NAME when empty.")
	flag.StringVar(&rootFinalizer, "root-finalizer", controller.WorkflowDrainFinalizer, "Finalizer owned by this controller on the root Application.")
	flag.StringVar(&workflowsNamespace, "workflows-namespace", "workflows", "Namespace containing Argo Workflow CRs to drain during platform destroy.")
	flag.DurationVar(&gracefulTimeout, "graceful-timeout", 60*time.Second, "How long to wait before force-clearing finalizers on deleting Workflows.")
	flag.Int64Var(&workflowListPageSize, "workflow-list-page-size", 200, "Page size used when listing Workflow CRs.")
	flag.Parse()

	crlog.SetLogger(zap.New(zap.UseDevMode(false)))

	rootAppNameList := splitCommaTrim(gitopsRootAppNames)
	if len(rootAppNameList) == 0 {
		rootAppNameList = splitCommaTrim(os.Getenv("GITOPS_ROOT_APP_NAME"))
	}

	ctx := ctrl.SetupSignalHandler()
	cfg := ctrl.GetConfigOrDie()

	argoAppObj := &unstructured.Unstructured{}
	argoAppObj.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         false,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				argoAppObj: {
					Namespaces: map[string]cache.Config{
						argocdNamespace: {},
					},
				},
			},
		},
	})
	if err != nil {
		log.Fatalf("creating manager: %v", err)
	}

	drainer := &controller.WorkflowDrainer{
		Client:          mgr.GetClient(),
		APIReader:       mgr.GetAPIReader(),
		WorkflowsNS:     workflowsNamespace,
		GracefulTimeout: gracefulTimeout,
		PageSize:        workflowListPageSize,
	}

	rootFin := &controller.RootFinalizerReconciler{
		Client:             mgr.GetClient(),
		Drainer:            drainer,
		ArgoCDNamespace:    argocdNamespace,
		GitOpsRootAppNames: rootAppNameList,
		Finalizer:          rootFinalizer,
	}
	if err := rootFin.SetupWithManager(mgr); err != nil {
		log.Fatalf("setting up root GitOps Application finalizer reconciler: %v", err)
	}

	if err := mgr.AddHealthzCheck("health", healthz.Ping); err != nil {
		log.Fatalf("adding health check: %v", err)
	}
	if err := mgr.AddReadyzCheck("ready", healthz.Ping); err != nil {
		log.Fatalf("adding ready check: %v", err)
	}

	if err := mgr.Start(ctx); err != nil {
		log.Printf("manager stopped: %v", err)
	}
}

func splitCommaTrim(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
