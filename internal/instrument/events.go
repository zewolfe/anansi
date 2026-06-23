// Package instrument provides timing instrumentation for cold-start trials
// by watching Kubernetes events and parsing runtime container logs.
package instrument

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// EventWatcher watches Kubernetes pod events in a namespace and captures
// scheduling (pulling and pulled) timestamps for cold-start instrumentation.
type EventWatcher struct {
	client    kubernetes.Interface
	namespace string
}

func NewEventWatcher(client kubernetes.Interface, namespace string) *EventWatcher {
	return &EventWatcher{
		client:    client,
		namespace: namespace,
	}
}

// WatchPodEvents starts watching for pod lifecycle events matching the given
// label selector. It writes captured timestamps to the provided PodTimestamps.
// The function blocks until the context is cancelled, all expected timestamps
// are captured, or the timeout expires.
//
// labelSelector example: "serving.kserve.io/inferenceservice=my-model"
func (ew *EventWatcher) WatchPodEvents(
	ctx context.Context,
	labelSelector string,
	timestamps *PodTimestamps,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher, err := ew.client.CoreV1().Events(ew.namespace).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("starting event watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case evt, ok := <-watcher.ResultChan():
			if !ok {
				return nil
			}
			ew.processEvent(evt, timestamps)
		}
	}
}

// processEvent extracts timestamps from K8s Event objects.
func (ew *EventWatcher) processEvent(
	evt watch.Event,
	timestamps *PodTimestamps,
) {
	if evt.Type != watch.Added && evt.Type != watch.Modified {
		return
	}

	event, ok := evt.Object.(*corev1.Event)
	if !ok {
		return
	}

	if event.InvolvedObject.Kind != "Pod" {
		return
	}

	eventTime := bestEventTime(event)
	if eventTime.IsZero() {
		return
	}

	timestamps.mu.Lock()
	defer timestamps.mu.Unlock()

	switch event.Reason {
	case "Pulling":
		if timestamps.Pulling.IsZero() {
			timestamps.Pulling = eventTime
		}

	case "Pulled":
		if timestamps.Pulled.IsZero() {
			timestamps.Pulled = eventTime
		}
	}
}

// hasEssentialTimestamps returns true when we have enough for decomposition.
// We need at minimum: Scheduled, ContainerStart, ContainersReady.
// ModelLoaded comes from the log parser, not the event watcher.
func (ts *PodTimestamps) hasEssentialTimestamps() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return !ts.Scheduled.IsZero() &&
		!ts.ContainerStart.IsZero() &&
		!ts.ContainersReady.IsZero()
}

// bestEventTime returns the most precise timestamp available for an event.
// K8s events have multiple timestamp fields; prefer the most recent/precise.
func bestEventTime(event *corev1.Event) time.Time {
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	// FirstTimestamp is the classic field
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time
	}
	// LastTimestamp as fallback
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	return time.Time{}
}

// extractNodeFromScheduledMessage parses the node name from a Scheduled event message.
// Format: "Successfully assigned <namespace>/<pod> to <node-name>"
func extractNodeFromScheduledMessage(msg string) string {
	const marker = " to "
	idx := len(msg)
	for i := len(msg) - len(marker); i >= 0; i-- {
		if msg[i:i+len(marker)] == marker {
			idx = i + len(marker)
			break
		}
	}
	if idx < len(msg) {
		return msg[idx:]
	}
	return ""
}
