package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var isvcGVR = schema.GroupVersionResource{
	Group:    "serving.kserve.io",
	Version:  "v1beta1",
	Resource: "inferenceservices",
}

func InferenceServiceClusterURL(
	ctx context.Context,
	client dynamic.Interface,
	namespace string,
	name string,
) (string, error) {
	obj, err := client.Resource(isvcGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get InferenceService: %w", err)
	}

	if url, found, _ := unstructured.NestedString(obj.Object, "status", "address", "url"); found && url != "" {
		return url, nil
	}
	if url, found, _ := unstructured.NestedString(obj.Object, "status", "url"); found && url != "" {
		return url, nil
	}

	fmt.Println("URL not found in InferenceService status")

	return "", nil
}
