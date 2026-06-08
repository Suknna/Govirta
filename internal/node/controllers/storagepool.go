// Package controllers holds govirtlet's per-kind reconcilers built on the node
// controller framework. Each controller decodes its own apis object from the
// raw watch event bytes, drives the local node services toward that desired
// state, and reports observed status back to the master.
package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage/local"
	"github.com/suknna/govirta/internal/storage/localfile"
	"github.com/suknna/govirta/internal/storage/pool"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
)

// errUnsupportedBackend marks a StoragePool spec whose backend has no mapping to
// a pool backend the node understands. It is a sentinel so callers can branch.
var errUnsupportedBackend = errors.New("storagepool controller: unsupported backend")

// errUnsupportedPoolType marks a StoragePool spec whose type has no mapping to a
// pool type the node understands.
var errUnsupportedPoolType = errors.New("storagepool controller: unsupported pool type")

// PoolRegistrar is the narrow slice of the storage pool service the controller
// needs: register a pool, read its live usage, and unregister it on teardown.
// *pool.Service satisfies it. UnregisterPool takes no ctx — it is a pure
// in-memory deregistration symmetric with RegisterPool (Task 1b).
type PoolRegistrar interface {
	RegisterPool(p *pool.Pool) error
	GetPoolUsage(ctx context.Context, name string) (pool.Usage, error)
	UnregisterPool(name string) error
}

// StatusReporter is the narrow slice of the master client the controller needs:
// PATCH an object's /status sub-resource with a raw JSON body, and remove a
// finalizer once live resources are torn down. *client.Client satisfies it.
type StatusReporter interface {
	PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error)
	FinalizerRemover
}

// Compile-time proof that the real production types satisfy the narrow
// interfaces the controller depends on (积木式 + 可测).
var (
	_ PoolRegistrar  = (*pool.Service)(nil)
	_ StatusReporter = (*client.Client)(nil)
)

// StoragePoolController reconciles StoragePool objects: it maps each spec into a
// pool.Pool, registers it with the local pool service (treating an already
// registered pool as an idempotent success), then reads live usage and reports
// a ready/failed status up to the master.
type StoragePoolController struct {
	pools  PoolRegistrar
	client StatusReporter
}

var _ controller.Controller = (*StoragePoolController)(nil)

// NewStoragePoolController wires a StoragePoolController against the pool service
// and the master status client.
func NewStoragePoolController(pools PoolRegistrar, client StatusReporter) *StoragePoolController {
	return &StoragePoolController{pools: pools, client: client}
}

// Kind is the apis kind this controller watches.
func (c *StoragePoolController) Kind() string {
	return string(metav1.KindStoragePool)
}

// Reconcile drives one StoragePool event toward its desired state.
//
// DELETED is a no-op in this slice (delete is a later cut). For ADDED/MODIFIED
// it decodes the object, maps the spec to a pool.Pool, registers it, reads live
// usage, and patches a ready status. A spec that cannot be mapped is a permanent
// failure: it is reported failed and not requeued. A registration or usage read
// failure is treated as transient: it is reported failed and requeued.
func (c *StoragePoolController) Reconcile(ctx context.Context, ev controller.Event) (controller.ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return controller.Done(), fmt.Errorf("storagepool controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("storagepool deleted; delete is a no-op in this slice")
		return controller.Done(), nil
	}

	var sp storagepoolv1.StoragePool
	if err := json.Unmarshal(ev.Object, &sp); err != nil {
		return controller.Done(), fmt.Errorf("storagepool controller: decode object %q: %w", ev.Key, err)
	}

	// Teardown branch: a deletion-stamped object means apiserver wants this pool
	// gone. Tear down the live registration before the ensure path runs. A
	// teardown failure keeps the finalizer (object stays "deleting") and requeues.
	if isDeleting(sp.ObjectMeta) {
		if err := c.teardown(sp); err != nil {
			return controller.Requeue(), fmt.Errorf("storagepool controller: teardown %q: %w", sp.Name, err)
		}
		if err := removeTeardownFinalizer(ctx, c.client, c.Kind(), sp.Name); err != nil {
			return controller.Requeue(), fmt.Errorf("storagepool controller: remove finalizer %q: %w", sp.Name, err)
		}
		return controller.Done(), nil
	}

	p, err := buildPool(sp)
	if err != nil {
		// A spec that does not map is a permanent failure: requeue cannot fix it.
		if perr := c.reportFailure(ctx, sp.Name, sp.Status, err); perr != nil {
			return controller.Done(), fmt.Errorf("storagepool controller: map spec %q failed and status report failed: %w", sp.Name, errors.Join(err, perr))
		}
		logger.Error().Err(err).Str("key", ev.Key).Msg("storagepool spec mapping failed")
		return controller.Done(), nil
	}

	if err := c.pools.RegisterPool(p); err != nil && !errors.Is(err, pool.ErrPoolAlreadyExists) {
		if perr := c.reportFailure(ctx, sp.Name, sp.Status, err); perr != nil {
			return controller.Requeue(), fmt.Errorf("storagepool controller: register pool %q failed and status report failed: %w", sp.Name, errors.Join(err, perr))
		}
		return controller.Requeue(), fmt.Errorf("storagepool controller: register pool %q: %w", sp.Name, err)
	}

	usage, err := c.pools.GetPoolUsage(ctx, sp.Name)
	if err != nil {
		if perr := c.reportFailure(ctx, sp.Name, sp.Status, err); perr != nil {
			return controller.Requeue(), fmt.Errorf("storagepool controller: read usage for %q failed and status report failed: %w", sp.Name, errors.Join(err, perr))
		}
		return controller.Requeue(), fmt.Errorf("storagepool controller: read usage for %q: %w", sp.Name, err)
	}

	status := storagepoolv1.StoragePoolStatus{
		Phase:          storagepoolv1.PoolPhaseReady,
		AllocatedBytes: usage.AllocatedBytes,
	}
	if err := c.patchStatus(ctx, sp.Name, sp.Status, status); err != nil {
		return controller.Requeue(), err
	}

	logger.Info().
		Str("key", ev.Key).
		Int64("allocatedBytes", usage.AllocatedBytes).
		Msg("storagepool ready")
	return controller.Done(), nil
}

// teardown unregisters the pool from the local pool service. Unregistering an
// already-gone pool (pool.ErrPoolNotFound) is treated as an idempotent success
// so a re-driven teardown still progresses to dropping the finalizer. A pool
// that still holds volumes/images (pool.ErrPoolNotEmpty) is a real conflict: the
// error is returned so the finalizer is kept and the reconcile requeued, letting
// the referencing volumes/images tear down first. UnregisterPool is a pure
// in-memory operation (symmetric with RegisterPool) and takes no ctx.
func (c *StoragePoolController) teardown(sp storagepoolv1.StoragePool) error {
	if err := c.pools.UnregisterPool(sp.Name); err != nil && !errors.Is(err, pool.ErrPoolNotFound) {
		return fmt.Errorf("storagepool controller: unregister pool %q: %w", sp.Name, err)
	}
	return nil
}

// reportFailure patches a failed status carrying cause's message, skipping the
// PATCH when the observed status already matches (no-op guard).
func (c *StoragePoolController) reportFailure(ctx context.Context, name string, observed storagepoolv1.StoragePoolStatus, cause error) error {
	return c.patchStatus(ctx, name, observed, storagepoolv1.StoragePoolStatus{
		Phase:   storagepoolv1.PoolPhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals desired and PATCHes it to the master's /status
// sub-resource, but only when it differs from observed (the status carried by the
// watched object). Skipping an identical PATCH breaks the status→MODIFIED→watch→
// reconcile→PATCH feedback loop that would otherwise spin every reconcile (level-
// triggered idempotence). The Status structs are comparable (scalar fields only),
// so == is a sound equality test.
func (c *StoragePoolController) patchStatus(ctx context.Context, name string, observed, desired storagepoolv1.StoragePoolStatus) error {
	if observed == desired {
		return nil
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("storagepool controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("storagepool controller: patch status for %q: %w", name, err)
	}
	return nil
}

// buildPool maps a StoragePool object into a pool.Pool, converting the backend
// and type enums explicitly (no blind string conversion) and attaching the
// matching host-local driver. pool.Service.RegisterPool requires a block pool to
// carry a block.Driver and a file pool to carry an image.Driver, so the driver
// is wired here rather than left nil. Backends with no host-local driver
// implementation in this phase (nfs-block / rbd-block) are a permanent config
// failure: the node cannot serve them yet.
func buildPool(sp storagepoolv1.StoragePool) (*pool.Pool, error) {
	backend, err := mapBackend(sp.Spec.Backend)
	if err != nil {
		return nil, err
	}
	poolType, err := mapPoolType(sp.Spec.Type)
	if err != nil {
		return nil, err
	}

	p := &pool.Pool{
		Config: pool.Config{
			Name:          sp.Name,
			Type:          poolType,
			Backend:       backend,
			StorageRoot:   sp.Spec.StorageRoot,
			CapacityBytes: sp.Spec.CapacityBytes,
		},
	}

	// Attach the host-local driver the pool type requires. Only local backends
	// have a driver implementation in this phase; nfs/rbd are rejected as
	// permanent config errors rather than registered without a working driver.
	switch backend {
	case pool.BackendLocalBlock:
		drv, derr := local.NewDriver(local.Config{
			PoolName:    sp.Name,
			StorageRoot: sp.Spec.StorageRoot,
		})
		if derr != nil {
			return nil, fmt.Errorf("%w: build local block driver for %q: %v", errUnsupportedBackend, sp.Name, derr)
		}
		p.Driver = drv
	case pool.BackendLocalFile:
		drv, derr := localfile.NewDriver(localfile.Config{
			PoolName:    sp.Name,
			StorageRoot: sp.Spec.StorageRoot,
		})
		if derr != nil {
			return nil, fmt.Errorf("%w: build local file driver for %q: %v", errUnsupportedBackend, sp.Name, derr)
		}
		p.ImageDriver = drv
	default:
		// nfs-block / rbd-block: no host-local driver implementation yet.
		return nil, fmt.Errorf("%w: %q has no host-local driver implementation", errUnsupportedBackend, backend)
	}

	return p, nil
}

// mapBackend converts an apis backend type to the storage pool backend type.
func mapBackend(b storagepoolv1.BackendType) (pool.BackendType, error) {
	switch b {
	case storagepoolv1.BackendLocalBlock:
		return pool.BackendLocalBlock, nil
	case storagepoolv1.BackendLocalFile:
		return pool.BackendLocalFile, nil
	case storagepoolv1.BackendNFSBlock:
		return pool.BackendNFSBlock, nil
	case storagepoolv1.BackendRBDBlock:
		return pool.BackendRBDBlock, nil
	default:
		return "", fmt.Errorf("%w: %q", errUnsupportedBackend, b)
	}
}

// mapPoolType converts an apis pool type to the storage pool type.
func mapPoolType(t storagepoolv1.PoolType) (pool.PoolType, error) {
	switch t {
	case storagepoolv1.PoolTypeBlock:
		return pool.PoolTypeBlock, nil
	case storagepoolv1.PoolTypeFile:
		return pool.PoolTypeFile, nil
	default:
		return "", fmt.Errorf("%w: %q", errUnsupportedPoolType, t)
	}
}
