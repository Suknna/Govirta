# Storage Pool Block Volume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Govirta's internal storage package with explicit block pools, OpenStack-style `VolumeService`, pool-level capacity accounting, and a local qcow2 `BlockDriver`.

**Architecture:** `internal/storage` root exposes `VolumeService`; `internal/storage/pool` owns pool registration, usage, capacity admission, and per-pool in-memory volume index; `internal/storage/block` defines the backend driver interface; `internal/storage/volume` defines volume and attachment data contracts; `internal/storage/local` implements a qcow2 block driver over `internal/virt/qemuimg`. VM orchestration imports only `internal/storage`; VM runtime later converts `volume.Attachment` to QEMU config.

**Tech Stack:** Go 1.26, standard library `context` / `errors` / `path/filepath` / `sync` / `syscall`, existing `internal/virt/qemuimg` client, table-driven Go tests.

---

## File Structure

Create and modify these files:

- Delete: `internal/store/store.go`
- Delete: `internal/store/store_test.go`
- Create: `internal/storage/errors.go` — root re-export helpers if needed by callers.
- Create: `internal/storage/service.go` — `VolumeService` VM-facing API.
- Create: `internal/storage/service_test.go` — root service behavior tests.
- Create: `internal/storage/volume/volume.go` — `Volume`, `Spec`, `PublishedVolume`, roles, states.
- Create: `internal/storage/volume/attachment.go` — `Attachment`, kind, format.
- Create: `internal/storage/volume/errors.go` — volume error sentinels.
- Create: `internal/storage/block/driver.go` — `BlockDriver` interface and driver requests.
- Create: `internal/storage/pool/pool.go` — pool types, config, usage, capacity helpers.
- Create: `internal/storage/pool/service.go` — `PoolService` and pool operations.
- Create: `internal/storage/pool/errors.go` — pool error sentinels.
- Create: `internal/storage/pool/service_test.go` — pool registration, usage, capacity, ctx tests.
- Create: `internal/storage/local/driver.go` — local qcow2 driver.
- Create: `internal/storage/local/driver_test.go` — local driver qemu-img/path/ctx/error tests.

Implementation dependency order:

```text
volume -> block -> pool -> storage root service -> local driver -> full verification
```

This avoids Go import cycles:

```text
storage imports pool + volume
pool imports block + volume
block imports volume
volume imports only stdlib
local imports block + volume + qemuimg
```

---

### Task 1: Remove old store and add volume data contracts

**Files:**
- Delete: `internal/store/store.go`
- Delete: `internal/store/store_test.go`
- Create: `internal/storage/volume/errors.go`
- Create: `internal/storage/volume/attachment.go`
- Create: `internal/storage/volume/volume.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: remove obsolete `internal/store` and define standalone volume data contracts with no imports from pool, block, qemu, or qemuimg.

Acceptance evidence:
- `go test ./internal/storage/volume` passes.
- `go test ./...` does not report imports of `internal/store`.
- `grep -R "internal/store\|package store" internal cmd` returns no production references.

- [ ] **Step 2: Delete obsolete store package**

Remove these files:

```text
internal/store/store.go
internal/store/store_test.go
```

No compatibility layer is allowed.

- [ ] **Step 3: Create volume errors**

Create `internal/storage/volume/errors.go`:

```go
package volume

import "errors"

var (
    ErrInvalidRequest      = errors.New("invalid storage request")
    ErrUnsupported         = errors.New("storage operation unsupported")
    ErrVolumeNotFound      = errors.New("volume not found")
    ErrVolumeAlreadyExists = errors.New("volume already exists")
    ErrVolumeConflict      = errors.New("volume conflict")
    ErrVolumeInUse         = errors.New("volume in use")
    ErrVolumeNotPublished  = errors.New("volume not published")
)
```

- [ ] **Step 4: Create attachment contract**

Create `internal/storage/volume/attachment.go`:

```go
package volume

type AttachmentKind string

const (
    AttachmentFile        AttachmentKind = "file"
    AttachmentBlockDevice AttachmentKind = "blockDevice"
    AttachmentDirectory   AttachmentKind = "directory"
    AttachmentNetwork     AttachmentKind = "network"
)

type DiskFormat string

const (
    DiskFormatQCOW2 DiskFormat = "qcow2"
    DiskFormatRaw   DiskFormat = "raw"
)

type Attachment struct {
    Kind       AttachmentKind
    Format     DiskFormat
    Path       string
    ReadOnly   bool
    Attributes map[string]string
}
```

- [ ] **Step 5: Create volume model**

Create `internal/storage/volume/volume.go`:

```go
package volume

type ID string

type State string

const (
    StateAvailable State = "available"
    StatePublished State = "published"
)

type Role string
const (
    RoleRoot Role = "root"
    RoleData Role = "data"
)

type Spec struct {
    Name          string
    Role          Role
    DiskIndex     int
    CapacityBytes int64
    ReadOnly      bool
}

type Volume struct {
    ID            ID
    Name          string
    VMID          string
    VMName        string
    PoolName      string
    Role          Role
    DiskIndex     int
    Backend       string
    CapacityBytes int64
    State         State
    Attachment    *AttachmentState
    Context       map[string]string
}

type AttachmentState struct {
    VMID       string
    ReadOnly   bool
    Attachment Attachment
}

type PublishedVolume struct {
    VolumeID   ID
    VMID       string
    PoolName   string
    Attachment Attachment
}

type Snapshot struct {
    ID       string
    Name     string
    VolumeID ID
}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/storage/volume
```

Expected: package builds and reports no tests or PASS.

- [ ] **Step 7: If verification fails, fix type/package errors**

Valid failures:
- Missing package directory: create it.
- Import cycle: remove non-stdlib imports from `internal/storage/volume`.
- Old store references: remove stale imports and tests.

- [ ] **Step 8: Commit**

```bash
git add internal/storage/volume internal/store
git commit -m "feat(storage): define volume contracts"
```

---

### Task 2: Add block driver interface

**Files:**
- Create: `internal/storage/block/driver.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: define `block.Driver` as the backend interface for block volume implementations without importing qemu/qemuimg.

Acceptance evidence:
- `go test ./internal/storage/block` passes.
- `go list -deps ./internal/storage/block` does not include `internal/virt/qemu` or `internal/virt/qemuimg`.

- [ ] **Step 2: Create driver interface**

Create `internal/storage/block/driver.go`:

```go
package block

import (
    "context"

    "github.com/suknna/govirta/internal/storage/volume"
)

type DriverInfo struct {
    Name         string
    Version      string
    Capabilities Capabilities
}

type Capabilities struct {
    CreateDelete  bool
    Publish       bool
    Snapshot      bool
    ResizeOffline bool
    ResizeOnline  bool
}

type Driver interface {
    DriverInfo(ctx context.Context) (DriverInfo, error)
    Create(ctx context.Context, req CreateRequest) (volume.Volume, error)
    Delete(ctx context.Context, vol volume.Volume) error
    GetActualUsedBytes(ctx context.Context) (int64, error)
    Publish(ctx context.Context, vol volume.Volume, req PublishRequest) (volume.PublishedVolume, error)
    Unpublish(ctx context.Context, vol volume.Volume, req UnpublishRequest) error
    Snapshot(ctx context.Context, vol volume.Volume, req SnapshotRequest) (volume.Snapshot, error)
    Resize(ctx context.Context, vol volume.Volume, req ResizeRequest) (volume.Volume, error)
}

type CreateRequest struct {
    Name          string
    PoolName      string
    VMID          string
    VMName        string
    VolumeID      volume.ID
    DiskIndex     int
    CapacityBytes int64
    ReadOnly      bool
}

type PublishRequest struct {
    VolumeID volume.ID
    VMID     string
    ReadOnly bool
}

type UnpublishRequest struct {
    VolumeID volume.ID
    VMID     string
}

type SnapshotRequest struct {
    Name string
}

type ResizeRequest struct {
    CapacityBytes int64
}
```

- [ ] **Step 3: Run targeted verification**

Run:

```bash
go test ./internal/storage/block
```

Expected: package builds and reports no tests or PASS.

- [ ] **Step 4: Verify dependency boundary**

Run:

```bash
go list -deps ./internal/storage/block
```

Expected: output includes `internal/storage/volume` and standard library packages, but not `internal/virt/qemu` or `internal/virt/qemuimg`.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/block/driver.go
git commit -m "feat(storage): add block driver contract"
```

---

### Task 3: Implement pool service and capacity accounting

**Files:**
- Create: `internal/storage/pool/errors.go`
- Create: `internal/storage/pool/pool.go`
- Create: `internal/storage/pool/service.go`
- Create: `internal/storage/pool/service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: implement explicit pool registration, lookup, usage reporting, and capacity admission using `DefaultOvercommitRatio = 1.5`.

Acceptance evidence:
- `go test ./internal/storage/pool -run 'TestRegisterPool|TestGetPool|TestCapacityAdmission|TestGetPoolUsage'` passes.
- Tests prove empty pool name is rejected and no implicit default pool exists.

- [ ] **Step 2: Create pool errors**

Create `internal/storage/pool/errors.go`:

```go
package pool

import "errors"

var (
    ErrPoolRequired         = errors.New("pool name is required")
    ErrPoolNotFound         = errors.New("pool not found")
    ErrPoolAlreadyExists    = errors.New("pool already exists")
    ErrPoolCapacityExceeded = errors.New("pool capacity exceeded")
)
```

- [ ] **Step 3: Create pool model and helpers**

Create `internal/storage/pool/pool.go`:

```go
package pool

import (
    "context"
    "math"
    "sync"

    "github.com/suknna/govirta/internal/storage/block"
    "github.com/suknna/govirta/internal/storage/volume"
)

type BackendType string
const (
    BackendLocalBlock BackendType = "local-block"
    BackendNFSBlock   BackendType = "nfs-block"
    BackendRBDBlock   BackendType = "rbd-block"
)

type PoolType string
const (
    PoolTypeBlock PoolType = "block"
    PoolTypeFile  PoolType = "file"
)

const DefaultOvercommitRatio = 1.5

type Config struct {
    Name          string
    Type          PoolType
    Backend       BackendType
    StorageRoot   string
    CapacityBytes int64
}

type Pool struct {
    Config Config
    Driver block.Driver
    mu      sync.Mutex
    volumes map[volume.ID]volume.Volume
}

type Usage struct {
    PoolName               string
    Type                   PoolType
    Backend                BackendType
    CapacityBytes          int64
    OvercommitRatio        float64
    AllocationLimitBytes   int64
    AllocatedBytes         int64
    ActualUsedBytes        int64
    AvailableForAllocation int64
}

func New(config Config, driver block.Driver) *Pool {
    return &Pool{Config: config, Driver: driver, volumes: map[volume.ID]volume.Volume{}}
}

func (p *Pool) allocatedLocked() int64 {
    var total int64
    for _, vol := range p.volumes {
        total += vol.CapacityBytes
    }
    return total
}

func allocationLimit(capacity int64) int64 {
    if capacity <= 0 {
        return 0
    }
    limit := float64(capacity) * DefaultOvercommitRatio
    if limit > float64(math.MaxInt64) {
        return math.MaxInt64
    }
    return int64(limit)
}

func (p *Pool) canAllocateLocked(size int64) bool {
    return p.allocatedLocked()+size <= allocationLimit(p.Config.CapacityBytes)
}

func (p *Pool) Usage(ctx context.Context) (Usage, error) {
    if err := ctx.Err(); err != nil {
        return Usage{}, err
    }
    actual, err := p.Driver.GetActualUsedBytes(ctx)
    if err != nil {
        return Usage{}, err
    }
    p.mu.Lock()
    allocated := p.allocatedLocked()
    p.mu.Unlock()
    limit := allocationLimit(p.Config.CapacityBytes)
    return Usage{
        PoolName: p.Config.Name,
        Type: p.Config.Type,
        Backend: p.Config.Backend,
        CapacityBytes: p.Config.CapacityBytes,
        OvercommitRatio: DefaultOvercommitRatio,
        AllocationLimitBytes: limit,
        AllocatedBytes: allocated,
        ActualUsedBytes: actual,
        AvailableForAllocation: limit - allocated,
    }, nil
}
```

- [ ] **Step 4: Create pool service**

Create `internal/storage/pool/service.go`:

```go
package pool

import (
    "context"
    "fmt"
    "sync"

    "github.com/suknna/govirta/internal/storage/volume"
)

type Service struct {
    mu    sync.RWMutex
    pools map[string]*Pool
}

func NewService() *Service {
    return &Service{pools: map[string]*Pool{}}
}

func (s *Service) RegisterPool(pool *Pool) error {
    if pool == nil || pool.Config.Name == "" || pool.Config.Type == "" || pool.Config.Backend == "" || pool.Config.CapacityBytes <= 0 || pool.Driver == nil {
        return volume.ErrInvalidRequest
    }
    if pool.Config.Type != PoolTypeBlock {
        return volume.ErrUnsupported
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.pools[pool.Config.Name]; exists {
        return ErrPoolAlreadyExists
    }
    if pool.volumes == nil {
        pool.volumes = map[volume.ID]volume.Volume{}
    }
    s.pools[pool.Config.Name] = pool
    return nil
}

func (s *Service) GetPool(name string) (*Pool, error) {
    if name == "" {
        return nil, ErrPoolRequired
    }
    s.mu.RLock()
    defer s.mu.RUnlock()
    pool, ok := s.pools[name]
    if !ok {
        return nil, ErrPoolNotFound
    }
    return pool, nil
}

func (s *Service) ListPools(ctx context.Context) ([]Pool, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    s.mu.RLock()
    defer s.mu.RUnlock()
    pools := make([]Pool, 0, len(s.pools))
    for _, p := range s.pools {
        pools = append(pools, Pool{Config: p.Config, Driver: p.Driver})
    }
    return pools, nil
}

func (s *Service) GetPoolUsage(ctx context.Context, poolName string) (Usage, error) {
    if err := ctx.Err(); err != nil {
        return Usage{}, err
    }
    p, err := s.GetPool(poolName)
    if err != nil {
        return Usage{}, err
    }
    usage, err := p.Usage(ctx)
    if err != nil {
        return Usage{}, fmt.Errorf("get pool usage %q: %w", poolName, err)
    }
    return usage, nil
}
```

- [ ] **Step 5: Add pool service tests**

Create `internal/storage/pool/service_test.go` with a fake driver:

```go
package pool

import (
    "context"
    "errors"
    "testing"

    "github.com/suknna/govirta/internal/storage/block"
    "github.com/suknna/govirta/internal/storage/volume"
)

type fakeDriver struct{ used int64 }

func (f fakeDriver) DriverInfo(context.Context) (block.DriverInfo, error) { return block.DriverInfo{Name: "fake"}, nil }
func (f fakeDriver) Create(context.Context, block.CreateRequest) (volume.Volume, error) { return volume.Volume{}, nil }
func (f fakeDriver) Delete(context.Context, volume.Volume) error { return nil }
func (f fakeDriver) GetActualUsedBytes(context.Context) (int64, error) { return f.used, nil }
func (f fakeDriver) Publish(context.Context, volume.Volume, block.PublishRequest) (volume.PublishedVolume, error) { return volume.PublishedVolume{}, nil }
func (f fakeDriver) Unpublish(context.Context, volume.Volume, block.UnpublishRequest) error { return nil }
func (f fakeDriver) Snapshot(context.Context, volume.Volume, block.SnapshotRequest) (volume.Snapshot, error) { return volume.Snapshot{}, volume.ErrUnsupported }
func (f fakeDriver) Resize(context.Context, volume.Volume, block.ResizeRequest) (volume.Volume, error) { return volume.Volume{}, volume.ErrUnsupported }

func newTestPool(name string, capacity int64) *Pool {
    return New(Config{Name: name, Type: PoolTypeBlock, Backend: BackendLocalBlock, StorageRoot: "/var/lib/govirta/storage", CapacityBytes: capacity}, fakeDriver{used: 7})
}

func TestRegisterPoolRejectsInvalidAndDuplicate(t *testing.T) {
    svc := NewService()
    if err := svc.RegisterPool(nil); !errors.Is(err, volume.ErrInvalidRequest) { t.Fatalf("nil pool error = %v", err) }
    p := newTestPool("default", 100)
    if err := svc.RegisterPool(p); err != nil { t.Fatalf("register pool: %v", err) }
    if err := svc.RegisterPool(p); !errors.Is(err, ErrPoolAlreadyExists) { t.Fatalf("duplicate error = %v", err) }
}

func TestGetPoolRequiresExplicitName(t *testing.T) {
    svc := NewService()
    if _, err := svc.GetPool(""); !errors.Is(err, ErrPoolRequired) { t.Fatalf("empty name error = %v", err) }
    if _, err := svc.GetPool("default"); !errors.Is(err, ErrPoolNotFound) { t.Fatalf("missing pool error = %v", err) }
}

func TestGetPoolUsageReportsOvercommitAccounting(t *testing.T) {
    svc := NewService()
    p := newTestPool("default", 100)
    p.volumes["vol-1"] = volume.Volume{ID: "vol-1", CapacityBytes: 100}
    if err := svc.RegisterPool(p); err != nil { t.Fatalf("register pool: %v", err) }
    usage, err := svc.GetPoolUsage(context.Background(), "default")
    if err != nil { t.Fatalf("usage: %v", err) }
    if usage.AllocationLimitBytes != 150 || usage.AllocatedBytes != 100 || usage.AvailableForAllocation != 50 || usage.ActualUsedBytes != 7 {
        t.Fatalf("usage = %+v", usage)
    }
}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/storage/pool
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/pool
git commit -m "feat(storage): add pool service"
```

---

### Task 4: Implement root VolumeService

**Files:**
- Create: `internal/storage/errors.go`
- Create: `internal/storage/service.go`
- Create: `internal/storage/service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: expose VM-facing `storage.VolumeService` that validates explicit pool/VM/disk fields, delegates capacity and backend work to `pool.Pool`, and returns `volume.PublishedVolume` without importing qemu/qemuimg.

Acceptance evidence:
- `go test ./internal/storage -run 'TestCreateVolume|TestPublishVolume|TestUnpublishVolume|TestDeleteVolume'` passes.
- `go list -deps ./internal/storage` does not include `internal/virt/qemu` or `internal/virt/qemuimg`.

- [ ] **Step 2: Add root error aliases**

Create `internal/storage/errors.go`:

```go
package storage

import (
    "github.com/suknna/govirta/internal/storage/pool"
    "github.com/suknna/govirta/internal/storage/volume"
)

var (
    ErrInvalidRequest = volume.ErrInvalidRequest
    ErrUnsupported    = volume.ErrUnsupported

    ErrPoolRequired         = pool.ErrPoolRequired
    ErrPoolNotFound         = pool.ErrPoolNotFound
    ErrPoolAlreadyExists    = pool.ErrPoolAlreadyExists
    ErrPoolCapacityExceeded = pool.ErrPoolCapacityExceeded

    ErrVolumeNotFound      = volume.ErrVolumeNotFound
    ErrVolumeAlreadyExists = volume.ErrVolumeAlreadyExists
    ErrVolumeConflict      = volume.ErrVolumeConflict
    ErrVolumeInUse         = volume.ErrVolumeInUse
    ErrVolumeNotPublished  = volume.ErrVolumeNotPublished
)
```

- [ ] **Step 3: Add VolumeService request types and constructor**

Create `internal/storage/service.go` with these declarations first:

```go
package storage

import (
    "context"
    "fmt"

    "github.com/suknna/govirta/internal/storage/block"
    "github.com/suknna/govirta/internal/storage/pool"
    "github.com/suknna/govirta/internal/storage/volume"
)

type VolumeService struct { pools *pool.Service }

func NewVolumeService(pools *pool.Service) *VolumeService { return &VolumeService{pools: pools} }

type CreateVolumeRequest struct {
    VMID     string
    VMName   string
    PoolName string
    Spec     volume.Spec
}

type CreateRootVolumeRequest struct {
    VMID string; VMName string; PoolName string; Name string; DiskIndex int; CapacityBytes int64; ReadOnly bool
}

type CreateDataVolumeRequest = CreateRootVolumeRequest

type PublishVolumeRequest struct { VolumeID volume.ID; VMID string; PoolName string; ReadOnly bool }
type UnpublishVolumeRequest struct { VolumeID volume.ID; VMID string; PoolName string }
type DeleteVolumeRequest struct { VolumeID volume.ID; PoolName string }
```

- [ ] **Step 4: Implement validation and create**

Append to `internal/storage/service.go`:

```go
func validateCreateRequest(req CreateVolumeRequest) error {
    if req.PoolName == "" { return pool.ErrPoolRequired }
    if req.VMID == "" || req.VMName == "" || req.Spec.Name == "" || req.Spec.CapacityBytes <= 0 || req.Spec.DiskIndex < 0 { return volume.ErrInvalidRequest }
    return nil
}

func (s *VolumeService) CreateVolume(ctx context.Context, req CreateVolumeRequest) (volume.Volume, error) {
    if err := ctx.Err(); err != nil { return volume.Volume{}, err }
    if err := validateCreateRequest(req); err != nil { return volume.Volume{}, err }
    p, err := s.pools.GetPool(req.PoolName)
    if err != nil { return volume.Volume{}, err }
    volID := volume.ID(fmt.Sprintf("%s-%s-%d", req.VMID, req.Spec.Role, req.Spec.DiskIndex))
    driverReq := block.CreateRequest{Name: req.Spec.Name, PoolName: req.PoolName, VMID: req.VMID, VMName: req.VMName, VolumeID: volID, DiskIndex: req.Spec.DiskIndex, CapacityBytes: req.Spec.CapacityBytes, ReadOnly: req.Spec.ReadOnly}
    return p.CreateVolume(ctx, driverReq)
}

func (s *VolumeService) CreateRootVolume(ctx context.Context, req CreateRootVolumeRequest) (volume.Volume, error) {
    return s.CreateVolume(ctx, CreateVolumeRequest{VMID: req.VMID, VMName: req.VMName, PoolName: req.PoolName, Spec: volume.Spec{Name: req.Name, Role: volume.RoleRoot, DiskIndex: req.DiskIndex, CapacityBytes: req.CapacityBytes, ReadOnly: req.ReadOnly}})
}

func (s *VolumeService) CreateDataVolume(ctx context.Context, req CreateDataVolumeRequest) (volume.Volume, error) {
    return s.CreateVolume(ctx, CreateVolumeRequest{VMID: req.VMID, VMName: req.VMName, PoolName: req.PoolName, Spec: volume.Spec{Name: req.Name, Role: volume.RoleData, DiskIndex: req.DiskIndex, CapacityBytes: req.CapacityBytes, ReadOnly: req.ReadOnly}})
}
```

Then add `CreateVolume`, `PublishVolume`, `UnpublishVolume`, and `DeleteVolume` methods to `pool.Pool` in `internal/storage/pool/service.go`. Use `errors.Join(primaryErr, cleanupErr)` whenever a rollback fails.

- [ ] **Step 5: Implement publish/unpublish/delete**

Append to `internal/storage/service.go`:

```go
func (s *VolumeService) PublishVolume(ctx context.Context, req PublishVolumeRequest) (volume.PublishedVolume, error) {
    if err := ctx.Err(); err != nil { return volume.PublishedVolume{}, err }
    if req.PoolName == "" { return volume.PublishedVolume{}, pool.ErrPoolRequired }
    if req.VolumeID == "" || req.VMID == "" { return volume.PublishedVolume{}, volume.ErrInvalidRequest }
    p, err := s.pools.GetPool(req.PoolName)
    if err != nil { return volume.PublishedVolume{}, err }
    return p.PublishVolume(ctx, req.VolumeID, block.PublishRequest{VolumeID: req.VolumeID, VMID: req.VMID, ReadOnly: req.ReadOnly})
}

func (s *VolumeService) UnpublishVolume(ctx context.Context, req UnpublishVolumeRequest) error {
    if err := ctx.Err(); err != nil { return err }
    if req.PoolName == "" { return pool.ErrPoolRequired }
    if req.VolumeID == "" || req.VMID == "" { return volume.ErrInvalidRequest }
    p, err := s.pools.GetPool(req.PoolName)
    if err != nil { return err }
    return p.UnpublishVolume(ctx, req.VolumeID, block.UnpublishRequest{VolumeID: req.VolumeID, VMID: req.VMID})
}

func (s *VolumeService) DeleteVolume(ctx context.Context, req DeleteVolumeRequest) error {
    if err := ctx.Err(); err != nil { return err }
    if req.PoolName == "" { return pool.ErrPoolRequired }
    if req.VolumeID == "" { return volume.ErrInvalidRequest }
    p, err := s.pools.GetPool(req.PoolName)
    if err != nil { return err }
    return p.DeleteVolume(ctx, req.VolumeID)
}
```

- [ ] **Step 6: Add root service tests**

Create `internal/storage/service_test.go` with a fake block driver. Test cases must cover explicit `PoolName`, create, publish idempotency, conflict, delete while published, and canceled ctx not calling driver.

Use this core assertion style:

```go
if _, err := svc.CreateRootVolume(context.Background(), storage.CreateRootVolumeRequest{VMID: "vm-1", VMName: "vmone", Name: "root", DiskIndex: 0, CapacityBytes: 10}); !errors.Is(err, storage.ErrPoolRequired) {
    t.Fatalf("missing pool error = %v", err)
}
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test ./internal/storage ./internal/storage/pool ./internal/storage/block ./internal/storage/volume
```

Expected: PASS.

- [ ] **Step 8: Verify dependency boundary**

Run:

```bash
go list -deps ./internal/storage
```

Expected: no `internal/virt/qemu` and no `internal/virt/qemuimg`.

- [ ] **Step 9: Commit**

```bash
git add internal/storage internal/storage/pool
git commit -m "feat(storage): add volume service"
```

---

### Task 5: Implement local qcow2 BlockDriver

**Files:**
- Create: `internal/storage/local/driver.go`
- Create: `internal/storage/local/driver_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: implement local qcow2 block driver that creates empty qcow2 files under explicit pool/VM paths and publishes file/qcow2 attachments.

Acceptance evidence:
- `go test ./internal/storage/local -run 'TestCreate|TestPublish|TestDelete|TestPath|TestContext'` passes.
- Tests prove no backing file or image source is used.

- [ ] **Step 2: Implement local driver skeleton and path validation**

Create `internal/storage/local/driver.go`:

```go
package local

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "regexp"
    "strings"

    "github.com/suknna/govirta/internal/storage/block"
    "github.com/suknna/govirta/internal/storage/volume"
    "github.com/suknna/govirta/internal/virt/qemuimg"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Config struct { PoolName string; StorageRoot string; QEMUImg qemuimg.Client }
type Driver struct { config Config }

func NewDriver(config Config) (*Driver, error) {
    if config.PoolName == "" || config.StorageRoot == "" || !safeName.MatchString(config.PoolName) {
        return nil, volume.ErrInvalidRequest
    }
    return &Driver{config: config}, nil
}

func (d *Driver) volumePath(req block.CreateRequest) (string, error) {
    if req.VMID == "" || req.VMName == "" || req.DiskIndex < 0 || !safeName.MatchString(req.PoolName) || !safeName.MatchString(req.VMID) || !safeName.MatchString(req.VMName) {
        return "", volume.ErrInvalidRequest
    }
    filename := fmt.Sprintf("%s-disk-%d.qcow2", req.VMName, req.DiskIndex)
    root := filepath.Join(d.config.StorageRoot, "pool", req.PoolName, req.VMID)
    target := filepath.Join(root, filename)
    rel, err := filepath.Rel(root, target)
    if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
        return "", volume.ErrInvalidRequest
    }
    return target, nil
}
```

- [ ] **Step 3: Implement create/delete/publish methods**

Append to `internal/storage/local/driver.go`:

```go
func (d *Driver) DriverInfo(ctx context.Context) (block.DriverInfo, error) {
    if err := ctx.Err(); err != nil { return block.DriverInfo{}, err }
    return block.DriverInfo{Name: "local-qcow2", Capabilities: block.Capabilities{CreateDelete: true, Publish: true}}, nil
}

func (d *Driver) Create(ctx context.Context, req block.CreateRequest) (volume.Volume, error) {
    if err := ctx.Err(); err != nil { return volume.Volume{}, err }
    path, err := d.volumePath(req)
    if err != nil { return volume.Volume{}, err }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { return volume.Volume{}, fmt.Errorf("create local volume directory: %w", err) }
    if err := d.config.QEMUImg.QCOW2().Create().Target(path).Size(req.CapacityBytes).Do(ctx); err != nil {
        cleanupErr := os.RemoveAll(filepath.Dir(path))
        if cleanupErr != nil { return volume.Volume{}, errors.Join(fmt.Errorf("create local qcow2 volume: %w", err), cleanupErr) }
        return volume.Volume{}, fmt.Errorf("create local qcow2 volume: %w", err)
    }
    return volume.Volume{ID: req.VolumeID, Name: req.Name, VMID: req.VMID, VMName: req.VMName, PoolName: req.PoolName, DiskIndex: req.DiskIndex, Backend: string(d.config.PoolName), CapacityBytes: req.CapacityBytes, State: volume.StateAvailable, Context: map[string]string{"path": path, "format": string(volume.DiskFormatQCOW2)}}, nil
}

func (d *Driver) Publish(ctx context.Context, vol volume.Volume, req block.PublishRequest) (volume.PublishedVolume, error) {
    if err := ctx.Err(); err != nil { return volume.PublishedVolume{}, err }
    path := vol.Context["path"]
    if path == "" { return volume.PublishedVolume{}, volume.ErrInvalidRequest }
    if _, err := d.config.QEMUImg.QCOW2().Info().Path(path).Do(ctx); err != nil { return volume.PublishedVolume{}, fmt.Errorf("inspect local qcow2 volume: %w", err) }
    return volume.PublishedVolume{VolumeID: vol.ID, VMID: req.VMID, PoolName: vol.PoolName, Attachment: volume.Attachment{Kind: volume.AttachmentFile, Format: volume.DiskFormatQCOW2, Path: path, ReadOnly: req.ReadOnly}}, nil
}
```

Add `errors` to imports when using `errors.Join`.

- [ ] **Step 4: Implement delete/unpublish/usage/unsupported methods**

Append to `internal/storage/local/driver.go`:

```go
func (d *Driver) Delete(ctx context.Context, vol volume.Volume) error {
    if err := ctx.Err(); err != nil { return err }
    path := vol.Context["path"]
    if path == "" { return volume.ErrInvalidRequest }
    if err := d.config.QEMUImg.QCOW2().Remove().Path(path).Do(ctx); err != nil { return fmt.Errorf("remove local qcow2 volume: %w", err) }
    if err := os.RemoveAll(filepath.Dir(path)); err != nil { return fmt.Errorf("remove local volume directory: %w", err) }
    return nil
}

func (d *Driver) Unpublish(ctx context.Context, vol volume.Volume, req block.UnpublishRequest) error { return ctx.Err() }
func (d *Driver) Snapshot(ctx context.Context, vol volume.Volume, req block.SnapshotRequest) (volume.Snapshot, error) { if err := ctx.Err(); err != nil { return volume.Snapshot{}, err }; return volume.Snapshot{}, volume.ErrUnsupported }
func (d *Driver) Resize(ctx context.Context, vol volume.Volume, req block.ResizeRequest) (volume.Volume, error) { if err := ctx.Err(); err != nil { return volume.Volume{}, err }; return volume.Volume{}, volume.ErrUnsupported }

func (d *Driver) GetActualUsedBytes(ctx context.Context) (int64, error) {
    if err := ctx.Err(); err != nil { return 0, err }
    root := filepath.Join(d.config.StorageRoot, "pool", d.config.PoolName)
    var total int64
    err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
        if walkErr != nil { return walkErr }
        if err := ctx.Err(); err != nil { return err }
        if entry.Type().IsRegular() {
            info, err := entry.Info(); if err != nil { return err }
            total += info.Size()
        }
        return nil
    })
    if err != nil { return 0, err }
    return total, nil
}
```

- [ ] **Step 5: Add local driver tests**

Create `internal/storage/local/driver_test.go` with a fake qemu-img runner injected through `qemuimg.NewClient(qemuimg.Config{Runner: fake})`. The existing `qemuimg.Config.Runner` field supports this injection, so tests must not invoke a real qemu-img binary.

Required test names:

```go
func TestCreateBuildsEmptyQCOW2InPoolVMPath(t *testing.T) {}
func TestCreateRejectsUnsafePathParts(t *testing.T) {}
func TestCreateHonorsCanceledContext(t *testing.T) {}
func TestPublishInspectsQCOW2AndReturnsFileAttachment(t *testing.T) {}
func TestDeleteRemovesQCOW2AndDirectory(t *testing.T) {}
func TestSnapshotAndResizeReturnUnsupported(t *testing.T) {}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/storage/local
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/local
git commit -m "feat(storage): add local qcow2 driver"
```

---

### Task 6: Integration cleanup and full verification

**Files:**
- Modify: `docs/superpowers/specs/2026-05-27-storage-pool-volume-design.md` only if implementation discovers a spec mismatch.
- No production file should import deleted `internal/store`.

- [ ] **Step 1: Confirm final acceptance criteria**

Goal: entire repository builds and tests with new storage package and no stale store package.

Acceptance evidence:
- `scripts/verify.sh` passes.
- `go test -race ./internal/storage/...` passes if concurrency locks are introduced.
- `git status --short` shows only intended files.

- [ ] **Step 2: Search for forbidden stale names**

Run:

```bash
grep -R "DiskService\|VolumeDriver\|ContentSource\|ImagePath\|internal/store\|package store" internal cmd docs/superpowers/plans docs/superpowers/specs
```

Expected: no production references. References inside this plan or old discussion docs are acceptable only if they explicitly describe removed names.

- [ ] **Step 3: Run storage package tests**

Run:

```bash
go test ./internal/storage/...
```

Expected: PASS.

- [ ] **Step 4: Run full local CI equivalent**

Run:

```bash
scripts/verify.sh
```

Expected:

```text
gofmt check passes
go test ./... passes
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl passes
```

- [ ] **Step 5: Run race tests if storage locks exist**

Run:

```bash
go test -race ./internal/storage/...
```

Expected: PASS with no data races.

- [ ] **Step 6: Inspect final diff**

Run:

```bash
git status --short
git diff -- internal/store internal/storage docs/superpowers/specs/2026-05-27-storage-pool-volume-design.md
```

Expected: diff includes only storage package implementation, old store removal, and approved spec updates.

- [ ] **Step 7: Commit final integration if prior tasks were not committed**

If task commits were skipped during execution, commit the complete implementation:

```bash
git add internal/store internal/storage docs/superpowers/specs/2026-05-27-storage-pool-volume-design.md
git commit -m "feat(storage): add pool-backed block volumes"
```

---

## Plan Self-Review

- Spec coverage: plan covers old `internal/store` deletion, subpackage structure, root `VolumeService`, standalone `PoolService`, `PoolType`, `BlockDriver`, local qcow2 driver, explicit `poolName`, in-memory metadata, no image source, context propagation, `errors.Join`, tests, and verification.
- Placeholder scan: no placeholder tasks; every task names exact files, method signatures, and verification commands.
- Type consistency: plan uses `storage.VolumeService`, `pool.Service`, `pool.PoolType`, `block.Driver`, `volume.Volume`, and `volume.Attachment` consistently.
- Scope note: plan intentionally excludes image repository/file pool implementation. First version creates empty qcow2 block volumes only.
