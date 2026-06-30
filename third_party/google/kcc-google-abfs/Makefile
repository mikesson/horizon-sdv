# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SHELL := /usr/bin/env bash
CHART := chart/abfs
# Override the instance to render: make render ENV=instances/my-instance.env
ENV ?= instances/example.env

.PHONY: help lint template kustomize validate render clean

help:
	@echo "Targets:"
	@echo "  lint       helm lint the chart"
	@echo "  template   helm template the chart (server/uploader + UI)"
	@echo "  kustomize  kubectl kustomize infra/ and infra/cicd/"
	@echo "  validate   lint + template + kustomize"
	@echo "  render     render an instance (ENV=instances/<name>.env)"
	@echo "  clean      remove rendered/ build artifacts"

lint:
	helm lint $(CHART)

template:
	@helm template abfs $(CHART) -n abfs --set licensed=true --set ui.enabled=true >/dev/null \
		&& echo "helm template OK"

kustomize:
	@kubectl kustomize infra/ >/dev/null && echo "kustomize infra/ OK"
	@kubectl kustomize infra/cicd/ >/dev/null && echo "kustomize infra/cicd/ OK"

validate: lint template kustomize

render:
	scripts/render.sh $(ENV)

clean:
	rm -rf rendered/
