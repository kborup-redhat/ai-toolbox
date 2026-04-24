package k8s

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(apiURL, token string, insecureSkipVerify bool) *Client {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify,
	}

	// Load the in-cluster CA bundle if available
	if !insecureSkipVerify {
		if caData, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(caData)
			tlsConfig.RootCAs = pool
		}
	}

	return &Client{
		baseURL: apiURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		},
	}
}

func (c *Client) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 10 << 20 // 10 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	return body, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// --- Node types ---

type NodeList struct {
	Items []Node `json:"items"`
}

type Node struct {
	Metadata ObjectMeta     `json:"metadata"`
	Status   NodeStatus     `json:"status"`
	Spec     NodeSpec       `json:"spec"`
}

type NodeSpec struct {
	Taints []Taint `json:"taints,omitempty"`
}

type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

type NodeStatus struct {
	Capacity    ResourceList   `json:"capacity"`
	Allocatable ResourceList   `json:"allocatable"`
	Conditions  []NodeCondition `json:"conditions"`
	Addresses   []NodeAddress   `json:"addresses"`
}

type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type NodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

type ResourceList map[string]string

// --- Pod types ---

type PodList struct {
	Items []Pod `json:"items"`
}

type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     PodSpec    `json:"spec"`
	Status   PodStatus  `json:"status"`
}

type PodSpec struct {
	NodeName     string            `json:"nodeName"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	Containers   []Container       `json:"containers"`
	Affinity     *Affinity         `json:"affinity,omitempty"`
}

type Affinity struct {
	NodeAffinity *NodeAffinity `json:"nodeAffinity,omitempty"`
}

type NodeAffinity struct {
	RequiredDuringScheduling *NodeSelector `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

type NodeSelector struct {
	NodeSelectorTerms []NodeSelectorTerm `json:"nodeSelectorTerms"`
}

type NodeSelectorTerm struct {
	MatchExpressions []NodeSelectorRequirement `json:"matchExpressions,omitempty"`
}

type NodeSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type Container struct {
	Name      string                `json:"name"`
	Command   []string              `json:"command,omitempty"`
	Args      []string              `json:"args,omitempty"`
	Resources ContainerResources    `json:"resources"`
}

type ContainerResources struct {
	Requests ResourceList `json:"requests,omitempty"`
	Limits   ResourceList `json:"limits,omitempty"`
}

type PodStatus struct {
	Phase string `json:"phase"`
	PodIP string `json:"podIP,omitempty"`
}

// --- InferenceService types ---

type InferenceServiceList struct {
	Items []InferenceService `json:"items"`
}

type InferenceService struct {
	Metadata ObjectMeta             `json:"metadata"`
	Spec     map[string]interface{} `json:"spec"`
	Status   map[string]interface{} `json:"status,omitempty"`
}

// --- NetworkPolicy types ---

type NetworkPolicyList struct {
	Items []NetworkPolicy `json:"items"`
}

type NetworkPolicy struct {
	Metadata ObjectMeta        `json:"metadata"`
	Spec     NetworkPolicySpec  `json:"spec"`
}

type NetworkPolicySpec struct {
	PodSelector LabelSelector          `json:"podSelector"`
	Ingress     []NetworkPolicyRule    `json:"ingress,omitempty"`
	Egress      []NetworkPolicyRule    `json:"egress,omitempty"`
	PolicyTypes []string               `json:"policyTypes,omitempty"`
}

type LabelSelector struct {
	MatchLabels      map[string]string          `json:"matchLabels,omitempty"`
	MatchExpressions []LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

type LabelSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type NetworkPolicyRule struct {
	From  []NetworkPolicyPeer `json:"from,omitempty"`
	To    []NetworkPolicyPeer `json:"to,omitempty"`
	Ports []NetworkPolicyPort `json:"ports,omitempty"`
}

type NetworkPolicyPeer struct {
	PodSelector       *LabelSelector `json:"podSelector,omitempty"`
	NamespaceSelector *LabelSelector `json:"namespaceSelector,omitempty"`
	IPBlock           *IPBlock       `json:"ipBlock,omitempty"`
}

type IPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except,omitempty"`
}

type NetworkPolicyPort struct {
	Protocol string      `json:"protocol,omitempty"`
	Port     interface{} `json:"port,omitempty"`
}

// --- Shared types ---

type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	CreationTimestamp  string            `json:"creationTimestamp,omitempty"`
}

// --- API methods ---

func (c *Client) GetNodes() ([]Node, error) {
	data, err := c.get("/api/v1/nodes")
	if err != nil {
		return nil, err
	}
	var list NodeList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing nodes: %w", err)
	}
	return list.Items, nil
}

func (c *Client) GetAllPods(labelSelector string) ([]Pod, error) {
	path := "/api/v1/pods"
	if labelSelector != "" {
		path += "?labelSelector=" + url.QueryEscape(labelSelector)
	}
	data, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var list PodList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing pods: %w", err)
	}
	return list.Items, nil
}

func (c *Client) GetPodsInNamespace(namespace, labelSelector string) ([]Pod, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace)
	if labelSelector != "" {
		path += "?labelSelector=" + url.QueryEscape(labelSelector)
	}
	data, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var list PodList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing pods: %w", err)
	}
	return list.Items, nil
}

func (c *Client) GetInferenceServices() ([]InferenceService, error) {
	data, err := c.get("/apis/serving.kserve.io/v1beta1/inferenceservices")
	if err != nil {
		return nil, err
	}
	var list InferenceServiceList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing inference services: %w", err)
	}
	return list.Items, nil
}

func (c *Client) GetNetworkPolicies(namespace string) ([]NetworkPolicy, error) {
	path := fmt.Sprintf("/apis/networking.k8s.io/v1/namespaces/%s/networkpolicies", namespace)
	data, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var list NetworkPolicyList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing network policies: %w", err)
	}
	return list.Items, nil
}

func (c *Client) GetAllNetworkPolicies() ([]NetworkPolicy, error) {
	data, err := c.get("/apis/networking.k8s.io/v1/networkpolicies")
	if err != nil {
		return nil, err
	}
	var list NetworkPolicyList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing network policies: %w", err)
	}
	return list.Items, nil
}

// GetUserGroups returns the OpenShift groups a user belongs to.
func (c *Client) GetUserGroups(username string) ([]string, error) {
	data, err := c.get("/apis/user.openshift.io/v1/groups")
	if err != nil {
		return nil, fmt.Errorf("querying groups: %w", err)
	}

	var raw struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Users []string `json:"users"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing groups: %w", err)
	}

	var groups []string
	for _, g := range raw.Items {
		for _, u := range g.Users {
			if u == username {
				groups = append(groups, g.Metadata.Name)
				break
			}
		}
	}
	return groups, nil
}

// GetServingRuntimes returns serving runtimes across all namespaces
// GetConsoleURL returns the OpenShift web console URL.
func (c *Client) GetConsoleURL() string {
	data, err := c.get("/apis/config.openshift.io/v1/consoles/cluster")
	if err != nil {
		return ""
	}
	var obj struct {
		Status struct {
			ConsoleURL string `json:"consoleURL"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	return obj.Status.ConsoleURL
}

// GetRHOAIDashboardURL returns the OpenShift AI (RHOAI) dashboard URL by looking for its route.
func (c *Client) GetRHOAIDashboardURL() string {
	// Try known namespaces for the data science gateway route
	for _, ns := range []string{"openshift-ingress", "redhat-ods-applications", "rhods-dashboard"} {
		data, err := c.get(fmt.Sprintf("/apis/route.openshift.io/v1/namespaces/%s/routes", ns))
		if err != nil {
			continue
		}
		var raw struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Host string `json:"host"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		for _, r := range raw.Items {
			if r.Metadata.Name == "data-science-gateway" || r.Metadata.Name == "rhods-dashboard" {
				if r.Spec.Host != "" {
					return "https://" + r.Spec.Host
				}
			}
		}
	}
	return ""
}

// CreatePod creates a pod in the given namespace.
func (c *Client) CreatePod(namespace string, podJSON []byte) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace)
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(podJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("create pod request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("create pod returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

// DeletePod deletes a pod by name in the given namespace.
func (c *Client) DeletePod(namespace, name string) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", namespace, name)
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete pod request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil // already gone
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("delete pod returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

// GetPod returns a single pod by name.
func (c *Client) GetPod(namespace, name string) (*Pod, error) {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", namespace, name)
	data, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var pod Pod
	if err := json.Unmarshal(data, &pod); err != nil {
		return nil, fmt.Errorf("parsing pod: %w", err)
	}
	return &pod, nil
}

// IsClusterAdmin checks if a user has the cluster-admin ClusterRole via any ClusterRoleBinding.
func (c *Client) IsClusterAdmin(username string) bool {
	data, err := c.get("/apis/rbac.authorization.k8s.io/v1/clusterrolebindings")
	if err != nil {
		return false
	}
	var raw struct {
		Items []struct {
			RoleRef struct {
				Name string `json:"name"`
			} `json:"roleRef"`
			Subjects []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"subjects"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	for _, b := range raw.Items {
		if b.RoleRef.Name != "cluster-admin" {
			continue
		}
		for _, s := range b.Subjects {
			if s.Kind == "User" && s.Name == username {
				return true
			}
		}
	}
	return false
}

// IsUserWorkloadMonitoringEnabled checks if user workload monitoring is enabled
// by inspecting the cluster-monitoring-config ConfigMap in openshift-monitoring.
func (c *Client) IsUserWorkloadMonitoringEnabled() bool {
	data, err := c.get("/api/v1/namespaces/openshift-monitoring/configmaps/cluster-monitoring-config")
	if err != nil {
		return false
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &cm); err != nil {
		return false
	}
	configYAML := cm.Data["config.yaml"]
	// Simple check: look for enableUserWorkload: true in the config
	return strings.Contains(configYAML, "enableUserWorkload: true")
}

// Ping checks if the API server is reachable.
func (c *Client) Ping() error {
	_, err := c.get("/api/v1/namespaces/default")
	return err
}

func (c *Client) GetServingRuntimes() ([]json.RawMessage, error) {
	data, err := c.get("/apis/serving.kserve.io/v1alpha1/servingruntimes")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing serving runtimes: %w", err)
	}
	return raw.Items, nil
}

func (c *Client) patch(path string, body []byte, contentType string) ([]byte, error) {
	req, err := http.NewRequest("PATCH", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("patch request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("reading patch response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("patch returned %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return respBody, nil
}

func (c *Client) post(path string, body []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("reading post response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("post returned %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return respBody, nil
}

func (c *Client) deleteResource(path string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("delete returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func (c *Client) getWithStatus(path string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func (c *Client) PatchNodeLabels(nodeName string, labels map[string]string) error {
	patch := struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}{}
	patch.Metadata.Labels = labels
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.patch(fmt.Sprintf("/api/v1/nodes/%s", nodeName), body, "application/strategic-merge-patch+json")
	return err
}

func (c *Client) CordonNode(nodeName string) error {
	body := []byte(`{"spec":{"unschedulable":true}}`)
	_, err := c.patch(fmt.Sprintf("/api/v1/nodes/%s", nodeName), body, "application/strategic-merge-patch+json")
	return err
}

func (c *Client) UncordonNode(nodeName string) error {
	body := []byte(`{"spec":{"unschedulable":false}}`)
	_, err := c.patch(fmt.Sprintf("/api/v1/nodes/%s", nodeName), body, "application/strategic-merge-patch+json")
	return err
}

func (c *Client) CreateConfigMap(namespace, name string, data map[string]string) error {
	cm := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}{}
	cm.APIVersion = "v1"
	cm.Kind = "ConfigMap"
	cm.Metadata.Name = name
	cm.Metadata.Namespace = namespace
	cm.Data = data
	body, err := json.Marshal(cm)
	if err != nil {
		return err
	}
	_, err = c.post(fmt.Sprintf("/api/v1/namespaces/%s/configmaps", namespace), body)
	return err
}

func (c *Client) PatchConfigMap(namespace, name string, data map[string]string) error {
	patch := struct {
		Data map[string]string `json:"data"`
	}{Data: data}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.patch(fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", namespace, name), body, "application/strategic-merge-patch+json")
	return err
}

func (c *Client) GetConfigMap(namespace, name string) (map[string]string, error) {
	data, err := c.get(fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", namespace, name))
	if err != nil {
		return nil, err
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &cm); err != nil {
		return nil, fmt.Errorf("parsing configmap: %w", err)
	}
	return cm.Data, nil
}

func (c *Client) CRDExists(crdName string) (bool, error) {
	_, statusCode, err := c.getWithStatus(fmt.Sprintf("/apis/apiextensions.k8s.io/v1/customresourcedefinitions/%s", crdName))
	if err != nil {
		return false, err
	}
	return statusCode == 200, nil
}

func (c *Client) PatchCustomResource(path string, patchBody []byte) ([]byte, error) {
	return c.patch(path, patchBody, "application/merge-patch+json")
}

func (c *Client) EvictPod(namespace, name string) error {
	eviction := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}{}
	eviction.APIVersion = "policy/v1"
	eviction.Kind = "Eviction"
	eviction.Metadata.Name = name
	eviction.Metadata.Namespace = namespace
	body, err := json.Marshal(eviction)
	if err != nil {
		return err
	}
	_, err = c.post(fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/eviction", namespace, name), body)
	return err
}

func (c *Client) CreateJob(namespace, name, image string, command []string, resourceRequests map[string]string) error {
	type containerSpec struct {
		Name      string                `json:"name"`
		Image     string                `json:"image"`
		Command   []string              `json:"command"`
		Resources *ContainerResources   `json:"resources,omitempty"`
	}
	job := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			BackoffLimit int `json:"backoffLimit"`
			Template     struct {
				Spec struct {
					Containers    []containerSpec `json:"containers"`
					RestartPolicy string          `json:"restartPolicy"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}{}
	job.APIVersion = "batch/v1"
	job.Kind = "Job"
	job.Metadata.Name = name
	job.Metadata.Namespace = namespace
	job.Spec.BackoffLimit = 0
	job.Spec.Template.Spec.RestartPolicy = "Never"
	cs := containerSpec{
		Name:    "worker",
		Image:   image,
		Command: command,
	}
	if len(resourceRequests) > 0 {
		cs.Resources = &ContainerResources{
			Requests: resourceRequests,
			Limits:   resourceRequests,
		}
	}
	job.Spec.Template.Spec.Containers = []containerSpec{cs}
	body, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = c.post(fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", namespace), body)
	return err
}

func (c *Client) DeleteJob(namespace, name string) error {
	return c.deleteResource(fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", namespace, name))
}

func (c *Client) GetPodsOnNode(nodeName string) ([]Pod, error) {
	path := fmt.Sprintf("/api/v1/pods?fieldSelector=%s", url.QueryEscape("spec.nodeName="+nodeName))
	data, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var list PodList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing pods: %w", err)
	}
	return list.Items, nil
}

func (c *Client) AddNodeTaint(nodeName, key, value, effect string) error {
	data, err := c.get(fmt.Sprintf("/api/v1/nodes/%s", nodeName))
	if err != nil {
		return fmt.Errorf("getting node for taint: %w", err)
	}
	var node Node
	if err := json.Unmarshal(data, &node); err != nil {
		return fmt.Errorf("parsing node: %w", err)
	}
	taints := node.Spec.Taints
	for _, t := range taints {
		if t.Key == key && t.Effect == effect {
			return nil
		}
	}
	taints = append(taints, Taint{Key: key, Value: value, Effect: effect})
	patch := struct {
		Spec struct {
			Taints []Taint `json:"taints"`
		} `json:"spec"`
	}{}
	patch.Spec.Taints = taints
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.patch(fmt.Sprintf("/api/v1/nodes/%s", nodeName), body, "application/strategic-merge-patch+json")
	return err
}

func (c *Client) RemoveNodeTaint(nodeName, key string) error {
	data, err := c.get(fmt.Sprintf("/api/v1/nodes/%s", nodeName))
	if err != nil {
		return fmt.Errorf("getting node for taint removal: %w", err)
	}
	var node Node
	if err := json.Unmarshal(data, &node); err != nil {
		return fmt.Errorf("parsing node: %w", err)
	}
	var filtered []Taint
	for _, t := range node.Spec.Taints {
		if t.Key != key {
			filtered = append(filtered, t)
		}
	}
	patch := struct {
		Spec struct {
			Taints []Taint `json:"taints"`
		} `json:"spec"`
	}{}
	patch.Spec.Taints = filtered
	if patch.Spec.Taints == nil {
		patch.Spec.Taints = []Taint{}
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.patch(fmt.Sprintf("/api/v1/nodes/%s", nodeName), body, "application/strategic-merge-patch+json")
	return err
}

func (c *Client) CreateNamespace(name string) error {
	ns := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}{}
	ns.APIVersion = "v1"
	ns.Kind = "Namespace"
	ns.Metadata.Name = name
	body, err := json.Marshal(ns)
	if err != nil {
		return err
	}
	_, err = c.post("/api/v1/namespaces", body)
	return err
}

func (c *Client) CreateSubscription(namespace, packageName, channel, source, sourceNamespace string) error {
	sub := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			Channel             string `json:"channel"`
			Name                string `json:"name"`
			Source              string `json:"source"`
			SourceNamespace     string `json:"sourceNamespace"`
			InstallPlanApproval string `json:"installPlanApproval"`
		} `json:"spec"`
	}{}
	sub.APIVersion = "operators.coreos.com/v1alpha1"
	sub.Kind = "Subscription"
	sub.Metadata.Name = packageName
	sub.Metadata.Namespace = namespace
	sub.Spec.Channel = channel
	sub.Spec.Name = packageName
	sub.Spec.Source = source
	sub.Spec.SourceNamespace = sourceNamespace
	sub.Spec.InstallPlanApproval = "Automatic"
	body, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	_, err = c.post(fmt.Sprintf("/apis/operators.coreos.com/v1alpha1/namespaces/%s/subscriptions", namespace), body)
	return err
}

func (c *Client) CreateOperatorGroup(namespace, name string) error {
	og := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			TargetNamespaces []string `json:"targetNamespaces"`
		} `json:"spec"`
	}{}
	og.APIVersion = "operators.coreos.com/v1"
	og.Kind = "OperatorGroup"
	og.Metadata.Name = name
	og.Metadata.Namespace = namespace
	og.Spec.TargetNamespaces = []string{namespace}
	body, err := json.Marshal(og)
	if err != nil {
		return err
	}
	_, err = c.post(fmt.Sprintf("/apis/operators.coreos.com/v1/namespaces/%s/operatorgroups", namespace), body)
	return err
}

func (c *Client) GetCustomResource(path string) ([]byte, error) {
	return c.get(path)
}

func (c *Client) CreateCustomResource(path string, body []byte) error {
	_, err := c.post(path, body)
	return err
}
