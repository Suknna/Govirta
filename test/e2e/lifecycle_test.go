//go:build e2e

package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resourceLifecycle declares one resource's standard lifecycle with optional
// guest-side live assertion hooks. The skeleton (apply→waitReady, delete→
// waitGone) is reused; afterReady/afterGone are the declarative insertion points
// for live verification (上下一致: assert lower-layer reality, not just API).
type resourceLifecycle struct {
	manifest  string // file name under the manifests dir, e.g. "08-snapshot.json"
	kind      string // "Snapshot"
	name      string // "snap-e2e"
	waitPhase string // "ready"
	waitFor   time.Duration

	afterReady func(ctx context.Context) // runs after the object reaches waitPhase
	afterGone  func(ctx context.Context) // runs after the object reaches 404
}

// applyAndVerify applies the manifest, waits for waitPhase, then runs afterReady.
func applyAndVerify(ctx context.Context, t *testing.T, ctl, server, manifests string, spec resourceLifecycle) {
	t.Helper()
	path := filepath.Join(manifests, spec.manifest)
	out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
	if err != nil {
		t.Fatalf("apply %s failed: %v\noutput:\n%s", spec.manifest, err, out)
	}
	t.Logf("applied %s: %s", spec.manifest, strings.TrimSpace(out))
	waitObjectPhase(ctx, t, ctl, server, spec.kind, spec.name, spec.waitPhase, spec.waitFor)
	if spec.afterReady != nil {
		spec.afterReady(ctx)
	}
}

// deleteAndVerify deletes the object, waits for 404, then runs afterGone.
func deleteAndVerify(ctx context.Context, t *testing.T, ctl, server string, spec resourceLifecycle) {
	t.Helper()
	deleteAndWaitGone(ctx, t, ctl, server, spec.kind, spec.name, spec.waitFor)
	if spec.afterGone != nil {
		spec.afterGone(ctx)
	}
}
