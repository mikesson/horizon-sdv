# AI review (Gemini) WorkflowTemplate

Helm chart for the **`ai-review`** WorkflowTemplate (Vertex / Gemini CLI). **aaos-builder** invokes this template on build failure via **`templateRef`**. See `workloads/common/agentic-ai/gemini/` for scripts and `docs/workloads/common/agentic-ai/gemini.md` for broader context.

**Platform GitOps:** This chart is a **`spec.sources`** entry on the **`workloads-android`** Module Manager Application (`gitops/modules/workloads-android`). The WorkflowTemplate uses **`argocd.argoproj.io/sync-wave: "8"`**. Module **`workloads-common`** must be enabled first (hard dependency) for shared ClusterWorkflowTemplates.

## Deploy

```bash
helm template common-ai-review \
  workloads/common/agentic-ai/gemini/helm \
  | kubectl apply -f - -n workflows
```

Optional local overrides:

```bash
helm template common-ai-review \
  workloads/common/agentic-ai/gemini/helm \
  -f workloads/common/agentic-ai/gemini/helm/values-local.yaml \
  | kubectl apply -f - -n workflows
```

## Same cluster as GitOps: pause sync, local `helm apply`, resync

If Argo CD manages this WorkflowTemplate (via **`workloads-android`**), pause sync on that Application before hand-applying, then resync when you want Git to win.
