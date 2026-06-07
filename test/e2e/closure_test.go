//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	e2eEnabledEnv  = "GOVIRTA_E2E"
	e2eServerEnv   = "GOVIRTA_E2E_SERVER"
	e2eCtlEnv      = "GOVIRTA_E2E_GOVIRTCTL"
	e2eManifestEnv = "GOVIRTA_E2E_MANIFESTS"
)

// Object names the spine is built from. They MUST match metadata.name in the
// manifests under test/e2e/manifests; the teardown drives govirtctl with these
// exact names, so a drift here would delete the wrong object (or nothing).
const (
	vmName    = "vm-e2e"       // 07-vm.json
	nicName   = "nic-e2e"      // 06-nic.json
	netName   = "net-e2e"      // 05-network.json
	volName   = "vol-e2e-root" // 04-volume.json
	imageName = "image-cirros" // 03-image.json
	poolBlock = "pool-block"   // 01-storagepool-block.json
	poolFile  = "pool-file"    // 02-storagepool-file.json
)

// applyOrder is the dependency order the controllers gate on: pools first, then
// image (needs file pool), volume (needs block pool + image), network, NIC
// (needs network), VM (needs volume + NIC).
var applyOrder = []string{
	"01-storagepool-block.json",
	"02-storagepool-file.json",
	"03-image.json",
	"04-volume.json",
	"05-network.json",
	"06-nic.json",
	"07-vm.json",
}

// TestDistributedSpineClosure drives the full lifecycle against the real
// three-node topology: apply the spine in dependency order, wait for the VM to
// reach Running (forward closure), then tear the spine down in reverse
// dependency order and prove every object truly disappears (reverse closure).
//
// The reverse segment exercises the deletion lifecycle end to end: the
// apiserver's reference-protection (409 while still referenced), the
// finalizer two-phase (delete stamps deletionTimestamp, the node tears down the
// live resource, the finalizer is dropped, the apiserver truly removes the
// object → 404). Host-side proof that no live kernel/QEMU resources leak is the
// job of scripts/e2e.sh verify_no_orphans, which inspects the guest directly.
func TestDistributedSpineClosure(t *testing.T) {
	if os.Getenv(e2eEnabledEnv) != "1" {
		t.Skipf("set %s=1 (via scripts/e2e.sh) to run the e2e closure test", e2eEnabledEnv)
	}
	server := requireEnv(t, e2eServerEnv)
	ctl := requireEnv(t, e2eCtlEnv)
	manifests := requireEnv(t, e2eManifestEnv)

	// The forward apply + wait-Running can take minutes; the reverse teardown
	// adds a VM stop+delete plus six more delete→404 polls. Give the whole
	// lifecycle ample headroom so a slow-but-correct teardown is not failed.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// Forward segment: apply the spine and wait for the VM to reach Running.
	applySpine(ctx, t, ctl, server, manifests)
	waitVMRunning(ctx, t, ctl, server)

	// Reverse segment: tear the spine down and prove the deletion lifecycle.
	teardownSpine(ctx, t, ctl, server)
}

// applySpine applies every manifest in dependency order, failing the test on the
// first apply that the master rejects.
func applySpine(ctx context.Context, t *testing.T, ctl, server, manifests string) {
	t.Helper()
	for _, name := range applyOrder {
		path := filepath.Join(manifests, name)
		out, err := runCtl(ctx, ctl, "apply", "--server", server, "-f", path)
		if err != nil {
			t.Fatalf("apply %s failed: %v\noutput:\n%s", name, err, out)
		}
		t.Logf("applied %s: %s", name, strings.TrimSpace(out))
	}
}

// waitVMRunning polls the VM until it reports Running, failing the test if it
// does not reach Running before the deadline.
func waitVMRunning(ctx context.Context, t *testing.T, ctl, server string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before VM reached Running: %v\nlast get:\n%s", err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName)
		last = out
		if err == nil && strings.Contains(out, "phase: running") {
			t.Logf("VM %s reached Running:\n%s", vmName, out)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("VM %s did not reach Running before deadline\nlast get:\n%s", vmName, last)
}

// teardownSpine deletes the spine in reverse dependency order and proves the
// deletion lifecycle: referenced objects are refused (409), then each object is
// deleted and polled to 404. The order is the reverse of applyOrder because each
// object can only be deleted once the object referencing it is gone — VM first
// (nothing references it), then its NIC and Volume, then the Network and Image
// they reference, then the pools.
func teardownSpine(ctx context.Context, t *testing.T, ctl, server string) {
	t.Helper()

	// 1. Reference protection: while the VM is alive it pins its Volume, and the
	// Volume pins its block pool, so both deletions must be refused with a 409
	// that names the referencing object. This is the key safety semantic of the
	// deletion chain — proving it before tearing anything down.
	expectReferencedRejection(ctx, t, ctl, server, "Volume", volName)
	expectReferencedRejection(ctx, t, ctl, server, "StoragePool", poolBlock)

	// 2. Reverse-order delete with finalizer two-phase verification. The VM goes
	// first (Running→Stop→Stopped→Delete is the slowest step), then the leaf
	// references it pinned, then the resources those reference, then the pools.
	deleteVMAndWaitGone(ctx, t, ctl, server)
	deleteAndWaitGone(ctx, t, ctl, server, "NIC", nicName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "Volume", volName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "Network", netName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "Image", imageName, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "StoragePool", poolBlock, 2*time.Minute)
	deleteAndWaitGone(ctx, t, ctl, server, "StoragePool", poolFile, 2*time.Minute)
}

// expectReferencedRejection asserts that deleting kind/name is refused because it
// is still referenced: govirtctl must exit non-zero (the client maps the
// apiserver 409 to an error) and the diagnostic must carry the apiserver's
// "still referenced by" protection text.
func expectReferencedRejection(ctx context.Context, t *testing.T, ctl, server, kind, name string) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "delete", "--server", server, kind, name)
	if err == nil {
		t.Fatalf("delete %s/%s must be rejected while still referenced, but it was accepted:\n%s", kind, name, out)
	}
	if !strings.Contains(out, "still referenced by") {
		t.Fatalf("delete %s/%s rejection must name the referencing object (%q), got:\n%s", kind, name, "still referenced by", out)
	}
	t.Logf("%s/%s correctly refused while referenced (409): %s", kind, name, strings.TrimSpace(out))
}

// deleteVMAndWaitGone deletes the VM and proves the finalizer two-phase: the
// delete is accepted (202 → "deleting"), the object may linger in the deleting
// state (deletionTimestamp stamped, finalizer not yet dropped) while the node
// stops and deletes the live VM, and it finally disappears (404). The mid-state
// window can be short, so a 404 on the immediate read is also valid (fast
// teardown); what is asserted is that IF the object is still readable, it carries
// deletionTimestamp — never a fully-live object after an accepted delete.
func deleteVMAndWaitGone(ctx context.Context, t *testing.T, ctl, server string) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "delete", "--server", server, "VM", vmName)
	if err != nil {
		t.Fatalf("delete VM %s failed: %v\noutput:\n%s", vmName, err, out)
	}
	if !strings.Contains(out, "deleting") {
		t.Fatalf("delete VM %s should report acceptance (%q), got:\n%s", vmName, "deleting", out)
	}
	t.Logf("delete VM %s accepted: %s", vmName, strings.TrimSpace(out))

	if body, gerr := runCtl(ctx, ctl, "get", "--server", server, "VM", vmName); gerr == nil {
		if !strings.Contains(body, "deletionTimestamp") {
			t.Fatalf("VM %s lingering after delete must carry deletionTimestamp (finalizer two-phase), got:\n%s", vmName, body)
		}
		t.Logf("VM %s is in the deleting state (deletionTimestamp stamped, finalizer pending)", vmName)
	} else if strings.Contains(body, "not found") {
		t.Logf("VM %s already fully removed before the mid-state read (fast teardown)", vmName)
	} else {
		t.Fatalf("VM %s get after delete failed but was not a 404 (%q) — a transient error must not be mistaken for fast teardown: %v\noutput:\n%s", vmName, "not found", gerr, body)
	}

	// The VM controller stops a Running VM before deleting it
	// (Running→Stop→Stopped→Delete), then drops the finalizer so the apiserver
	// truly removes the object. Give that real teardown ample time.
	waitGone(ctx, t, ctl, server, "VM", vmName, 4*time.Minute)
}

// deleteAndWaitGone deletes kind/name, asserts the master accepted it (202 →
// "deleting"), then polls until the object is gone (404). It is used for every
// non-VM object, whose teardown is a live-resource delete plus finalizer drop.
func deleteAndWaitGone(ctx context.Context, t *testing.T, ctl, server, kind, name string, timeout time.Duration) {
	t.Helper()
	out, err := runCtl(ctx, ctl, "delete", "--server", server, kind, name)
	if err != nil {
		t.Fatalf("delete %s/%s failed: %v\noutput:\n%s", kind, name, err, out)
	}
	if !strings.Contains(out, "deleting") {
		t.Fatalf("delete %s/%s should report acceptance (%q), got:\n%s", kind, name, "deleting", out)
	}
	t.Logf("delete %s/%s accepted: %s", kind, name, strings.TrimSpace(out))
	waitGone(ctx, t, ctl, server, kind, name, timeout)
}

// waitGone polls get kind/name until the master returns not-found (404), failing
// the test if it is still present after timeout. A 404 is recognised by govirtctl
// exiting non-zero AND the diagnostic carrying "not found"; a transient error
// without that text is not treated as gone, so a flaky connection cannot be
// mistaken for a successful delete.
func waitGone(ctx context.Context, t *testing.T, ctl, server, kind, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("context ended before %s/%s disappeared: %v\nlast get:\n%s", kind, name, err, last)
		}
		out, err := runCtl(ctx, ctl, "get", "--server", server, kind, name)
		last = out
		if err != nil && strings.Contains(out, "not found") {
			t.Logf("%s/%s is gone (404)", kind, name)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%s/%s still present after %s\nlast get:\n%s", kind, name, timeout, last)
}

func runCtl(ctx context.Context, ctl string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, ctl, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required when %s=1", name, e2eEnabledEnv)
	}
	return v
}
