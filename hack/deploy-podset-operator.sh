#!/usr/bin/env bash
# Deploy the podset-operator to a cluster (e.g. ACK) from the all-in-one manifest.
#
# This script only deploys the operator. It does not run a load test and it
# never creates or copies image pull secrets.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/ack}"
IMAGE="tars-shanghai-registry-vpc.cn-shanghai.cr.aliyuncs.com/infra/podset-operator:v4"
NAMESPACE="podset-operator-system"
DRY_RUN=0

usage() {
  cat <<'EOF'
Usage: deploy-podset-operator.sh [options]

  --kubeconfig PATH   kubeconfig to use (default: $KUBECONFIG or ~/.kube/ack)
  --image IMAGE       manager image (default: .../infra/podset-operator:v4)
  --namespace NAME    operator namespace (default: podset-operator-system)
  --dry-run           run read-only checks and print the plan; create nothing
  -h, --help          show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --kubeconfig) KUBECONFIG_PATH="$2"; shift 2 ;;
    --image) IMAGE="$2"; shift 2 ;;
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

KUBECTL=(kubectl --kubeconfig "${KUBECONFIG_PATH}")
MANIFEST="${ROOT_DIR}/config/manifests/all.yaml"

if [[ ! -f "${MANIFEST}" ]]; then
  echo "manifest not found: ${MANIFEST}" >&2
  exit 1
fi

echo "cluster:   ${KUBECONFIG_PATH}"
echo "namespace: ${NAMESPACE}"
echo "image:     ${IMAGE}"
echo "manifest:  ${MANIFEST}"

# Read-only checks.
"${KUBECTL[@]}" auth can-i create customresourcedefinitions >/dev/null || {
  echo "missing permission: create customresourcedefinitions" >&2
  exit 1
}
"${KUBECTL[@]}" auth can-i create clusterrolebindings >/dev/null || {
  echo "missing permission: create clusterrolebindings" >&2
  exit 1
}

if (( DRY_RUN )); then
  echo "dry-run: checks passed; no resources created"
  exit 0
fi

# Create the namespace first so the platform can inject its ACR pull secret.
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
The operator image is private. Create/link a pull secret for the namespace,
then rerun this script. This script does not create or copy secrets.
EOF
  exit 1
fi
echo "imagePullSecrets: ${PULL_SECRETS}"

TMP_MANIFEST="$(mktemp)"
trap 'rm -f "${TMP_MANIFEST}"' EXIT

python3 -I - "${MANIFEST}" "${TMP_MANIFEST}" "${IMAGE}" <<'PY'
import sys
import yaml

src, dst, image = sys.argv[1:4]
docs = list(yaml.safe_load_all(open(src)))
found = False
for doc in docs:
    if not doc or doc.get("kind") != "Deployment":
        continue
    for container in doc["spec"]["template"]["spec"]["containers"]:
        if container["name"] == "manager":
            container["image"] = image
            found = True
if not found:
    raise SystemExit("manager container not found in manifest")
yaml.safe_dump_all(docs, open(dst, "w"), sort_keys=False)
PY

"${KUBECTL[@]}" apply -f "${TMP_MANIFEST}"
"${KUBECTL[@]}" -n "${NAMESPACE}" rollout status \
  deploy/podset-operator-controller-manager --timeout=300s
"${KUBECTL[@]}" -n "${NAMESPACE}" get deploy,pods,svc
