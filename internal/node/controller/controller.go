// Package controller is govirtlet's self-built controller-manager framework. It
// is a lightweight watch → workqueue → reconcile → status loop, deliberately not
// k8s client-go: it binds to the project's own master HTTP watch contract and
// pulls in no heavy dependencies. The framework is kind-agnostic; each Controller
// decodes its own apis object from the raw event bytes.
package controller

import (
	"context"
	"time"
)

// EventType is the kind of change observed on a watched resource. State-machine
// discriminator, so it is a dedicated type with named constants (项目铁律).
type EventType string

const (
	// EventAdded means the object was created.
	EventAdded EventType = "ADDED"
	// EventModified means the object was updated.
	EventModified EventType = "MODIFIED"
	// EventDeleted means the object was removed.
	EventDeleted EventType = "DELETED"
)

// Event is one resource change delivered to a Controller. Object is the resource
// object's raw JSON (the framework never parses it); Key is the dedup identity
// (the object's name within its kind).
type Event struct {
	Type EventType
	Key  string
	// ResourceVersion is the watched object's metadata.resourceVersion, filled by
	// the watch EventSource when translating a wire event. The manager's feeder
	// records it as the resume cursor (startRevision) so a reconnect can resume
	// after the last version seen rather than replaying from the beginning.
	ResourceVersion string
	Object          []byte
}

// ReconcileResult describes what the framework should do after one reconcile
// attempt. Requeue requests an immediate retry; RequeueAfter requests a delayed
// retry owned by the reconcile loop's context.
type ReconcileResult struct {
	Requeue      bool
	RequeueAfter time.Duration
}

// Done reports that no retry is needed.
func Done() ReconcileResult { return ReconcileResult{} }

// Requeue requests an immediate retry.
func Requeue() ReconcileResult { return ReconcileResult{Requeue: true} }

// RequeueAfter requests a delayed retry. Non-positive delays are immediate
// requeues so callers cannot accidentally express a no-op by passing zero.
func RequeueAfter(delay time.Duration) ReconcileResult {
	if delay <= 0 {
		return Requeue()
	}
	return ReconcileResult{RequeueAfter: delay}
}

// ShouldRequeue reports whether either immediate or delayed retry was requested.
func (r ReconcileResult) ShouldRequeue() bool {
	return r.Requeue || r.RequeueAfter > 0
}

// Controller reconciles one resource kind toward its spec. Kind names the apis
// kind this controller watches (used to build the watch URL). Reconcile drives
// one object toward its desired state and reports status; returning a result with
// requeue intent asks the loop to retry later (e.g. a dependency is not Ready
// yet). An error is logged and also triggers a requeue unless the result already
// declares a requeue policy.
type Controller interface {
	// Kind is the apis kind this controller watches, e.g. "VM".
	Kind() string
	// Reconcile processes one event. A result with requeue intent means retry
	// later (dependency not ready); err means the attempt failed and should be
	// retried unless the result already declares a policy.
	Reconcile(ctx context.Context, ev Event) (ReconcileResult, error)
}
