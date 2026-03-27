#!/usr/bin/env bash
set -euo pipefail

APP_NAME="ai-toolbox"
NAMESPACE="${APP_NAME}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

OAUTH_PROXY_IMAGE="${OAUTH_PROXY_IMAGE:-quay.io/openshift/origin-oauth-proxy:latest}"
BUILD_IN_CLUSTER="${BUILD_IN_CLUSTER:-false}"
APP_IMAGE="${APP_IMAGE:-quay.io/kborup-redhat/${APP_NAME}:latest}"

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Deploy AI Toolbox to an OpenShift cluster.

Options:
  --build       Build the container image in OpenShift before deploying
                (no local container tools or registry login needed)
  --image IMG   Use a specific container image (default: ${APP_IMAGE})
  --help        Show this help message

Environment variables:
  APP_IMAGE             Container image to deploy (overridden by --image or --build)
  OAUTH_PROXY_IMAGE     OAuth proxy image (default: quay.io/openshift/origin-oauth-proxy:latest)
  CLUSTER_DOMAIN        Cluster apps domain (auto-detected if not set)
  BUILD_TIMEOUT         Timeout for in-cluster build (default: 600s)

Examples:
  # Build in OpenShift and deploy (easiest — no registry login needed):
  ./deploy.sh --build

  # Deploy a pre-built image from a registry:
  ./deploy.sh --image quay.io/myuser/ai-toolbox:v1.0

  # Deploy with defaults (quay.io/kborup-redhat/ai-toolbox:latest):
  ./deploy.sh
EOF
    exit 0
}

# --- Parse arguments ---
while [[ $# -gt 0 ]]; do
    case "$1" in
        --build)
            BUILD_IN_CLUSTER=true
            shift
            ;;
        --image)
            APP_IMAGE="$2"
            shift 2
            ;;
        --help|-h)
            usage
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
done

# --- Preflight ---
if ! command -v oc &>/dev/null; then
    echo "ERROR: oc CLI not found in PATH"
    exit 1
fi

if ! oc whoami &>/dev/null; then
    echo "ERROR: Not logged in to an OpenShift cluster"
    exit 1
fi

CLUSTER_DOMAIN="${CLUSTER_DOMAIN:-$(oc get ingresses.config cluster -o jsonpath='{.spec.domain}' 2>/dev/null || echo "")}"
if [[ -z "${CLUSTER_DOMAIN}" ]]; then
    echo "ERROR: Could not detect cluster domain. Set CLUSTER_DOMAIN env var."
    exit 1
fi

COOKIE_SECRET=$(head -c 32 /dev/urandom | base64 | tr -d '\n')

# --- Create namespace first (needed for build and deploy) ---
echo "Creating namespace..."
oc apply -f "${SCRIPT_DIR}/deploy/namespace.yaml"

# --- In-cluster build ---
if [[ "${BUILD_IN_CLUSTER}" == "true" ]]; then
    BUILD_TIMEOUT="${BUILD_TIMEOUT:-600s}"
    INTERNAL_REGISTRY="image-registry.openshift-image-registry.svc:5000"
    APP_IMAGE="${INTERNAL_REGISTRY}/${NAMESPACE}/${APP_NAME}:latest"

    echo ""
    echo "=== Building ${APP_NAME} in OpenShift ==="
    echo "  Namespace: ${NAMESPACE}"
    echo "  Timeout:   ${BUILD_TIMEOUT}"
    echo ""

    # Create ImageStream if it doesn't exist
    if ! oc get is "${APP_NAME}" -n "${NAMESPACE}" &>/dev/null; then
        echo "Creating ImageStream..."
        oc create imagestream "${APP_NAME}" -n "${NAMESPACE}"
    fi

    # Create or update BuildConfig
    if ! oc get bc "${APP_NAME}" -n "${NAMESPACE}" &>/dev/null; then
        echo "Creating BuildConfig..."
        oc new-build --name="${APP_NAME}" --binary --strategy=docker \
            --to="${APP_NAME}:latest" -n "${NAMESPACE}" 2>&1 | grep -v "^$"
    fi

    # The OpenShift builder expects a file named Dockerfile.
    # Create a temporary copy if only Containerfile exists.
    CLEANUP_DOCKERFILE=false
    if [[ ! -f "${SCRIPT_DIR}/Dockerfile" && -f "${SCRIPT_DIR}/Containerfile" ]]; then
        cp "${SCRIPT_DIR}/Containerfile" "${SCRIPT_DIR}/Dockerfile"
        CLEANUP_DOCKERFILE=true
    fi

    echo "Uploading source and building (this may take a few minutes)..."
    oc start-build "${APP_NAME}" --from-dir="${SCRIPT_DIR}" --follow \
        --wait -n "${NAMESPACE}"
    BUILD_EXIT=$?

    # Clean up temporary Dockerfile
    if [[ "${CLEANUP_DOCKERFILE}" == "true" ]]; then
        rm -f "${SCRIPT_DIR}/Dockerfile"
    fi

    if [[ ${BUILD_EXIT} -ne 0 ]]; then
        echo "ERROR: Build failed. Check build logs:"
        echo "  oc logs bc/${APP_NAME} -n ${NAMESPACE}"
        exit 1
    fi

    echo ""
    echo "Build complete. Image: ${APP_IMAGE}"
    echo ""
fi

echo "=== Deploying ${APP_NAME} ==="
echo "  Namespace:      ${NAMESPACE}"
echo "  Cluster domain: ${CLUSTER_DOMAIN}"
echo "  App image:      ${APP_IMAGE}"
echo "  OAuth proxy:    ${OAUTH_PROXY_IMAGE}"
echo ""

# --- Create resources ---
echo "Creating service account..."
oc apply -f "${SCRIPT_DIR}/deploy/serviceaccount.yaml"

echo "Creating RBAC (read-only ClusterRole)..."
oc apply -f "${SCRIPT_DIR}/deploy/clusterrole.yaml"

echo "Creating config map..."
oc apply -f "${SCRIPT_DIR}/deploy/configmap.yaml"

echo "Creating service..."
oc apply -f "${SCRIPT_DIR}/deploy/service.yaml"

echo "Creating route..."
oc apply -f "${SCRIPT_DIR}/deploy/route.yaml"

echo "Creating ServiceMonitor for Prometheus..."
oc apply -f "${SCRIPT_DIR}/deploy/servicemonitor.yaml"

echo "Creating NetworkPolicy for load test runner..."
oc apply -f "${SCRIPT_DIR}/deploy/networkpolicy-runner.yaml"

echo "Deploying application..."
sed -e "s|OAUTH_PROXY_IMAGE_PLACEHOLDER|${OAUTH_PROXY_IMAGE}|g" \
    -e "s|IMAGE_PLACEHOLDER|${APP_IMAGE}|g" \
    -e "s|COOKIE_SECRET_PLACEHOLDER|${COOKIE_SECRET}|g" \
    -e "s|CLUSTER_DOMAIN_PLACEHOLDER|${CLUSTER_DOMAIN}|g" \
    "${SCRIPT_DIR}/deploy/deployment.yaml" | oc apply -f -

echo "Waiting for rollout..."
oc rollout status deployment/${APP_NAME} -n ${NAMESPACE} --timeout=120s

ROUTE_URL="https://$(oc get route ${APP_NAME} -n ${NAMESPACE} -o jsonpath='{.spec.host}')"
echo ""
echo "=== Deployment complete ==="
echo "  URL: ${ROUTE_URL}"
echo "  Access is restricted to groups listed in the ConfigMap (default: cluster-admins)."
echo "  Edit the ConfigMap to add groups: oc edit configmap ${APP_NAME} -n ${NAMESPACE}"
echo "  The service account has read-only cluster access (no cluster-admin)."
