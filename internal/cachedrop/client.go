// Package cachedrop provides an HTTP client for the Anansi cache-drop
// DaemonSet, which exposes an endpoint to flush the Linux page cache
// on a target Kubernetes node.
package cachedrop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	daemonSetPort = 9999
	dropPath      = "/drop"
	healthPath    = "/health"
)

type Client struct {
	httpClient *http.Client
	k8sClient  kubernetes.Interface
	namespace  string

	// nodeIPCache maps node names to their internal IPs.
	// Populated lazily on first use.
	nodeIPs map[string]string
}

func NewClient(k8sClient kubernetes.Interface, namespace string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		k8sClient:  k8sClient,
		namespace:  namespace,
		nodeIPs:    make(map[string]string),
	}
}

// Drop flushes the Linux page cache on the specified Kubernetes node.
// This calls `sync && echo 3 > /proc/sys/vm/drop_caches` on the node
// via the DaemonSet pod running on that node.
func (c *Client) Drop(ctx context.Context, nodeName string) error {
	ip, err := c.resolveNodeIP(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("resolving node IP for %s: %w", nodeName, err)
	}

	url := fmt.Sprintf("http://%s:%d%s", ip, daemonSetPort, dropPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cache drop request to %s failed: %w", nodeName, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := strings.TrimSpace(string(body))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cache drop on %s returned %d: %s", nodeName, resp.StatusCode, bodyStr)
	}

	if bodyStr != "ok" {
		return fmt.Errorf("unexpected cache drop response from %s: %q", nodeName, bodyStr)
	}

	return nil
}

// HealthCheck verifies the cache-drop DaemonSet is reachable on a node.
func (c *Client) HealthCheck(ctx context.Context, nodeName string) error {
	ip, err := c.resolveNodeIP(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("resolving node IP for %s: %w", nodeName, err)
	}

	url := fmt.Sprintf("http://%s:%d%s", ip, daemonSetPort, healthPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check on %s failed: %w", nodeName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check on %s returned %d", nodeName, resp.StatusCode)
	}

	return nil
}

// HealthCheckAll verifies the DaemonSet is reachable on all nodes in the cluster.
// Returns a map of node name ->  error (nil = healthy).
func (c *Client) HealthCheckAll(ctx context.Context) (map[string]error, error) {
	nodes, err := c.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	results := make(map[string]error)
	for _, node := range nodes.Items {
		results[node.Name] = c.HealthCheck(ctx, node.Name)
	}

	return results, nil
}

// resolveNodeIP returns the internal IP of a Kubernetes node.
// Uses a cached mapping to avoid repeated API calls.
func (c *Client) resolveNodeIP(ctx context.Context, nodeName string) (string, error) {
	// Check cache
	if ip, ok := c.nodeIPs[nodeName]; ok {
		return ip, nil
	}

	// Fetch node
	node, err := c.k8sClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting node %s: %w", nodeName, err)
	}

	// Find InternalIP address
	for _, addr := range node.Status.Addresses {
		if addr.Type == "InternalIP" {
			c.nodeIPs[nodeName] = addr.Address
			return addr.Address, nil
		}
	}

	return "", fmt.Errorf("no InternalIP found for node %s", nodeName)
}
