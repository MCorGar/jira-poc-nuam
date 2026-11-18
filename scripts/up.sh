#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077
mkdir -p .runtime
export KUBECONFIG="$PWD/.runtime/kubeconfig.yaml"
catalog_file="${CATALOG_FILE:-../Catálogo de servicio TI 1.xlsx}"
[[ -f "$catalog_file" ]] || { echo "Falta Excel: configura CATALOG_FILE" >&2; exit 1; }
command -v docker >/dev/null
command -v kubectl >/dev/null
command -v go >/dev/null
[[ -x .tools/k3d ]] || { echo "Ejecuta bash scripts/install-tools.sh" >&2; exit 1; }
docker info >/dev/null
bash scripts/build.sh
if ! .tools/k3d cluster list --no-headers | awk '{print $1}' | rg -qx jira-poc-nuam; then
  .tools/k3d cluster create jira-poc-nuam --servers 1 --agents 0 --servers-memory 2g \
    --image rancher/k3s:v1.35.5-k3s1 \
    --api-port 127.0.0.1:6550 --port '127.0.0.1:8000:30080@server:0' \
    --k3s-arg '--disable=traefik,servicelb,metrics-server@server:0' \
    --kubeconfig-update-default=false --kubeconfig-switch-context=false --wait
fi
.tools/k3d cluster start jira-poc-nuam --wait
.tools/k3d kubeconfig get jira-poc-nuam > "$KUBECONFIG"
kube=(kubectl --context k3d-jira-poc-nuam)
"${kube[@]}" apply -f k8s/namespace.yaml
# Conservar secretos entre reinicios; nunca rotar la clave de un PVC existente por accidente.
if ! "${kube[@]}" -n jira-poc get secret jira-poc-secrets >/dev/null 2>&1; then
  .runtime/bin/jira-poc prepare-local
  "${kube[@]}" -n jira-poc create secret generic jira-poc-secrets --from-env-file=.runtime/secrets.env
fi
"${kube[@]}" -n jira-poc get secret jira-poc-secrets -o json | .runtime/bin/jira-poc postman-env
"${kube[@]}" apply -f k8s/postgres.yaml
"${kube[@]}" -n jira-poc rollout status statefulset/postgres --timeout=180s
docker build -t jira-poc-nuam:local .
.tools/k3d image import jira-poc-nuam:local -c jira-poc-nuam
# Job de importación. El Excel se monta temporalmente y no se incorpora a la imagen.
import_name="catalog-import-$(date +%s)"
"${kube[@]}" -n jira-poc create configmap "$import_name" --from-file="catalog.xlsx=$catalog_file"
.runtime/bin/jira-poc import-job --name "$import_name" --file "$catalog_file" > .runtime/import-job.json
"${kube[@]}" create -f .runtime/import-job.json
if ! "${kube[@]}" -n jira-poc wait --for=condition=complete "job/$import_name" --timeout=180s; then
  "${kube[@]}" -n jira-poc logs "job/$import_name"
  echo 'Importación fallida: se conserva el Job para diagnóstico.' >&2
  exit 1
fi
"${kube[@]}" -n jira-poc logs "job/$import_name"
"${kube[@]}" -n jira-poc delete job "$import_name"
"${kube[@]}" -n jira-poc delete configmap "$import_name"
# Preserve the configured real provider when its Secret exists. An explicit
# DECISION_PROVIDER=mock still starts the lexical simulator for contract tests.
provider="${DECISION_PROVIDER:-mock}"
if [[ -z "${DECISION_PROVIDER+x}" ]] && "${kube[@]}" -n jira-poc get secret jira-decision-credentials >/dev/null 2>&1; then
  provider=decision-1
fi
[[ "$provider" == mock || "$provider" == decision-1 ]] || { echo 'DECISION_PROVIDER debe ser mock o decision-1' >&2; exit 1; }
"${kube[@]}" apply -f k8s/api.yaml
"${kube[@]}" -n jira-poc set env deployment/jira-api DECISION_PROVIDER="$provider"
"${kube[@]}" -n jira-poc rollout restart deployment/jira-api
"${kube[@]}" -n jira-poc rollout status deployment/jira-api --timeout=180s
echo 'API: http://127.0.0.1:8000/docs'
echo 'Postman: importa postman/collection.json y .runtime/postman.local.environment.json'
