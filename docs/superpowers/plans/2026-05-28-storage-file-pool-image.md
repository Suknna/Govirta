# Storage File Pool Image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement file pools, image byte storage, and full-copy image-derived VM root volumes for Govirta storage.

**Architecture:** `internal/storage/image` defines a file-image driver contract, `ImageService` exposes reader/writer image I/O, `pool.Service` supports both block and file pools with independent in-memory indexes, and `VolumeService.CreateRootVolumeFromReader` creates standalone qcow2 root volumes from explicit qcow2/raw bytes. Cross-pool orchestration remains above storage; storage never uses qcow2 backing-file links.

**Tech Stack:** Go 1.26, standard library `context` / `errors` / `io` / `os` / `path/filepath` / `sync`, existing `internal/storage` packages, existing `internal/virt/qemuimg` runner boundary, table-driven Go tests.

---

## File Structure

Create and modify these files:

- Create: `internal/storage/diskformat/format.go` — strong `Format` type and qcow2/raw constants.
- Create: `internal/storage/diskformat/format_test.go` — explicit format validation tests.
- Create: `internal/storage/image/driver.go` — `image.Driver`, `ImageWriter`, request types, image error sentinels.
- Modify: `internal/storage/errors.go` — re-export image errors if useful to root callers.
- Modify: `internal/storage/block/driver.go` — add `CreateFromReader` and request type.
- Modify: `internal/storage/pool/pool.go` — add file backend constants, block/image driver fields, image index, pending/ready state, file overcommit ratio.
- Modify: `internal/storage/pool/service.go` — support file pool registration, usage, image lifecycle, pending writer wrapper, type checks.
- Modify: `internal/storage/pool/service_test.go` — file pool, image index, pending, ctx, capacity and type mismatch tests.
- Create: `internal/storage/image_service.go` — `ImageService` root API.
- Modify: `internal/storage/service.go` — add `CreateRootVolumeFromReader`.
- Modify: `internal/storage/service_test.go` — root image service and reader-derived root volume tests.
- Create: `internal/storage/localfile/driver.go` — local file image driver.
- Create: `internal/storage/localfile/driver_test.go` — localfile path safety, put/get/delete, close/cancel, ctx tests.
- Modify: `internal/storage/local/driver.go` — implement `CreateFromReader` full-copy qcow2/raw path.
- Modify: `internal/storage/local/driver_test.go` — qcow2 reader copy, raw convert, resize, cleanup, ctx tests.
- Modify: `internal/virt/qemuimg/convert/convert.go` — support explicit input format while keeping default qcow2->qcow2.
- Modify: `internal/virt/qemuimg/convert/convert_test.go` — raw->qcow2 argv and validation tests.
- Create: `internal/virt/qemuimg/resize/resize.go` — typed `qemu-img resize -f qcow2` builder.
- Create: `internal/virt/qemuimg/resize/resize_test.go` — resize argv, validation, runner error, ctx tests.
- Modify: `internal/virt/qemuimg/client.go` — expose `QCOW2Client.Resize()`.
- Modify: `internal/virt/qemuimg/client_test.go` — builder presence and configured runner coverage for resize/convert formats.

Dependency order:

```text
diskformat -> image -> qemuimg convert/resize -> block interface -> pool file support -> ImageService -> localfile -> VolumeService reader path -> local CreateFromReader -> full verification
```

---

### Task 1: Strong disk format type and image driver contract

**Files:**
- Create: `internal/storage/diskformat/format.go`
- Create: `internal/storage/diskformat/format_test.go`
- Create: `internal/storage/image/driver.go`
- Modify: `internal/storage/errors.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: storage has a shared strong format type and an image driver contract that does not depend on block or root storage packages.

Acceptance evidence:
- `go test ./internal/storage/diskformat ./internal/storage/image` passes.
- `go test ./internal/storage/diskformat ./internal/storage/image` passes before downstream interface updates.
- `grep -R "Format string" internal/storage` finds no image/block request field using a naked `string` format.

- [ ] **Step 2: Add `diskformat.Format`**

Create `internal/storage/diskformat/format.go`:

```go
package diskformat

// Format identifies a storage image byte format accepted by storage APIs.
type Format string

const (
    // FormatQCOW2 identifies QEMU qcow2 image bytes.
    FormatQCOW2 Format = "qcow2"
    // FormatRaw identifies raw disk image bytes.
    FormatRaw Format = "raw"
)

// Valid reports whether f is one of the explicitly supported formats.
func (f Format) Valid() bool {
    switch f {
    case FormatQCOW2, FormatRaw:
        return true
    default:
        return false
    }
}
```

- [ ] **Step 3: Test format validation**

Create `internal/storage/diskformat/format_test.go`:

```go
package diskformat

import "testing"

func TestFormatValidAcceptsExplicitFormats(t *testing.T) {
    for _, f := range []Format{FormatQCOW2, FormatRaw} {
        if !f.Valid() {
            t.Fatalf("%q Valid() = false, want true", f)
        }
    }
}

func TestFormatValidRejectsImplicitOrUnknownFormats(t *testing.T) {
    for _, f := range []Format{"", "vmdk", "QCOW2"} {
        if f.Valid() {
            t.Fatalf("%q Valid() = true, want false", f)
        }
    }
}
```

- [ ] **Step 4: Add image driver API and errors**

Create `internal/storage/image/driver.go`:

```go
package image

import (
    "context"
    "errors"
    "io"

    "github.com/suknna/govirta/internal/storage/diskformat"
)

var (
    // ErrInvalidImage marks caller input that cannot identify or store an image.
    ErrInvalidImage = errors.New("invalid image request")
    // ErrImageExists marks duplicate image IDs in the same pool.
    ErrImageExists = errors.New("image already exists")
    // ErrImageNotFound marks lookup or delete requests for unknown images.
    ErrImageNotFound = errors.New("image not found")
)

// DriverInfo describes an image backend implementation.
type DriverInfo struct {
    Name         string
    Capabilities Capabilities
}

// Capabilities describes image backend operations.
type Capabilities struct {
    SupportsRaw   bool
    SupportsQCOW2 bool
}

// Driver defines the backend contract for file-image pool implementations.
type Driver interface {
    DriverInfo(ctx context.Context) (DriverInfo, error)
    Put(ctx context.Context, req PutRequest) (ImageWriter, error)
    Get(ctx context.Context, req GetRequest) (io.ReadCloser, error)
    Delete(ctx context.Context, req DeleteRequest) error
    GetActualUsedBytes(ctx context.Context) (int64, error)
}

// ImageWriter accepts image bytes and either commits or cancels the upload.
type ImageWriter interface {
    io.Writer
    Close() error
    Cancel() error
}

// PutRequest identifies a new image write operation inside a file pool.
type PutRequest struct {
    ImageID           string
    Format            diskformat.Format
    DeclaredSizeBytes int64
}

// GetRequest identifies an image read operation inside a file pool.
type GetRequest struct { ImageID string }

// DeleteRequest identifies an image delete operation inside a file pool.
type DeleteRequest struct { ImageID string }
```

- [ ] **Step 5: Re-export image errors from root storage**

Modify `internal/storage/errors.go` to import `internal/storage/image` and add:

```go
// ErrInvalidImage marks caller input that cannot identify or store an image.
ErrInvalidImage = image.ErrInvalidImage
// ErrImageExists marks duplicate image IDs in the same pool.
ErrImageExists = image.ErrImageExists
// ErrImageNotFound marks lookup or delete requests for unknown images.
ErrImageNotFound = image.ErrImageNotFound
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/storage/diskformat ./internal/storage/image
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/storage/diskformat internal/storage/image internal/storage/errors.go
git commit -m "feat(storage): add image format and driver contracts"
```

---

### Task 2: qemu-img convert format support and resize builder

**Files:**
- Modify: `internal/virt/qemuimg/convert/convert.go`
- Modify: `internal/virt/qemuimg/convert/convert_test.go`
- Create: `internal/virt/qemuimg/resize/resize.go`
- Create: `internal/virt/qemuimg/resize/resize_test.go`
- Modify: `internal/virt/qemuimg/client.go`
- Modify: `internal/virt/qemuimg/client_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: qemuimg can explicitly convert raw->qcow2 and resize qcow2 through the existing runner boundary.

Acceptance evidence:
- `go test ./internal/virt/qemuimg/...` passes.
- Raw conversion argv is `convert -f raw -O qcow2 <source> <target>`.
- Resize argv is `resize -f qcow2 <path> <size>`.

- [ ] **Step 2: Extend convert builder with source format**

Modify `internal/virt/qemuimg/convert/convert.go`:

```go
type Builder struct {
    binary       string
    runner       imgexec.Runner
    source       string
    target       string
    sourceFormat string
}

func (b *Builder) SourceFormat(format string) *Builder {
    b.sourceFormat = format
    return b
}

func (b *Builder) Do(ctx context.Context) error {
    source, err := imgargv.PathOperand("source", b.source)
    if err != nil { return err }
    target, err := imgargv.PathOperand("target", b.target)
    if err != nil { return err }
    format := b.sourceFormat
    if format == "" { format = "qcow2" }
    switch format {
    case "qcow2", "raw":
    default:
        return imgexec.InvalidRequest("source format must be qcow2 or raw")
    }
    result, err := b.runner.Run(ctx, b.binary, []string{"convert", "-f", format, "-O", "qcow2", source, target})
    return imgexec.WrapError(result, err)
}
```

- [ ] **Step 3: Add resize builder**

Create `internal/virt/qemuimg/resize/resize.go`:

```go
package resize

import (
    "context"
    "strconv"

    imgargv "github.com/suknna/govirta/internal/virt/qemuimg/internal/argv"
    imgexec "github.com/suknna/govirta/internal/virt/qemuimg/internal/exec"
)

type Builder struct { binary string; runner imgexec.Runner; path string; size int64 }

func New(binary string, runner imgexec.Runner) *Builder {
    if runner == nil { runner = imgexec.OSRunner{} }
    return &Builder{binary: binary, runner: runner}
}
func (b *Builder) Path(path string) *Builder { b.path = path; return b }
func (b *Builder) SizeBytes(size int64) *Builder { b.size = size; return b }
func (b *Builder) Do(ctx context.Context) error {
    path, err := imgargv.PathOperand("path", b.path)
    if err != nil { return err }
    if b.size <= 0 { return imgexec.InvalidRequest("size must be positive") }
    result, err := b.runner.Run(ctx, b.binary, []string{"resize", "-f", "qcow2", path, strconv.FormatInt(b.size, 10)})
    return imgexec.WrapError(result, err)
}
```

- [ ] **Step 4: Expose resize from client**

Modify `internal/virt/qemuimg/client.go` to import `resize` and add:

```go
func (c QCOW2Client) Resize() *resize.Builder { return resize.New(c.binary, c.runner) }
```

- [ ] **Step 5: Add tests**

Add/extend tests:

```go
func TestDoBuildsRawToQCOW2ConvertArgv(t *testing.T) {
    runner := &recordingRunner{}
    err := New("qemu-img", runner).Source("src.raw").SourceFormat("raw").Target("dst.qcow2").Do(context.Background())
    if err != nil { t.Fatalf("Do() error = %v", err) }
    wantArgs := []string{"convert", "-f", "raw", "-O", "qcow2", "src.raw", "dst.qcow2"}
    if !reflect.DeepEqual(runner.args, wantArgs) { t.Fatalf("args = %#v, want %#v", runner.args, wantArgs) }
}
```

For resize, mirror existing builder tests: builds argv, requires path, requires positive size, returns runner error, returns context canceled.

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test ./internal/virt/qemuimg/convert ./internal/virt/qemuimg/resize ./internal/virt/qemuimg
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/virt/qemuimg/convert internal/virt/qemuimg/resize internal/virt/qemuimg/client.go internal/virt/qemuimg/client_test.go
git commit -m "feat(qemuimg): support image conversion and resize"
```

---

### Task 3: Extend block interfaces and pool for file pools

**Files:**
- Modify: `internal/storage/block/driver.go`
- Modify: `internal/storage/pool/pool.go`
- Modify: `internal/storage/pool/service.go`
- Modify: `internal/storage/pool/service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: pool service can register block and file pools, account file image capacity including pending writes, and keep block operations rejected on file pools.

Acceptance evidence:
- `go test ./internal/storage/block ./internal/storage/pool` passes.
- `go test -race ./internal/storage/pool` passes.

- [ ] **Step 2: Add `CreateFromReader` to block driver**

Modify `internal/storage/block/driver.go`:

```go
type Driver interface {
    DriverInfo(ctx context.Context) (DriverInfo, error)
    Create(ctx context.Context, req CreateRequest) (volume.Volume, error)
    CreateFromReader(ctx context.Context, req CreateFromReaderRequest) (volume.Volume, error)
    Delete(ctx context.Context, vol volume.Volume) error
    GetActualUsedBytes(ctx context.Context) (int64, error)
    Publish(ctx context.Context, vol volume.Volume, req PublishRequest) (volume.PublishedVolume, error)
    Unpublish(ctx context.Context, vol volume.Volume, req UnpublishRequest) error
    Snapshot(ctx context.Context, vol volume.Volume, req SnapshotRequest) (volume.Snapshot, error)
    Resize(ctx context.Context, vol volume.Volume, req ResizeRequest) (volume.Volume, error)
}

type CreateFromReaderRequest struct {
    Reader        io.Reader
    Format        diskformat.Format
    Name          string
    PoolName      string
    VMID          string
    VMName        string
    VolumeID      volume.ID
    DiskIndex     int
    CapacityBytes int64
    ReadOnly      bool
}
```

Update all fake block drivers in tests to implement `CreateFromReader`, returning `volume.ErrUnsupported` until the relevant task needs behavior.

- [ ] **Step 3: Reshape pool for file and block drivers**

Modify `internal/storage/pool/pool.go`:

```go
const DefaultFileOvercommitRatio = 1.0

const BackendLocalFile BackendType = "local-file"

type ImageState string
const (ImageStatePending ImageState = "pending"; ImageStateReady ImageState = "ready")

type ImageRecord struct {
    ID string
    Format diskformat.Format
    DeclaredSizeBytes int64
    State ImageState
}

type Pool struct {
    Config Config
    Driver block.Driver
    ImageDriver image.Driver
    mu sync.RWMutex
    volumes map[volume.ID]volume.Volume
    images map[string]ImageRecord
}
```

Keep `Driver block.Driver` for existing block callers. Add `ImageDriver image.Driver` for file pools.

- [ ] **Step 4: Update registration and usage**

In `RegisterPool`, replace the current `if p.Config.Type != PoolTypeBlock` rejection with:

```go
switch p.Config.Type {
case PoolTypeBlock:
    if p.Driver == nil || p.ImageDriver != nil { return volume.ErrInvalidRequest }
case PoolTypeFile:
    if p.ImageDriver == nil || p.Driver != nil { return volume.ErrInvalidRequest }
default:
    return volume.ErrInvalidRequest
}
```

Initialize `p.volumes` for block pools and `p.images` for file pools. In `GetPoolUsage`, choose driver and overcommit by pool type:

```go
ratio := DefaultOvercommitRatio
allocated := p.allocatedLocked()
var actual int64
switch config.Type {
case PoolTypeBlock:
    actual, err = blockDriver.GetActualUsedBytes(ctx)
case PoolTypeFile:
    ratio = DefaultFileOvercommitRatio
    actual, err = imageDriver.GetActualUsedBytes(ctx)
}
```

Use an `allocationLimitForRatio(capacity, ratio)` helper instead of hard-coded block ratio.

- [ ] **Step 5: Add image lifecycle methods with pending writer wrapper**

Add to `pool.Service`:

```go
func (s *Service) PutImage(ctx context.Context, poolName string, req image.PutRequest) (image.ImageWriter, error)
func (s *Service) GetImage(ctx context.Context, poolName string, req image.GetRequest) (io.ReadCloser, error)
func (s *Service) DeleteImage(ctx context.Context, poolName string, req image.DeleteRequest) error
```

`PutImage` must reserve capacity and pending ID under `p.mu`, then call driver `Put(ctx, req)`. Return a wrapper whose `Close` promotes pending to ready and whose `Cancel` removes pending. If driver Put fails after reservation, release pending before returning, using `errors.Join` if cleanup has an error.

- [ ] **Step 6: Add pool tests**

Add tests for:
- file pool registration succeeds with `ImageDriver` and fails with `Driver` mismatch.
- `GetPoolUsage` returns `OvercommitRatio == 1.0` for file pool.
- pending `PutImage` counts in `AllocatedBytes` before `Close`.
- duplicate `ImageID` returns `image.ErrImageExists`.
- `Close` moves pending to ready; `Cancel` releases capacity.
- canceled ctx returns context error and does not call driver.
- `CreateVolume` on file pool returns `volume.ErrUnsupported`.

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test ./internal/storage/block ./internal/storage/pool
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/storage/block/driver.go internal/storage/pool
git commit -m "feat(storage): add file pool lifecycle support"
```

---

### Task 4: Root ImageService and VolumeService reader API

**Files:**
- Create: `internal/storage/image_service.go`
- Modify: `internal/storage/service.go`
- Modify: `internal/storage/service_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: root storage APIs expose image reader/writer operations and root volume creation from a caller-provided reader without coupling the two services.

Acceptance evidence:
- `go test ./internal/storage` passes.
- Tests prove `ImageService` does not import or call `VolumeService`.

- [ ] **Step 2: Add `ImageService`**

Create `internal/storage/image_service.go`:

```go
package storage

import (
    "context"
    "io"

    "github.com/suknna/govirta/internal/storage/diskformat"
    "github.com/suknna/govirta/internal/storage/image"
    "github.com/suknna/govirta/internal/storage/pool"
)

type ImageService struct { pools *pool.Service }
func NewImageService(pools *pool.Service) *ImageService { return &ImageService{pools: pools} }

type PutImageRequest struct { PoolName string; ImageID string; Format diskformat.Format; DeclaredSizeBytes int64 }
type GetImageRequest struct { PoolName string; ImageID string }
type DeleteImageRequest struct { PoolName string; ImageID string }

func (s *ImageService) PutImage(ctx context.Context, req PutImageRequest) (image.ImageWriter, error) {
    if err := ctx.Err(); err != nil { return nil, err }
    if req.PoolName == "" { return nil, pool.ErrPoolRequired }
    return s.pools.PutImage(ctx, req.PoolName, image.PutRequest{ImageID: req.ImageID, Format: req.Format, DeclaredSizeBytes: req.DeclaredSizeBytes})
}
func (s *ImageService) GetImage(ctx context.Context, req GetImageRequest) (io.ReadCloser, error) {
    if err := ctx.Err(); err != nil { return nil, err }
    if req.PoolName == "" { return nil, pool.ErrPoolRequired }
    return s.pools.GetImage(ctx, req.PoolName, image.GetRequest{ImageID: req.ImageID})
}
func (s *ImageService) DeleteImage(ctx context.Context, req DeleteImageRequest) error {
    if err := ctx.Err(); err != nil { return err }
    if req.PoolName == "" { return pool.ErrPoolRequired }
    return s.pools.DeleteImage(ctx, req.PoolName, image.DeleteRequest{ImageID: req.ImageID})
}
```

- [ ] **Step 3: Add reader root volume request**

Modify `internal/storage/service.go`:

```go
type CreateRootVolumeFromReaderRequest struct {
    VMID string; VMName string; PoolName string; Name string; DiskIndex int
    CapacityBytes int64; ReadOnly bool; Reader io.Reader; Format diskformat.Format
}

func (s *VolumeService) CreateRootVolumeFromReader(ctx context.Context, req CreateRootVolumeFromReaderRequest) (volume.Volume, error) {
    if err := ctx.Err(); err != nil { return volume.Volume{}, err }
    if req.PoolName == "" { return volume.Volume{}, pool.ErrPoolRequired }
    if req.Reader == nil || !req.Format.Valid() || req.VMID == "" || req.VMName == "" || req.Name == "" || req.DiskIndex < 0 || req.CapacityBytes <= 0 {
        return volume.Volume{}, volume.ErrInvalidRequest
    }
    volID := volume.ID(fmt.Sprintf("%s-%s-%d", req.VMID, volume.RoleRoot, req.DiskIndex))
    created, err := s.pools.CreateVolumeFromReader(ctx, req.PoolName, block.CreateFromReaderRequest{Reader: req.Reader, Format: req.Format, Name: req.Name, PoolName: req.PoolName, VMID: req.VMID, VMName: req.VMName, VolumeID: volID, DiskIndex: req.DiskIndex, CapacityBytes: req.CapacityBytes, ReadOnly: req.ReadOnly})
    if err != nil { return volume.Volume{}, err }
    created.Role = volume.RoleRoot
    return created, nil
}
```

Add `pool.Service.CreateVolumeFromReader` mirroring `CreateVolume` but calling `driver.CreateFromReader`.

- [ ] **Step 4: Add root service tests**

Add tests proving:
- `PutImage/GetImage/DeleteImage` require explicit pool name and forward requests.
- canceled ctx does not call pool/driver.
- `CreateRootVolumeFromReader` rejects nil reader, invalid format, missing pool, invalid VM fields.
- successful reader root volume sets `RoleRoot` and deterministic ID `<VMID>-root-<DiskIndex>`.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test ./internal/storage ./internal/storage/pool
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/storage/image_service.go internal/storage/service.go internal/storage/service_test.go internal/storage/pool
git commit -m "feat(storage): expose image and reader volume services"
```

---

### Task 5: Local file image driver

**Files:**
- Create: `internal/storage/localfile/driver.go`
- Create: `internal/storage/localfile/driver_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: localfile stores image bytes under driver-owned paths and exposes `image.Driver` without leaking host paths to callers.

Acceptance evidence:
- `go test ./internal/storage/localfile` passes.
- Tests verify no unsafe `ImageID` or `PoolName` can escape `StorageRoot`.

- [ ] **Step 2: Implement localfile driver**

Create `internal/storage/localfile/driver.go` with:

```go
type Config struct { PoolName string; StorageRoot string }
type Driver struct { poolName string; root string }
func NewDriver(config Config) (*Driver, error)
func (d *Driver) DriverInfo(ctx context.Context) (image.DriverInfo, error)
func (d *Driver) Put(ctx context.Context, req image.PutRequest) (image.ImageWriter, error)
func (d *Driver) Get(ctx context.Context, req image.GetRequest) (io.ReadCloser, error)
func (d *Driver) Delete(ctx context.Context, req image.DeleteRequest) error
func (d *Driver) GetActualUsedBytes(ctx context.Context) (int64, error)
```

Use path layout:

```text
<StorageRoot>/pool/<PoolName>/images/<ImageID>/<ImageID>.<format>
```

Implement safe names by copying the local block driver's pattern semantics: reject empty, `.`, `..`, path separators, and control characters.

- [ ] **Step 3: Implement commit/cancel writer**

`Put` creates a temporary file next to the final target and returns a writer. `Close` closes the temporary file and renames it to the final target. `Cancel` closes the temporary file and removes it. Both operations return all errors, joining close/remove errors when both fail.

- [ ] **Step 4: Add localfile tests**

Cover:
- Put/Write/Close/Get reads same bytes.
- Cancel removes tmp and Get returns `image.ErrImageNotFound`.
- duplicate image target returns `image.ErrImageExists`.
- Delete removes image and empty dir.
- unsafe names rejected.
- canceled ctx returns context error before filesystem mutation.
- GetActualUsedBytes counts regular files and observes canceled ctx.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test ./internal/storage/localfile
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/storage/localfile
git commit -m "feat(storage): add local file image driver"
```

---

### Task 6: Local block CreateFromReader full-copy root volumes

**Files:**
- Modify: `internal/storage/local/driver.go`
- Modify: `internal/storage/local/driver_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: local block driver creates standalone qcow2 root volumes from qcow2/raw readers without qcow2 backing links.

Acceptance evidence:
- `go test ./internal/storage/local` passes.
- raw path uses qemuimg convert runner with `-f raw -O qcow2`.
- resize path uses qemuimg resize runner when requested capacity is larger than source virtual size.

- [ ] **Step 2: Implement `CreateFromReader`**

Add method to `internal/storage/local/driver.go`:

```go
func (d *Driver) CreateFromReader(ctx context.Context, req block.CreateFromReaderRequest) (volume.Volume, error) {
    if err := ctx.Err(); err != nil { return volume.Volume{}, err }
    if req.Reader == nil || !req.Format.Valid() { return volume.Volume{}, volume.ErrInvalidRequest }
    targetPath, err := d.pathForCreate(req.PoolName, req.VMID, req.VMName, req.DiskIndex)
    if err != nil { return volume.Volume{}, err }
    tmpPath := targetPath + ".tmp"
    if err := copyReaderToPath(ctx, req.Reader, tmpPath); err != nil {
        return volume.Volume{}, cleanupCreateArtifacts(err, tmpPath, targetPath)
    }
    if err := ctx.Err(); err != nil {
        return volume.Volume{}, cleanupCreateArtifacts(err, tmpPath, targetPath)
    }
    switch req.Format {
    case diskformat.FormatQCOW2:
        if err := os.Rename(tmpPath, targetPath); err != nil {
            return volume.Volume{}, cleanupCreateArtifacts(err, tmpPath, targetPath)
        }
    case diskformat.FormatRaw:
        if err := d.qemuimg.QCOW2().Convert().SourceFormat("raw").Source(tmpPath).Target(targetPath).Do(ctx); err != nil {
            return volume.Volume{}, cleanupCreateArtifacts(err, tmpPath, targetPath)
        }
        if err := os.Remove(tmpPath); err != nil {
            return volume.Volume{}, cleanupCreateArtifacts(err, tmpPath, targetPath)
        }
    }
    if req.CapacityBytes > 0 {
        if err := d.qemuimg.QCOW2().Resize().Path(targetPath).SizeBytes(req.CapacityBytes).Do(ctx); err != nil {
            return volume.Volume{}, cleanupCreateArtifacts(err, tmpPath, targetPath)
        }
    }
    return volume.Volume{ID: req.VolumeID, Name: req.Name, PoolName: req.PoolName, VMID: req.VMID, VMName: req.VMName, Role: volume.RoleRoot, DiskIndex: req.DiskIndex, CapacityBytes: req.CapacityBytes, State: volume.StateAvailable}, nil
}
```

Implementation must check `ctx.Err()` before copy, after copy, before qemuimg convert, before resize, and before cleanup decisions.

- [ ] **Step 3: Add tests**

Cover:
- qcow2 reader bytes are copied to target qcow2 path.
- raw reader triggers convert argv `convert -f raw -O qcow2 tmp target`.
- invalid format and nil reader return `volume.ErrInvalidRequest`.
- target exists returns conflict/invalid request without overwrite.
- context canceled before copy does not call runner.
- convert failure cleans tmp/target and returns runner error.
- cleanup failure is returned with primary error using `errors.Join`.

- [ ] **Step 4: Run targeted verification**

Run:

```bash
go test ./internal/storage/local
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/local
git commit -m "feat(storage): create local volumes from image readers"
```

---

### Task 7: Integration and verification

**Files:**
- Read-only unless verification reveals a spec contradiction. If that happens, update `docs/superpowers/specs/2026-05-28-storage-file-pool-image-design.md` in a separate documented change.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
go test ./internal/virt/qemuimg/...
```

Expected: PASS.

- [ ] **Step 2: Run race tests for concurrent lifecycle**

Run:

```bash
go test -race ./internal/storage/...
```

Expected: PASS.

- [ ] **Step 3: Run local CI equivalent**

Run:

```bash
scripts/verify.sh
```

Expected: PASS, including gofmt check, `go test ./...`, and main service builds.

- [ ] **Step 4: Inspect final diff**

Run:

```bash
git status --short
```

Expected: only intended source/tests/docs changed; spec diff empty unless a documented contradiction was fixed.

- [ ] **Step 5: Final commit if verification-only or spec-sync changes exist**

If only verification documentation or spec-sync changes remain after Tasks 1-6 commits:

```bash
git add docs/superpowers/specs/2026-05-28-storage-file-pool-image-design.md docs/superpowers/plans/2026-05-28-storage-file-pool-image.md
git commit -m "docs(storage): sync file pool image plan"
```

If there are no remaining files after verification, skip this commit.

---

## Plan Self-Review Checklist

- Spec coverage: diskformat, image driver, ImageService, file pool registration, pending index, localfile, reader-derived root volume, qemuimg convert/resize, ctx propagation, error propagation, full-copy no-backing behavior, tests and verification are each covered by a task.
- No hidden defaults: every pool name, format, VMID, VMName, DiskIndex, image ID, and capacity remains explicit.
- No ctx break: every task mentions ctx where it crosses service/driver/runner boundaries.
- No `_ = err`: cleanup and close errors must be returned or joined.
- No libvirt/qemu-nbd/qemu-storage-daemon/qemu-io: all external disk operations go through existing qemuimg runner boundary.
