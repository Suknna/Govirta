# 垂直切片 Plan 1：`pkg/apis` 契约层实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Explicitly invoke/load superpowers:goal-driven-development before implementation tasks. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现六个一等公民资源对象（StoragePool / Image / Volume / Network / NIC / VM）共享 k8s 式信封的纯类型契约包 `pkg/apis`，供 master 与 node 双向 import。

**Architecture:** 纯类型 + 强类型枚举 + 基础校验的自包含契约层。每个资源放独立子包 `pkg/apis/<resource>/v1alpha1`，共享信封类型放 `pkg/apis/meta/v1alpha1`。契约层是项目最底层，**只依赖 Go stdlib**，绝不 import `internal/` 或 `pkg/hostnet`——`internal/` 控制器反向 import 它并做「apis 类型 → 执行面请求类型」映射。API Spec 只暴露语义意图，底层 nftables/TAP 内核身份不进 Spec（由 node 控制器确定性派生）。

**Tech Stack:** Go 1.26，标准库（`net`、`net/netip`、`encoding/json`），无第三方依赖。

---

## 文件结构

```
pkg/apis/meta/v1alpha1/
  types.go          # TypeMeta + ObjectMeta + Phase 信封约定
  types_test.go
pkg/apis/storagepool/v1alpha1/
  types.go          # StoragePool + StoragePoolSpec + StoragePoolStatus + BackendType/PoolType/PoolPhase
  types_test.go
pkg/apis/image/v1alpha1/
  types.go          # Image + ImageSpec + ImageStatus + ImageFormat + ImageSourceType + ImagePhase
  types_test.go
pkg/apis/volume/v1alpha1/
  types.go          # Volume + VolumeSpec + VolumeStatus + VolumeRole + VolumePhase
  types_test.go
pkg/apis/network/v1alpha1/
  types.go          # Network + NetworkSpec + NetworkStatus + NetworkPhase
  types_test.go
pkg/apis/nic/v1alpha1/
  types.go          # NIC + NICSpec + NICStatus + NICPhase
  types_test.go
pkg/apis/vm/v1alpha1/
  types.go          # VM + VMSpec + VMStatus + VMPhase
  types_test.go
```

设计依据（来自已确认 spec `2026-06-06-controlplane-node-vertical-slice-design.md`）：
- 六对象共享信封 `TypeMeta + ObjectMeta + Spec + Status`，`ResourceVersion = etcd revision`。
- 全显式提交：UID/名称/引用由调用方提供，apis 不生成不补默认。唯一例外 NIC 的 MAC（apiserver 准入期分配，Plan 2 实现；apis 侧 `MAC` 字段允许空值入库）。
- Format 权威 = `Image.Spec.Format`；Volume 不带源 format 字段。
- Volume.Spec 显式引用 `ImageRef` + `ImageFilePoolRef` + 自身 block `PoolRef`。
- Spec 只收语义意图；nftables 表名/链/优先级、TAP owner UID/GID、VNetHeader 不进 Spec。
- 状态机值（Phase / BackendType / Role / 源类型 / 格式）必须用专属 Go 类型 + 命名常量（项目铁律，禁止裸 string）。

---

## Task 1: 共享信封 `pkg/apis/meta/v1alpha1`

**Files:**
- Create: `pkg/apis/meta/v1alpha1/types.go`
- Test: `pkg/apis/meta/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义所有资源共享的 `TypeMeta` + `ObjectMeta`，并提供一个统一的 `APIGroupVersion` 常量。`ObjectMeta.Validate()` 在 `Name` 或 `UID` 为空时返回错误（显式铁律：身份调用方提供）。
Acceptance evidence:
- `go test ./pkg/apis/meta/v1alpha1/ -run TestObjectMetaValidate -v` 通过
- 测试覆盖：合法 meta 通过；空 Name 拒绝；空 UID 拒绝

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the shared API envelope for Govirta first-class
// resource objects. It is the project's bottom contract layer and depends only
// on the Go standard library; it never imports internal/ or pkg/hostnet.
package v1alpha1

import (
	"errors"
	"fmt"
)

// APIGroupVersion is the apiVersion string carried by every Govirta API object.
const APIGroupVersion = "govirta.io/v1alpha1"

// Kind names a Govirta API object kind. State-machine-like discriminator, so it
// is a dedicated type with named constants (项目铁律：no bare string).
type Kind string

const (
	// KindStoragePool identifies a StoragePool object.
	KindStoragePool Kind = "StoragePool"
	// KindImage identifies an Image object.
	KindImage Kind = "Image"
	// KindVolume identifies a Volume object.
	KindVolume Kind = "Volume"
	// KindNetwork identifies a Network object.
	KindNetwork Kind = "Network"
	// KindNIC identifies a NIC object.
	KindNIC Kind = "NIC"
	// KindVM identifies a VM object.
	KindVM Kind = "VM"
)

// ErrInvalidObjectMeta is returned when required identity fields are missing.
var ErrInvalidObjectMeta = errors.New("apis: invalid object metadata")

// TypeMeta carries the apiVersion + kind discriminator shared by all objects.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       Kind   `json:"kind"`
}

// ObjectMeta carries identity and routing metadata shared by all objects.
//
// UID and Name are caller-provided (一等公民判据 + 显式铁律); the API layer never
// generates them. ResourceVersion mirrors the etcd revision and is written by
// the store, not the caller. NodeName is set by govirtctl for node-local
// resources and by the scheduler for VM; node watch filters on it.
type ObjectMeta struct {
	Name            string            `json:"name"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	NodeName        string            `json:"nodeName,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

// Validate reports whether the caller-provided identity fields are present.
// ResourceVersion is intentionally not required: the store assigns it.
func (m ObjectMeta) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidObjectMeta)
	}
	if m.UID == "" {
		return fmt.Errorf("%w: uid is required", ErrInvalidObjectMeta)
	}
	return nil
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func TestObjectMetaValidate(t *testing.T) {
	tests := []struct {
		name    string
		meta    ObjectMeta
		wantErr bool
	}{
		{"valid", ObjectMeta{Name: "n1", UID: "u1"}, false},
		{"empty name", ObjectMeta{UID: "u1"}, true},
		{"empty uid", ObjectMeta{Name: "n1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.meta.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidObjectMeta) {
					t.Fatalf("got err %v, want ErrInvalidObjectMeta", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		})
	}
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/meta/v1alpha1/ -run TestObjectMetaValidate -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/meta/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/meta/v1alpha1/
git commit -m "feat(apis): add shared TypeMeta/ObjectMeta envelope"
```

---

## Task 2: StoragePool 契约 `pkg/apis/storagepool/v1alpha1`

**Files:**
- Create: `pkg/apis/storagepool/v1alpha1/types.go`
- Test: `pkg/apis/storagepool/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义 `StoragePool` 对象（信封 + Spec + Status），含 `BackendType`、`PoolType`、`PoolPhase` 强类型枚举与 `.Valid()`。`StoragePoolSpec.Validate()` 拒绝空 backend/type 或非正容量。apis 枚举值字符串与执行面 `internal/storage/pool`（`local-block`/`local-file`/`nfs-block`/`rbd-block`、`block`/`file`）一致，但 apis **不 import** 该包。
Acceptance evidence:
- `go test ./pkg/apis/storagepool/v1alpha1/ -v` 通过
- 测试覆盖：合法 spec 通过；空 BackendType 拒绝；非正 CapacityBytes 拒绝；枚举 `.Valid()` 正负样本

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the StoragePool API object. Depends only on stdlib
// and the shared meta envelope; never imports internal/ or pkg/hostnet.
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// BackendType names the storage backend family. Mirrors internal/storage/pool
// values by string but is defined independently (契约层不依赖 internal).
type BackendType string

const (
	// BackendLocalBlock identifies host-local block storage.
	BackendLocalBlock BackendType = "local-block"
	// BackendLocalFile identifies host-local file image storage.
	BackendLocalFile BackendType = "local-file"
	// BackendNFSBlock identifies NFS-backed block storage.
	BackendNFSBlock BackendType = "nfs-block"
	// BackendRBDBlock identifies RBD-backed block storage.
	BackendRBDBlock BackendType = "rbd-block"
)

// Valid reports whether b is a known backend type.
func (b BackendType) Valid() bool {
	switch b {
	case BackendLocalBlock, BackendLocalFile, BackendNFSBlock, BackendRBDBlock:
		return true
	default:
		return false
	}
}

// PoolType describes the storage object model exposed by a pool.
type PoolType string

const (
	// PoolTypeBlock identifies pools that manage block volumes.
	PoolTypeBlock PoolType = "block"
	// PoolTypeFile identifies pools that manage file images.
	PoolTypeFile PoolType = "file"
)

// Valid reports whether t is a known pool type.
func (t PoolType) Valid() bool {
	return t == PoolTypeBlock || t == PoolTypeFile
}

// PoolPhase is the observed lifecycle phase reported by the node controller.
type PoolPhase string

const (
	// PoolPhasePending means the pool object exists but is not yet registered.
	PoolPhasePending PoolPhase = "pending"
	// PoolPhaseReady means the node has registered the pool.
	PoolPhaseReady PoolPhase = "ready"
	// PoolPhaseFailed means registration failed.
	PoolPhaseFailed PoolPhase = "failed"
)

// ErrInvalidSpec is returned when a StoragePoolSpec is not internally valid.
var ErrInvalidSpec = errors.New("storagepool: invalid spec")

// StoragePoolSpec is the desired state of a storage pool (explicit semantic
// intent only). StorageRoot is the host path the node driver registers under.
type StoragePoolSpec struct {
	Backend       BackendType `json:"backend"`
	Type          PoolType    `json:"type"`
	StorageRoot   string      `json:"storageRoot"`
	CapacityBytes int64       `json:"capacityBytes"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s StoragePoolSpec) Validate() error {
	if !s.Backend.Valid() {
		return fmt.Errorf("%w: backend %q", ErrInvalidSpec, s.Backend)
	}
	if !s.Type.Valid() {
		return fmt.Errorf("%w: type %q", ErrInvalidSpec, s.Type)
	}
	if s.StorageRoot == "" {
		return fmt.Errorf("%w: storageRoot is required", ErrInvalidSpec)
	}
	if s.CapacityBytes <= 0 {
		return fmt.Errorf("%w: capacityBytes must be positive", ErrInvalidSpec)
	}
	return nil
}

// StoragePoolStatus is the observed state written by the node controller.
type StoragePoolStatus struct {
	Phase          PoolPhase `json:"phase"`
	AllocatedBytes int64     `json:"allocatedBytes,omitempty"`
	Message        string    `json:"message,omitempty"`
}

// StoragePool is a first-class storage pool API object.
type StoragePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              StoragePoolSpec   `json:"spec"`
	Status            StoragePoolStatus `json:"status"`
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func TestStoragePoolSpecValidate(t *testing.T) {
	valid := StoragePoolSpec{
		Backend:       BackendLocalBlock,
		Type:          PoolTypeBlock,
		StorageRoot:   "/var/lib/govirtlet/pools/p1",
		CapacityBytes: 1 << 30,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *StoragePoolSpec)
	}{
		{"empty backend", func(s *StoragePoolSpec) { s.Backend = "" }},
		{"bad type", func(s *StoragePoolSpec) { s.Type = "object" }},
		{"empty root", func(s *StoragePoolSpec) { s.StorageRoot = "" }},
		{"zero capacity", func(s *StoragePoolSpec) { s.CapacityBytes = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestStoragePoolEnumsValid(t *testing.T) {
	for _, b := range []BackendType{BackendLocalBlock, BackendLocalFile, BackendNFSBlock, BackendRBDBlock} {
		if !b.Valid() {
			t.Errorf("backend %q should be valid", b)
		}
	}
	if BackendType("bogus").Valid() {
		t.Error("bogus backend should be invalid")
	}
	for _, p := range []PoolType{PoolTypeBlock, PoolTypeFile} {
		if !p.Valid() {
			t.Errorf("pool type %q should be valid", p)
		}
	}
	if PoolType("object").Valid() {
		t.Error("object pool type should be invalid")
	}
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/storagepool/v1alpha1/ -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/storagepool/v1alpha1/
git commit -m "feat(apis): add StoragePool object contract"
```

---

## Task 3: Image 契约 `pkg/apis/image/v1alpha1`

**Files:**
- Create: `pkg/apis/image/v1alpha1/types.go`
- Test: `pkg/apis/image/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义 `Image` 对象。`ImageFormat`（qcow2/raw，权威源格式）、`ImageSourceType`（file/http，第一刀仅两种显式外部源）、`ImagePhase`（pending/ready/deleting）强类型枚举。`ImageSpec` 含 `FilePoolRef`（镜像落地的 file pool）+ `Source`（显式外部源）+ `Format` + `DeclaredSizeBytes`。`ImageSpec.Validate()` 拒绝空 poolRef、未知 format、未知源类型、空源位置、非正声明大小。
Acceptance evidence:
- `go test ./pkg/apis/image/v1alpha1/ -v` 通过
- 测试覆盖：合法 file 源通过；合法 http 源通过；空 FilePoolRef 拒绝；bad format 拒绝；bad source type 拒绝；空 Location 拒绝；零 DeclaredSizeBytes 拒绝

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the Image API object. The image format here is the
// authoritative source-byte format used by the volume controller when deriving
// an independent root volume (spec: Format 权威 = Image.Spec.Format).
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// ImageFormat is the byte format of the source image. Mirrors
// internal/storage/diskformat by string but defined independently.
type ImageFormat string

const (
	// ImageFormatQCOW2 identifies QEMU qcow2 image bytes.
	ImageFormatQCOW2 ImageFormat = "qcow2"
	// ImageFormatRaw identifies raw disk image bytes.
	ImageFormatRaw ImageFormat = "raw"
)

// Valid reports whether f is a known image format.
func (f ImageFormat) Valid() bool {
	return f == ImageFormatQCOW2 || f == ImageFormatRaw
}

// ImageSourceType names how the node controller fetches image bytes. The first
// slice supports only an explicit local file path and an HTTP(S) URL; no
// registry/container-image source (spec §3 镜像分发).
type ImageSourceType string

const (
	// ImageSourceFile fetches bytes from a node-local file path.
	ImageSourceFile ImageSourceType = "file"
	// ImageSourceHTTP fetches bytes from an http(s) URL.
	ImageSourceHTTP ImageSourceType = "http"
)

// Valid reports whether t is a known source type.
func (t ImageSourceType) Valid() bool {
	return t == ImageSourceFile || t == ImageSourceHTTP
}

// ImagePhase is the observed lifecycle phase. Mirrors internal/storage/pool
// ImageState (pending/ready/deleting) by string but defined independently.
type ImagePhase string

const (
	// ImagePhasePending means bytes are reserved but not yet fully fetched.
	ImagePhasePending ImagePhase = "pending"
	// ImagePhaseReady means bytes are committed and available for reads.
	ImagePhaseReady ImagePhase = "ready"
	// ImagePhaseDeleting means a ready image is being removed.
	ImagePhaseDeleting ImagePhase = "deleting"
	// ImagePhaseFailed means the fetch failed.
	ImagePhaseFailed ImagePhase = "failed"
)

// ImageSource is the explicit external byte source for an image.
type ImageSource struct {
	Type     ImageSourceType `json:"type"`
	Location string          `json:"location"` // file path (file) or url (http)
}

// ErrInvalidSpec is returned when an ImageSpec is not internally valid.
var ErrInvalidSpec = errors.New("image: invalid spec")

// ImageSpec is the desired state of an image.
type ImageSpec struct {
	FilePoolRef       string      `json:"filePoolRef"` // file pool object name
	Source            ImageSource `json:"source"`
	Format            ImageFormat `json:"format"`
	DeclaredSizeBytes int64       `json:"declaredSizeBytes"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s ImageSpec) Validate() error {
	if s.FilePoolRef == "" {
		return fmt.Errorf("%w: filePoolRef is required", ErrInvalidSpec)
	}
	if !s.Source.Type.Valid() {
		return fmt.Errorf("%w: source type %q", ErrInvalidSpec, s.Source.Type)
	}
	if s.Source.Location == "" {
		return fmt.Errorf("%w: source location is required", ErrInvalidSpec)
	}
	if !s.Format.Valid() {
		return fmt.Errorf("%w: format %q", ErrInvalidSpec, s.Format)
	}
	if s.DeclaredSizeBytes <= 0 {
		return fmt.Errorf("%w: declaredSizeBytes must be positive", ErrInvalidSpec)
	}
	return nil
}

// ImageStatus is the observed state written by the node Image controller.
type ImageStatus struct {
	Phase          ImagePhase `json:"phase"`
	LocalSizeBytes int64      `json:"localSizeBytes,omitempty"`
	Message        string     `json:"message,omitempty"`
}

// Image is a first-class image API object. The caller provides ObjectMeta.Name
// as the image ID; a duplicate ID in the same file pool is rejected downstream.
type Image struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              ImageSpec   `json:"spec"`
	Status            ImageStatus `json:"status"`
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func validImageSpec() ImageSpec {
	return ImageSpec{
		FilePoolRef:       "files",
		Source:            ImageSource{Type: ImageSourceHTTP, Location: "https://example/cirros.img"},
		Format:            ImageFormatQCOW2,
		DeclaredSizeBytes: 1 << 20,
	}
}

func TestImageSpecValidate(t *testing.T) {
	if err := validImageSpec().Validate(); err != nil {
		t.Fatalf("valid http spec rejected: %v", err)
	}
	fileSpec := validImageSpec()
	fileSpec.Source = ImageSource{Type: ImageSourceFile, Location: "/var/img/cirros.img"}
	if err := fileSpec.Validate(); err != nil {
		t.Fatalf("valid file spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *ImageSpec)
	}{
		{"empty pool", func(s *ImageSpec) { s.FilePoolRef = "" }},
		{"bad source type", func(s *ImageSpec) { s.Source.Type = "registry" }},
		{"empty location", func(s *ImageSpec) { s.Source.Location = "" }},
		{"bad format", func(s *ImageSpec) { s.Format = "vmdk" }},
		{"zero size", func(s *ImageSpec) { s.DeclaredSizeBytes = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validImageSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestImageEnumsValid(t *testing.T) {
	if !ImageFormatQCOW2.Valid() || !ImageFormatRaw.Valid() {
		t.Error("known formats should be valid")
	}
	if ImageFormat("vmdk").Valid() {
		t.Error("vmdk should be invalid")
	}
	if !ImageSourceFile.Valid() || !ImageSourceHTTP.Valid() {
		t.Error("known source types should be valid")
	}
	if ImageSourceType("registry").Valid() {
		t.Error("registry should be invalid")
	}
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/image/v1alpha1/ -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/image/v1alpha1/
git commit -m "feat(apis): add Image object contract with explicit external source"
```

---

## Task 4: Volume 契约 `pkg/apis/volume/v1alpha1`

**Files:**
- Create: `pkg/apis/volume/v1alpha1/types.go`
- Test: `pkg/apis/volume/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义 `Volume` 对象。`VolumeRole`（root/data）、`VolumePhase` 强类型枚举。`VolumeSpec` 显式带 block `PoolRef` + `VMRef` + `VMName` + `DiskIndex` + `CapacityBytes` + `Role`，root 卷额外显式 `ImageRef` + `ImageFilePoolRef`（源镜像 + 其 file pool）。Volume **不带源 format 字段**（Format 权威在 Image）。`VolumeSpec.Validate()`：空 PoolRef / 空 VMRef / 负 DiskIndex / 非正容量 / 未知 Role 拒绝；`Role=root` 时 `ImageRef`+`ImageFilePoolRef` 必填，`Role=data` 时两者必须为空。
Acceptance evidence:
- `go test ./pkg/apis/volume/v1alpha1/ -v` 通过
- 测试覆盖：合法 root（带 image 引用）通过；合法 data（无 image 引用）通过；root 缺 ImageRef 拒绝；root 缺 ImageFilePoolRef 拒绝；data 带 ImageRef 拒绝；空 PoolRef / 空 VMRef / 负 DiskIndex / 零容量 / bad role 拒绝

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the Volume API object. A root volume is always a
// full independent copy derived from an Image's bytes (no backing-file chain);
// the source byte format authority is Image.Spec.Format, so Volume carries no
// source format field.
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// VolumeRole names the disk role. Mirrors internal/storage/volume by string but
// defined independently.
type VolumeRole string

const (
	// VolumeRoleRoot identifies the VM boot/root disk.
	VolumeRoleRoot VolumeRole = "root"
	// VolumeRoleData identifies an additional non-boot data disk.
	VolumeRoleData VolumeRole = "data"
)

// Valid reports whether r is a known role.
func (r VolumeRole) Valid() bool {
	return r == VolumeRoleRoot || r == VolumeRoleData
}

// VolumePhase is the observed lifecycle phase written by the node controller.
type VolumePhase string

const (
	// VolumePhasePending means the volume object exists but is not yet created.
	VolumePhasePending VolumePhase = "pending"
	// VolumePhaseReady means the qcow2 volume exists on the node.
	VolumePhaseReady VolumePhase = "ready"
	// VolumePhaseFailed means creation failed.
	VolumePhaseFailed VolumePhase = "failed"
)

// ErrInvalidSpec is returned when a VolumeSpec is not internally valid.
var ErrInvalidSpec = errors.New("volume: invalid spec")

// VolumeSpec is the desired state of a block volume. ImageRef + ImageFilePoolRef
// are required for a root volume (the source image and the file pool holding it)
// and must be empty for a data volume.
type VolumeSpec struct {
	PoolRef          string     `json:"poolRef"` // block pool object name
	VMRef            string     `json:"vmRef"`   // owning VM uid
	VMName           string     `json:"vmName"`
	DiskIndex        int        `json:"diskIndex"`
	CapacityBytes    int64      `json:"capacityBytes"`
	Role             VolumeRole `json:"role"`
	ImageRef         string     `json:"imageRef,omitempty"`         // root only
	ImageFilePoolRef string     `json:"imageFilePoolRef,omitempty"` // root only
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s VolumeSpec) Validate() error {
	if s.PoolRef == "" {
		return fmt.Errorf("%w: poolRef is required", ErrInvalidSpec)
	}
	if s.VMRef == "" {
		return fmt.Errorf("%w: vmRef is required", ErrInvalidSpec)
	}
	if s.DiskIndex < 0 {
		return fmt.Errorf("%w: diskIndex must be non-negative", ErrInvalidSpec)
	}
	if s.CapacityBytes <= 0 {
		return fmt.Errorf("%w: capacityBytes must be positive", ErrInvalidSpec)
	}
	if !s.Role.Valid() {
		return fmt.Errorf("%w: role %q", ErrInvalidSpec, s.Role)
	}
	switch s.Role {
	case VolumeRoleRoot:
		if s.ImageRef == "" {
			return fmt.Errorf("%w: root volume requires imageRef", ErrInvalidSpec)
		}
		if s.ImageFilePoolRef == "" {
			return fmt.Errorf("%w: root volume requires imageFilePoolRef", ErrInvalidSpec)
		}
	case VolumeRoleData:
		if s.ImageRef != "" || s.ImageFilePoolRef != "" {
			return fmt.Errorf("%w: data volume must not carry image refs", ErrInvalidSpec)
		}
	}
	return nil
}

// VolumeStatus is the observed state written by the node controller.
type VolumeStatus struct {
	Phase      VolumePhase `json:"phase"`
	VolumePath string      `json:"volumePath,omitempty"`
	Message    string      `json:"message,omitempty"`
}

// Volume is a first-class block volume API object.
type Volume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              VolumeSpec   `json:"spec"`
	Status            VolumeStatus `json:"status"`
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func validRootSpec() VolumeSpec {
	return VolumeSpec{
		PoolRef:          "blocks",
		VMRef:            "vm-uid-1",
		VMName:           "vm1",
		DiskIndex:        0,
		CapacityBytes:    2 << 30,
		Role:             VolumeRoleRoot,
		ImageRef:         "cirros",
		ImageFilePoolRef: "files",
	}
}

func validDataSpec() VolumeSpec {
	return VolumeSpec{
		PoolRef:       "blocks",
		VMRef:         "vm-uid-1",
		VMName:        "vm1",
		DiskIndex:     1,
		CapacityBytes: 1 << 30,
		Role:          VolumeRoleData,
	}
}

func TestVolumeSpecValidate(t *testing.T) {
	if err := validRootSpec().Validate(); err != nil {
		t.Fatalf("valid root rejected: %v", err)
	}
	if err := validDataSpec().Validate(); err != nil {
		t.Fatalf("valid data rejected: %v", err)
	}

	rootTests := []struct {
		name string
		mut  func(s *VolumeSpec)
	}{
		{"root missing imageRef", func(s *VolumeSpec) { s.ImageRef = "" }},
		{"root missing imageFilePoolRef", func(s *VolumeSpec) { s.ImageFilePoolRef = "" }},
		{"empty poolRef", func(s *VolumeSpec) { s.PoolRef = "" }},
		{"empty vmRef", func(s *VolumeSpec) { s.VMRef = "" }},
		{"negative disk index", func(s *VolumeSpec) { s.DiskIndex = -1 }},
		{"zero capacity", func(s *VolumeSpec) { s.CapacityBytes = 0 }},
		{"bad role", func(s *VolumeSpec) { s.Role = "swap" }},
	}
	for _, tt := range rootTests {
		t.Run(tt.name, func(t *testing.T) {
			s := validRootSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}

	t.Run("data with imageRef", func(t *testing.T) {
		s := validDataSpec()
		s.ImageRef = "cirros"
		if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("got %v, want ErrInvalidSpec", err)
		}
	})
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/volume/v1alpha1/ -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/volume/v1alpha1/
git commit -m "feat(apis): add Volume object contract with image-derived root rules"
```

---

## Task 5: Network 契约 `pkg/apis/network/v1alpha1`

**Files:**
- Create: `pkg/apis/network/v1alpha1/types.go`
- Test: `pkg/apis/network/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义 `Network` 对象。Spec **只收语义意图**：bridge 名、subnet（CIDR 串）、gatewayCIDR、DHCP 池起止、egress 接口、可选 DNS/router 列表、租期秒数。nftables 表名/链/优先级**不进 Spec**（node 控制器确定性派生，Plan 3）。`NetworkSpec.Validate()` 拒绝空 bridge / 空 egress / 非法 subnet 或 gatewayCIDR / 空 DHCP 池。地址用字符串字段（apis 不依赖具体 net 类型语义，控制器侧解析），但 Validate 用 `net/netip` 解析校验格式。
Acceptance evidence:
- `go test ./pkg/apis/network/v1alpha1/ -v` 通过
- 测试覆盖：合法 spec 通过；空 BridgeName / 空 EgressInterface / 非法 Subnet / 非法 GatewayCIDR / 空 DHCPRangeStart / 非法 DNS 条目 拒绝

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the Network API object. The spec exposes only
// high-level semantic intent; the node controller deterministically derives the
// underlying nftables table/chain/priority identities (spec 决策: Spec 不泄漏内核身份).
package v1alpha1

import (
	"errors"
	"fmt"
	"net/netip"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// NetworkPhase is the observed lifecycle phase written by the node controller.
type NetworkPhase string

const (
	// NetworkPhasePending means the object exists but the network is not ensured.
	NetworkPhasePending NetworkPhase = "pending"
	// NetworkPhaseReady means the bridge + rules + DHCP are ensured.
	NetworkPhaseReady NetworkPhase = "ready"
	// NetworkPhaseFailed means ensuring failed.
	NetworkPhaseFailed NetworkPhase = "failed"
)

// ErrInvalidSpec is returned when a NetworkSpec is not internally valid.
var ErrInvalidSpec = errors.New("network: invalid spec")

// NetworkSpec is the desired state of a network segment (semantic intent only).
// Addresses are strings on the wire; Validate parses them with net/netip.
type NetworkSpec struct {
	BridgeName      string   `json:"bridgeName"`
	Subnet          string   `json:"subnet"`      // CIDR, e.g. 192.168.100.0/24
	GatewayCIDR     string   `json:"gatewayCIDR"` // CIDR, e.g. 192.168.100.1/24
	DHCPRangeStart  string   `json:"dhcpRangeStart"`
	DHCPRangeEnd    string   `json:"dhcpRangeEnd"`
	EgressInterface string   `json:"egressInterface"`
	DNS             []string `json:"dns,omitempty"`
	Router          []string `json:"router,omitempty"`
	LeaseSeconds    int      `json:"leaseSeconds,omitempty"`
}

// Validate reports whether the spec carries explicit, well-formed fields.
func (s NetworkSpec) Validate() error {
	if s.BridgeName == "" {
		return fmt.Errorf("%w: bridgeName is required", ErrInvalidSpec)
	}
	if s.EgressInterface == "" {
		return fmt.Errorf("%w: egressInterface is required", ErrInvalidSpec)
	}
	if _, err := netip.ParsePrefix(s.Subnet); err != nil {
		return fmt.Errorf("%w: subnet %q: %v", ErrInvalidSpec, s.Subnet, err)
	}
	if _, err := netip.ParsePrefix(s.GatewayCIDR); err != nil {
		return fmt.Errorf("%w: gatewayCIDR %q: %v", ErrInvalidSpec, s.GatewayCIDR, err)
	}
	if err := requireAddr(s.DHCPRangeStart, "dhcpRangeStart"); err != nil {
		return err
	}
	if err := requireAddr(s.DHCPRangeEnd, "dhcpRangeEnd"); err != nil {
		return err
	}
	for _, d := range s.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("%w: dns %q: %v", ErrInvalidSpec, d, err)
		}
	}
	for _, r := range s.Router {
		if _, err := netip.ParseAddr(r); err != nil {
			return fmt.Errorf("%w: router %q: %v", ErrInvalidSpec, r, err)
		}
	}
	return nil
}

func requireAddr(v, field string) error {
	if v == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidSpec, field)
	}
	if _, err := netip.ParseAddr(v); err != nil {
		return fmt.Errorf("%w: %s %q: %v", ErrInvalidSpec, field, v, err)
	}
	return nil
}

// NetworkStatus is the observed state written by the node controller.
type NetworkStatus struct {
	Phase   NetworkPhase `json:"phase"`
	Message string       `json:"message,omitempty"`
}

// Network is a first-class network API object.
type Network struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              NetworkSpec   `json:"spec"`
	Status            NetworkStatus `json:"status"`
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func validNetworkSpec() NetworkSpec {
	return NetworkSpec{
		BridgeName:      "govirta0",
		Subnet:          "192.168.100.0/24",
		GatewayCIDR:     "192.168.100.1/24",
		DHCPRangeStart:  "192.168.100.10",
		DHCPRangeEnd:    "192.168.100.200",
		EgressInterface: "eth0",
		DNS:             []string{"8.8.8.8"},
		LeaseSeconds:    3600,
	}
}

func TestNetworkSpecValidate(t *testing.T) {
	if err := validNetworkSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *NetworkSpec)
	}{
		{"empty bridge", func(s *NetworkSpec) { s.BridgeName = "" }},
		{"empty egress", func(s *NetworkSpec) { s.EgressInterface = "" }},
		{"bad subnet", func(s *NetworkSpec) { s.Subnet = "192.168.100.0" }},
		{"bad gateway", func(s *NetworkSpec) { s.GatewayCIDR = "nope" }},
		{"empty range start", func(s *NetworkSpec) { s.DHCPRangeStart = "" }},
		{"bad dns", func(s *NetworkSpec) { s.DNS = []string{"not-an-ip"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validNetworkSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/network/v1alpha1/ -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/network/v1alpha1/
git commit -m "feat(apis): add Network object contract (semantic intent only)"
```

---

## Task 6: NIC 契约 `pkg/apis/nic/v1alpha1`

**Files:**
- Create: `pkg/apis/nic/v1alpha1/types.go`
- Test: `pkg/apis/nic/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义 `NIC` 对象。Spec 显式带 `NetworkRef` + `VMRef` + 期望 `IP` + 可选 `Hostname`。**`MAC` 字段允许提交时为空**（apiserver 准入期分配，Plan 2），但若非空必须是合法 MAC。TAP owner UID/GID、VNetHeader、anti-spoof 身份**不进 Spec**（node 控制器派生）。`NICSpec.Validate()` 拒绝空 NetworkRef / 空 VMRef / 非法 IP；MAC 为空放行，非空则用 `net.ParseMAC` 校验。
Acceptance evidence:
- `go test ./pkg/apis/nic/v1alpha1/ -v` 通过
- 测试覆盖：合法 spec（空 MAC）通过；合法 spec（显式合法 MAC）通过；空 NetworkRef / 空 VMRef / 非法 IP / 非法非空 MAC 拒绝

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the NIC API object. MAC may be empty at submit time
// and is filled by the apiserver MAC allocator at admission (spec: 平台分配 MAC).
// TAP owner/vnet-header/anti-spoof identities are not in the spec; the node
// controller derives them deterministically.
package v1alpha1

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// NICPhase is the observed lifecycle phase written by the node controller.
type NICPhase string

const (
	// NICPhasePending means the object exists but the NIC is not ensured.
	NICPhasePending NICPhase = "pending"
	// NICPhaseReady means the TAP + DHCP binding + anti-spoofing are ensured.
	NICPhaseReady NICPhase = "ready"
	// NICPhaseFailed means ensuring failed.
	NICPhaseFailed NICPhase = "failed"
)

// ErrInvalidSpec is returned when a NICSpec is not internally valid.
var ErrInvalidSpec = errors.New("nic: invalid spec")

// NICSpec is the desired state of a VM NIC. MAC is platform-allocated; an empty
// MAC at submit time is valid (the apiserver fills it). A non-empty MAC must be
// well-formed.
type NICSpec struct {
	NetworkRef string `json:"networkRef"`
	VMRef      string `json:"vmRef"`
	MAC        string `json:"mac,omitempty"`
	IP         string `json:"ip"`
	Hostname   string `json:"hostname,omitempty"`
}

// Validate reports whether the spec carries explicit, well-formed fields. An
// empty MAC is allowed (platform allocation pending); a non-empty MAC must parse.
func (s NICSpec) Validate() error {
	if s.NetworkRef == "" {
		return fmt.Errorf("%w: networkRef is required", ErrInvalidSpec)
	}
	if s.VMRef == "" {
		return fmt.Errorf("%w: vmRef is required", ErrInvalidSpec)
	}
	if _, err := netip.ParseAddr(s.IP); err != nil {
		return fmt.Errorf("%w: ip %q: %v", ErrInvalidSpec, s.IP, err)
	}
	if s.MAC != "" {
		if _, err := net.ParseMAC(s.MAC); err != nil {
			return fmt.Errorf("%w: mac %q: %v", ErrInvalidSpec, s.MAC, err)
		}
	}
	return nil
}

// NICStatus is the observed state written by the node controller.
type NICStatus struct {
	Phase   NICPhase `json:"phase"`
	TapName string   `json:"tapName,omitempty"`
	Message string   `json:"message,omitempty"`
}

// NIC is a first-class VM NIC API object.
type NIC struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              NICSpec   `json:"spec"`
	Status            NICStatus `json:"status"`
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func validNICSpec() NICSpec {
	return NICSpec{
		NetworkRef: "net1",
		VMRef:      "vm-uid-1",
		IP:         "192.168.100.10",
	}
}

func TestNICSpecValidate(t *testing.T) {
	if err := validNICSpec().Validate(); err != nil {
		t.Fatalf("valid spec with empty MAC rejected: %v", err)
	}
	withMAC := validNICSpec()
	withMAC.MAC = "52:54:00:12:34:56"
	if err := withMAC.Validate(); err != nil {
		t.Fatalf("valid spec with explicit MAC rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *NICSpec)
	}{
		{"empty networkRef", func(s *NICSpec) { s.NetworkRef = "" }},
		{"empty vmRef", func(s *NICSpec) { s.VMRef = "" }},
		{"bad ip", func(s *NICSpec) { s.IP = "999.1.1.1" }},
		{"bad mac", func(s *NICSpec) { s.MAC = "zz:zz" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validNICSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/nic/v1alpha1/ -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/nic/v1alpha1/
git commit -m "feat(apis): add NIC object contract (platform-allocated MAC, empty allowed)"
```

---

## Task 7: VM 契约 `pkg/apis/vm/v1alpha1`

**Files:**
- Create: `pkg/apis/vm/v1alpha1/types.go`
- Test: `pkg/apis/vm/v1alpha1/types_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 定义 `VM` 对象。`VMPhase` 强类型枚举，取值镜像 `internal/vmm.Phase`（defined/starting/running/stopping/stopped/failed）。`VMSpec` 显式带 `Arch` + `VCPUs` + `MemoryMiB` + `VolumeRefs []string` + `NICRefs []string`。`VMSpec.Validate()` 拒绝空 Arch / 非正 VCPUs / 非正 MemoryMiB / 空 VolumeRefs / 空 NICRefs。
Acceptance evidence:
- `go test ./pkg/apis/vm/v1alpha1/ -v` 通过
- 测试覆盖：合法 spec 通过；空 Arch / 零 VCPUs / 零 MemoryMiB / 空 VolumeRefs / 空 NICRefs 拒绝

- [ ] **Step 2: 写实现**

```go
// Package v1alpha1 defines the VM API object. VMPhase mirrors internal/vmm.Phase
// by string but is defined independently (契约层不依赖 internal).
package v1alpha1

import (
	"errors"
	"fmt"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// VMPhase is the observed VM run phase derived live by the node VM controller.
type VMPhase string

const (
	// VMPhaseDefined means created but never started.
	VMPhaseDefined VMPhase = "defined"
	// VMPhaseStarting means the process is alive but QMP is not yet running.
	VMPhaseStarting VMPhase = "starting"
	// VMPhaseRunning means the process is alive and QMP reports running.
	VMPhaseRunning VMPhase = "running"
	// VMPhaseStopping means a powerdown was sent but the process is still alive.
	VMPhaseStopping VMPhase = "stopping"
	// VMPhaseStopped means the process is dead with stopped intent.
	VMPhaseStopped VMPhase = "stopped"
	// VMPhaseFailed means the process is dead with running intent.
	VMPhaseFailed VMPhase = "failed"
)

// ErrInvalidSpec is returned when a VMSpec is not internally valid.
var ErrInvalidSpec = errors.New("vm: invalid spec")

// VMSpec is the desired state of a VM. VolumeRefs and NICRefs name the Volume
// and NIC objects this VM depends on; the VM controller gates on their readiness.
type VMSpec struct {
	Arch       string   `json:"arch"`
	VCPUs      int      `json:"vcpus"`
	MemoryMiB  int      `json:"memoryMiB"`
	VolumeRefs []string `json:"volumeRefs"`
	NICRefs    []string `json:"nicRefs"`
}

// Validate reports whether the spec carries explicit, internally consistent fields.
func (s VMSpec) Validate() error {
	if s.Arch == "" {
		return fmt.Errorf("%w: arch is required", ErrInvalidSpec)
	}
	if s.VCPUs <= 0 {
		return fmt.Errorf("%w: vcpus must be positive", ErrInvalidSpec)
	}
	if s.MemoryMiB <= 0 {
		return fmt.Errorf("%w: memoryMiB must be positive", ErrInvalidSpec)
	}
	if len(s.VolumeRefs) == 0 {
		return fmt.Errorf("%w: at least one volumeRef is required", ErrInvalidSpec)
	}
	if len(s.NICRefs) == 0 {
		return fmt.Errorf("%w: at least one nicRef is required", ErrInvalidSpec)
	}
	return nil
}

// VMStatus is the observed state written by the node VM controller.
type VMStatus struct {
	Phase   VMPhase `json:"phase"`
	Message string  `json:"message,omitempty"`
}

// VM is a first-class VM API object.
type VM struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              VMSpec   `json:"spec"`
	Status            VMStatus `json:"status"`
}
```

- [ ] **Step 3: 写测试**

```go
package v1alpha1

import (
	"errors"
	"testing"
)

func validVMSpec() VMSpec {
	return VMSpec{
		Arch:       "aarch64",
		VCPUs:      2,
		MemoryMiB:  512,
		VolumeRefs: []string{"vol-root"},
		NICRefs:    []string{"nic0"},
	}
}

func TestVMSpecValidate(t *testing.T) {
	if err := validVMSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name string
		mut  func(s *VMSpec)
	}{
		{"empty arch", func(s *VMSpec) { s.Arch = "" }},
		{"zero vcpus", func(s *VMSpec) { s.VCPUs = 0 }},
		{"zero memory", func(s *VMSpec) { s.MemoryMiB = 0 }},
		{"no volumes", func(s *VMSpec) { s.VolumeRefs = nil }},
		{"no nics", func(s *VMSpec) { s.NICRefs = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validVMSpec()
			tt.mut(&s)
			if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("got %v, want ErrInvalidSpec", err)
			}
		})
	}
}
```

- [ ] **Step 4: 运行定向验证**

Run: `go test ./pkg/apis/vm/v1alpha1/ -v`
Expected: PASS

- [ ] **Step 5: 验证失败则修复**

合法行为测试失败：改实现。陈旧实现细节测试失败：改/删测试。

- [ ] **Step 6: 运行更广验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/vm/v1alpha1/
git commit -m "feat(apis): add VM object contract with volume/nic refs"
```

---

## Task 8: 契约层 JSON round-trip + 分层约束验证

**Files:**
- Create: `pkg/apis/roundtrip_test.go`

- [ ] **Step 1: 确认目标与验收标准**

Goal: 一个汇总测试，证明六对象都能 JSON marshal→unmarshal 无损（信封 inline 正确），且 `go list` 确认 `pkg/apis/...` 不依赖 `internal/` 或 `pkg/hostnet`（分层铁律）。
Acceptance evidence:
- `go test ./pkg/apis/ -run TestEnvelopeRoundTrip -v` 通过
- `go list -deps ./pkg/apis/... | grep -E 'govirta/(internal|pkg/hostnet)'` 输出为空（命令退出非零即无匹配，符合预期）

- [ ] **Step 2: 写测试**

```go
package apis_test

import (
	"encoding/json"
	"testing"

	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
)

// TestEnvelopeRoundTrip proves the shared envelope marshals inline and survives
// a JSON round-trip on a representative object (VM).
func TestEnvelopeRoundTrip(t *testing.T) {
	in := vmv1.VM{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVM},
		ObjectMeta: metav1.ObjectMeta{Name: "vm1", UID: "u1", ResourceVersion: "42", NodeName: "node0"},
		Spec: vmv1.VMSpec{
			Arch: "aarch64", VCPUs: 2, MemoryMiB: 512,
			VolumeRefs: []string{"vol-root"}, NICRefs: []string{"nic0"},
		},
		Status: vmv1.VMStatus{Phase: vmv1.VMPhaseRunning},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out vmv1.VM
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Kind != metav1.KindVM || out.Name != "vm1" || out.Spec.VCPUs != 2 || out.Status.Phase != vmv1.VMPhaseRunning {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
	// Envelope must be inline: apiVersion/kind/metadata/spec/status at top level.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	for _, k := range []string{"apiVersion", "kind", "metadata", "spec", "status"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("missing top-level key %q in %s", k, b)
		}
	}
}
```

- [ ] **Step 3: 运行定向验证**

Run: `go test ./pkg/apis/ -run TestEnvelopeRoundTrip -v`
Expected: PASS

- [ ] **Step 4: 验证分层约束**

Run: `go list -deps ./pkg/apis/... | grep -E 'govirta/(internal|pkg/hostnet)'`
Expected: 无输出（grep 退出码 1）。若有输出，说明契约层违规依赖了 internal/hostnet，必须移除该依赖。

- [ ] **Step 5: 若验证失败则修复**

round-trip 失败：检查 json tag / inline 嵌入。分层约束失败：删除违规 import，把映射逻辑挪到 `internal/` 控制器侧。

- [ ] **Step 6: 运行全量 apis 验证**

Run: `go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add pkg/apis/roundtrip_test.go
git commit -m "test(apis): add envelope round-trip and layering guard"
```

---

## Task 9: 全量验证 + gofmt

**Files:** 无新增

- [ ] **Step 1: gofmt 检查**

Run: `gofmt -l pkg/apis/`
Expected: 无输出（无未格式化文件）

- [ ] **Step 2: 全量构建 + 测试**

Run: `go build ./... && go test ./pkg/apis/...`
Expected: PASS

- [ ] **Step 3: 确认现有包未被破坏**

Run: `go test ./...`
Expected: PASS（契约层是新增包，不应影响既有包）

- [ ] **Step 4: 若有格式问题则修复并提交**

```bash
gofmt -w pkg/apis/
git add -A
git commit -m "style(apis): gofmt contract layer"
```

---

## Self-Review 结论

- **Spec 覆盖**：六对象 + 共享信封全部建模（Task 1-7）；Format 权威=Image、Volume 无源 format 字段（Task 4）；Spec 只收语义意图、内核身份不暴露（Task 5 Network 注释 + 未含 nftables 字段）；MAC 允许空待平台分配（Task 6）；分层铁律有自动化 guard（Task 8）。
- **未在本 plan 范围**（留给 Plan 2/3）：MACAllocator 实际分配逻辑、Store/etcd、handler、controller、apis→执行面请求类型的映射函数。本 plan 只产出纯契约类型。
- **类型一致性**：所有 `ErrInvalidSpec` 为各资源包内私有 sentinel；`metav1` 别名统一指向 `pkg/apis/meta/v1alpha1`；枚举 `.Valid()` 命名一致。
- **占位符**：无 TODO/TBD，每个 Task 含完整代码 + 测试 + 精确验证命令。
