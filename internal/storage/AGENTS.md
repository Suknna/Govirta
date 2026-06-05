# internal/storage Knowledge Base

<!--
Verified-against:
  base_commit: ec0c430
  files:
    - internal/storage/service.go
    - internal/storage/image_service.go
    - internal/storage/errors.go
    - internal/storage/pool/service.go
    - internal/storage/pool/pool.go
    - internal/storage/block/driver.go
    - internal/storage/image/driver.go
    - internal/storage/volume/volume.go
    - internal/storage/volume/attachment.go
    - internal/storage/diskformat/format.go
    - internal/storage/local/driver.go
    - internal/storage/localfile/driver.go
    - pkg/virt/qemuimg/client.go
  flows:
    - anchor: flow-storage-volume
      sources:
        - internal/storage/service.go
        - internal/storage/pool/service.go
        - internal/storage/local/driver.go
        - pkg/virt/qemuimg/client.go
    - anchor: flow-storage-image
      sources:
        - internal/storage/image_service.go
        - internal/storage/pool/service.go
        - internal/storage/localfile/driver.go
    - anchor: flow-storage-image-root-volume
      sources:
        - internal/storage/image_service.go
        - internal/storage/service.go
        - internal/storage/pool/service.go
        - internal/storage/local/driver.go
        - pkg/virt/qemuimg/client.go
-->

## OVERVIEW

OpenStack-style internal storage boundary: VM-facing services call explicit named pools; pool service owns in-memory indexes/capacity; block/image drivers own backend filesystem and qemu-img interactions.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| VM block volume API | `service.go` | `VolumeService`; all requests carry explicit `PoolName`, VM identity, disk identity |
| File image API | `image_service.go` | `ImageService`; `PutImage` returns commit/cancel writer, `GetImage` returns `io.ReadCloser` |
| Pool registry/accounting | `pool/service.go`, `pool/pool.go` | block/file pool type split, capacity admission, in-memory indexes |
| Block driver contract | `block/driver.go` | `Create`, `CreateFromReader`, `Publish`, `Delete`, `Snapshot`, `Resize` |
| Image driver contract | `image/driver.go` | raw/qcow2 byte image `Put/Get/Delete` + `ImageWriter` |
| Volume model | `volume/` | `Volume`, `PublishedVolume`, file attachment contract |
| Explicit source format | `diskformat/format.go` | only `qcow2` and `raw` are valid |
| Local block backend | `local/driver.go` | host-local qcow2 volumes via `pkg/virt/qemuimg` |
| Local file backend | `localfile/driver.go` | host-local raw/qcow2 image byte store |

## CONVENTIONS

- Every storage outcome that depends on pool/source/format is explicit: no default pool, no image format inference, no backing-file links.
- Metadata is in memory only. After restart, callers must re-register pools and image catalog state; drivers do not scan storage roots.
- Block pools use `DefaultOvercommitRatio = 1.5`; file/image pools use `DefaultFileOvercommitRatio = 1.0`.
- `pool.Service` returns cloned pool/volume state to callers; callers must not mutate internal indexes.
- `CreateRootVolumeFromReader` requires `diskformat.Format`; raw input is converted to qcow2, qcow2 input is copied as a full independent file.
- Unit tests use fake qemu-img runners and temporary directories; they must not require real qemu-img or persistent host storage.

## ANTI-PATTERNS

- Do not introduce CSI sidecars, gRPC storage services, libvirt storage abstractions, qemu-nbd, qemu-storage-daemon, or qemu-io in this phase.
- Do not make storage infer image format from bytes or file extension; upper layers must pass `diskformat.Format`.
- Do not scan storage roots to rebuild metadata after restart; catalog persistence belongs above this package.
- Do not create qcow2 backing chains from image-derived root volumes; every VM root disk is an independent copy.
- Do not perform snapshot/resize lifecycle operations against running VMs; current `local.Driver.Snapshot` and `Resize` return unsupported.

## CALL GRAPHS & DATA FLOW (LOCAL)

### Flow: storage block volume lifecycle {#flow-storage-volume}

- Entry from root flow: `internal/storage/service.go:82 (VolumeService.CreateVolume)` / `:179 (PublishVolume)` / `:214 (DeleteVolume)`
- Local chain:
  1. `internal/storage/service.go:82 (VolumeService.CreateVolume)` — validate `ctx`, explicit `PoolName`, VM and disk identity; derive `volume.ID`
  2. `internal/storage/service.go:91 (VolumeService.CreateVolume → pool.Service.CreateVolume)` — convert VM-facing request to `block.CreateRequest`
  3. `internal/storage/pool/service.go:158 (Service.CreateVolume)` — lookup named block pool, check idempotency/conflicts, reserve capacity under pool lock
  4. `internal/storage/local/driver.go:92 (Driver.Create)` — allocate driver-owned qcow2 path and run qemu-img create
  5. `internal/storage/pool/service.go:203 (Service.CreateVolume → recordCreatedVolumeLocked)` — normalize returned volume and store clone in `Pool.volumes` (map write at `:290`)
  6. `internal/storage/pool/service.go:325 (Service.PublishVolume)` — validate VM ownership, call driver publish, record attachment state
- Data (within module): `CreateVolumeRequest` → `block.CreateRequest` → `volume.Volume` → `volume.PublishedVolume{AttachmentFile,qcow2,path}`
- Side effects (within module): creates/deletes qcow2 under `StorageRoot/pool/<pool>/...`; mutates in-memory `Pool.volumes`; publish records attachment state only
- Exit / next hop: `pkg/virt/qemuimg/client.go:105 (QCOW2Client.Create)` / `:110 (Info)` / `:135 (QCOW2Client.Remove)` [详见 `../virt/qemuimg/AGENTS.md#flow-qcow2-do`]

### Flow: storage image lifecycle {#flow-storage-image}

- Entry from root flow: `internal/storage/image_service.go:44 (ImageService.PutImage)` / `:59 (GetImage)` / `:70 (DeleteImage)`
- Local chain:
  1. `internal/storage/image_service.go:44 (ImageService.PutImage)` — validate `ctx` and explicit `PoolName`, forward `image.PutRequest`
  2. `internal/storage/pool/service.go:455 (Service.PutImage)` — validate image ID/format/declared size, require file pool, reserve capacity
  3. `internal/storage/pool/service.go:483 (Service.PutImage)` — insert `ImageRecord{State: pending}` before opening backend writer
  4. `internal/storage/localfile/driver.go:74 (Driver.Put)` — create per-image directory and `target.tmp` writer
  5. `internal/storage/localfile/driver.go:254 (imageWriter.Close)` — close tmp and hard-link tmp → target as no-overwrite commit point (`os.Link` at `:266`)
  6. `internal/storage/pool/service.go:685 (pendingImageWriter.Close)` — change pending record to ready; `Cancel` deletes pending metadata
- Data (within module): `PutImageRequest` → `image.PutRequest` → `image.ImageWriter` → `ImageRecord{pending}` → `ImageRecord{ready}`
- Side effects (within module): writes image bytes under `StorageRoot/pool/<pool>/images/<imageID>/`; mutates in-memory `Pool.images`
- Exit / next hop: filesystem image bytes for future `ImageService.GetImage`; no downstream process

### Flow: image-derived root volume {#flow-storage-image-root-volume}

- Entry from root flow: `internal/storage/image_service.go:59 (ImageService.GetImage)` then `internal/storage/service.go:128 (VolumeService.CreateRootVolumeFromReader)`
- Local chain:
  1. `internal/storage/pool/service.go:499 (Service.GetImage)` — require ready file-pool image and return backend reader
  2. `internal/storage/service.go:128 (VolumeService.CreateRootVolumeFromReader)` — require explicit block pool, reader, `diskformat.Format`, VM identity, capacity
  3. `internal/storage/pool/service.go:213 (Service.CreateVolumeFromReader)` — block pool lookup, idempotency/conflict checks, capacity reserve
  4. `internal/storage/local/driver.go:152 (Driver.CreateFromReader)` — copy reader to temp path under block pool
  5. `internal/storage/local/driver.go:186 (Driver.CreateFromReader)` — qcow2 input copies reader bytes to a full independent file (`:195` raw input calls qemu-img convert with explicit source format)
  6. `internal/storage/local/driver.go:206 (Driver.CreateFromReader)` — optional qemu-img resize for requested capacity, then commit temp via hard-link (`commitTempImage` at `:215`)
- Data (within module): `io.ReadCloser` + `diskformat.Format` → `block.CreateFromReaderRequest` → independent qcow2 `volume.Volume`
- Side effects (within module): reads source image, writes standalone qcow2 root disk, updates `Pool.volumes`; source image remains independent
- Exit / next hop: `pkg/virt/qemuimg/client.go:115 (QCOW2Client.Convert)` / `:120 (Resize)` [详见 `../virt/qemuimg/AGENTS.md#flow-qcow2-do`]

## NOTES

- `local.Driver.Publish` (`local/driver.go:290`) validates a regular `.qcow2` file and runs `qemu-img info` before returning a file attachment for QEMU.
- `localfile.imageWriter.Close` (`localfile/driver.go:254`) returns `image.ErrImageCleanupFailed` when the hard-link commit succeeds but removing the source tmp fails: the target is already durable, so the commit is honored and only the cleanup failure is surfaced. `pendingImageWriter.Close` (`pool/service.go:685`) detects that sentinel, still transitions the record to ready via `markReady`, and `errors.Join`s the cleanup error so the caller sees a committed-but-not-cleaned image rather than a rolled-back one.
- Focused verification: `go test -count=1 ./internal/storage/... ./pkg/virt/qemuimg/...`; concurrency-sensitive storage changes also run `go test -race -count=1 ./internal/storage/...`.
- Evidence: read-only subagent scan + AFT outline/zoom + direct source reads. `[已验证]` / `[降级: LSP call hierarchy]`
