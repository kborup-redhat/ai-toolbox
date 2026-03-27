# AI Toolbox

A web dashboard for monitoring and testing AI/ML models running on OpenShift with KServe/vLLM.

## Features

- **Model Overview** — GPU usage, model status, resource allocation per model
- **Metrics** — Request rate, latency (avg/P50/P99), error rate, GPU utilization with time-series charts
- **Analyzer** — Performance analysis, scheduling efficiency, config audit, health checks
- **Load Testing** — Run concurrent load tests against deployed models with live stats
- **Firewall Rules** — View NetworkPolicies affecting model namespaces
- **Status Page** — Connection health for cluster API, monitoring, and console services

## Prerequisites

- OpenShift 4.x cluster
- `oc` CLI installed and logged in with cluster-admin privileges
- Models deployed via KServe InferenceServices with vLLM runtime
- User Workload Monitoring enabled (the app will warn you if it is not)

## Deploy

### Build in OpenShift and deploy (recommended)

No registry login or local container tools needed:

```bash
./deploy.sh --build
```

### Deploy a pre-built image

```bash
./deploy.sh --image quay.io/myuser/ai-toolbox:v1.0
```

### Deploy with defaults

```bash
./deploy.sh
```

## Configuration

After deployment, edit the ConfigMap to control access:

```bash
oc edit configmap ai-toolbox -n ai-toolbox
```

- `allowed-groups` — Comma-separated list of OpenShift groups. Leave empty to allow all authenticated users. Users with `cluster-admin` ClusterRoleBinding are always allowed regardless of this setting.

## Environment Variables

| Variable | Description | Default |
|---|---|---|
| `CLUSTER_DOMAIN` | Cluster apps domain | Auto-detected |
| `APP_IMAGE` | Container image to deploy | `quay.io/kborup-redhat/ai-toolbox:latest` |
| `OAUTH_PROXY_IMAGE` | OAuth proxy sidecar image | `quay.io/openshift/origin-oauth-proxy:latest` |
| `BUILD_TIMEOUT` | In-cluster build timeout | `600s` |

## Running a Load Test

1. Navigate to the **Load Test** tab
2. Select a deployed model from the dropdown
3. Configure the test parameters:
   - **Max Tokens** — Number of tokens to generate per request (default: 50)
   - **Concurrency** — Number of parallel requests (default: 1)
4. Click **Start Load Test** — you will be prompted to also enable live metric watching
5. Monitor real-time stats: requests/sec, latency, success/failure counts
6. Click **Stop Load Test** when done — the runner pod is automatically cleaned up

If another user is already running a load test, you will see a confirmation dialog before overwriting their test.

## Architecture

The app runs behind an OpenShift OAuth proxy for authentication. It uses the in-cluster service account token to query the Kubernetes API and Thanos/Prometheus for metrics. Load tests run in a separate runner pod with a NetworkPolicy restricting access.

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
Assisted-By: Claude
