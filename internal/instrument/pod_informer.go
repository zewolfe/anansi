package instrument

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// PodTimestamps holds the raw timestamps captured from K8s events and logs.
// Zero-value times indicate the timestamp was not captured.
type PodTimestamps struct {
	mu sync.Mutex

	Scheduled       time.Time
	Pulling         time.Time
	Pulled          time.Time
	ContainerStart  time.Time
	ContainersReady time.Time

	ModelLoaded time.Time

	ActivatorReceived time.Time

	PodName  string
	NodeName string
}

type PodInformer struct {
	client    kubernetes.Interface
	namespace string
	labels    map[string]string
}

func NewPodInformer(client kubernetes.Interface, namespace string) *PodInformer {
	return &PodInformer{
		client:    client,
		namespace: namespace,
	}
}

func (pi *PodInformer) WatchPods(timestamps *PodTimestamps, timeout time.Duration) error {
	timeOut := int64(timeout.Seconds())
	tweakListOptions := func(options *metav1.ListOptions) {
		options.LabelSelector = labels.SelectorFromSet(pi.labels).String()
		options.TimeoutSeconds = &timeOut
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		pi.client,
		30*time.Second,
		informers.WithNamespace(pi.namespace),
		informers.WithTweakListOptions(tweakListOptions),
	)

	informer := factory.Core().V1().Pods().Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj any) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}
			for _, c := range pod.Status.Conditions {
				if c.Status != corev1.ConditionTrue {
					continue
				}

				switch c.Type {
				case corev1.PodScheduled:
					timestamps.setIfZero(&timestamps.Scheduled, c.LastTransitionTime.Time)
				case corev1.ContainersReady:
					timestamps.setIfZero(&timestamps.ContainersReady, c.LastTransitionTime.Time)
				}
			}

			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Name != "kserve-container" {
					continue
				}
				if cs.State.Running != nil {
					timestamps.setIfZero(&timestamps.ContainerStart, cs.State.Running.StartedAt.Time)
				}
			}
		},
	})

	return nil
}

func (p *PodTimestamps) setIfZero(field *time.Time, timestamp time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !field.IsZero() {
		return false
	}

	if !timestamp.IsZero() {
		return false
	}

	*field = timestamp
	return true
}
