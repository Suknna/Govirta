package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"

	"github.com/suknna/govirta/internal/node/controller"
	"github.com/suknna/govirta/internal/storage"
	"github.com/suknna/govirta/internal/storage/diskformat"
	"github.com/suknna/govirta/internal/storage/image"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

// 永久错误（配置错误）的哨兵：requeue 重试无法修复，必须报告 failed 但不重排。
// Permanent (config) error sentinels: a requeue cannot fix these, so they are
// reported as failed without being re-enqueued.
var (
	// errUnsupportedSource 标记无法识别的源类型或源 URL scheme（如 ftp://）。
	errUnsupportedSource = errors.New("image controller: unsupported source")
	// errUnsafeSource 标记越出允许根目录、相对路径或经 symlink 逃逸的本地文件源。
	errUnsafeSource = errors.New("image controller: unsafe file source path")
	// errUnsupportedFormat 标记没有映射到本地 diskformat 的镜像格式。
	errUnsupportedFormat = errors.New("image controller: unsupported image format")
)

// ImagePutter 是控制器需要的 ImageService 的窄切片：在文件池中打开一个镜像写入器。
// *storage.ImageService 满足它（积木式 + 可测）。
//
// ImagePutter is the narrow slice of the image service the controller needs:
// open a writer for a new image in a file pool, and delete a committed image on
// teardown. *storage.ImageService satisfies it.
type ImagePutter interface {
	PutImage(ctx context.Context, req storage.PutImageRequest) (image.ImageWriter, error)
	DeleteImage(ctx context.Context, req storage.DeleteImageRequest) error
}

// 编译期证明真实生产类型满足窄接口。
var _ ImagePutter = (*storage.ImageService)(nil)

// ImageController reconciles Image objects: it fetches source bytes from the
// spec's explicit source (a node-local file path or an http(s) URL) into the
// local file pool via PutImage, then reports a ready/failed status with the
// committed local size up to the master.
//
// 证明 etcd-only 控制面下 blob 走带外分发：对象只承载“从哪取字节”的意图，真正的
// 字节由本控制器在节点侧拉取并落入本地池，绝不进 etcd。
type ImageController struct {
	images ImagePutter
	client StatusReporter
	httpc  *http.Client
	// sourceRoot 是 file 源允许的根目录；任何解析后越出该根的路径都视为不安全。
	// 这是 plan 描述之外为满足路径安全铁律而增加的字段（见报告“偏离决策”）。
	sourceRoot string
}

var _ controller.Controller = (*ImageController)(nil)

// NewImageController wires an ImageController against the image service, the
// master status client, an HTTP client for http(s) sources, and the allowed
// root directory for local file sources. A nil httpc falls back to
// http.DefaultClient.
func NewImageController(images ImagePutter, client StatusReporter, httpc *http.Client, sourceRoot string) *ImageController {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &ImageController{images: images, client: client, httpc: httpc, sourceRoot: sourceRoot}
}

// Kind is the apis kind this controller watches.
func (c *ImageController) Kind() string {
	return string(metav1.KindImage)
}

// Reconcile drives one Image event toward its desired state.
//
// DELETED is a no-op in this slice. For ADDED/MODIFIED it decodes the object,
// fetches the source bytes into the file pool, and patches a ready status with
// the committed local size. A permanent (config) failure — unsupported source,
// unsafe path, or unmappable format — is reported failed and NOT requeued: a
// retry cannot fix it. A transport failure (open/get/copy/commit/PutImage) is
// reported failed and requeued.
func (c *ImageController) Reconcile(ctx context.Context, ev controller.Event) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("image controller: context done before reconcile: %w", err)
	}

	logger := zerolog.Ctx(ctx)

	if ev.Type == controller.EventDeleted {
		logger.Info().
			Str("kind", c.Kind()).
			Str("key", ev.Key).
			Msg("image deleted; delete is a no-op in this slice")
		return false, nil
	}

	var img imagev1.Image
	if err := json.Unmarshal(ev.Object, &img); err != nil {
		return false, fmt.Errorf("image controller: decode object %q: %w", ev.Key, err)
	}

	// Teardown branch: a deletion-stamped object means apiserver wants this image
	// gone. Delete the committed image from its file pool before the ensure path
	// runs. A teardown failure keeps the finalizer (object stays "deleting") and
	// requeues.
	if isDeleting(img.ObjectMeta) {
		if err := c.teardown(ctx, img); err != nil {
			return true, fmt.Errorf("image controller: teardown %q: %w", img.Name, err)
		}
		if err := removeTeardownFinalizer(ctx, c.client, c.Kind(), img.Name); err != nil {
			return true, fmt.Errorf("image controller: remove finalizer %q: %w", img.Name, err)
		}
		return false, nil
	}

	// Level-triggered idempotence: a ready image is already at its desired state.
	// Re-reconciling (e.g. on the MODIFIED event our own ready-patch produced)
	// must not re-fetch — PutImage would return ErrImageExists, which fails
	// before reaching the no-op-guarded status patch, so the controller would
	// spin forever (the exact e2e blind-spot loop). The early return is the only
	// thing that breaks it, because the failure happens in fetch, not in patch.
	if img.Status.Phase == imagev1.ImagePhaseReady {
		return false, nil
	}

	copied, err := c.fetch(ctx, img)
	if err != nil {
		permanent := isPermanent(err)
		if perr := c.reportFailure(ctx, img.Name, img.Status, err); perr != nil {
			// 状态上报也失败：永久错误不重排，瞬时错误重排。
			return !permanent, fmt.Errorf("image controller: fetch %q failed and status report failed: %w", img.Name, errors.Join(err, perr))
		}
		if permanent {
			logger.Error().Err(err).Str("key", ev.Key).Msg("image fetch failed permanently (config error); not requeuing")
			return false, nil
		}
		return true, fmt.Errorf("image controller: fetch %q: %w", img.Name, err)
	}

	status := imagev1.ImageStatus{
		Phase:          imagev1.ImagePhaseReady,
		LocalSizeBytes: copied,
	}
	if err := c.patchStatus(ctx, img.Name, img.Status, status); err != nil {
		return true, err
	}

	logger.Info().
		Str("key", ev.Key).
		Int64("localSizeBytes", copied).
		Msg("image ready")
	return false, nil
}

// teardown deletes the committed image from its file pool. The format must be
// mapped (DeleteImage matches on it); an unmappable format is propagated as a
// permanent error — the finalizer stays and the reconcile requeues, but a
// requeue cannot fix a bad format, so it surfaces loudly rather than silently
// dropping the finalizer on a half-understood object (ensure validated the
// format already, so this is a defensive guard). Deleting an already-gone image
// (image.ErrImageNotFound) is an idempotent success so a re-driven teardown
// still progresses to dropping the finalizer.
func (c *ImageController) teardown(ctx context.Context, img imagev1.Image) error {
	format, err := mapImageFormat(img.Spec.Format)
	if err != nil {
		return err
	}
	if err := c.images.DeleteImage(ctx, storage.DeleteImageRequest{
		PoolName: img.Spec.FilePoolRef,
		ImageID:  img.Name,
		Format:   format,
	}); err != nil && !errors.Is(err, image.ErrImageNotFound) {
		return fmt.Errorf("image controller: delete image %q from pool %q: %w", img.Name, img.Spec.FilePoolRef, err)
	}
	return nil
}

// fetch maps the format, then copies the source bytes into the file pool and
// returns the number of committed bytes.
func (c *ImageController) fetch(ctx context.Context, img imagev1.Image) (int64, error) {
	// 格式映射在打开源之前完成：格式非法是永久错误，不应付出网络/IO 代价。
	format, err := mapImageFormat(img.Spec.Format)
	if err != nil {
		return 0, err
	}
	return c.copyIntoPool(ctx, img, format)
}

// copyIntoPool opens the source reader, opens a pool writer via PutImage, copies
// the bytes, and commits. On a copy/source-close failure it cancels the pending
// write so the pool does not keep a partial image. The source reader is closed
// before deciding commit-vs-cancel so its close error can abort the commit.
func (c *ImageController) copyIntoPool(ctx context.Context, img imagev1.Image, format diskformat.Format) (int64, error) {
	reader, err := c.openSource(ctx, img.Spec.Source)
	if err != nil {
		return 0, err
	}

	writer, err := c.images.PutImage(ctx, storage.PutImageRequest{
		PoolName:          img.Spec.FilePoolRef,
		ImageID:           img.Name,
		Format:            format,
		DeclaredSizeBytes: img.Spec.DeclaredSizeBytes,
	})
	if err != nil {
		// PutImage 失败：尚未拥有任何字节，关闭源并合并 close 错误后传播。
		return 0, errors.Join(fmt.Errorf("image controller: put image %q: %w", img.Name, err), closeReader(reader))
	}

	copied, copyErr := io.Copy(writer, reader)
	// 在判定 commit/cancel 之前关闭源：源 close 错误也应使写入回滚。
	closeErr := closeReader(reader)
	if copyErr != nil || closeErr != nil {
		transfer := errors.Join(copyErr, closeErr)
		if cancelErr := writer.Cancel(); cancelErr != nil {
			transfer = errors.Join(transfer, fmt.Errorf("image controller: cancel image writer: %w", cancelErr))
		}
		return copied, fmt.Errorf("image controller: transfer image %q: %w", img.Name, transfer)
	}

	// Close 提交 pending→ready；成功路径也必须传播其错误。
	if err := writer.Close(); err != nil {
		return copied, fmt.Errorf("image controller: commit image %q: %w", img.Name, err)
	}
	return copied, nil
}

// openSource dispatches on the explicit source type. An unknown type is a
// permanent config error.
func (c *ImageController) openSource(ctx context.Context, src imagev1.ImageSource) (io.ReadCloser, error) {
	switch src.Type {
	case imagev1.ImageSourceFile:
		return c.openFileSource(src.Location)
	case imagev1.ImageSourceHTTP:
		return c.openHTTPSource(ctx, src.Location)
	default:
		return nil, fmt.Errorf("%w: source type %q", errUnsupportedSource, src.Type)
	}
}

// openFileSource opens a node-local file source after validating that the path
// stays inside the configured source root. It rejects relative paths and any
// path that escapes the root either lexically or via a symlink (参考 storage
// 层 symlink 拒绝惯例).
func (c *ImageController) openFileSource(location string) (io.ReadCloser, error) {
	// file 源 location 可带可选的 file:// 前缀。
	path := strings.TrimPrefix(location, "file://")

	if c.sourceRoot == "" {
		return nil, fmt.Errorf("%w: file sources require a configured source root", errUnsafeSource)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: file source must be absolute: %q", errUnsafeSource, path)
	}

	clean := filepath.Clean(path)
	root := filepath.Clean(c.sourceRoot)
	if !pathWithin(root, clean) {
		return nil, fmt.Errorf("%w: %q escapes source root %q", errUnsafeSource, clean, root)
	}

	// 解析 symlink 后再次校验包含关系，挫败 symlink 逃逸。macOS 临时目录本身就是
	// 符号链接，因此对 root 也做同样解析以保持比较一致。EvalSymlinks 要求目标存在；
	// 不存在时交由下面的 os.Open 给出权威错误并按瞬时失败处理。
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		rootResolved, rerr := filepath.EvalSymlinks(root)
		if rerr != nil {
			return nil, fmt.Errorf("image controller: resolve source root %q: %w", root, rerr)
		}
		if !pathWithin(rootResolved, resolved) {
			return nil, fmt.Errorf("%w: %q resolves outside root via symlink", errUnsafeSource, clean)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("image controller: resolve source path %q: %w", clean, err)
	}

	f, err := os.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("image controller: open file source %q: %w", clean, err)
	}
	return f, nil
}

// openHTTPSource issues a ctx-bound GET against an http(s) URL. A non-http(s)
// scheme is a permanent config error; a transport/non-2xx failure is transient.
func (c *ImageController) openHTTPSource(ctx context.Context, location string) (io.ReadCloser, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("%w: parse url %q: %v", errUnsupportedSource, location, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("%w: url scheme %q", errUnsupportedSource, u.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, fmt.Errorf("image controller: build http request for %q: %w", location, err)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image controller: http get %q: %w", location, err)
	}
	if resp.StatusCode/100 != 2 {
		statusErr := fmt.Errorf("image controller: http get %q: status %d", location, resp.StatusCode)
		return nil, errors.Join(statusErr, closeReader(resp.Body))
	}
	return resp.Body, nil
}

// reportFailure patches a failed status carrying cause's message, skipping the
// PATCH when the observed status already matches (no-op guard).
func (c *ImageController) reportFailure(ctx context.Context, name string, observed imagev1.ImageStatus, cause error) error {
	return c.patchStatus(ctx, name, observed, imagev1.ImageStatus{
		Phase:   imagev1.ImagePhaseFailed,
		Message: cause.Error(),
	})
}

// patchStatus marshals desired and PATCHes it to the master's /status
// sub-resource, but only when it differs from observed (the status carried by the
// watched object). Skipping an identical PATCH breaks the status→MODIFIED→watch→
// reconcile→PATCH feedback loop that would otherwise spin every reconcile (level-
// triggered idempotence). The Status structs are comparable (scalar fields only),
// so == is a sound equality test.
func (c *ImageController) patchStatus(ctx context.Context, name string, observed, desired imagev1.ImageStatus) error {
	if observed == desired {
		return nil
	}
	body, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("image controller: marshal status for %q: %w", name, err)
	}
	if _, err := c.client.PatchStatus(ctx, c.Kind(), name, body); err != nil {
		return fmt.Errorf("image controller: patch status for %q: %w", name, err)
	}
	return nil
}

// mapImageFormat converts an apis image format to the storage diskformat,
// converting explicitly (no blind string conversion). An unknown format is a
// permanent config error.
func mapImageFormat(f imagev1.ImageFormat) (diskformat.Format, error) {
	switch f {
	case imagev1.ImageFormatQCOW2:
		return diskformat.FormatQCOW2, nil
	case imagev1.ImageFormatRaw:
		return diskformat.FormatRaw, nil
	default:
		return "", fmt.Errorf("%w: %q", errUnsupportedFormat, f)
	}
}

// isPermanent reports whether err is a config error a requeue cannot fix.
func isPermanent(err error) bool {
	return errors.Is(err, errUnsupportedSource) ||
		errors.Is(err, errUnsafeSource) ||
		errors.Is(err, errUnsupportedFormat)
}

// pathWithin reports whether path is root or nested under root, lexically.
func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// closeReader closes r and wraps any error for propagation (禁止吞错).
func closeReader(r io.ReadCloser) error {
	if err := r.Close(); err != nil {
		return fmt.Errorf("image controller: close source reader: %w", err)
	}
	return nil
}
