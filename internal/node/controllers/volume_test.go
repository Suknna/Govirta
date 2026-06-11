package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/suknna/govirta/internal/node/client"
	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/volume"
	"github.com/suknna/govirta/internal/vmm"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	storagepoolv1 "github.com/suknna/govirta/pkg/apis/storagepool/v1alpha1"
	vmv1 "github.com/suknna/govirta/pkg/apis/vm/v1alpha1"
	volumev1 "github.com/suknna/govirta/pkg/apis/volume/v1alpha1"
)

// fakeRootVolumeCreator records the CreateRootVolumeFromReader request and hands
// back a configured volume/error. It honours ctx cancellation before returning
// and drains the reader (faithful to the storage layer copying image bytes) so a
// test can assert the controller streamed the image through. *storage.VolumeService
// is the production type it stands in for.
type fakeRootVolumeCreator struct {
	created     volume.Volume
	createErr   error
	gotReq      storage.CreateRootVolumeFromReaderRequest
	gotReader   []byte
	createCalls int

	deleteErr    error
	deleteCalls  int
	gotDeleteReq storage.DeleteVolumeRequest

	resizeErr    error
	resizeCalls  int
	gotResizeReq storage.ResizeVolumeRequest

	dataCreated     volume.Volume
	dataCreateErr   error
	gotDataReq      storage.CreateDataVolumeRequest
	dataCreateCalls int
}

func (f *fakeRootVolumeCreator) CreateRootVolumeFromReader(ctx context.Context, req storage.CreateRootVolumeFromReaderRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	f.createCalls++
	f.gotReq = req
	if req.Reader != nil {
		// Drain the reader the way the real storage layer does, so the
		// controller's reader.Close happens after a full consume.
		b, _ := io.ReadAll(req.Reader)
		f.gotReader = b
	}
	if f.createErr != nil {
		return volume.Volume{}, f.createErr
	}
	return f.created, nil
}

// CreateDataVolume records the data-disk create request and returns a canned
// volume/error. A data volume derives no image bytes (blank qcow2), so unlike
// CreateRootVolumeFromReader there is no reader to drain. Faithful to
// *storage.VolumeService, it honours ctx cancellation.
func (f *fakeRootVolumeCreator) CreateDataVolume(ctx context.Context, req storage.CreateDataVolumeRequest) (volume.Volume, error) {
	if err := ctx.Err(); err != nil {
		return volume.Volume{}, err
	}
	f.dataCreateCalls++
	f.gotDataReq = req
	if f.dataCreateErr != nil {
		return volume.Volume{}, f.dataCreateErr
	}
	return f.dataCreated, nil
}

// DeleteVolume records the teardown delete request and returns a canned error so
// a test can assert the controller tore the volume down with the right id/pool.
// It honours ctx cancellation, faithful to *storage.VolumeService.
func (f *fakeRootVolumeCreator) DeleteVolume(ctx context.Context, req storage.DeleteVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.deleteCalls++
	f.gotDeleteReq = req
	return f.deleteErr
}

// ResizeVolume records the cold-resize request and returns a canned error so a
// test can assert the controller declared the absolute target capacity against
// the right (derived id, pool). It honours ctx cancellation, faithful to
// *storage.VolumeService.
func (f *fakeRootVolumeCreator) ResizeVolume(ctx context.Context, req storage.ResizeVolumeRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.resizeCalls++
	f.gotResizeReq = req
	return f.resizeErr
}

// fakeVMStatusReader is the narrow VMStatusReader the resize cold gate needs: it
// returns a configurable live phase or error (so a test can model vmm.ErrNotFound
// = runtime absent = cold). It honours ctx cancellation, faithful to the vmm
// runtime read path.
type fakeVMStatusReader struct {
	phase     vmm.Phase
	statusErr error
}

func (f *fakeVMStatusReader) Status(ctx context.Context, uuid string) (vmm.VM, error) {
	if err := ctx.Err(); err != nil {
		return vmm.VM{}, err
	}
	if f.statusErr != nil {
		return vmm.VM{}, f.statusErr
	}
	return vmm.VM{UUID: uuid, Phase: f.phase}, nil
}

// trackingReadCloser wraps a reader and records whether Close was called. It lets
// a test assert the controller always closes the image reader (项目铁律: 不吞 close).
type trackingReadCloser struct {
	r        io.Reader
	closed   bool
	closeErr error
}

func (t *trackingReadCloser) Read(p []byte) (int, error) { return t.r.Read(p) }

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return t.closeErr
}

// fakeImageGetter hands back a configured reader/error and records the request.
// It honours ctx cancellation, faithful to *storage.ImageService.
type fakeImageGetter struct {
	reader   *trackingReadCloser
	getErr   error
	gotReq   storage.GetImageRequest
	getCalls int
}

func (f *fakeImageGetter) GetImage(ctx context.Context, req storage.GetImageRequest) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.getCalls++
	f.gotReq = req
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.reader, nil
}

// fakeDependencyReader serves canned Get responses per (kind,name) and captures
// status patches. A configured ErrNotFound for a ref models a not-yet-existing
// dependency. It honours ctx cancellation, faithful to *client.Client.
type fakeDependencyReader struct {
	// objects maps "Kind/name" → raw JSON returned by Get.
	objects map[string][]byte
	// notFound maps "Kind/name" → true to return client.ErrNotFound.
	notFound map[string]bool
	// getErr, when set for a "Kind/name", is returned by Get as a transient error.
	getErr map[string]error

	patches    []capturedVolumePatch
	patchErr   error
	patchCalls int
	getCalls   int

	removeFinalizerErr   error
	removeFinalizerCalls int
	lastFinalizerName    string
	lastFinalizer        string
}

type capturedVolumePatch struct {
	kind   string
	name   string
	status volumev1.VolumeStatus
}

func depKey(kind, name string) string { return kind + "/" + name }

func (f *fakeDependencyReader) Get(ctx context.Context, kind, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.getCalls++
	key := depKey(kind, name)
	if f.getErr != nil {
		if err := f.getErr[key]; err != nil {
			return nil, err
		}
	}
	if f.notFound != nil && f.notFound[key] {
		return nil, client.ErrNotFound
	}
	if raw, ok := f.objects[key]; ok {
		return raw, nil
	}
	// Default: an unconfigured ref is treated as not existing yet.
	return nil, client.ErrNotFound
}

func (f *fakeDependencyReader) PatchStatus(ctx context.Context, kind, name string, status []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.patchCalls++
	if f.patchErr != nil {
		return nil, f.patchErr
	}
	var decoded volumev1.VolumeStatus
	if err := json.Unmarshal(status, &decoded); err != nil {
		return nil, err
	}
	f.patches = append(f.patches, capturedVolumePatch{kind: kind, name: name, status: decoded})
	return status, nil
}

// RemoveFinalizer records the teardown finalizer removal so a test can assert
// the controller dropped the finalizer after a successful teardown. It honours
// ctx cancellation, faithful to *client.Client.
func (f *fakeDependencyReader) RemoveFinalizer(ctx context.Context, kind, name, finalizer string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.removeFinalizerCalls++
	f.lastFinalizerName = name
	f.lastFinalizer = finalizer
	return f.removeFinalizerErr
}

// --- builders ---------------------------------------------------------------

func rootVolumeObject(name string) volumev1.Volume {
	return volumev1.Volume{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVolume},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: volumev1.VolumeSpec{
			PoolRef:          "block-pool",
			VMRef:            "vm-uid",
			VMName:           "vm-name",
			DiskIndex:        0,
			CapacityBytes:    4 << 30,
			Role:             volumev1.VolumeRoleRoot,
			ImageRef:         "img-a",
			ImageFilePoolRef: "file-pool",
		},
	}
}

func storagePoolReady(name string) storagepoolv1.StoragePool {
	return storagepoolv1.StoragePool{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindStoragePool},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Status:     storagepoolv1.StoragePoolStatus{Phase: storagepoolv1.PoolPhaseReady},
	}
}

func storagePoolWithPhase(name string, phase storagepoolv1.PoolPhase) storagepoolv1.StoragePool {
	sp := storagePoolReady(name)
	sp.Status.Phase = phase
	return sp
}

// TestVolumeReconcileReadyVolumeDoesNotRecreate guards the level-triggered
// idempotence fix: a volume already Ready must not be re-created. Re-reconciling
// (e.g. on the MODIFIED event the ready-patch itself produced) would otherwise
// call CreateRootVolumeFromReader again, hit ErrVolumeAlreadyExists, and spin
// the controller forever — the same blind-spot loop the image controller had.
// A ready volume reconcile must touch neither the creator-create path nor the
// image getter; instead it drives cold-resize convergence (see the resize tests
// below). Here the owning VM is cold and already at the target capacity, so the
// driver's no-op guard converges (Done) without re-creating or re-streaming.
func TestVolumeReconcileReadyVolumeDoesNotRecreate(t *testing.T) {
	vol := rootVolumeObject("vol-a")
	vol.Status.Phase = volumev1.VolumePhaseReady

	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDepsWithColdVM(t, vol)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{phase: vmm.PhaseStopped}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() result = %+v, want Done on a converged ready volume", result)
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times on ready volume, want 0", creator.createCalls)
	}
	if getter.getCalls != 0 {
		t.Errorf("GetImage called %d times on ready volume, want 0", getter.getCalls)
	}
}

func imageReadyObject(name string, format imagev1.ImageFormat) imagev1.Image {
	return imagev1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: imagev1.ImageSpec{
			FilePoolRef:       "file-pool",
			Source:            imagev1.ImageSource{Type: imagev1.ImageSourceFile, Location: "/x"},
			Format:            format,
			DeclaredSizeBytes: 1 << 20,
		},
		Status: imagev1.ImageStatus{Phase: imagev1.ImagePhaseReady},
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func newVolumeEvent(t *testing.T, evType controller.EventType, vol volumev1.Volume) controller.Event {
	t.Helper()
	return controller.Event{Type: evType, Key: vol.Name, Object: mustMarshal(t, vol)}
}

// readyDeps wires a dependency reader where all three refs of vol are live Ready
// and the image carries the given format.
func readyDeps(t *testing.T, vol volumev1.Volume, imageFormat imagev1.ImageFormat) *fakeDependencyReader {
	t.Helper()
	return &fakeDependencyReader{
		objects: map[string][]byte{
			depKey(string(metav1.KindStoragePool), vol.Spec.PoolRef):          mustMarshal(t, storagePoolReady(vol.Spec.PoolRef)),
			depKey(string(metav1.KindStoragePool), vol.Spec.ImageFilePoolRef): mustMarshal(t, storagePoolReady(vol.Spec.ImageFilePoolRef)),
			depKey(string(metav1.KindImage), vol.Spec.ImageRef):               mustMarshal(t, imageReadyObject(vol.Spec.ImageRef, imageFormat)),
		},
	}
}

// --- tests ------------------------------------------------------------------

// dataVolumeObject builds a data-disk Volume (role=data, no image refs — the
// shape VolumeSpec.Validate requires for data volumes).
func dataVolumeObject(name string) volumev1.Volume {
	return volumev1.Volume{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVolume},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + name},
		Spec: volumev1.VolumeSpec{
			PoolRef:       "block-pool",
			VMRef:         "vm-uid",
			VMName:        "vm-name",
			DiskIndex:     1,
			CapacityBytes: 1 << 30,
			Role:          volumev1.VolumeRoleData,
		},
	}
}

// TestVolumeReconcileDataVolumeCreatesBlankVolume proves a data volume takes the
// blank-create path: it gates only on its own block StoragePool (NOT the image /
// image file pool refs it doesn't carry) and calls CreateDataVolume, never
// GetImage / CreateRootVolumeFromReader. This is the path the cold-config-change
// "add a second disk" scenario exercises — the first data volume the spine
// creates.
func TestVolumeReconcileDataVolumeCreatesBlankVolume(t *testing.T) {
	vol := dataVolumeObject("vol-data")

	wantPath := "/srv/pool/block-pool/vm-uid/disk-1.qcow2"
	creator := &fakeRootVolumeCreator{
		dataCreated: volume.Volume{
			ID:       "vm-uid-data-1",
			Name:     "vol-data",
			PoolName: "block-pool",
			Context:  map[string]string{"path": wantPath},
		},
	}
	getter := &fakeImageGetter{}
	// Only the block pool is wired ready; no image / image file pool objects exist.
	dep := &fakeDependencyReader{
		objects: map[string][]byte{
			depKey(string(metav1.KindStoragePool), vol.Spec.PoolRef): mustMarshal(t, storagePoolReady(vol.Spec.PoolRef)),
		},
	}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() result = %+v, want Done on a created data volume", result)
	}

	// Data volume derives no bytes: GetImage / CreateRootVolumeFromReader untouched.
	if getter.getCalls != 0 {
		t.Errorf("GetImage called %d times on a data volume, want 0", getter.getCalls)
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times on a data volume, want 0", creator.createCalls)
	}

	// CreateDataVolume called once with the right blank-volume request.
	if creator.dataCreateCalls != 1 {
		t.Fatalf("CreateDataVolume called %d times, want 1", creator.dataCreateCalls)
	}
	got := creator.gotDataReq
	if got.VMID != "vm-uid" {
		t.Errorf("data create VMID = %q, want %q", got.VMID, "vm-uid")
	}
	if got.VMName != "vm-name" {
		t.Errorf("data create VMName = %q, want %q", got.VMName, "vm-name")
	}
	if got.PoolName != "block-pool" {
		t.Errorf("data create PoolName = %q, want %q", got.PoolName, "block-pool")
	}
	if got.Name != "vol-data" {
		t.Errorf("data create Name = %q, want %q", got.Name, "vol-data")
	}
	if got.DiskIndex != 1 {
		t.Errorf("data create DiskIndex = %d, want 1", got.DiskIndex)
	}
	if got.CapacityBytes != 1<<30 {
		t.Errorf("data create CapacityBytes = %d, want %d", got.CapacityBytes, int64(1<<30))
	}

	// status ready + VolumePath from the created data volume.
	if len(dep.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(dep.patches))
	}
	patched := dep.patches[0].status
	if patched.Phase != volumev1.VolumePhaseReady {
		t.Errorf("patched phase = %q, want %q", patched.Phase, volumev1.VolumePhaseReady)
	}
	if patched.VolumePath != wantPath {
		t.Errorf("patched VolumePath = %q, want %q", patched.VolumePath, wantPath)
	}
}

// TestVolumeReconcileDataVolumeWaitsForPool proves a data volume requeues
// (without creating) while its block pool is not yet ready — the gate is the
// pool alone, with no spurious image dependency.
func TestVolumeReconcileDataVolumeWaitsForPool(t *testing.T) {
	vol := dataVolumeObject("vol-data")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{}
	// No objects wired: the block pool Get returns ErrNotFound → not ready.
	dep := &fakeDependencyReader{objects: map[string][]byte{}}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on a wait", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result = %+v, want Requeue while pool not ready", result)
	}
	if creator.dataCreateCalls != 0 {
		t.Errorf("CreateDataVolume called %d times while pool not ready, want 0", creator.dataCreateCalls)
	}
	if len(dep.patches) != 0 {
		t.Errorf("PatchStatus captured %d patches while waiting, want 0", len(dep.patches))
	}
}

func TestVolumeReconcileAllReadyCreatesRootVolume(t *testing.T) {
	vol := rootVolumeObject("vol-a")

	const imageBytes = "qcow2-image-bytes"
	wantPath := "/srv/pool/block-pool/vol-a/disk.qcow2"
	creator := &fakeRootVolumeCreator{
		created: volume.Volume{
			ID:       "vm-uid-root-0",
			Name:     "vol-a",
			PoolName: "block-pool",
			Context:  map[string]string{"path": wantPath},
		},
	}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader(imageBytes)}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false on success")
	}

	// GetImage 用被引用的 file pool + image ref 取流。
	if getter.getCalls != 1 {
		t.Fatalf("GetImage called %d times, want 1", getter.getCalls)
	}
	if getter.gotReq.PoolName != "file-pool" {
		t.Errorf("GetImage PoolName = %q, want %q", getter.gotReq.PoolName, "file-pool")
	}
	if getter.gotReq.ImageID != "img-a" {
		t.Errorf("GetImage ImageID = %q, want %q", getter.gotReq.ImageID, "img-a")
	}
	if !getter.reader.closed {
		t.Errorf("image reader Close not called; reader leaked")
	}

	// CreateRootVolumeFromReader 收到完整请求，format 权威 = Image.Spec.Format。
	if creator.createCalls != 1 {
		t.Fatalf("CreateRootVolumeFromReader called %d times, want 1", creator.createCalls)
	}
	got := creator.gotReq
	if got.VMID != "vm-uid" {
		t.Errorf("create VMID = %q, want %q", got.VMID, "vm-uid")
	}
	if got.VMName != "vm-name" {
		t.Errorf("create VMName = %q, want %q", got.VMName, "vm-name")
	}
	if got.PoolName != "block-pool" {
		t.Errorf("create PoolName = %q, want %q", got.PoolName, "block-pool")
	}
	if got.Name != "vol-a" {
		t.Errorf("create Name = %q, want %q", got.Name, "vol-a")
	}
	if got.DiskIndex != 0 {
		t.Errorf("create DiskIndex = %d, want 0", got.DiskIndex)
	}
	if got.CapacityBytes != 4<<30 {
		t.Errorf("create CapacityBytes = %d, want %d", got.CapacityBytes, int64(4<<30))
	}
	if got.Format != diskformat.FormatQCOW2 {
		t.Errorf("create Format = %q, want %q", got.Format, diskformat.FormatQCOW2)
	}
	if string(creator.gotReader) != imageBytes {
		t.Errorf("create Reader streamed %q, want %q", creator.gotReader, imageBytes)
	}

	// status ready + VolumePath。
	if len(dep.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(dep.patches))
	}
	patch := dep.patches[0]
	if patch.kind != string(metav1.KindVolume) {
		t.Errorf("patch kind = %q, want %q", patch.kind, metav1.KindVolume)
	}
	if patch.name != "vol-a" {
		t.Errorf("patch name = %q, want %q", patch.name, "vol-a")
	}
	if patch.status.Phase != volumev1.VolumePhaseReady {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, volumev1.VolumePhaseReady)
	}
	if patch.status.VolumePath != wantPath {
		t.Errorf("patch VolumePath = %q, want %q", patch.status.VolumePath, wantPath)
	}
	if patch.status.Message != "" {
		t.Errorf("patch message = %q, want empty on ready", patch.status.Message)
	}
}

func TestVolumeReconcileRawImageFormatPropagates(t *testing.T) {
	// format 权威 = Image.Spec.Format：raw 镜像应以 raw 喂入 create。
	vol := rootVolumeObject("vol-raw")
	creator := &fakeRootVolumeCreator{
		created: volume.Volume{Context: map[string]string{"path": "/p/disk.qcow2"}},
	}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("raw-bytes")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatRaw)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false")
	}
	if creator.gotReq.Format != diskformat.FormatRaw {
		t.Errorf("create Format = %q, want %q", creator.gotReq.Format, diskformat.FormatRaw)
	}
}

func TestVolumeReconcileImageNotReadyRequeues(t *testing.T) {
	vol := rootVolumeObject("vol-img-pending")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	// Override the image to pending.
	img := imageReadyObject(vol.Spec.ImageRef, imagev1.ImageFormatQCOW2)
	img.Status.Phase = imagev1.ImagePhasePending
	dep.objects[depKey(string(metav1.KindImage), vol.Spec.ImageRef)] = mustMarshal(t, img)

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for not-ready dependency", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true when image not ready")
	}
	if getter.getCalls != 0 {
		t.Errorf("GetImage called %d times, want 0 when image not ready", getter.getCalls)
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0 when image not ready", creator.createCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when waiting on a dependency", dep.patchCalls)
	}
}

func TestVolumeReconcileImageNotFoundRequeues(t *testing.T) {
	// 被引用 Image 还不存在（ErrNotFound）→ 等待（result.Requeue, 不报 failed）。
	vol := rootVolumeObject("vol-img-missing")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	delete(dep.objects, depKey(string(metav1.KindImage), vol.Spec.ImageRef))
	dep.notFound = map[string]bool{depKey(string(metav1.KindImage), vol.Spec.ImageRef): true}

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for ErrNotFound dependency", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true when image not found")
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0", creator.createCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when waiting", dep.patchCalls)
	}
}

func TestVolumeReconcileBlockPoolNotReadyRequeues(t *testing.T) {
	vol := rootVolumeObject("vol-pool-pending")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	dep.objects[depKey(string(metav1.KindStoragePool), vol.Spec.PoolRef)] =
		mustMarshal(t, storagePoolWithPhase(vol.Spec.PoolRef, storagepoolv1.PoolPhasePending))

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for not-ready pool", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true when block pool not ready")
	}
	if getter.getCalls != 0 {
		t.Errorf("GetImage called %d times, want 0 when block pool not ready", getter.getCalls)
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0", creator.createCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when waiting", dep.patchCalls)
	}
}

func TestVolumeReconcileImageFilePoolNotReadyRequeues(t *testing.T) {
	vol := rootVolumeObject("vol-filepool-pending")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	dep.objects[depKey(string(metav1.KindStoragePool), vol.Spec.ImageFilePoolRef)] =
		mustMarshal(t, storagePoolWithPhase(vol.Spec.ImageFilePoolRef, storagepoolv1.PoolPhaseFailed))

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for not-ready file pool", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true when image file pool not ready")
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0", creator.createCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when waiting", dep.patchCalls)
	}
}

func TestVolumeReconcileCreateFailureRequeues(t *testing.T) {
	vol := rootVolumeObject("vol-create-fail")
	createErr := errors.New("qemu-img convert failed")
	creator := &fakeRootVolumeCreator{createErr: createErr}
	reader := &trackingReadCloser{r: strings.NewReader("bytes")}
	getter := &fakeImageGetter{reader: reader}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want non-nil on create failure")
	}
	if !errors.Is(err, createErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, createErr)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true on transient create failure")
	}
	if !reader.closed {
		t.Errorf("image reader Close not called on create failure path")
	}
	if len(dep.patches) != 1 {
		t.Fatalf("PatchStatus captured %d patches, want 1", len(dep.patches))
	}
	patch := dep.patches[0]
	if patch.status.Phase != volumev1.VolumePhaseFailed {
		t.Errorf("patch phase = %q, want %q", patch.status.Phase, volumev1.VolumePhaseFailed)
	}
	if patch.status.Message == "" {
		t.Errorf("patch message empty, want failure cause")
	}
	if patch.status.VolumePath != "" {
		t.Errorf("patch VolumePath = %q, want empty on failure", patch.status.VolumePath)
	}
}

func TestVolumeReconcileGetImageFailureRequeues(t *testing.T) {
	vol := rootVolumeObject("vol-get-fail")
	getErr := errors.New("image not committed yet")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{getErr: getErr}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err == nil || !errors.Is(err, getErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, getErr)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true on transient GetImage failure")
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0 when GetImage failed", creator.createCalls)
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != volumev1.VolumePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", dep.patches)
	}
}

func TestVolumeReconcileUnsupportedFormatIsPermanentFailure(t *testing.T) {
	vol := rootVolumeObject("vol-bad-fmt")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	// Image is Ready but carries an unmappable format.
	img := imageReadyObject(vol.Spec.ImageRef, imagev1.ImageFormat("vmdk"))
	dep.objects[depKey(string(metav1.KindImage), vol.Spec.ImageRef)] = mustMarshal(t, img)

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for permanent config failure", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false for unsupported image format")
	}
	if getter.getCalls != 0 {
		t.Errorf("GetImage called %d times, want 0; format maps before any byte work", getter.getCalls)
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0", creator.createCalls)
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != volumev1.VolumePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", dep.patches)
	}
}

func TestVolumeReconcileDependencyReadErrorRequeuesWithoutPatch(t *testing.T) {
	// 依赖读取瞬时失败（非 ErrNotFound）→ result.Requeue 且不 patch failed（无法评估就绪）。
	vol := rootVolumeObject("vol-dep-err")
	readErr := errors.New("master unreachable")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	dep.getErr = map[string]error{depKey(string(metav1.KindStoragePool), vol.Spec.PoolRef): readErr}

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err == nil || !errors.Is(err, readErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, readErr)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true on transient dependency read failure")
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times, want 0", creator.createCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 when readiness could not be assessed", dep.patchCalls)
	}
}

func TestVolumeReconcileMissingHostPathRequeues(t *testing.T) {
	// create 成功但返回的 volume 无 host path：内部不一致 → 报 failed + result.Requeue。
	vol := rootVolumeObject("vol-no-path")
	creator := &fakeRootVolumeCreator{created: volume.Volume{Context: map[string]string{}}}
	reader := &trackingReadCloser{r: strings.NewReader("bytes")}
	getter := &fakeImageGetter{reader: reader}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)

	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventAdded, vol))
	if err == nil {
		t.Fatalf("Reconcile() error = nil, want non-nil when created volume has no host path")
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true on missing host path")
	}
	if len(dep.patches) != 1 || dep.patches[0].status.Phase != volumev1.VolumePhaseFailed {
		t.Fatalf("expected one failed patch, got %+v", dep.patches)
	}
}

func TestVolumeReconcileDeletedIsNoOp(t *testing.T) {
	vol := rootVolumeObject("vol-del")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := &fakeDependencyReader{}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventDeleted, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false")
	}
	if getter.getCalls != 0 || creator.createCalls != 0 {
		t.Errorf("downstream called on DELETED: get=%d create=%d, want 0/0", getter.getCalls, creator.createCalls)
	}
	if dep.getCalls != 0 || dep.patchCalls != 0 {
		t.Errorf("client called on DELETED: get=%d patch=%d, want 0/0", dep.getCalls, dep.patchCalls)
	}
}

func TestVolumeReconcileContextCancelledPropagates(t *testing.T) {
	vol := rootVolumeObject("vol-ctx")
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDeps(t, vol, imagev1.ImageFormatQCOW2)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := c.Reconcile(ctx, newVolumeEvent(t, controller.EventAdded, vol))
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconcile() error = %v, want wrapped context.Canceled", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false when context cancelled before work")
	}
	if getter.getCalls != 0 || creator.createCalls != 0 {
		t.Errorf("downstream called after ctx cancel: get=%d create=%d, want 0/0", getter.getCalls, creator.createCalls)
	}
	if dep.getCalls != 0 || dep.patchCalls != 0 {
		t.Errorf("client called after ctx cancel: get=%d patch=%d, want 0/0", dep.getCalls, dep.patchCalls)
	}
}

// deletingVolume returns a valid root volume stamped for deletion (carrying a
// deletionTimestamp), driving the controller into its teardown branch.
func deletingVolume(name string) volumev1.Volume {
	vol := rootVolumeObject(name)
	vol.ObjectMeta.DeletionTimestamp = "2026-01-02T15:04:05Z"
	return vol
}

// wantTeardownVolumeID derives the SAME server-side key the create path stores a
// volume under — volume.ID(fmt.Sprintf("%s-%s-%d", VMRef, role, DiskIndex)), see
// storage.VolumeService.CreateRootVolumeFromReader — so teardown tests assert the
// controller deletes by the real storage key, NOT the object name. Modeling real
// storage keying is what catches a teardown that keys by the wrong field: because
// a wrong-key delete returns the tolerated volume.ErrVolumeNotFound (silent leak),
// only asserting the exact VolumeID can detect the bug.
func wantTeardownVolumeID(vol volumev1.Volume) volume.ID {
	return volume.ID(fmt.Sprintf("%s-%s-%d", vol.Spec.VMRef, vol.Spec.Role, vol.Spec.DiskIndex))
}

// TestVolumeReconcileTeardownDeletesAndRemovesFinalizer proves the teardown
// branch: a deletion-stamped volume is deleted from its block pool keyed by the
// SERVER-DERIVED volume id (<VMRef>-<role>-<DiskIndex>, the same key the create
// path stored it under — NOT the object name) and the spec PoolRef, and, once
// deleted, the node-teardown finalizer is removed so apiserver can finalize the
// delete. The ensure path (GetImage / CreateRootVolumeFromReader) must not run.
func TestVolumeReconcileTeardownDeletesAndRemovesFinalizer(t *testing.T) {
	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := &fakeDependencyReader{}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	vol := deletingVolume("vol-del")
	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on successful teardown", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false after teardown + finalizer removal")
	}
	if creator.deleteCalls != 1 {
		t.Fatalf("DeleteVolume called %d times, want 1", creator.deleteCalls)
	}
	// Real storage keying: teardown must delete by the derived id the create path
	// stored, not the object name. wantTeardownVolumeID rebuilds that key.
	wantID := wantTeardownVolumeID(vol)
	if creator.gotDeleteReq.VolumeID != wantID {
		t.Errorf("DeleteVolume VolumeID = %q, want %q (derived <VMRef>-<role>-<DiskIndex>)", creator.gotDeleteReq.VolumeID, wantID)
	}
	if creator.gotDeleteReq.PoolName != "block-pool" {
		t.Errorf("DeleteVolume PoolName = %q, want %q", creator.gotDeleteReq.PoolName, "block-pool")
	}
	if creator.createCalls != 0 {
		t.Errorf("CreateRootVolumeFromReader called %d times during teardown, want 0", creator.createCalls)
	}
	if getter.getCalls != 0 {
		t.Errorf("GetImage called %d times during teardown, want 0", getter.getCalls)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1", dep.removeFinalizerCalls)
	}
	if dep.lastFinalizerName != "vol-del" {
		t.Errorf("RemoveFinalizer name = %q, want %q", dep.lastFinalizerName, "vol-del")
	}
	if dep.lastFinalizer != string(metav1.FinalizerNodeTeardown) {
		t.Errorf("RemoveFinalizer finalizer = %q, want %q", dep.lastFinalizer, metav1.FinalizerNodeTeardown)
	}
}

// TestVolumeReconcileTeardownAlreadyGoneIsIdempotent proves a teardown where the
// volume is already gone (volume.ErrVolumeNotFound) still drops the finalizer: an
// already-deleted volume is a tear-down success, not a stall.
func TestVolumeReconcileTeardownAlreadyGoneIsIdempotent(t *testing.T) {
	creator := &fakeRootVolumeCreator{deleteErr: volume.ErrVolumeNotFound}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := &fakeDependencyReader{}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	vol := deletingVolume("vol-gone")
	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil for already-deleted volume", err)
	}
	if result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = true, want false when volume already gone")
	}
	if creator.deleteCalls != 1 {
		t.Fatalf("DeleteVolume called %d times, want 1", creator.deleteCalls)
	}
	// Even on the idempotent NotFound path the controller must key by the derived
	// id (the storage layer matched the wrong-key delete to NotFound — exactly the
	// silent-leak the wrong convention produced), so assert the real key here too.
	if wantID := wantTeardownVolumeID(vol); creator.gotDeleteReq.VolumeID != wantID {
		t.Errorf("DeleteVolume VolumeID = %q, want %q (derived <VMRef>-<role>-<DiskIndex>)", creator.gotDeleteReq.VolumeID, wantID)
	}
	if dep.removeFinalizerCalls != 1 {
		t.Fatalf("RemoveFinalizer called %d times, want 1 (NotFound is idempotent success)", dep.removeFinalizerCalls)
	}
}

// TestVolumeReconcileTeardownDeleteFailureRequeuesKeepingFinalizer proves a real
// delete error (e.g. volume.ErrVolumeInUse: still attached to a running VM) keeps
// the finalizer and requeues so the referencing VM tears down first. This is the
// execution-layer backstop behind the apiserver reference guard.
func TestVolumeReconcileTeardownDeleteFailureRequeuesKeepingFinalizer(t *testing.T) {
	creator := &fakeRootVolumeCreator{deleteErr: volume.ErrVolumeInUse}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := &fakeDependencyReader{}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{}, dep)

	vol := deletingVolume("vol-busy")
	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err == nil || !errors.Is(err, volume.ErrVolumeInUse) {
		t.Fatalf("Reconcile() error = %v, want wrapped volume.ErrVolumeInUse", err)
	}
	if !result.Requeue {
		t.Fatalf("Reconcile() result.Requeue = false, want true on a real teardown conflict")
	}
	// The conflict must come from deleting the right (derived) key: assert it so a
	// regression to keying by the object name can't masquerade as this conflict.
	if wantID := wantTeardownVolumeID(vol); creator.gotDeleteReq.VolumeID != wantID {
		t.Errorf("DeleteVolume VolumeID = %q, want %q (derived <VMRef>-<role>-<DiskIndex>)", creator.gotDeleteReq.VolumeID, wantID)
	}
	if dep.removeFinalizerCalls != 0 {
		t.Fatalf("RemoveFinalizer called %d times, want 0 when teardown conflicts (finalizer kept)", dep.removeFinalizerCalls)
	}
}

// vmObject builds a VM object owning vol (UID = vol.Spec.VMRef, the identity the
// cold gate reads live phase by; Name = vol.Spec.VMName, the key the resize path
// Gets it under). The object's stored phase is irrelevant — 上下一致: the cold
// gate trusts the live vmm phase (fakeVMStatusReader), never this projection.
func vmObject(vol volumev1.Volume) vmv1.VM {
	return vmv1.VM{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindVM},
		ObjectMeta: metav1.ObjectMeta{Name: vol.Spec.VMName, UID: vol.Spec.VMRef},
	}
}

// readyDepsWithColdVM wires a dependency reader carrying the owning VM object so
// the resize path's client.Get(KindVM, vmName) resolves. The live cold/warm
// decision is the fakeVMStatusReader's job, not this object — so this helper just
// makes the VM object resolvable for any resize test that should get past the
// 404 gate.
func readyDepsWithColdVM(t *testing.T, vol volumev1.Volume) *fakeDependencyReader {
	t.Helper()
	return &fakeDependencyReader{
		objects: map[string][]byte{
			depKey(string(metav1.KindVM), vol.Spec.VMName): mustMarshal(t, vmObject(vol)),
		},
	}
}

// TestVolumeReconcileResizeVMNotFoundRequeues proves the orphan-volume guard
// (决策6): a ready volume whose owning VM object is gone (client.ErrNotFound) must
// wait (RequeueAfter) and must NOT resize — never grow a qcow2 for a VM that no
// longer exists.
func TestVolumeReconcileResizeVMNotFoundRequeues(t *testing.T) {
	vol := rootVolumeObject("vol-orphan")
	vol.Status.Phase = volumev1.VolumePhaseReady

	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	// No VM object registered → fakeDependencyReader.Get returns client.ErrNotFound.
	dep := &fakeDependencyReader{}
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{phase: vmm.PhaseStopped}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil when owning VM not found", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() result = %+v, want RequeueAfter when owning VM object missing", result)
	}
	if creator.resizeCalls != 0 {
		t.Errorf("ResizeVolume called %d times, want 0 for an orphan volume", creator.resizeCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 (volume stays Ready, just waits)", dep.patchCalls)
	}
}

// TestVolumeReconcileResizeVMNotColdDefers proves the cold gate (决策1): a ready
// volume whose owning VM is live-running is deferred (RequeueAfter) without
// resizing — the spec change is accepted but only lands once the VM is cold. The
// gate keys on the live vmm phase, not the VM object's status projection.
func TestVolumeReconcileResizeVMNotColdDefers(t *testing.T) {
	vol := rootVolumeObject("vol-warm")
	vol.Status.Phase = volumev1.VolumePhaseReady

	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDepsWithColdVM(t, vol)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{phase: vmm.PhaseRunning}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil when VM not cold", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() result = %+v, want RequeueAfter when owning VM is running", result)
	}
	if creator.resizeCalls != 0 {
		t.Errorf("ResizeVolume called %d times, want 0 while VM is running", creator.resizeCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 (volume stays Ready, deferred)", dep.patchCalls)
	}
}

// TestVolumeReconcileResizeColdConverges proves the convergence path (决策2): a
// ready volume whose owning VM is cold declares the absolute target capacity
// (spec.CapacityBytes) against the SERVER-DERIVED volume id + spec PoolRef, then
// reports Done. The controller never reads/compares live size — it hands the
// driver the absolute target and lets the driver decide idempotently.
func TestVolumeReconcileResizeColdConverges(t *testing.T) {
	vol := rootVolumeObject("vol-grow")
	vol.Status.Phase = volumev1.VolumePhaseReady

	creator := &fakeRootVolumeCreator{}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDepsWithColdVM(t, vol)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{phase: vmm.PhaseStopped}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil on converged resize", err)
	}
	if result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() result = %+v, want Done after a cold resize converged", result)
	}
	if creator.resizeCalls != 1 {
		t.Fatalf("ResizeVolume called %d times, want 1 on a cold volume", creator.resizeCalls)
	}
	got := creator.gotResizeReq
	if got.PoolName != vol.Spec.PoolRef {
		t.Errorf("ResizeVolume PoolName = %q, want %q", got.PoolName, vol.Spec.PoolRef)
	}
	if wantID := deriveVolumeID(vol.Spec); got.VolumeID != wantID {
		t.Errorf("ResizeVolume VolumeID = %q, want %q (derived <VMRef>-<role>-<DiskIndex>)", got.VolumeID, wantID)
	}
	if got.CapacityBytes != vol.Spec.CapacityBytes {
		t.Errorf("ResizeVolume CapacityBytes = %d, want %d (absolute target)", got.CapacityBytes, vol.Spec.CapacityBytes)
	}
	// 决策2: 控制器不读 live、不创建、不重新流式喂入。
	if creator.createCalls != 0 || getter.getCalls != 0 {
		t.Errorf("create/getImage touched on resize path: create=%d get=%d, want 0/0", creator.createCalls, getter.getCalls)
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 (already Ready, no-op guard)", dep.patchCalls)
	}
}

// TestVolumeReconcileResizeFailureKeepsReady proves the A2 failure semantics
// (决策5): a resize failure on a cold volume keeps phase=Ready (the volume is
// still usable — a failed grow does not negate the achieved availability), surfaces
// the error, and requeues for retry. It must NOT flip the volume to Failed.
func TestVolumeReconcileResizeFailureKeepsReady(t *testing.T) {
	vol := rootVolumeObject("vol-grow-fail")
	vol.Status.Phase = volumev1.VolumePhaseReady

	resizeErr := errors.New("qemu-img resize: no space left on device")
	creator := &fakeRootVolumeCreator{resizeErr: resizeErr}
	getter := &fakeImageGetter{reader: &trackingReadCloser{r: strings.NewReader("x")}}
	dep := readyDepsWithColdVM(t, vol)
	c := NewVolumeController(creator, getter, &fakeVMStatusReader{phase: vmm.PhaseStopped}, dep)

	result, err := c.Reconcile(context.Background(), newVolumeEvent(t, controller.EventModified, vol))
	if err == nil || !errors.Is(err, resizeErr) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, resizeErr)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("Reconcile() result = %+v, want RequeueAfter to retry the failed resize", result)
	}
	if creator.resizeCalls != 1 {
		t.Fatalf("ResizeVolume called %d times, want 1", creator.resizeCalls)
	}
	// A2: 卷仍可用 → 不翻 Failed，不 patch status。
	for _, p := range dep.patches {
		if p.status.Phase == volumev1.VolumePhaseFailed {
			t.Errorf("volume flipped to Failed on resize failure; want it to stay Ready (A2)")
		}
	}
	if dep.patchCalls != 0 {
		t.Errorf("PatchStatus called %d times, want 0 (Ready unchanged on resize failure)", dep.patchCalls)
	}
}
