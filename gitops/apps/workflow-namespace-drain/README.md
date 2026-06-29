<!-- Copyright (c) 2026 Accenture, All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

        http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# Workflow Namespace Drain

WARNING: This chart is consumed by Terraform (`helm_release.workflow_namespace_drain` in `terraform/modules/sdv-gke-apps/main.tf`) and MUST NOT be added to `gitops/templates/` or referenced by the ArgoCD app-of-apps. Doing so would cause the controller to be pruned by the cascade during platform destroy, defeating its purpose.

This controller owns the `horizon-sdv.io/workflow-namespace-drain` finalizer on the root `horizon-sdv` ArgoCD Application. During platform teardown it deletes remaining Argo Workflow CRs in the configured workflows namespace, force-clears stuck Workflow finalizers after a grace period, and then removes only its own root Application finalizer.
