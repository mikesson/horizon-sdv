// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
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

package api

// OpenAPISpec returns the OpenAPI 3.0 JSON specification for the Module Manager REST API.
func OpenAPISpec() []byte {
	return []byte(`{
  "openapi": "3.0.3",
  "info": {
    "title": "Horizon SDV Module Manager API",
    "description": "REST API for enabling and disabling Horizon SDV modules. Used by the Developer Portal. Discoverable via GET /openapi.json; interactive docs at GET /swagger.",
    "version": "0.5.0"
  },
  "paths": {
    "/settings/workflows-visibility": {
      "get": {
        "summary": "Get workflows visibility settings",
        "description": "Returns which workflow submitted-from sources are shown in the Developer Portal. Omitted allowedSubmittedFrom means no filter (show all).",
        "operationId": "getWorkflowsVisibility",
        "responses": {
          "200": {
            "description": "Current settings",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/WorkflowsVisibility" }
              }
            }
          }
        }
      },
      "put": {
        "summary": "Update workflows visibility settings",
        "description": "Persist Developer Portal workflow source filter. Send null or omit allowedSubmittedFrom to clear the filter (show all). Empty array hides all workflows.",
        "operationId": "putWorkflowsVisibility",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/WorkflowsVisibility" }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Updated",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": { "status": { "type": "string", "example": "ok" } }
                }
              }
            }
          },
          "400": { "description": "Invalid JSON body" }
        }
      }
    },
    "/modules": {
      "get": {
        "summary": "List modules",
        "description": "Returns all modules (from catalog and registrations). Use query parameter 'name' to filter by module name.",
        "operationId": "listModules",
        "parameters": [
          {
            "name": "name",
            "in": "query",
            "description": "Filter by module name",
            "required": false,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "List of modules",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": { "$ref": "#/components/schemas/Module" }
                }
              }
            }
          }
        }
      }
    },
    "/modules/{idOrName}": {
      "get": {
        "summary": "Get module",
        "description": "Returns a single module by assigned ID or by name.",
        "operationId": "getModule",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Module details",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/Module" }
              }
            }
          },
          "404": { "description": "Module not found" }
        }
      }
    },
    "/modules/{idOrName}/target-revision": {
      "put": {
        "summary": "Set module Git target revision",
        "description": "Updates the persisted ref and patches the module's Argo CD Application (spec.source.targetRevision and Helm values). Module must be enabled.",
        "operationId": "putModuleTargetRevision",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["targetRevision"],
                "properties": {
                  "targetRevision": { "type": "string", "description": "Git branch, tag, or commit SHA" }
                }
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Updated",
            "content": {
              "application/json": {
                "schema": { "type": "object", "properties": { "status": { "type": "string", "example": "ok" } } }
              }
            }
          },
          "400": { "description": "Invalid body or empty targetRevision" },
          "404": { "description": "Module not found" },
          "409": { "description": "Module not enabled" },
          "500": { "description": "Cluster update failed" }
        }
      },
      "delete": {
        "summary": "Clear module Git target revision pin",
        "description": "Removes the per-module pin so the module follows the Module Manager cluster default (--target-revision). Patches the module's Argo CD Application to the cluster default. Module must be enabled.",
        "operationId": "deleteModuleTargetRevision",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Pin cleared; module now follows platform branch",
            "content": {
              "application/json": {
                "schema": { "type": "object", "properties": { "status": { "type": "string", "example": "ok" } } }
              }
            }
          },
          "404": { "description": "Module not found" },
          "409": { "description": "Module not enabled" },
          "500": { "description": "Cluster update failed" }
        }
      }
    },
    "/modules/{idOrName}/status": {
      "get": {
        "summary": "Get module status",
        "description": "Returns Argo CD sync, health, and operation state aggregated across the module parent Application and child Applications (labels horizon-sdv.io/module and horizon-sdv.io/app-role parent or child). desiredRevision and syncRevision reflect the parent Application. applicationDeletionTimestamp is set if the parent or any managed Application has metadata.deletionTimestamp (e.g. child deleting during disable before state flips). When the module is disabled, remainingManagedApplications counts how many of those Applications still exist (Developer Portal uses this so uninstall stays in progress until all are gone). When the module is enabled and listing succeeds, managedApplicationCount is that same parent+child count (Portal uses it to detect teardown while GET /modules still reports enabled).",
        "operationId": "getModuleStatus",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Sync and health status",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ModuleStatus" }
              }
            }
          },
          "404": { "description": "Module not found" }
        }
      }
    },
    "/modules/{idOrName}/overview": {
      "get": {
        "summary": "Get module overview HTML",
        "description": "Returns self-contained HTML for the Developer Portal Overview tab. Module Manager performs an in-cluster HTTP GET to http://{overviewService}.{overviewServiceNamespace}.svc.cluster.local:80/ (Cluster DNS; port and path are fixed). For catalog modules whose name starts with workloads-, overview Services are deployed with the parent workload chart in Module Manager's own namespace (--namespace); the catalog should list that namespace. If the catalog still has the bare name \"workflows\" or omits the namespace, Module Manager falls back to its configured namespace. Other modules use overviewServiceNamespace from the catalog, with a legacy correction when the catalog has the bare name \"workflows\" but MODULE_CONFIG declares a non-empty namespacePrefix. If overviewService is unset or the resolved namespace is empty, this endpoint returns 404.",
        "operationId": "getModuleOverview",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "text/html document",
            "content": {
              "text/html": {
                "schema": { "type": "string", "format": "binary" }
              }
            }
          },
          "400": { "description": "Invalid overview HTTP target" },
          "404": { "description": "Module not found, overview not configured in catalog, or upstream overview returned 404" },
          "502": { "description": "In-cluster overview HTTP error or overview pod not ready" }
        }
      }
    },
    "/modules/{idOrName}/enable": {
      "post": {
        "summary": "Enable module",
        "description": "Creates the ArgoCD Application for the module so it is deployed. Fails if hard dependencies are not enabled. When targetRevision is omitted the module follows the cluster default (unpinned). When targetRevision is set the module is pinned to that ref.",
        "operationId": "enableModule",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "requestBody": {
          "required": false,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "targetRevision": {
                    "type": "string",
                    "description": "Optional Git ref to pin this module. When omitted the module follows the cluster default (unpinned)."
                  }
                }
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Module enabled",
            "content": {
              "application/json": {
                "schema": { "type": "object", "properties": { "status": { "type": "string", "example": "enabled" } } }
              }
            }
          },
          "400": { "description": "Bad request (e.g. module path not in catalog)" },
          "404": { "description": "Module not found" },
          "409": { "description": "Conflict (e.g. hard dependency not enabled)" }
        }
      }
    },
    "/modules/{idOrName}/disable": {
      "delete": {
        "summary": "Disable module",
        "description": "Deletes the ArgoCD Application for the module. Fails with 409 if any enabled module has a hard dependency on this module.",
        "operationId": "disableModule",
        "parameters": [
          {
            "name": "idOrName",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Module disabled",
            "content": {
              "application/json": {
                "schema": { "type": "object", "properties": { "status": { "type": "string", "example": "disabled" } } }
              }
            }
          },
          "404": { "description": "Module not found" },
          "409": {
            "description": "Conflict - module is a hard dependency of other enabled modules",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "error": { "type": "string" },
                    "hardDependents": { "type": "array", "items": { "type": "string" } }
                  }
                }
              }
            }
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "ModuleApplication": {
        "type": "object",
        "required": ["id", "url"],
        "properties": {
          "id": { "type": "string", "description": "Stable id (ModuleCatalog entry, or Argo Application metadata.name / horizon-sdv.io/portal-id)" },
          "title": { "type": "string", "description": "Human-readable label" },
          "url": {
            "type": "string",
            "description": "Public URL: site-relative path (starts with /) resolved against the Developer Portal origin, or an absolute http(s) URL"
          }
        }
      },
      "Module": {
        "type": "object",
        "properties": {
          "id": { "type": "string", "description": "Stable ID assigned by the controller" },
          "name": { "type": "string", "description": "Logical module name" },
          "overviewPath": {
            "type": "string",
            "description": "Path to overview HTML under the catalog module path (e.g. portal/overview.html) for Helm chart packaging only; not used as a runtime Git path"
          },
          "overviewService": {
            "type": "string",
            "description": "Kubernetes Service name that serves overview HTML in overviewServiceNamespace"
          },
          "overviewServiceNamespace": {
            "type": "string",
            "description": "Namespace for overviewService (in-cluster HTTP from Module Manager; always port 80 and path /)"
          },
          "enabled": { "type": "boolean" },
          "hardDependencies": { "type": "array", "items": { "type": "string" } },
          "softDependencies": { "type": "array", "items": { "type": "string" } },
          "applicationName": { "type": "string" },
          "applicationNamespace": { "type": "string" },
          "applications": {
            "type": "array",
            "description": "Developer Portal links: union of ModuleCatalog.spec.modules[].applications and Argo CD child Applications (labels horizon-sdv.io/module, horizon-sdv.io/app-role=child, horizon-sdv.io/expose=true) with annotation horizon-sdv.io/portal-url (required), optional horizon-sdv.io/portal-title and horizon-sdv.io/portal-id. Catalog rows are listed first; duplicates by id or url are dropped.",
            "items": { "$ref": "#/components/schemas/ModuleApplication" }
          },
          "hardDependentCount": { "type": "integer", "description": "Number of enabled modules that list this module as a hard dependency" },
          "hardDependents": { "type": "array", "items": { "type": "string" }, "description": "Names of those hard dependent modules (sorted)" },
          "softDependentCount": { "type": "integer", "description": "Number of enabled modules that list this module as a soft dependency" },
          "softDependents": { "type": "array", "items": { "type": "string" }, "description": "Names of those soft dependent modules (sorted)" },
          "autoDisableWhenUnused": { "type": "boolean", "description": "When true, ModuleCatalog entry allows auto-disable when both hard and soft dependent counts drop to zero" },
          "targetRevision": { "type": "string", "description": "Effective Git ref for the module Application" },
          "clusterTargetRevision": { "type": "string", "description": "Module Manager default Git ref (--target-revision)" },
          "pinned": { "type": "boolean", "description": "True when the module has an explicit Git ref pin; false when following the cluster default" }
        }
      },
      "ModuleStatus": {
        "type": "object",
        "properties": {
          "syncStatus": { "type": "string", "description": "Argo CD sync status (worst of parent + child Applications)" },
          "healthStatus": { "type": "string", "description": "Argo CD health status (worst of parent + child Applications)" },
          "operationPhase": { "type": "string", "description": "Argo CD operation phase (e.g. Running, Pending); aggregated across parent + children" },
          "desiredRevision": { "type": "string", "description": "spec.source.targetRevision (parent Application only)" },
          "syncRevision": { "type": "string", "description": "Resolved revision from status.sync.revision (parent Application only)" },
          "applicationDeletionTimestamp": { "type": "string", "description": "RFC3339 time when the parent or any module-managed Argo CD Application has metadata.deletionTimestamp (uninstall in progress)" },
          "remainingManagedApplications": {
            "type": "integer",
            "minimum": 0,
            "description": "Present only when the module is disabled: number of parent+child Argo CD Applications still on the cluster for this module"
          },
          "managedApplicationCount": {
            "type": "integer",
            "minimum": 0,
            "description": "Present only when the module is enabled and listing succeeded: number of labeled parent+child Argo CD Applications for this module"
          },
          "expectedManagedApplicationCount": {
            "type": "integer",
            "minimum": 1,
            "description": "2 for workloads-android and workloads-common (mod-* parent plus prefixed child Application); used by the Developer Portal to avoid READY when only the parent exists"
          },
          "managedChildApplicationPresent": {
            "type": "boolean",
            "description": "When the module uses a prefixed child Application: false if the labeled child Argo CD Application is not on the cluster"
          },
          "parentSkipReconcile": {
            "type": "boolean",
            "description": "True when the mod-* parent Application has argocd.argoproj.io/skip-reconcile (prefixed-module disable teardown)"
          }
        }
      },
      "WorkflowsVisibility": {
        "type": "object",
        "properties": {
          "allowedSubmittedFrom": {
            "type": "array",
            "items": { "type": "string" },
            "description": "If set, only workflows whose horizon-sdv.io/submitted-from label is in this list are shown in the Developer Portal. Omit or null for no restriction; empty array hides all."
          }
        }
      }
    }
  }
}`)
}
