# Workflow Namespace Drain

WARNING: This chart is consumed by Terraform (`helm_release.workflow_namespace_drain` in `terraform/modules/sdv-gke-apps/main.tf`) and MUST NOT be added to `gitops/templates/` or referenced by the ArgoCD app-of-apps. Doing so would cause the controller to be pruned by the cascade during platform destroy, defeating its purpose.

This controller owns the `horizon-sdv.io/workflow-namespace-drain` finalizer on the root `horizon-sdv` ArgoCD Application. During platform teardown it deletes remaining Argo Workflow CRs in the configured workflows namespace, force-clears stuck Workflow finalizers after a grace period, and then removes only its own root Application finalizer.
