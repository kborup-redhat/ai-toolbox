package k8s

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testClient(handler http.Handler) (*Client, *httptest.Server) {
	server := httptest.NewTLSServer(handler)
	client := &Client{
		baseURL:    server.URL,
		token:      "test-token",
		httpClient: server.Client(),
	}
	return client, server
}

func TestPatch(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody []byte

	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"kind":"Node"}`))
	}))
	defer server.Close()

	body, err := c.patch("/api/v1/nodes/node1", []byte(`{"metadata":{}}`), "application/strategic-merge-patch+json")
	if err != nil {
		t.Fatalf("patch returned error: %v", err)
	}
	if gotMethod != "PATCH" {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/nodes/node1" {
		t.Errorf("path = %q, want /api/v1/nodes/node1", gotPath)
	}
	if gotContentType != "application/strategic-merge-patch+json" {
		t.Errorf("content-type = %q, want application/strategic-merge-patch+json", gotContentType)
	}
	if string(gotBody) != `{"metadata":{}}` {
		t.Errorf("body = %q, want {\"metadata\":{}}", string(gotBody))
	}
	if body == nil {
		t.Error("response body should not be nil")
	}
}

func TestPatchError(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		w.Write([]byte(`{"message":"conflict"}`))
	}))
	defer server.Close()

	_, err := c.patch("/api/v1/nodes/node1", []byte(`{}`), "application/merge-patch+json")
	if err == nil {
		t.Fatal("expected error for 409 response")
	}
}

func TestPost(t *testing.T) {
	var gotMethod, gotPath string
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(201)
		w.Write([]byte(`{"kind":"ConfigMap"}`))
	}))
	defer server.Close()

	body, err := c.post("/api/v1/namespaces/default/configmaps", []byte(`{"metadata":{"name":"test"}}`))
	if err != nil {
		t.Fatalf("post returned error: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/namespaces/default/configmaps" {
		t.Errorf("path = %q, want /api/v1/namespaces/default/configmaps", gotPath)
	}
	if body == nil {
		t.Error("response body should not be nil")
	}
}

func TestDeleteResource(t *testing.T) {
	var gotMethod, gotPath string
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(200)
	}))
	defer server.Close()

	err := c.deleteResource("/apis/batch/v1/namespaces/default/jobs/test-job")
	if err != nil {
		t.Fatalf("deleteResource returned error: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/apis/batch/v1/namespaces/default/jobs/test-job" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestDeleteResourceNotFound(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer server.Close()

	err := c.deleteResource("/api/v1/namespaces/default/pods/gone")
	if err != nil {
		t.Errorf("deleteResource should not error on 404, got: %v", err)
	}
}

func TestGetWithStatus(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer server.Close()

	_, statusCode, err := c.getWithStatus("/apis/apiextensions.k8s.io/v1/customresourcedefinitions/clusterpolicies.nvidia.com")
	if err != nil {
		t.Fatalf("getWithStatus should not error on 404, got: %v", err)
	}
	if statusCode != 404 {
		t.Errorf("statusCode = %d, want 404", statusCode)
	}
}

func TestGetWithStatusSuccess(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"metadata":{"name":"test"}}`))
	}))
	defer server.Close()

	body, statusCode, err := c.getWithStatus("/apis/apiextensions.k8s.io/v1/customresourcedefinitions/test.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusCode != 200 {
		t.Errorf("statusCode = %d, want 200", statusCode)
	}
	if len(body) == 0 {
		t.Error("body should not be empty")
	}
}

func TestPatchNodeLabel(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"kind":"Node"}`))
	}))
	defer server.Close()

	err := c.PatchNodeLabels("gpu-node-1", map[string]string{"nvidia.com/mig.config": "all-2g.10gb"})
	if err != nil {
		t.Fatalf("PatchNodeLabels returned error: %v", err)
	}

	var patch struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(gotBody, &patch); err != nil {
		t.Fatalf("failed to parse patch body: %v", err)
	}
	if patch.Metadata.Labels["nvidia.com/mig.config"] != "all-2g.10gb" {
		t.Errorf("label value = %q, want all-2g.10gb", patch.Metadata.Labels["nvidia.com/mig.config"])
	}
}

func TestCordonNode(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"kind":"Node"}`))
	}))
	defer server.Close()

	err := c.CordonNode("gpu-node-1")
	if err != nil {
		t.Fatalf("CordonNode returned error: %v", err)
	}

	var patch struct {
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(gotBody, &patch); err != nil {
		t.Fatalf("failed to parse patch body: %v", err)
	}
	if !patch.Spec.Unschedulable {
		t.Error("expected unschedulable=true")
	}
}

func TestUncordonNode(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		w.Write([]byte(`{"kind":"Node"}`))
	}))
	defer server.Close()

	err := c.UncordonNode("gpu-node-1")
	if err != nil {
		t.Fatalf("UncordonNode returned error: %v", err)
	}

	var patch struct {
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(gotBody, &patch); err != nil {
		t.Fatalf("failed to parse patch body: %v", err)
	}
	if patch.Spec.Unschedulable {
		t.Error("expected unschedulable=false")
	}
}

func TestCreateConfigMap(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		w.Write([]byte(`{"kind":"ConfigMap"}`))
	}))
	defer server.Close()

	err := c.CreateConfigMap("nvidia-gpu-operator", "device-plugin-config", map[string]string{
		"Tesla-T4": `{"sharing":{"timeSlicing":{"resources":[{"name":"nvidia.com/gpu","replicas":4}]}}}`,
	})
	if err != nil {
		t.Fatalf("CreateConfigMap returned error: %v", err)
	}

	var cm struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &cm); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}
	if cm.Metadata.Name != "device-plugin-config" {
		t.Errorf("name = %q, want device-plugin-config", cm.Metadata.Name)
	}
	if cm.Metadata.Namespace != "nvidia-gpu-operator" {
		t.Errorf("namespace = %q, want nvidia-gpu-operator", cm.Metadata.Namespace)
	}
	if _, ok := cm.Data["Tesla-T4"]; !ok {
		t.Error("expected Tesla-T4 key in data")
	}
}

func TestPatchConfigMap(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"kind":"ConfigMap"}`))
	}))
	defer server.Close()

	err := c.PatchConfigMap("nvidia-gpu-operator", "device-plugin-config", map[string]string{
		"Tesla-T4": "updated",
	})
	if err != nil {
		t.Fatalf("PatchConfigMap returned error: %v", err)
	}
}

func TestCRDExists(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/clusterpolicies.nvidia.com" {
			w.WriteHeader(200)
			w.Write([]byte(`{"metadata":{"name":"clusterpolicies.nvidia.com"}}`))
		} else {
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	exists, err := c.CRDExists("clusterpolicies.nvidia.com")
	if err != nil {
		t.Fatalf("CRDExists returned error: %v", err)
	}
	if !exists {
		t.Error("expected CRD to exist")
	}

	exists, err = c.CRDExists("nonexistent.example.com")
	if err != nil {
		t.Fatalf("CRDExists returned error: %v", err)
	}
	if exists {
		t.Error("expected CRD to not exist")
	}
}

func TestEvictPod(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()

	err := c.EvictPod("default", "my-pod")
	if err != nil {
		t.Fatalf("EvictPod returned error: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/namespaces/default/pods/my-pod/eviction" {
		t.Errorf("path = %q, want /api/v1/namespaces/default/pods/my-pod/eviction", gotPath)
	}
	var eviction struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal(gotBody, &eviction); err != nil {
		t.Fatalf("failed to parse eviction body: %v", err)
	}
	if eviction.APIVersion != "policy/v1" {
		t.Errorf("apiVersion = %q, want policy/v1", eviction.APIVersion)
	}
	if eviction.Kind != "Eviction" {
		t.Errorf("kind = %q, want Eviction", eviction.Kind)
	}
}

func TestEvictPodPDBBlocked(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"message":"Cannot evict pod as it would violate the pod's disruption budget."}`))
	}))
	defer server.Close()

	err := c.EvictPod("default", "my-pod")
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
}

func TestCreateJob(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		w.Write([]byte(`{"kind":"Job"}`))
	}))
	defer server.Close()

	err := c.CreateJob("default", "test-job", "busybox:latest", []string{"echo", "hello"}, nil)
	if err != nil {
		t.Fatalf("CreateJob returned error: %v", err)
	}

	var job struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					RestartPolicy string `json:"restartPolicy"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(gotBody, &job); err != nil {
		t.Fatalf("failed to parse job body: %v", err)
	}
	if job.APIVersion != "batch/v1" {
		t.Errorf("apiVersion = %q, want batch/v1", job.APIVersion)
	}
	if job.Spec.Template.Spec.RestartPolicy != "Never" {
		t.Errorf("restartPolicy = %q, want Never", job.Spec.Template.Spec.RestartPolicy)
	}
}

func TestGetConfigMap(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"metadata":{"name":"test"},"data":{"key1":"val1"}}`))
	}))
	defer server.Close()

	data, err := c.GetConfigMap("default", "test")
	if err != nil {
		t.Fatalf("GetConfigMap returned error: %v", err)
	}
	if data["key1"] != "val1" {
		t.Errorf("data[key1] = %q, want val1", data["key1"])
	}
}

func TestPatchCustomResource(t *testing.T) {
	var gotContentType string
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(200)
		w.Write([]byte(`{"kind":"ClusterPolicy"}`))
	}))
	defer server.Close()

	_, err := c.PatchCustomResource("/apis/nvidia.com/v1/clusterpolicies/gpu-cluster-policy", []byte(`{"spec":{}}`))
	if err != nil {
		t.Fatalf("PatchCustomResource returned error: %v", err)
	}
	if gotContentType != "application/merge-patch+json" {
		t.Errorf("content-type = %q, want application/merge-patch+json (CRDs do not support strategic merge)", gotContentType)
	}
}

func TestCreateNamespace(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		w.Write([]byte(`{"kind":"Namespace"}`))
	}))
	defer server.Close()

	err := c.CreateNamespace("nvidia-gpu-operator")
	if err != nil {
		t.Fatalf("CreateNamespace returned error: %v", err)
	}

	var ns struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(gotBody, &ns); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}
	if ns.Metadata.Name != "nvidia-gpu-operator" {
		t.Errorf("name = %q, want nvidia-gpu-operator", ns.Metadata.Name)
	}
}

func TestCreateSubscription(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		w.Write([]byte(`{"kind":"Subscription"}`))
	}))
	defer server.Close()

	err := c.CreateSubscription("nvidia-gpu-operator", "gpu-operator-certified", "v24.9", "certified-operators", "openshift-marketplace")
	if err != nil {
		t.Fatalf("CreateSubscription returned error: %v", err)
	}

	var sub struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Spec       struct {
			Channel         string `json:"channel"`
			Name            string `json:"name"`
			Source          string `json:"source"`
			SourceNamespace string `json:"sourceNamespace"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(gotBody, &sub); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}
	if sub.APIVersion != "operators.coreos.com/v1alpha1" {
		t.Errorf("apiVersion = %q, want operators.coreos.com/v1alpha1", sub.APIVersion)
	}
	if sub.Spec.Channel != "v24.9" {
		t.Errorf("channel = %q, want v24.9", sub.Spec.Channel)
	}
}

func TestCreateOperatorGroup(t *testing.T) {
	var gotBody []byte
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		w.Write([]byte(`{"kind":"OperatorGroup"}`))
	}))
	defer server.Close()

	err := c.CreateOperatorGroup("nvidia-gpu-operator", "nvidia-gpu-operator-group")
	if err != nil {
		t.Fatalf("CreateOperatorGroup returned error: %v", err)
	}

	var og struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal(gotBody, &og); err != nil {
		t.Fatalf("failed to parse body: %v", err)
	}
	if og.APIVersion != "operators.coreos.com/v1" {
		t.Errorf("apiVersion = %q, want operators.coreos.com/v1", og.APIVersion)
	}
	if og.Kind != "OperatorGroup" {
		t.Errorf("kind = %q, want OperatorGroup", og.Kind)
	}
}

func TestGetPodsOnNode(t *testing.T) {
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fieldSelector") != "spec.nodeName=gpu-node-1" {
			t.Errorf("fieldSelector = %q, want spec.nodeName=gpu-node-1", r.URL.Query().Get("fieldSelector"))
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"items":[{"metadata":{"name":"pod1"},"spec":{"nodeName":"gpu-node-1"},"status":{"phase":"Running"}}]}`))
	}))
	defer server.Close()

	pods, err := c.GetPodsOnNode("gpu-node-1")
	if err != nil {
		t.Fatalf("GetPodsOnNode returned error: %v", err)
	}
	if len(pods) != 1 {
		t.Errorf("got %d pods, want 1", len(pods))
	}
}

func TestAddNodeTaint(t *testing.T) {
	callCount := 0
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: GET node to read existing taints
			w.WriteHeader(200)
			w.Write([]byte(`{"metadata":{"name":"gpu-node-1"},"spec":{"taints":[{"key":"existing","value":"true","effect":"NoSchedule"}]}}`))
		} else {
			// Second call: PATCH with merged taints
			w.WriteHeader(200)
			w.Write([]byte(`{"kind":"Node"}`))
		}
	}))
	defer server.Close()

	err := c.AddNodeTaint("gpu-node-1", "maintenance", "true", "NoExecute")
	if err != nil {
		t.Fatalf("AddNodeTaint returned error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls (get + patch), got %d", callCount)
	}
}

func TestRemoveNodeTaint(t *testing.T) {
	callCount := 0
	c, server := testClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(200)
			w.Write([]byte(`{"metadata":{"name":"gpu-node-1"},"spec":{"taints":[{"key":"existing","value":"true","effect":"NoSchedule"},{"key":"maintenance","value":"true","effect":"NoExecute"}]}}`))
		} else {
			body, _ := io.ReadAll(r.Body)
			var patch struct {
				Spec struct {
					Taints []Taint `json:"taints"`
				} `json:"spec"`
			}
			json.Unmarshal(body, &patch)
			if len(patch.Spec.Taints) != 1 {
				t.Errorf("expected 1 remaining taint, got %d", len(patch.Spec.Taints))
			}
			w.WriteHeader(200)
			w.Write([]byte(`{"kind":"Node"}`))
		}
	}))
	defer server.Close()

	err := c.RemoveNodeTaint("gpu-node-1", "maintenance")
	if err != nil {
		t.Fatalf("RemoveNodeTaint returned error: %v", err)
	}
}
