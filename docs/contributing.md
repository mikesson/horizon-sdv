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

# **How to Contribute**

We would love to accept your patches and contributions to the Horizon SDV project.

## **Before you begin**

### **Sign our Contributor License Agreement**

Contributions to this project must be accompanied by a [Contributor License Agreement](https://cla.developers.google.com/about) (CLA). You (or your employer) retain the copyright to your contribution; this simply gives us permission to use and redistribute your contributions as part of the project.

If you or your current employer have already signed the Google CLA (even if it was for a different project), you probably don’t need to do it again.

Visit [https://cla.developers.google.com/](https://cla.developers.google.com/) to see your current agreements or to sign a new one.

### **Review our Community Guidelines**

This project follows [Google’s Open Source Community Guidelines](https://opensource.google/conduct/).

## **Prerequisites: Self-Deployment**

Before submitting any code, documentation, or configuration changes, you must have a functional, self-deployed instance of Horizon SDV running on your own Google Cloud Platform (GCP) project.

This requirement ensures that contributors have first-hand experience with the deployment workflow, can test their changes in a live environment, and verify that their contributions do not break existing platform functionality.

* **Follow the Deployment Guide:** Refer to the [Deployment Guide](deployment_guide.md) to set up your own environment.
* **Verify your Instance:** Ensure your deployment is healthy and that you can access the core services before beginning your development work.
* **Local Testing:** Use your deployed instance to test your changes. We generally require proof that your changes have been successfully applied and verified in a live environment.

## **Pull Request Workflow & Release Cycle**

Horizon SDV follows a structured release cycle. To maintain quality and compatibility, we manage contributions by incorporating them into specific future releases.

### **How to Submit a PR**

* **Targeting Releases:** When you open a Pull Request, please indicate which feature set or fix you are addressing.
* **Communication:** Our maintainers will review your PR and determine the appropriate release version for your changes. We will communicate this target release directly within the PR thread.
* **Testing:** Please provide logs, screenshots, or testing steps performed on your self-deployed instance to demonstrate that your changes work as expected. Alternatively, a virtual meet can be scheduled to walk through your changes.

## **Contribution Checklist**

When submitting a Pull Request, confirm the following:

* [ ] I have deployed Horizon SDV on my own GCP project.
* [ ] I have tested my changes on my deployed instance.
* [ ] My PR description clearly states the purpose of the change.
* [ ] I understand that my PR will be reviewed and assigned to a specific Horizon SDV release cycle.

## **Getting Help**

If you encounter issues during your initial deployment or have questions about the development workflow, please:

* Review the [Troubleshooting](deployment_guide.md#section-6---troubleshooting) section in the documentation.
* Reach out to the team at [horizon-sdv@google.com](mailto:horizon-sdv@google.com) for consultation regarding setup or specific technical blockers.

# **Contribution process**

## **Code Reviews**

All submissions, including submissions by project members, require review. We use [GitHub pull requests](https://docs.github.com/articles/about-pull-requests) for this purpose.

# **Github Collaboration Rules For External Contributors**

*  Pull Requests that contain functionality changes shall be raised against branch “devel” [GitHub \- GoogleCloudPlatform/horizon-sdv at devel](https://github.com/GoogleCloudPlatform/horizon-sdv/tree/devel) . Pull Requests raised against branch “main” [GitHub \- GoogleCloudPlatform/horizon-sdv](https://github.com/GoogleCloudPlatform/horizon-sdv/tree/main) will be considered as a mistake and shall be rebased onto branch “devel”.  
* Functionality changes branches shall follow a naming convention “contrib/\*”. Trivial changes that don’t impact the functionality can be contributed as regular commits.

**Partner Contribution Repository Integration Rules**

To further enhance Horizon project capabilities and foster a collaborative ecosystem, we outline strategies for effectively integrating contributions from partners into the Horizon SDV repository. The goal is to ensure that external tools, components, and integrations can be incorporated seamlessly, maintaining the project’s modularity while clearly separating core Horizon efforts from partner contributions.

## **Proposed Contribution Structure**

The top-level directory structure for external contributions:

* The third\_party/ directory will be specifically reserved for dependencies that cannot be licensed under Apache 2.0.  
* The partner/ directory serves as a dedicated location for all partner-specific contributions. Each partner will have a named subfolder within this directory (e.g., partner/\<partner\_name\>/\<solution\_name\>/).

Contributions are also expected in other functional areas within the Horizon SDV repository, such as workloads/android/pipelines.

partner/\<partner\_name\>/ **Structure**

Within each partner/\<partner\_name\>/\<solution\_name\>/ directory, contributions are expected to be organized logically by their functional domain. While the specific subfolder structure within a partner’s directory can be flexible, common categories will include:

### **Virtual Device Contributions**

**Purpose:** To encompass contributions related to virtual device environments provided by a specific partner.

**Examples:**

*  partner/\<partner\_name\>/\<solution\_name\>/virtual\_devices/targets/: Configurations for new Android launch targets or specific virtual hardware configurations introduced by the partner (e.g., custom Cuttlefish configurations, unique Android Virtual Devices).  
* partner/\<partner\_name\>/\<solution\_name\>/virtual\_devices/emulators/: Custom emulator images, specific configurations for various virtual device types, or scripts for launching and managing bespoke virtual device environments.

To better organize partner-specific virtual device configurations, a virtual\_vendor directory will be introduced within the partner’s virtual device contributions. For example, specific QEMU configurations and instance configurations from SOC vendors would reside in

partner/\<partner\_name\>/\<solution\_name\>/virtual\_devices/virtual\_vendor/.

### **CI/CT Integrations**

**Purpose:** For extensions and integrations with the CI/CT toolchain specific to a partner’s offerings.

**Examples:**

* partner/\<partner\_name\>/\<solution\_name\>/ci\_ct\_integrations/ci\_addons/: Custom CI addons or Groovy scripts that contribute to existing build and test pipelines relevant to the partner.  
* partner/\<partner\_name\>/\<solution\_name\>/ci\_ct\_integrations/gitops\_addons/: Custom Argo CD applications, specialized sync hooks for partner-specific GitOps flows.  
* partner/\<partner\_name\>/\<solution\_name\>/ci\_ct\_integrations/workflow\_templates/: Reusable workflow templates that encapsulate common partner-specific CI/CT patterns.

### **Infrastructure Modules**

**Purpose:** For Infrastructure as Code (IaC) components that provision partner-specific infrastructure.

**Examples:**

* partner/\<partner\_name\>/\<solution\_name\>/infrastructure\_modules/gcp\_resources/: Terraform modules for new Google Cloud Platform (GCP) services or custom configurations that support partner workflows.

### **License Terms & Files**

**Purpose:** For specialized license terms and trial license files that need to be included or populated in pre-deployment steps

**Examples:**

*  partner/\<partner\_name\>/\<solution\_name\>/LICENSE: For specific LICENSE terms need to be stated \- type of software etc  
* partner/\<partner\_name\>/\<solution\_name\>/license.json: For pre-canned trial licenses or as placeholder to retrieve a trial license from the partner.

## **Key Integration Principles**

### **Clear and Comprehensive Documentation**

Every contribution must include thorough and concise README.md files within its respective subfolder. Documentation should detail the component’s purpose, prerequisites, usage instructions, and clearly identify its integration points within the broader Horizon SDV framework.

### **Modularity and Self-Containment**

Contributions should be designed as modular, self-contained units, ensuring easy integration and subsequent removal or updating without breaking core functionalities of Horizon core.

### **Prioritize Configuration over Custom Code**

Whenever feasible, contributions should leverage declarative configuration rather than requiring extensive, custom code changes.