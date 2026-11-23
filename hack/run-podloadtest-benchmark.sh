#!/usr/bin/env bash
# Run a PodLoadTest benchmark and collect metrics for an existing operator.
#
# Prerequisites:
#   - the podset-operator (and its CRDs/metrics service) is deployed
#   - the test namespace can auto-create its ACR imagePullSecret
#   - the test image is pullable from the cluster
#
# The script never creates or copies image pull secrets.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/ack}"
NAMESPACE="podloadtest-bench"
SAMPLE="${ROOT_DIR}/config/samples/data.clond_v1_podloadtest_1min.yaml"
OPERATOR_NAMESPACE="podset-operator-system"
METRICS_ROLE="podset-operator-metrics-reader"
LOCAL_PORT="18450"
CLEANUP=1
DRY_RUN=0
WAIT_TIMEOUT=1800
DRAIN_TIMEOUT=1800

usage() {
  cat <<'EOF'
Usage: run-podloadtest-benchmark.sh [options]

  --kubeconfig PATH        kubeconfig to use (default: $KUBECONFIG or ~/.kube/ack)
  --sample PATH            PodLoadTest manifest (default: .../data.clond_v1_podloadtest_1min.yaml)
  --namespace NAME         test namespace (default: podloadtest-bench)
  --operator-namespace NAME  operator namespace (default: podset-operator-system)
  --metrics-role NAME      ClusterRole used by the scraper (default: podset-operator-metrics-reader)
  --local-port PORT        local port for the metrics port-forward (default: 18450)
  --no-cleanup             keep the test namespace after the run
  --dry-run                run read-only preflight checks and print the plan
  -h, --help               show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --kubeconfig) KUBECONFIG_PATH="$2"; shift 2 ;;
    --sample) SAMPLE="$2"; shift 2 ;;
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --operator-namespace) OPERATOR_NAMESPACE="$2"; shift 2 ;;
    --metrics-role) METRICS_ROLE="$2"; shift 2 ;;
    --local-port) LOCAL_PORT="$2"; shift 2 ;;
    --no-cleanup) CLEANUP=0; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

KUBECTL=(kubectl --kubeconfig "${KUBECONFIG_PATH}")
CRD="podloadtests.data.clond.com.shalousun"
DEPLOY="podset-operator-controller-manager"
METRICS_SVC="podset-operator-controller-manager-metrics-service"

[[ -f "${SAMPLE}" ]] || { echo "sample not found: ${SAMPLE}" >&2; exit 1; }
NAME="$(awk '/^metadata:/{m=1; next} m && /^  name:/{print $2; exit}' "${SAMPLE}")"
TOTAL="$(awk '/^spec:/{s=1; next} s && /^  total:/{print $2; exit}' "${SAMPLE}")"
[[ -n "${NAME}" ]] || { echo "cannot read metadata.name from ${SAMPLE}" >&2; exit 1; }

echo "cluster:            ${KUBECONFIG_PATH}"
echo "operator namespace: ${OPERATOR_NAMESPACE}"
echo "test namespace:     ${NAMESPACE}"
echo "sample:             ${SAMPLE} (name=${NAME}, total=${TOTAL:-unlimited})"
echo "metrics role:       ${METRICS_ROLE}"
echo "local metrics port: ${LOCAL_PORT}"

# Read-only preflight.
"${KUBECTL[@]}" get crd "${CRD}" >/dev/null 2>&1 || {
  echo "operator CRD not found: ${CRD}. Deploy the operator first." >&2
  exit 1
}
"${KUBECTL[@]}" -n "${OPERATOR_NAMESPACE}" get deploy "${DEPLOY}" >/dev/null 2>&1 || {
  echo "operator deployment not found: ${OPERATOR_NAMESPACE}/${DEPLOY}" >&2
  exit 1
}
"${KUBECTL[@]}" -n "${OPERATOR_NAMESPACE}" get svc "${METRICS_SVC}" >/dev/null 2>&1 || {
  echo "metrics service not found: ${OPERATOR_NAMESPACE}/${METRICS_SVC}" >&2
  exit 1
}
"${KUBECTL[@]}" get clusterrole "${METRICS_ROLE}" >/dev/null 2>&1 || {
  echo "metrics ClusterRole not found: ${METRICS_ROLE}" >&2
  exit 1
}

if (( DRY_RUN )); then
  echo "dry-run: preflight passed; no resources created"
  exit 0
fi

# Create the test namespace and verify the platform injected its ACR secret.
if ! "${KUBECTL[@]}" get namespace "${NAMESPACE}" >/dev/null 2>&1; then
  "${KUBECTL[@]}" create namespace "${NAMESPACE}"
fi

PULL_SECRETS=""
for _ in $(seq 1 30); do
  PULL_SECRETS="$("${KUBECTL[@]}" -n "${NAMESPACE}" get sa default \
    -o jsonpath='{.imagePullSecrets[*].name}' 2>/dev/null || true)"
  [[ -n "${PULL_SECRETS}" ]] && break
  sleep 2
done
if [[ -z "${PULL_SECRETS}" ]]; then
  cat >&2 <<EOF
ERROR: the default ServiceAccount in ${NAMESPACE} has no imagePullSecrets.
Create/link a pull secret for the namespace, then rerun. This script does not
create or copy secrets.
EOF
  exit 1
fi
echo "imagePullSecrets:   ${PULL_SECRETS}"

if "${KUBECTL[@]}" -n "${NAMESPACE}" get plt "${NAME}" >/dev/null 2>&1; then
  echo "PodLoadTest ${NAMESPACE}/${NAME} already exists; use a fresh namespace or delete it first." >&2
  exit 1
fi

"${KUBECTL[@]}" -n "${NAMESPACE}" get resourcequota 2>/dev/null || true

# Temporary metrics scraper.
"${KUBECTL[@]}" -n "${NAMESPACE}" create serviceaccount metrics-scraper \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
CRB_NAME="podloadtest-metrics-scraper-${NAMESPACE}"
"${KUBECTL[@]}" create clusterrolebinding "${CRB_NAME}" \
  --clusterrole="${METRICS_ROLE}" \
  --serviceaccount="${NAMESPACE}:metrics-scraper" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null

TOKEN="$("${KUBECTL[@]}" create token metrics-scraper -n "${NAMESPACE}" --duration=2h)"

PF_LOG="/tmp/podloadtest-portforward-${NAMESPACE}.log"
"${KUBECTL[@]}" -n "${OPERATOR_NAMESPACE}" port-forward \
  "svc/${METRICS_SVC}" "${LOCAL_PORT}:8443" >"${PF_LOG}" 2>&1 &
PF_PID=$!

SNAPSHOT_DIR="/tmp/podloadtest-snapshots-${NAMESPACE}-$(date +%Y%m%d-%H%M%S)"
mkdir -p "${SNAPSHOT_DIR}"
SAMPLER_PID=""

cleanup() {
  [[ -n "${SAMPLER_PID}" ]] && kill "${SAMPLER_PID}" 2>/dev/null || true
  kill "${PF_PID}" 2>/dev/null || true
  "${KUBECTL[@]}" delete clusterrolebinding "${CRB_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  if (( CLEANUP )); then
    "${KUBECTL[@]}" delete namespace "${NAMESPACE}" --wait=false >/dev/null 2>&1 || true
  fi
  echo "artifacts: snapshots=${SNAPSHOT_DIR} metrics=/tmp/podloadtest-metrics-${NAMESPACE}.txt status=/tmp/podloadtest-status-${NAMESPACE}.json"
}
trap cleanup EXIT

# Apply the test after the scraper is ready so no early metrics are missed.
"${KUBECTL[@]}" -n "${NAMESPACE}" apply -f "${SAMPLE}"

# Wait for the HTTPS metrics endpoint.
METRICS_FILE="/tmp/podloadtest-metrics-${NAMESPACE}.txt"
READY=0
for _ in $(seq 1 20); do
  if curl -sk --max-time 3 -H "Authorization: Bearer ${TOKEN}" \
      "https://127.0.0.1:${LOCAL_PORT}/metrics" -o "${METRICS_FILE}" 2>/dev/null; then
    READY=1
    break
  fi
  sleep 1
done
if (( ! READY )); then
  echo "WARNING: metrics endpoint not reachable; continuing without metrics" >&2
fi

# Keep sampling while the run is active.
(
  while true; do
    curl -sk --max-time 5 -H "Authorization: Bearer ${TOKEN}" \
      "https://127.0.0.1:${LOCAL_PORT}/metrics" \
      -o "${SNAPSHOT_DIR}/$(date +%s).txt" 2>/dev/null || true
    sleep 15
  done
) &
SAMPLER_PID=$!

echo "waiting for PodLoadTest to finish submitting..."
if ! "${KUBECTL[@]}" -n "${NAMESPACE}" wait \
    --for=jsonpath='{.status.phase}'=Completed "plt/${NAME}" \
    --timeout="${WAIT_TIMEOUT}s"; then
  echo "WARNING: wait for phase=Completed timed out" >&2
fi

echo "waiting for all test Pods to be reclaimed..."
END=$((SECONDS + DRAIN_TIMEOUT))
while (( SECONDS < END )); do
  COUNT="$("${KUBECTL[@]}" -n "${NAMESPACE}" get pods --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  echo "$(date +%T) pods=${COUNT}"
  (( COUNT == 0 )) && break
  sleep 10
done

if (( READY )); then
  curl -sk --max-time 10 -H "Authorization: Bearer ${TOKEN}" \
    "https://127.0.0.1:${LOCAL_PORT}/metrics" -o "${METRICS_FILE}" || true
fi
"${KUBECTL[@]}" -n "${NAMESPACE}" get plt "${NAME}" -o jsonpath='{.status}' \
  >"/tmp/podloadtest-status-${NAMESPACE}.json" 2>/dev/null || true
"${KUBECTL[@]}" -n "${NAMESPACE}" get plt "${NAME}" 2>/dev/null || true

echo "done"
