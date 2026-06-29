#!/usr/bin/env bash

# Copyright (c) 2024-2026 Accenture, All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#         http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -e

APISERVER=https://kubernetes.default.svc
SERVICEACCOUNT=/var/run/secrets/kubernetes.io/serviceaccount
NAMESPACE=$(cat ${SERVICEACCOUNT}/namespace)
TOKEN=$(cat ${SERVICEACCOUNT}/token)
CACERT=${SERVICEACCOUNT}/ca.crt

cd /root
MTKC_APIKEY=$(kubectl exec "$(kubectl get pod -l app.kubernetes.io/name=mtk-connect -n "${NAMESPACE}" -o name | sed 's@^pod/@@')" -n "${NAMESPACE}" -c authenticator -- node createServiceAccount.js mtk-connect-admin)

sed -i "s/##MTKC_APIKEY##/${MTKC_APIKEY}/g" ./secret-jenkins.json
sed -i "s/##NAMESPACE##/${NAMESPACE_PREFIX}jenkins/g" ./secret-jenkins.json
sed -i "s/##MTKC_APIKEY##/${MTKC_APIKEY}/g" ./secret-mtk-connect.json
sed -i "s/##NAMESPACE##/${NAMESPACE_PREFIX}mtk-connect/g" ./secret-mtk-connect.json
sed -i "s/##MTKC_APIKEY##/${MTKC_APIKEY}/g" ./secret-workflows.json
sed -i "s/##NAMESPACE##/${NAMESPACE_PREFIX}workflows/g" ./secret-workflows.json

# DELETE may 404 on first run when secrets do not exist yet
curl -sf --cacert ${CACERT} --header "Authorization: Bearer ${TOKEN}" -X DELETE ${APISERVER}/api/v1/namespaces/${NAMESPACE_PREFIX}jenkins/secrets/jenkins-mtk-connect-apikey || true
curl --cacert ${CACERT} --header "Authorization: Bearer ${TOKEN}" -H 'Accept: application/json' -H 'Content-Type: application/json' -X POST ${APISERVER}/api/v1/namespaces/${NAMESPACE_PREFIX}jenkins/secrets -d @secret-jenkins.json

curl -sf --cacert ${CACERT} --header "Authorization: Bearer ${TOKEN}" -X DELETE ${APISERVER}/api/v1/namespaces/${NAMESPACE_PREFIX}mtk-connect/secrets/mtk-connect-apikey || true
curl --cacert ${CACERT} --header "Authorization: Bearer ${TOKEN}" -H 'Accept: application/json' -H 'Content-Type: application/json' -X POST ${APISERVER}/api/v1/namespaces/${NAMESPACE_PREFIX}mtk-connect/secrets -d @secret-mtk-connect.json

curl -sf --cacert ${CACERT} --header "Authorization: Bearer ${TOKEN}" -X DELETE ${APISERVER}/api/v1/namespaces/${NAMESPACE_PREFIX}workflows/secrets/workflow-mtk-connect-apikey || true
curl --cacert ${CACERT} --header "Authorization: Bearer ${TOKEN}" -H 'Accept: application/json' -H 'Content-Type: application/json' -X POST ${APISERVER}/api/v1/namespaces/${NAMESPACE_PREFIX}workflows/secrets -d @secret-workflows.json
