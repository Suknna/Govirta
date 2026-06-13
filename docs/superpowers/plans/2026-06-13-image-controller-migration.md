# Image Controller Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Image 控制逻辑迁移到 control plane，建立 master-local ImageStore + node image-cache 分发闭环，并让现有 root-disk VM 启动 E2E 走新镜像缓存链路。

**Architecture:** Image 变成集群镜像目录资源：`govirtad` 持有权威 ImageStore 和 etcd 元数据，control-plane ImageController 为每个节点生成 `CacheImage`/`DeleteCachedImage` NodeTask，`govirtlet` 只执行 Task 并 patch `Task.status`。root Volume 不完整迁移，但最小调整为消费本节点 image-cache；ISO 通过显式 `VM.spec.cdromImageRefs` 引用本节点缓存，并进入 VMM `CDROMs` 与 QEMU typed CD/DVD argv。

**Tech Stack:** Go 1.26、etcd-backed `store.Store`、control-plane apiserver/controller-manager、`pkg/apis/{image,task,volume}`、`internal/node/controllers`、`internal/storage`、`pkg/virt/qemu`、`internal/vmm`、`scripts/e2e.sh`。

**Spec:** `docs/superpowers/specs/2026-06-13-image-controller-migration-design.md`

---

## File Structure

### API contracts

- Modify: `pkg/apis/image/v1alpha1/types.go` — Image format/source/version/status/node-cache contract.
- Create: `pkg/apis/image/v1alpha1/types_test.go` — Image spec/status validation tests.
- Modify: `pkg/apis/task/v1alpha1/types.go` — add image Task operations and typed payload validation dispatch.
- Create: `pkg/apis/task/v1alpha1/image_task.go` — image Task input/observed structs and validation helpers.
- Modify: `pkg/apis/task/v1alpha1/types_test.go` — Task validation tests for image operations.
- Modify: `pkg/apis/volume/v1alpha1/types.go` — remove root-volume dependency on `imageFilePoolRef`; root volumes require `imageRef` only.
- Modify: `pkg/apis/vm/v1alpha1/types.go` — add explicit `cdromImageRefs` for ISO image media.
- Modify: `pkg/apis/vm/v1alpha1/types_test.go` — validate CD/DVD references and boot index mode.
- Modify: `pkg/apis/meta/v1alpha1/types.go` — add Image-specific control-plane finalizer constant.
- Modify: existing API round-trip tests — update Image/Task fixtures for the new contract.

### Control plane

- Create: `internal/controlplane/imagestore/store.go` — `ImageStore` interface and request/result types.
- Create: `internal/controlplane/imagestore/local.go` — master-local filesystem-backed ImageStore.
- Create: `internal/controlplane/imagestore/local_test.go` — atomic write, checksum, version conflict, safe path tests.
- Modify: `internal/controlplane/apiserver/server.go` — add ImageStore dependency and upload/download routes.
- Create: `internal/controlplane/apiserver/handler_image_store.go` — upload/download handlers for master-local ImageStore.
- Modify: `internal/controlplane/apiserver/admission/fields.go`, `internal/controlplane/apiserver/admission/registry.go`, `internal/controlplane/apiserver/admission/references.go`, and `internal/controlplane/apiserver/admission/delete.go` — reject legacy `filePoolRef`/unknown Image fields, validate Image CRUD/replace semantics, remove file-pool reverse refs.
- Modify: `internal/controlplane/apiserver/*_test.go` — Image create/get/list/replace/delete/upload route tests.
- Create: `internal/controlplane/controller/image_controller.go` — Image reconcile loop.
- Create: `internal/controlplane/controller/image_tasks.go` — deterministic CacheImage/DeleteCachedImage Task construction and taskRef matching.
- Create: `internal/controlplane/controller/image_status.go` — Task terminal result aggregation into Image status.
- Create: `internal/controlplane/controller/image_controller_test.go` — single-node and multi-node aggregation tests.
- Modify: `internal/controlplane/controller/manager.go` — keep phase-one no-op manager behavior while allowing ImageController to run under control-plane service.
- Modify: `internal/controlplane/service.go` and `cmd/govirtad/main.go` — wire ImageStore/ImageController with explicit `--image-store-root` and `--image-store-public-url`.

### Node execution and storage consumption

- Create: `internal/node/controllers/image_cache.go` — node-local cache service for safe atomic image writes and deletes.
- Create: `internal/node/controllers/image_task.go` — Task operation handlers for CacheImage/DeleteCachedImage.
- Modify: `internal/node/controllers/task.go` — dispatch no-op and image Task handlers without re-executing Running/terminal Tasks.
- Modify wiring only: remove `internal/node/controllers/image.go` registration from `internal/node/agent.go`; leave the file in place until a later cleanup task deletes unused legacy code.
- Modify: `internal/node/agent.go` and `cmd/govirtlet/main.go` — explicit image-cache subdir config under `--runtime-root`; do not register old ImageController.
- Modify: `internal/node/controllers/volume.go` — root Volume reads cached image path via Image status instead of `ImageService.GetImage(filePoolRef)`.
- Modify: `internal/node/controllers/volume_test.go` — root Volume consumes cached image, not file-pool image bytes.
- Modify: `internal/node/controllers/vm.go` / `internal/node/controllers/vm_config.go` — resolve `VM.spec.cdromImageRefs` into VMM `CDROMs` from node image-cache status.
- Modify: `internal/node/controllers/vm_test.go` / `internal/node/controllers/vm_config_test.go` — VM CD-ROM dependency and cold config tests.

### QEMU/VMM CD-ROM support

- Modify: `pkg/virt/qemu/blockdev/blockdev.go` — introduce a typed blockdev interface so QEMU builder is not hard-coded to qcow2.
- Create: `pkg/virt/qemu/blockdev/iso.go` — read-only raw ISO blockdev wrapper.
- Create: `pkg/virt/qemu/device/cdrom.go` — typed SCSI controller + CD/DVD device args.
- Modify: `pkg/virt/qemu/vm.go` — reject generic `-cdrom`/`-drive`; ensure typed CD-ROM participates in builder validation.
- Modify: `pkg/virt/qemu/vm_test.go` — builder golden argv and generic bypass rejection.
- Modify: `internal/vmm/vm.go` — add persisted `CDROMSpec` in `SpecSummary`.
- Modify: `internal/vmm/argv.go` — derive typed CD/DVD argv from `SpecSummary.CDROMs`.
- Modify: `internal/vmm/argv_test.go` — CD/DVD argv derivation and state round-trip tests.

### CLI and E2E

- Modify: `internal/govirtctl/client.go` — upload HTTP client method for `govirtctl image upload`.
- Modify: `internal/govirtctl/command.go` — add `image upload` command with explicit flags.
- Do not modify: `cmd/govirtctl/main.go` — entrypoint already delegates to `govirtctl.Run`.
- Modify: `scripts/e2e.sh` — pass `--image-store-root` and `--image-store-public-url`; prepare/upload CirrOS image through ImageStore.
- Modify: `test/e2e/manifests/03-image.json` — new Image spec without `filePoolRef`, with `version`/`sha256`/`source`.
- Modify: `test/e2e/manifests/04-volume.json` — root Volume no longer carries `imageFilePoolRef`.
- Do not create static ISO manifest: ISO Image is created by `govirtctl image upload --format iso` in `scripts/e2e.sh`.
- Modify: `test/e2e/manifests/07-vm.json` — include explicit `cdromImageRefs` for ISO media.
- Modify: `test/e2e/closure_test.go` — wait for Image cache ready before Volume/VM path and keep VM boot assertion.
- Modify: `test/e2e/guest.go` — add Image cache live-file assertions and running QEMU CD-ROM argv assertions.

---

### Task 1: Image API contract

**Files:**
- Modify: `pkg/apis/image/v1alpha1/types.go:13-133`
- Create: `pkg/apis/image/v1alpha1/types_test.go`
- Modify: `pkg/apis/volume/v1alpha1/types.go:58-103`
- Modify: `pkg/apis/vm/v1alpha1/types.go:130-180`
- Modify: `pkg/apis/vm/v1alpha1/types_test.go`
- Test: `pkg/apis/image/v1alpha1/types_test.go`, `pkg/apis/volume/v1alpha1/types_test.go`, `pkg/apis/vm/v1alpha1/types_test.go`.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Image no longer belongs to a file pool; it has explicit content identity (`format`, `source`, `version`, `declaredSizeBytes`, `sha256`) and node-cache status. Root Volume references only `imageRef`; VM spec explicitly references ISO Images through `cdromImageRefs`.

Acceptance evidence:
- `go test -count=1 ./pkg/apis/image/... ./pkg/apis/volume/... ./pkg/apis/vm/...` passes.
- Tests reject empty `version`, empty/invalid `sha256`, non-positive size, legacy `filePoolRef`, unknown `source.type`, unknown `format`, root Volume with missing `imageRef`, and root Volume with legacy `imageFilePoolRef`.
- Tests reject invalid VM `cdromImageRefs`: empty image ref, duplicate image ref, boot index value without explicit mode, and negative boot index.

- [ ] **Step 2: Replace Image format/source/status contract**

Update `pkg/apis/image/v1alpha1/types.go` to this shape, preserving package comments and existing exported error names:

```go
type ImageFormat string

const (
    ImageFormatQCOW2 ImageFormat = "qcow2"
    ImageFormatRaw   ImageFormat = "raw"
    ImageFormatISO   ImageFormat = "iso"
)

func (f ImageFormat) Valid() bool {
    switch f {
    case ImageFormatQCOW2, ImageFormatRaw, ImageFormatISO:
        return true
    default:
        return false
    }
}

type ImageSourceType string

const (
    ImageSourceUpload ImageSourceType = "upload"
    ImageSourceHTTP   ImageSourceType = "http"
)

func (t ImageSourceType) Valid() bool {
    switch t {
    case ImageSourceUpload, ImageSourceHTTP:
        return true
    default:
        return false
    }
}

type ImagePhase string

const (
    ImagePhasePending  ImagePhase = "pending"
    ImagePhaseCaching  ImagePhase = "caching"
    ImagePhaseReady    ImagePhase = "ready"
    ImagePhaseDeleting ImagePhase = "deleting"
    ImagePhaseFailed   ImagePhase = "failed"
)

type ImageCachePhase string

const (
    ImageCachePhasePending  ImageCachePhase = "pending"
    ImageCachePhaseCaching  ImageCachePhase = "caching"
    ImageCachePhaseReady    ImageCachePhase = "ready"
    ImageCachePhaseDeleting ImageCachePhase = "deleting"
    ImageCachePhaseFailed   ImageCachePhase = "failed"
)

type ImageSource struct {
    Type     ImageSourceType `json:"type"`
    Location string          `json:"location"`
}

type TaskRef struct {
    Name string `json:"name"`
    UID  string `json:"uid"`
}

type NodeCacheStatus struct {
    NodeName   string          `json:"nodeName"`
    Phase      ImageCachePhase `json:"phase"`
    TaskRef    TaskRef         `json:"taskRef"`
    CachedPath string          `json:"cachedPath,omitempty"`
    SizeBytes  int64           `json:"sizeBytes,omitempty"`
    SHA256     string          `json:"sha256,omitempty"`
    Message    string          `json:"message,omitempty"`
}

type ImageSpec struct {
    Source            ImageSource `json:"source"`
    Format            ImageFormat `json:"format"`
    Version           string      `json:"version"`
    DeclaredSizeBytes int64       `json:"declaredSizeBytes"`
    SHA256            string      `json:"sha256"`
}

type ImageStatus struct {
    Phase             ImagePhase        `json:"phase"`
    ObservedVersion   string            `json:"observedVersion,omitempty"`
    ObservedSHA256    string            `json:"observedSHA256,omitempty"`
    ObservedSizeBytes int64             `json:"observedSizeBytes,omitempty"`
    NodeCaches        []NodeCacheStatus `json:"nodeCaches,omitempty"`
    Message           string            `json:"message,omitempty"`
}
```

Validation rules:
- `ImageSpec.Validate` requires valid source type, non-empty location, valid format, non-empty version, `declaredSizeBytes > 0`, and a lowercase 64-hex-character SHA256.
- `ImageSourceUpload` location is an ImageStore object path or URL generated by control plane; do not infer location when empty.
- `ImageStatus.Validate` requires known phase. `Ready` requires non-empty observed version/SHA256, positive observed size, at least one node cache, and every node cache `Ready` with matching SHA256 and positive size. `Failed` requires a non-empty message.
- `NodeCacheStatus.Validate` requires node name, valid phase, and task ref name/UID. `Ready` requires cached path, positive size, and valid SHA256. `Failed` requires message.

- [ ] **Step 2a: Add Image-specific finalizer**

Update `pkg/apis/meta/v1alpha1/types.go`:

```go
const FinalizerImageCache Finalizer = "govirta.io/image-cache"
```

Use this finalizer only for Image cache cleanup. Do not reuse `FinalizerNodeTeardown`, because node business controllers no longer own Image deletion.

- [ ] **Step 3: Remove root Volume file-pool image coupling**

Update `pkg/apis/volume/v1alpha1/types.go`:

```go
type VolumeSpec struct {
    PoolRef       string     `json:"poolRef"`
    VMRef         string     `json:"vmRef"`
    VMName        string     `json:"vmName"`
    DiskIndex     int        `json:"diskIndex"`
    CapacityBytes int64      `json:"capacityBytes"`
    Role          VolumeRole `json:"role"`
    ImageRef      string     `json:"imageRef,omitempty"`
}
```

Root validation requires `ImageRef`; data validation rejects `ImageRef`. Delete `ImageFilePoolRef` from API and tests. If any JSON fixture still uses `imageFilePoolRef`, update it in the E2E task rather than tolerating it.

- [ ] **Step 3a: Add explicit VM CD-ROM image references**

Update `pkg/apis/vm/v1alpha1/types.go`:

```go
type BootIndexMode string

const (
    BootIndexModeUnset BootIndexMode = "unset"
    BootIndexModeIndex BootIndexMode = "index"
)

type CDROMImageRef struct {
    ImageRef      string        `json:"imageRef"`
    BootIndexMode BootIndexMode `json:"bootIndexMode"`
    BootIndex     *int          `json:"bootIndex,omitempty"`
}

type VMSpec struct {
    Arch            string          `json:"arch"`
    VCPUs           int             `json:"vcpus"`
    MemoryMiB       int             `json:"memoryMiB"`
    VolumeRefs      []string        `json:"volumeRefs"`
    CDROMImageRefs  []CDROMImageRef `json:"cdromImageRefs,omitempty"`
    NICRefs         []string        `json:"nicRefs"`
    PowerState      PowerState      `json:"powerState"`
    PowerOffMode    PowerOffMode    `json:"powerOffMode,omitempty"`
}
```

Validation rules:
- `ImageRef` is required.
- duplicate `ImageRef` values in `cdromImageRefs` are invalid.
- `BootIndexMode=unset` requires `BootIndex == nil` and omits `bootIndex` from JSON.
- `BootIndexMode=index` requires `BootIndex != nil` and `*BootIndex >= 0`; explicit zero must be encoded as `"bootIndex":0`.
- unknown boot index mode is invalid.
- `cdromImageRefs` does not replace `volumeRefs`; at least one root/data volume rule remains unchanged.

- [ ] **Step 4: Add API validation tests**

Create tests with these names:

```go
func TestImageSpecValidateRequiresContentIdentity(t *testing.T) {}
func TestImageStatusValidateRequiresReadyNodeCaches(t *testing.T) {}
func TestImageSpecValidateRejectsUnknownFormat(t *testing.T) {}
func TestVolumeSpecValidateRootRequiresOnlyImageRef(t *testing.T) {}
func TestVolumeSpecValidateDataRejectsImageRef(t *testing.T) {}
func TestMetaDefinesImageCacheFinalizer(t *testing.T) {}
func TestVMSpecValidateAcceptsCDROMImageRefs(t *testing.T) {}
func TestVMSpecValidateRejectsInvalidCDROMBootIndex(t *testing.T) {}
func TestVMSpecValidateRejectsDuplicateCDROMImageRefs(t *testing.T) {}
```

Each test should call the exported `Validate` method and assert `errors.Is(err, ErrInvalidSpec)` or `errors.Is(err, ErrInvalidStatus)` for invalid cases.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./pkg/apis/image/... ./pkg/apis/volume/...
go test -count=1 ./pkg/apis/vm/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/apis/image/v1alpha1 pkg/apis/volume/v1alpha1 pkg/apis/vm/v1alpha1 pkg/apis/meta/v1alpha1
git commit -m "feat(api): redefine image as cached cluster resource"
```

---

### Task 2: Image Task operations

**Files:**
- Modify: `pkg/apis/task/v1alpha1/types.go:33-235`
- Create: `pkg/apis/task/v1alpha1/image_task.go`
- Modify: `pkg/apis/task/v1alpha1/types_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: Task API can validate `CacheImage` and `DeleteCachedImage` NodeTasks without accepting invalid raw JSON or relying on no-op-only validation.

Acceptance evidence:
- `go test -count=1 ./pkg/apis/task/...` passes.
- Tests prove invalid `CacheImageInput` fails Task validation and valid payload succeeds.

- [ ] **Step 2: Add operations and payload structs**

Add constants:

```go
const (
    TaskOperationNoopCluster           TaskOperation = "NoopCluster"
    TaskOperationNoopNode              TaskOperation = "NoopNode"
    TaskOperationCacheImageNode        TaskOperation = "CacheImageNode"
    TaskOperationDeleteCachedImageNode TaskOperation = "DeleteCachedImageNode"
)
```

Add source and payload types:

```go
type ImageTaskSourceType string

const (
    ImageTaskSourceUpload ImageTaskSourceType = "upload"
    ImageTaskSourceHTTP   ImageTaskSourceType = "http"
)

type CacheImageInput struct {
    ImageName         string              `json:"imageName"`
    ImageUID          string              `json:"imageUID"`
    Version           string              `json:"version"`
    Format            string              `json:"format"`
    Source            ImageTaskSource     `json:"source"`
    DeclaredSizeBytes int64               `json:"declaredSizeBytes"`
    SHA256            string              `json:"sha256"`
    CacheRoot         string              `json:"cacheRoot"`
}

type ImageTaskSource struct {
    Type     ImageTaskSourceType `json:"type"`
    Location string              `json:"location"`
}

type CacheImageObserved struct {
    NodeName   string `json:"nodeName"`
    ImageName  string `json:"imageName"`
    Version    string `json:"version"`
    Format     string `json:"format"`
    CachedPath string `json:"cachedPath"`
    SizeBytes  int64  `json:"sizeBytes"`
    SHA256     string `json:"sha256"`
}

type DeleteCachedImageInput struct {
    ImageName string `json:"imageName"`
    ImageUID  string `json:"imageUID"`
    Version   string `json:"version"`
    SHA256    string `json:"sha256"`
    CacheRoot string `json:"cacheRoot"`
}

type DeleteCachedImageObserved struct {
    NodeName  string `json:"nodeName"`
    ImageName string `json:"imageName"`
    Version   string `json:"version"`
    Deleted   bool   `json:"deleted"`
}
```

- [ ] **Step 3: Dispatch validation by operation**

Update `TaskOperation.Valid` and `TaskSpec.ValidateWithMetadata` so:
- `NoopCluster`/`NoopNode` still decode `NoopInput`.
- `CacheImageNode` requires `Scope=Node`, `metadata.nodeName` non-empty, and valid `CacheImageInput`.
- `DeleteCachedImageNode` requires `Scope=Node`, `metadata.nodeName` non-empty, and valid `DeleteCachedImageInput`.
- Unknown operation is invalid.

`CacheImageInput.Validate` requires non-empty image name/UID/version/cache root, valid format (`qcow2|raw|iso` as strings to avoid importing image API into task API if that would create a cycle), valid source type, non-empty source location, positive declared size, and valid SHA256.

- [ ] **Step 4: Validate terminal observed payloads at aggregation boundaries**

Keep existing `TaskStatus.Validate` for generic phase invariants. Add operation-specific observed decoders in `image_task.go`:

```go
func DecodeCacheImageObserved(raw json.RawMessage) (CacheImageObserved, error)
func DecodeDeleteCachedImageObserved(raw json.RawMessage) (DeleteCachedImageObserved, error)
```

Control-plane ImageController must call these decoders before aggregating Task results. This avoids broad rewrites to `TaskStatus.Validate` while still preventing malformed observed payloads from updating Image status.

- [ ] **Step 5: Add Task tests**

Add tests:

```go
func TestTaskValidateAcceptsCacheImageNode(t *testing.T) {}
func TestTaskValidateRejectsCacheImageWithoutNodeName(t *testing.T) {}
func TestTaskValidateRejectsCacheImageBadChecksum(t *testing.T) {}
func TestTaskValidateRejectsDeleteCachedImageBadScope(t *testing.T) {}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test -count=1 ./pkg/apis/task/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/apis/task/v1alpha1
git commit -m "feat(task): add image cache operations"
```

---

### Task 3: Master-local ImageStore

**Files:**
- Create: `internal/controlplane/imagestore/store.go`
- Create: `internal/controlplane/imagestore/local.go`
- Create: `internal/controlplane/imagestore/local_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: provide a swappable control-plane ImageStore that writes upload bytes atomically under explicit `--image-store-root`, validates size/SHA256, exposes a stable local path for HTTP download, and rejects same name/version with different SHA256.

Acceptance evidence:
- `go test -count=1 ./internal/controlplane/imagestore/...` passes.
- Tests prove no `/tmp`, symlink escape, or path traversal can write outside the ImageStore root.

- [ ] **Step 2: Define interface and metadata types**

Create `store.go`:

```go
package imagestore

import (
    "context"
    "io"
)

type PutRequest struct {
    Name              string
    Version           string
    Format            string
    DeclaredSizeBytes int64
    SHA256            string
    Reader            io.Reader
}

type ObjectRef struct {
    Name      string `json:"name"`
    Version   string `json:"version"`
    Format    string `json:"format"`
    SizeBytes int64  `json:"sizeBytes"`
    SHA256    string `json:"sha256"`
    Path      string `json:"path"`
}

type Store interface {
    Put(ctx context.Context, req PutRequest) (ObjectRef, error)
    Get(ctx context.Context, name, version string) (ObjectRef, error)
    Open(ctx context.Context, name, version string) (io.ReadCloser, ObjectRef, error)
    Delete(ctx context.Context, name, version string, sha256 string) error
}
```

Define sentinel errors in the same package: `ErrInvalidRequest`, `ErrNotFound`, `ErrConflict`, `ErrChecksumMismatch`, `ErrUnsafePath`.

- [ ] **Step 3: Implement local store layout**

Create `local.go` with constructor:

```go
func NewLocal(root string) (*LocalStore, error)
```

Validation rules:
- `root` must be absolute and non-empty.
- `name` and `version` use the same safe segment policy as storage (`[A-Za-z0-9._-]+`, reject `.` and `..`).
- final object directory is `<root>/images/<name>/<version>/`.
- write to `upload.tmp.<pid-or-random>` inside the object dir, hash while copying, fsync if existing local helpers support it, then `os.Rename` to `image`.
- write `metadata.json` only after `image` exists.
- if existing metadata has same SHA256/size/format, `Put` is idempotent success.
- if existing metadata differs, return `ErrConflict` and do not overwrite.

- [ ] **Step 4: Add local store tests**

Test names:

```go
func TestLocalStorePutCommitsAtomically(t *testing.T) {}
func TestLocalStorePutRejectsChecksumMismatch(t *testing.T) {}
func TestLocalStorePutIdempotentForSameContent(t *testing.T) {}
func TestLocalStorePutRejectsSameVersionDifferentHash(t *testing.T) {}
func TestLocalStoreRejectsUnsafeNames(t *testing.T) {}
func TestLocalStoreDeleteRequiresMatchingSHA256(t *testing.T) {}
```

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/controlplane/imagestore/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controlplane/imagestore
git commit -m "feat(controlplane): add local image store"
```

---

### Task 4: Image upload/download API and CLI

**Files:**
- Modify: `internal/controlplane/apiserver/server.go`
- Create: `internal/controlplane/apiserver/handler_image_store.go`
- Modify: `internal/controlplane/apiserver/handler_apply.go`
- Modify: `internal/controlplane/apiserver/handler_replace.go`
- Modify: `internal/controlplane/apiserver/handler_delete.go`
- Modify: `internal/controlplane/apiserver/handler_finalizers.go`
- Modify: `internal/controlplane/apiserver/admission/*.go`
- Modify: `internal/govirtctl/client.go`
- Modify: `internal/govirtctl/command.go`
- Test: create `internal/controlplane/apiserver/handler_image_store_test.go`; modify/create `internal/govirtctl/command_image_test.go`.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: users can create/read/list/replace/delete Image resources and upload bytes to ImageStore without letting etcd store blobs or accepting legacy `filePoolRef`.

Acceptance evidence:
- `go test -count=1 ./internal/controlplane/apiserver ./internal/govirtctl` passes.
- Tests cover upload route, Image apply/replace/delete, and legacy `filePoolRef` rejection.
- Tests cover unknown-field fail-closed behavior for Image JSON so `filePoolRef` cannot be silently ignored by `json.Unmarshal`.

- [ ] **Step 2: Add apiserver ImageStore dependency**

Extend `apiserver.Server` with:

```go
imageStore          imagestore.Store
imageStorePublicURL string
```

Introduce an explicit `ServerConfig` struct in apiserver and update all construction sites to use it. Do not add more positional parameters to `NewServer`.

`imageStorePublicURL` is required when `imageStore != nil` because node Tasks need a download URL that is not inferred from listener address.

- [ ] **Step 3: Add upload/download routes**

In `Server.Handler`, register explicit routes:

```go
mux.HandleFunc("PUT /apis/Image/{name}/store/{version}", s.UploadImage)
mux.HandleFunc("GET /apis/Image/{name}/store/{version}", s.DownloadImage)
```

`UploadImage` reads query/header fields explicitly:
- `format`
- `sha256`
- `declaredSizeBytes`

It calls `imageStore.Put`, then writes/updates the Image object in store with `source.type=upload`, `source.location=<imageStorePublicURL>/apis/Image/<name>/store/<version>`, and the explicit version/format/size/SHA256. It must not mark Image ready; status starts `pending` or `caching`.

`DownloadImage` opens the store object and streams bytes. It must not expose arbitrary paths.

- [ ] **Step 4: Tighten Image admission**

Apply admission rules:
- Image create/replace rejects any JSON carrying `spec.filePoolRef`.
- Image create/replace uses strict JSON decoding or raw-field inspection so unknown legacy fields are rejected fail-closed before typed admission.
- Image `source.type=upload` requires source location to match the configured public ImageStore URL prefix generated by the server.
- Image `source.type=http` requires explicit external URL, size, SHA256, version, and format.
- Replace requires `metadata.resourceVersion`; changing `version/SHA256/size/source` is a new explicit version update and must reset status to `pending` or `caching`.
- Apply injects `metav1.FinalizerImageCache`; delete keeps that finalizer until ImageController confirms cache cleanup and ImageStore deletion.
- `ReferenceValidator` no longer requires `Image.spec.filePoolRef` and must still validate any remaining `Volume.spec.imageRef` reference.
- `ReverseReferenceValidator` no longer blocks StoragePool deletion through Image filePoolRef, but must continue blocking Image deletion while a Volume references it.

- [ ] **Step 5: Add govirtctl image upload**

Extend `internal/govirtctl/command.go` command parsing:

```text
govirtctl image upload --name <name> --version <version> --format <qcow2|raw|iso> --file <path>
```

CLI behavior:
- read file path explicitly;
- compute size and SHA256 locally;
- call new `Client.UploadImage(ctx, name, version, format, size, sha256, reader)`;
- print the returned Image JSON or no output consistent with current command style.

Do not infer version from hash in this task.

- [ ] **Step 6: Add API/CLI tests**

Test names:

```go
func TestUploadImageStoresBytesAndCreatesImageMetadata(t *testing.T) {}
func TestDownloadImageStreamsStoredBytes(t *testing.T) {}
func TestApplyImageRejectsLegacyFilePoolRef(t *testing.T) {}
func TestApplyImageRejectsUnknownLegacyFields(t *testing.T) {}
func TestReplaceImageRequiresResourceVersionForNewVersion(t *testing.T) {}
func TestDeleteImageKeepsFinalizerUntilControllerCleanup(t *testing.T) {}
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/controlplane/apiserver ./internal/govirtctl
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/controlplane/apiserver internal/govirtctl
git commit -m "feat(apiserver): add image store upload API"
```

---

### Task 5: Control-plane ImageController

**Files:**
- Create: `internal/controlplane/controller/image_controller.go`
- Create: `internal/controlplane/controller/image_tasks.go`
- Create: `internal/controlplane/controller/image_status.go`
- Create: `internal/controlplane/controller/image_controller_test.go`
- Modify: `internal/controlplane/controller/task_client.go`
- Modify: `internal/controlplane/controller/manager.go`
- Modify: `internal/controlplane/service.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: control plane owns Image status, finalizer, node target fan-out, CacheImage/DeleteCachedImage Task creation, and Task terminal aggregation.

Acceptance evidence:
- `go test -count=1 ./internal/controlplane/controller` passes.
- Tests prove all nodes ready => Image ready, one node failed => Image failed, old taskRef ignored, delete waits for DeleteCachedImage tasks.

- [ ] **Step 2: Extend TaskClient only where necessary**

Current `TaskClient` has `CreateOrGetTask(ctx, task)`, `PatchStatus(ctx, name, status)`, and private `taskKey`. Add read/list helpers rather than bypassing store in every controller:

```go
func (c *TaskClient) GetTask(ctx context.Context, name string) (taskv1.Task, error)
func (c *TaskClient) ListTasks(ctx context.Context) ([]taskv1.Task, error)
```

Keep `CreateOrGetTask` spec equality check so deterministic task reuse remains explicit.

- [ ] **Step 3: Implement deterministic task construction**

`image_tasks.go` should expose unexported helpers:

```go
func cacheImageTask(img imagev1.Image, nodeName string, cacheRoot string) (taskv1.Task, error)
func deleteCachedImageTask(img imagev1.Image, nodeName string, cacheRoot string) (taskv1.Task, error)
func imageTaskName(prefix, imageName, version, nodeName string) string
```

Task metadata:
- `Kind=Task`, `APIVersion=govirta.io/v1alpha1`;
- deterministic `Name`, deterministic `UID` derived from operation/image UID/version/node;
- `NodeName=nodeName`;
- `OwnerKind=Image`, owner name/UID from Image metadata;
- `Scope=Node`;
- `Operation=CacheImageNode` or `DeleteCachedImageNode`;
- `Status.Phase=Pending`.

- [ ] **Step 4: Implement ImageController reconcile**

Controller loop responsibilities:
- list/watch Image objects via store;
- ensure `metav1.FinalizerImageCache` for non-deleting images;
- for each configured node, create/reuse matching CacheImage Task;
- set Image status `caching` with node cache taskRefs;
- on matching Task `Succeeded`, decode `CacheImageObserved` and update node cache ready;
- on matching Task `Failed`, update node cache failed and Image failed;
- once all configured nodes are ready for current version/SHA256, set Image ready;
- if Image has deletion timestamp, create DeleteCachedImage Tasks, wait for success, delete ImageStore object, remove finalizer.

Do not read node filesystem or write image bytes.

- [ ] **Step 5: Wire into control-plane service**

Add explicit config fields:

```go
ImageStoreRoot      string
ImageStorePublicURL string
ImageCacheDirName   string
```

`cmd/govirtad/main.go` must require `--image-store-root` and `--image-store-public-url`. Existing tests must set these fields explicitly.

Service Run should start apiserver and the existing phase-one Task manager plus ImageController without letting a normally idle controller terminate the process.

- [ ] **Step 6: Add controller tests**

Test names:

```go
func TestImageControllerCreatesCacheTaskPerNode(t *testing.T) {}
func TestImageControllerMarksReadyAfterAllNodeTasksSucceed(t *testing.T) {}
func TestImageControllerIgnoresOldTaskRef(t *testing.T) {}
func TestImageControllerFailsClosedOnNodeTaskFailure(t *testing.T) {}
func TestImageControllerDeleteWaitsForCacheDeletionBeforeFinalizerRemoval(t *testing.T) {}
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/controlplane ./internal/controlplane/controller
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/controlplane cmd/govirtad/main.go
git commit -m "feat(controlplane): reconcile image cache tasks"
```

---

### Task 6: Node ImageTaskExecutor and cache service

**Files:**
- Create: `internal/node/controllers/image_cache.go`
- Create: `internal/node/controllers/image_task.go`
- Modify: `internal/node/controllers/task.go`
- Modify: `internal/node/controllers/task_test.go`
- Modify: `internal/node/agent.go`
- Modify: `cmd/govirtlet/main.go`
- Stop wiring: `internal/node/controllers/image.go` in `internal/node/agent.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: govirtlet executes only assigned image cache Tasks, writes verified bytes under `<runtimeRoot>/image-cache`, and reports results through Task status. The old node ImageController no longer watches Image resources.

Acceptance evidence:
- `go test -count=1 ./internal/node/...` passes.
- Tests prove cache idempotency, checksum mismatch failure, same version different hash failure, Running no-op behavior, and old ImageController not registered.

- [ ] **Step 2: Create cache service**

Implement:

```go
type ImageCache struct {
    root string
    httpc *http.Client
}

func NewImageCache(root string, httpc *http.Client) (*ImageCache, error)
func (c *ImageCache) Cache(ctx context.Context, input taskv1.CacheImageInput) (taskv1.CacheImageObserved, error)
func (c *ImageCache) Delete(ctx context.Context, input taskv1.DeleteCachedImageInput) (taskv1.DeleteCachedImageObserved, error)
```

Rules:
- root must be explicit and absolute, derived from `filepath.Join(runtimeRoot, imageCacheDirName)` in composition root;
- cache path `<root>/<imageName>/<version>/image`;
- temp path in same dir;
- validate final path stays under root;
- stream HTTP/upload URL through SHA256 hash;
- size and SHA256 must match input;
- existing same SHA256 is success;
- existing different SHA256 fails closed;
- delete requires matching image/version/SHA256 metadata when present.

- [ ] **Step 3: Extend TaskController dispatch**

Change `TaskController` to accept a handler map:

```go
type TaskHandler interface {
    Operation() taskv1.TaskOperation
    Execute(ctx context.Context, task taskv1.Task) (json.RawMessage, error)
}
```

Keep existing no-op behavior as a `NoopNodeHandler`. Add `ImageTaskHandler` for `CacheImageNode` and `DeleteCachedImageNode`.

Reconcile rules:
- ignore non-matching node;
- ignore `Running` and terminal Tasks;
- patch `Running` before execution;
- on success patch `Succeeded` with observed JSON;
- on failure patch `Failed` with stable class (`ValidationFailed`, `ChecksumMismatch`, `TransientIO`, or existing nearest Task error class).

- [ ] **Step 4: Wire govirtlet**

In `node.Config`, add:

```go
ImageCacheDirName string
```

`cmd/govirtlet` exposes `--image-cache-dir-name`, with default set only in command parsing. `node.NewAgent` constructs `NewImageCache(filepath.Join(cfg.RuntimeRoot, cfg.ImageCacheDirName), http.DefaultClient)` and passes image handlers to TaskController.

Remove `NewImageController(...)` from controller manager registration. Keep `internal/node/controllers/image.go` only as unregistered legacy code during this task if compile still requires its types; no runtime path may instantiate it.

- [ ] **Step 5: Add node tests**

Test names:

```go
func TestImageCacheCachesHTTPBytesAtomically(t *testing.T) {}
func TestImageCacheRejectsChecksumMismatch(t *testing.T) {}
func TestImageCacheIdempotentForSameVersionAndHash(t *testing.T) {}
func TestImageCacheRejectsSameVersionDifferentHash(t *testing.T) {}
func TestTaskControllerExecutesCacheImageTask(t *testing.T) {}
func TestTaskControllerDoesNotReexecuteRunningImageTask(t *testing.T) {}
```

- [ ] **Step 6: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/node/...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/node cmd/govirtlet/main.go
git commit -m "feat(node): execute image cache tasks"
```

---

### Task 7: Root Volume consumes node image-cache

**Files:**
- Modify: `internal/node/controllers/volume.go`
- Modify: `internal/node/controllers/volume_test.go`
- Do not modify: `internal/storage/service.go` — use existing `CreateRootVolumeFromReader` with explicit format.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: root Volume creation reads source bytes from Image status cache for the same node, not from `ImageService.GetImage(filePoolRef)` or old file pool metadata.

Acceptance evidence:
- `go test -count=1 ./internal/node/controllers -run 'TestVolume.*Root|Test.*ImageCache' -v` passes.
- Tests prove root Volume waits until Image cache for this node is ready and uses cached path/format/SHA256.

- [ ] **Step 2: Replace ImageGetter dependency**

Remove `ImageGetter` use from `VolumeController` for root creation. It should still use `DependencyReader.Get/List/PatchStatus` to read Image API object status. `createRootVolume` should:
- get `Image` by `vol.Spec.ImageRef`;
- require `Image.Status.Phase=Ready`;
- find `NodeCacheStatus` where `nodeName == vol.NodeName`;
- require cache phase ready, cached path non-empty, SHA256 matches `Image.Spec.SHA256`;
- open cached path with safe local open helper;
- map `Image.Spec.Format` to `diskformat.Format` for `qcow2/raw`; reject `iso` for root Volume with `ErrInvalidSpec`/permanent failure.

- [ ] **Step 3: Keep independent root copy semantics**

Call existing:

```go
VolumeService.CreateRootVolumeFromReader(ctx, storage.CreateRootVolumeFromReaderRequest{
    PoolName: vol.Spec.PoolRef,
    VMID: vol.Spec.VMRef,
    VMName: vol.Spec.VMName,
    DiskIndex: vol.Spec.DiskIndex,
    CapacityBytes: vol.Spec.CapacityBytes,
    Format: mappedFormat,
    Reader: reader,
})
```

Do not pass cached path into storage driver as authoritative metadata; path is only a source reader.

- [ ] **Step 4: Add/update Volume tests**

Test names:

```go
func TestVolumeControllerCreatesRootFromNodeImageCache(t *testing.T) {}
func TestVolumeControllerWaitsForImageCacheOnThisNode(t *testing.T) {}
func TestVolumeControllerRejectsISOAsRootDiskSource(t *testing.T) {}
func TestVolumeControllerDoesNotUseImageFilePoolRef(t *testing.T) {}
```

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/node/controllers -run 'TestVolume|Test.*ImageCache' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/node/controllers internal/storage
git commit -m "feat(node): create root volumes from image cache"
```

---

### Task 8: VM controller resolves CD-ROM image refs

**Files:**
- Modify: `internal/node/controllers/vm.go`
- Modify: `internal/node/controllers/vm_config.go`
- Modify: `internal/node/controllers/vm_test.go`
- Modify: `internal/node/controllers/vm_config_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: VM controller converts `VM.spec.cdromImageRefs` into VMM `CDROMSpec` entries by reading Image status for the same node and requiring cached ISO readiness.

Acceptance evidence:
- `go test -count=1 ./internal/node/controllers -run 'TestVM.*CDROM|Test.*Config.*CDROM' -v` passes.
- Tests prove qcow2/raw Images are rejected for CD-ROM refs, ISO cache not ready blocks VM reconcile, and ready ISO cache appears in VMM spec.

- [ ] **Step 2: Resolve CD-ROM dependencies**

Where VM controller currently gathers volumes/NICs into `vmm.SpecSummary`, add Image dependency reads for each `vm.Spec.CDROMImageRefs` entry:
- get Image by `ImageRef`;
- require `Image.Spec.Format == imagev1.ImageFormatISO`;
- require `Image.Status.Phase == imagev1.ImagePhaseReady`;
- find `NodeCacheStatus` for `vm.Metadata.NodeName` or current controller node name;
- require cache phase ready, cached path non-empty, and SHA256 equals `Image.Spec.SHA256`;
- append `vmm.CDROMSpec{Path: cachedPath, Format: "iso", BootIndexMode: mapped mode, BootIndex: value}`.

Do not read external URL or ImageStore from VM controller.

- [ ] **Step 3: Preserve cold config behavior**

`cdromImageRefs` is cold-mutable like `volumeRefs` and `nicRefs`: replacing it while VM is Off should call `vmm.Redefine`; replacing it while On should be rejected by admission in the apiserver field policy task or left blocked by existing cold-mutable validation extended to include the new field.

- [ ] **Step 4: Add VM controller tests**

Test names:

```go
func TestVMControllerAddsCDROMsFromReadyISOImageCache(t *testing.T) {}
func TestVMControllerWaitsForCDROMImageCache(t *testing.T) {}
func TestVMControllerRejectsNonISOCDROMImage(t *testing.T) {}
func TestVMConfigDriftIncludesCDROMImageRefs(t *testing.T) {}
```

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/node/controllers -run 'TestVM.*CDROM|Test.*Config.*CDROM' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/node/controllers
git commit -m "feat(node): resolve vm cdrom image refs"
```

---

### Task 9: QEMU CD/DVD typed argv

**Files:**
- Modify: `pkg/virt/qemu/blockdev/blockdev.go`
- Create: `pkg/virt/qemu/blockdev/iso.go`
- Create: `pkg/virt/qemu/blockdev/iso_test.go`
- Create: `pkg/virt/qemu/device/cdrom.go`
- Create: `pkg/virt/qemu/device/cdrom_test.go`
- Modify: `pkg/virt/qemu/vm.go`
- Modify/Create: `pkg/virt/qemu/*_test.go`, `pkg/virt/qemu/device/*_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: QEMU builder can express a read-only CD/DVD ISO through typed `-blockdev` + `-device`, while generic `-cdrom` and `-drive` remain rejected.

Acceptance evidence:
- `go test -count=1 ./pkg/virt/qemu/...` passes.
- Golden argv includes explicit read-only blockdev and CD-ROM device; generic bypass tests fail closed.

- [ ] **Step 2: Generalize typed blockdev input**

Current `Builder.AddBlockdev` accepts only `blockdev.Qcow2`, which would force every new block backend to edit builder signatures. Introduce an interface in `pkg/virt/qemu/blockdev/blockdev.go`:

```go
type Blockdev interface {
    Arg() (string, error)
}
```

Then change `qemu.Builder` and `qemu.VM` internal `blockdevs` slice plus `AddBlockdev` signature to use `blockdev.Blockdev`. `blockdev.Qcow2` already satisfies this interface.

- [ ] **Step 3: Add read-only ISO blockdev support**

Create `pkg/virt/qemu/blockdev/iso.go`:

```go
type ISO struct {
    NodeName string
    File     FileProtocol
    Cache    Cache
    AIO      AIO
}
```

`Validate` requires node name and file filename. `Arg` always renders read-only raw semantics:

```text
driver=raw,node-name=<node>,file.driver=file,file.filename=<path>,read-only=on
```

Do not expose a writable flag; ISO in this project is always read-only install media.

- [ ] **Step 4: Add SCSI controller and CD-ROM device types**

Create `device/cdrom.go`:

```go
type VirtioSCSIPCI struct {
    ID string
}

type SCSICD struct {
    ID        string
    Drive     blockdev.Ref
    Bus       string
    SCSIID    qflag.OptionalInt
    BootIndex qflag.OptionalInt
}
```

Render examples:

```text
virtio-scsi-pci,id=scsi0
scsi-cd,id=cd0,drive=iso0,bus=scsi0.0,scsi-id=0[,bootindex=<n>]
```

Inputs must be explicit. Optional `BootIndex` absence means “do not set bootindex”, not “default to 0”.

- [ ] **Step 5: Keep generic argument policy strict**

Update `validateGenericArgumentPolicy` tests to reject:

```go
qemu.Arg("-cdrom", "/image.iso")
qemu.Arg("-drive", "file=/image.iso,media=cdrom")
```

Do not add `-cdrom` or `-drive` to the allowlist.

- [ ] **Step 6: Add golden tests**

Test names:

```go
func TestISOBlockdevBuildsReadOnlyRawArg(t *testing.T) {}
func TestISOBlockdevRequiresNodeNameAndFile(t *testing.T) {}
func TestSCSICDDeviceBuildsArg(t *testing.T) {}
func TestQEMUBuilderBuildsCDROMArgv(t *testing.T) {}
func TestGenericArgumentsRejectCDROMShortcuts(t *testing.T) {}
```

- [ ] **Step 7: Run targeted verification**

Run:

```bash
go test -count=1 ./pkg/virt/qemu/...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/virt/qemu
git commit -m "feat(qemu): add typed cdrom device"
```

---

### Task 10: VMM CD-ROM argv derivation

**Files:**
- Modify: `internal/vmm/vm.go`
- Modify: `internal/vmm/argv.go`
- Modify: `internal/vmm/argv_test.go`

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: VMM persisted `SpecSummary` carries explicit CD-ROM media and derives deterministic typed QEMU CD/DVD argv.

Acceptance evidence:
- `go test -count=1 ./internal/vmm -run 'TestDeriveBuilder.*CDROM|TestEncodeDecodeStateRoundTrip' -v` passes.

- [ ] **Step 2: Add explicit CDROMSpec**

Update `internal/vmm/vm.go`:

```go
type SpecSummary struct {
    Name      string      `json:"name"`
    Arch      string      `json:"arch"`
    VCPUs     int         `json:"vcpus"`
    MemoryMiB int         `json:"memory_mib"`
    CPUModel  string      `json:"cpu_model"`
    Disks     []DiskSpec  `json:"disks"`
    CDROMs    []CDROMSpec `json:"cdroms,omitempty"`
    NICs      []NICSpec   `json:"nics"`
}

type CDROMBootIndexMode string

const (
    CDROMBootIndexUnset CDROMBootIndexMode = "unset"
    CDROMBootIndexSet   CDROMBootIndexMode = "index"
)

type CDROMSpec struct {
    Path          string              `json:"path"`
    Format        string              `json:"format"`
    BootIndexMode CDROMBootIndexMode  `json:"boot_index_mode"`
    BootIndex     int                 `json:"boot_index,omitempty"`
}
```

`Format` must be explicit and currently accepts only `iso`. Do not infer from the file suffix.

- [ ] **Step 3: Derive builder entries**

In `deriveBuilder`, after disks and before NICs, add for each CD-ROM:
- `blockdev.ISO{NodeName:"iso%d", File: blockdev.FileProtocol{Filename: spec.Path}}`;
- one shared `device.VirtioSCSIPCI{ID:"scsi0"}` if at least one CD-ROM exists;
- `device.SCSICD{ID:"cd%d", Drive:blockdev.Ref("iso%d"), Bus:"scsi0.0", SCSIID:qflag.Int(i)}` when `BootIndexMode=unset`;
- `device.SCSICD{ID:"cd%d", Drive:blockdev.Ref("iso%d"), Bus:"scsi0.0", SCSIID:qflag.Int(i), BootIndex:qflag.Int(spec.BootIndex)}` when `BootIndexMode=index`.

Reject empty path, unsupported format, unknown boot index mode, or negative boot index.

- [ ] **Step 4: Add VMM tests**

Test names:

```go
func TestDeriveBuilderAddsCDROMArgv(t *testing.T) {}
func TestDeriveBuilderRejectsInvalidCDROM(t *testing.T) {}
func TestEncodeDecodeStateRoundTripPreservesCDROMs(t *testing.T) {}
```

Golden argv fragments:

```text
-blockdev driver=raw,node-name=iso0,file.driver=file,file.filename=/cache/alpine.iso,read-only=on
-device virtio-scsi-pci,id=scsi0
-device scsi-cd,id=cd0,drive=iso0,bus=scsi0.0,scsi-id=0
```

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/vmm
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/vmm
git commit -m "feat(vmm): derive cdrom argv from spec"
```

---

### Task 11: Distributed Image cache integration test

**Files:**
- Modify/Create: `internal/node/task_integration_test.go` or `internal/controlplane/controller/image_integration_test.go`
- Modify: `internal/controlplane/service_test.go` if service config needs fixtures.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: fake apiserver/store + ImageController + node TaskController prove Image moves from pending/caching to ready via CacheImage Task, without running full Lima E2E.

Acceptance evidence:
- `go test -count=1 ./internal/controlplane ./internal/node/... -run 'Test.*Image.*Task|Test.*CacheImage' -v` passes.

- [ ] **Step 2: Build integration fixture**

Fixture components:
- fake store or real `store/fake`;
- apiserver handler with ImageStore configured to a temp root and public URL from `httptest.Server`;
- ImageController with `nodeNames=[]string{"node0"}` and cache root name `image-cache`;
- node TaskController with ImageTaskHandler pointing at a temp cache root;
- Image object created via upload route or direct store seed with `source.type=http` to `httptest.Server`.

- [ ] **Step 3: Assert ready status and observed cache path**

Test should wait until Image status:
- `phase=ready`;
- `observedVersion` matches spec;
- `observedSHA256` matches spec;
- `nodeCaches[0].nodeName=node0`;
- `nodeCaches[0].phase=ready`;
- `nodeCaches[0].cachedPath` exists on disk;
- Task `cache-image-...` is `Succeeded`.

- [ ] **Step 4: Add failure case**

Second test uses wrong SHA256 and asserts Image status becomes `failed` with node cache failed and Task failed. Do not retry forever in the test.

- [ ] **Step 5: Run targeted verification**

Run:

```bash
go test -count=1 ./internal/controlplane ./internal/node/... -run 'Test.*Image.*Task|Test.*CacheImage' -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controlplane internal/node
git commit -m "test: cover image cache task integration"
```

---

### Task 12: E2E image cache root-disk boot and CD-ROM argv

**Files:**
- Modify: `scripts/e2e.sh`
- Modify: `test/e2e/manifests/03-image.json`
- Modify: `test/e2e/manifests/04-volume.json`
- Modify: `test/e2e/closure_test.go`
- Modify: `test/e2e/guest.go` — add an image-cache file assertion helper used by `closure_test.go`.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: existing distributed spine VM boot still boots from qcow2/root Volume, but image bytes now flow through ImageStore -> ImageController -> CacheImage Task -> node image-cache -> root Volume; the same VM also declares an ISO `cdromImageRefs` entry that appears in running QEMU argv as a read-only CD-ROM.

Acceptance evidence:
- `go test -tags e2e -run TestNonExistent -count=0 ./test/e2e` compiles.
- `scripts/e2e.sh full` passes in final verification.
- Test waits for Image ready and Task success before root Volume creation/VM boot.
- Test waits for ISO Image cache ready and asserts running QEMU argv contains typed CD-ROM blockdev/device args.

- [ ] **Step 2: Update e2e script flags and paths**

In `scripts/e2e.sh`:
- create `.tmp/e2e/image-store` for `govirtad --image-store-root`;
- pass `--image-store-public-url http://host.lima.internal:${GOVIRTAD_PORT}` or the script’s actual host-accessible port;
- pass `govirtlet --image-cache-dir-name image-cache`;
- upload the CirrOS qcow2 with `govirtctl image upload --name image-cirros --version v1 --format qcow2 --file <prepared-cirros-path>` before applying dependent manifests;
- create a tiny deterministic ISO fixture under `.tmp/e2e/cdrom.iso` and upload it with `govirtctl image upload --name image-cdrom --version v1 --format iso --file .tmp/e2e/cdrom.iso`.

All generated temporary files remain under `.tmp/e2e/` or existing `.lima/cache/`.

- [ ] **Step 3: Update manifests**

Remove `test/e2e/manifests/03-image.json` from `applySpineDependencies`; `govirtctl image upload` creates both `image-cirros` and `image-cdrom`. Delete the static `03-image.json` file if no test still references it. Do not keep a static Image manifest with placeholder SHA256.

`04-volume.json` removes `imageFilePoolRef` and keeps `imageRef`. `07-vm.json` adds:

```json
"cdromImageRefs": [
  { "imageRef": "image-cdrom", "bootIndexMode": "unset" }
]
```

- [ ] **Step 4: Update closure test waiting order**

In `applySpineDependencies`, ensure Image ready is observed before applying root Volume or before waiting root Volume ready. Add helper:

```go
func waitImageCacheReady(ctx context.Context, ctl string, name string, nodeName string) imageStatusSnapshot
```

It should parse `Image.status.nodeCaches` and assert the selected node cache is ready with non-empty `cachedPath`, `sha256`, and `taskRef`.

Continue to assert VM boot via existing `waitVMOnRunning`, QEMU argv MAC/memory/disk count, snapshot, resize, and cold config tests. Add `Guest.AssertQEMUArgvContainsCDROM(vmUID, "image-cdrom")` or equivalent helper that checks `-blockdev ...read-only=on...cdrom.iso` and `-device scsi-cd` in the running QEMU argv.

- [ ] **Step 5: Compile E2E package**

Run:

```bash
go test -tags e2e -run TestNonExistent -count=0 ./test/e2e
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add scripts/e2e.sh test/e2e
git commit -m "test(e2e): boot vm from cached image"
```

---

### Task 13: Documentation and final verification

**Files:**
- Modify: `AGENTS.md` if architecture knowledge changes substantially.
- Modify: `internal/controlplane/AGENTS.md`, `internal/node/AGENTS.md`, `pkg/virt/AGENTS.md`, or `test/e2e/AGENTS.md` if touched flows drift materially.
- Modify: `docs/superpowers/specs/2026-06-13-image-controller-migration-design.md` only for final status/clarification updates.

- [ ] **Step 1: Confirm goal and acceptance criteria**

Goal: project knowledge reflects the new Image control-plane/cache flow, and verification proves no regressions in unit, race, local verify, and e2e compile paths.

Acceptance evidence:
- `gofmt -l .` prints nothing.
- `go test ./...` passes.
- race-focused command passes.
- `scripts/verify.sh` passes.
- `go test -tags e2e -run TestNonExistent -count=0 ./test/e2e` passes.

- [ ] **Step 2: Run formatting**

Run:

```bash
gofmt -w pkg/apis/image/v1alpha1 pkg/apis/task/v1alpha1 pkg/apis/volume/v1alpha1 internal/controlplane internal/node pkg/virt/qemu internal/vmm internal/govirtctl cmd/govirtad cmd/govirtlet test/e2e
```

Expected: command exits 0.

- [ ] **Step 3: Run targeted suites**

Run:

```bash
go test -count=1 ./pkg/apis/image/... ./pkg/apis/task/... ./pkg/apis/volume/... ./internal/controlplane/imagestore/... ./internal/controlplane/apiserver ./internal/controlplane/controller ./internal/node/... ./pkg/virt/qemu/... ./internal/vmm ./internal/govirtctl
```

Expected: PASS.

- [ ] **Step 4: Run full local verification**

Run:

```bash
go test ./...
go test -race -count=1 ./internal/controlplane ./internal/controlplane/controller ./internal/node/...
scripts/verify.sh
go test -tags e2e -run TestNonExistent -count=0 ./test/e2e
git diff --check
```

Expected: every command passes; `git diff --check` has no output.

- [ ] **Step 5: Optional heavy E2E before final claim**

Run when ready for full acceptance:

```bash
scripts/e2e.sh full
```

Expected: PASS and log path under `test/log/*-e2e-full.log`.

- [ ] **Step 6: Update knowledge docs**

Update the scoped AGENTS knowledge files touched by this migration with:
- Image is control-plane reconciled.
- Node image cache is under `<runtimeRoot>/image-cache`.
- Root Volume consumes node image-cache.
- QEMU CD/DVD typed builder exists but ISO VM API is not fully wired.

- [ ] **Step 7: Commit final docs/verification updates**

```bash
git add AGENTS.md internal/controlplane/AGENTS.md internal/node/AGENTS.md pkg/virt/AGENTS.md test/e2e/AGENTS.md docs/superpowers/specs/2026-06-13-image-controller-migration-design.md
git commit -m "docs: document image controller migration"
```

If an AGENTS file proves fully current after review, leave it unchanged and record that in the final implementation handoff.

---

## Self-Review Checklist

- Spec coverage:
  - Image API content identity and node cache status: Task 1.
  - Image CRUD/upload/download: Task 4.
  - Task operations: Task 2.
  - Control-plane ImageController: Task 5.
  - Node cache executor: Task 6.
  - Root Volume image-cache consumption and existing VM boot E2E: Tasks 7 and 12.
  - VM CD-ROM refs: Task 8.
  - QEMU CD/DVD typed wrapper and VMM argv: Tasks 9 and 10.
  - Verification/docs: Task 13.
- No placeholders: no `TBD`, `TODO`, or unspecified “handle edge cases” steps are allowed.
- Type consistency: exact names introduced in earlier tasks are reused later (`CacheImageInput`, `CacheImageObserved`, `ImageCache`, `ImageStorePublicURL`, `FinalizerImageCache`).
- Scope guard: no Ceph/NFS/S3 backend implementation, no ISO VM API end-to-end install flow, no complete StoragePool/Volume migration.
