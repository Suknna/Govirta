package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/imagestore"
	"github.com/suknna/govirta/internal/controlplane/store"
	imagev1 "github.com/suknna/govirta/pkg/apis/image/v1alpha1"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
)

func (s *Server) UploadImageStore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, apiErr := s.uploadImageStore(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write image upload error response")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if _, err := w.Write(body); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write image upload response")
	}
}

func (s *Server) DownloadImageStore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reader, ref, apiErr := s.openImageStore(ctx, r)
	if apiErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(apiErr.code)
		if _, err := w.Write(errorBody(apiErr)); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: write image download error response")
		}
		return
	}
	defer func() {
		if err := reader.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: close image download reader")
		}
	}()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Image-Format", ref.Format)
	w.Header().Set("X-Image-SHA256", ref.SHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(ref.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Msg("apiserver: stream image download")
	}
}

func (s *Server) uploadImageStore(ctx context.Context, r *http.Request) ([]byte, *apiError) {
	if s.imageStore == nil {
		return nil, unavailable(fmt.Errorf("apiserver: image store is not configured"))
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	query := r.URL.Query()
	format := query.Get("format")
	sha256 := query.Get("sha256")
	declaredSizeRaw := query.Get("declaredSizeBytes")
	uid := query.Get("uid")
	if format == "" || sha256 == "" || declaredSizeRaw == "" || uid == "" {
		return nil, badRequest(fmt.Errorf("apiserver: image upload requires format, sha256, declaredSizeBytes, and uid query parameters"))
	}
	imageFormat := imagev1.ImageFormat(format)
	if !imageFormat.Valid() {
		return nil, badRequest(fmt.Errorf("apiserver: invalid image upload format %q", format))
	}
	declaredSize, err := strconv.ParseInt(declaredSizeRaw, 10, 64)
	if err != nil {
		return nil, badRequest(fmt.Errorf("apiserver: invalid declaredSizeBytes %q: %w", declaredSizeRaw, err))
	}
	key := storeKey(metav1.KindImage, name)
	oldRaw, oldObj, err := s.getExistingObject(ctx, metav1.KindImage, key)
	if err != nil {
		return nil, internalErr(err)
	}
	var oldImage imagev1.Image
	if existing, ok := oldObj.(imagev1.Image); ok {
		oldImage = existing
		if oldImage.DeletionTimestamp != "" {
			return nil, conflictErr(fmt.Errorf("apiserver: cannot upload Image %q while deletionTimestamp is set", name))
		}
		if oldImage.UID != uid {
			return nil, conflictErr(fmt.Errorf("apiserver: upload Image %q uid %q does not match existing uid %q", name, uid, oldImage.UID))
		}
	}

	ref, err := s.imageStore.Put(ctx, imagestore.PutRequest{
		Name:              name,
		Version:           version,
		Format:            string(imageFormat),
		DeclaredSizeBytes: declaredSize,
		SHA256:            sha256,
		Reader:            r.Body,
	})
	if err != nil {
		return nil, imageStoreError("upload", name, version, err)
	}

	image := imagev1.Image{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindImage},
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			UID:        uid,
			Finalizers: []metav1.Finalizer{metav1.FinalizerImageCache},
		},
		Spec: imagev1.ImageSpec{
			Source: imagev1.ImageSource{
				Type:     imagev1.ImageSourceUpload,
				Location: s.imageStoreURL(name, version),
			},
			Format:            imagev1.ImageFormat(ref.Format),
			Version:           ref.Version,
			DeclaredSizeBytes: ref.SizeBytes,
			SHA256:            ref.SHA256,
		},
		Status: imagev1.ImageStatus{Phase: imagev1.ImagePhasePending},
	}
	if oldImage.UID != "" {
		image.ObjectMeta = oldImage.ObjectMeta
		image.ResourceVersion = ""
		image.Finalizers = oldImage.Finalizers
		if len(image.Finalizers) == 0 {
			image.Finalizers = []metav1.Finalizer{metav1.FinalizerImageCache}
		}
	}
	data, err := json.Marshal(image)
	if err != nil {
		return nil, internalErr(fmt.Errorf("apiserver: marshal upload Image %q: %w", name, err))
	}
	expectedVersion := ""
	if len(oldRaw.Value) != 0 {
		expectedVersion = oldRaw.ResourceVersion
	}
	raw, err := s.store.Put(ctx, key, data, expectedVersion)
	if err != nil {
		// ImageStore and metadata store are not transactional; metadata-gated
		// downloads make orphan bytes unreachable, while deleting here can remove
		// bytes referenced by a concurrent successful metadata write.
		if errors.Is(err, store.ErrRevisionConflict) {
			return nil, conflictErr(fmt.Errorf("apiserver: upload Image %q metadata: %w", name, err))
		}
		return nil, internalErr(fmt.Errorf("apiserver: upload Image %q metadata: %w", name, err))
	}
	image.ResourceVersion = raw.ResourceVersion
	return marshalResponse(image)
}

func (s *Server) openImageStore(ctx context.Context, r *http.Request) (io.ReadCloser, imagestore.ObjectRef, *apiError) {
	if s.imageStore == nil {
		return nil, imagestore.ObjectRef{}, unavailable(fmt.Errorf("apiserver: image store is not configured"))
	}
	name := r.PathValue("name")
	version := r.PathValue("version")
	image, apiErr := s.imageMetadataForDownload(ctx, name, version)
	if apiErr != nil {
		return nil, imagestore.ObjectRef{}, apiErr
	}
	reader, ref, err := s.imageStore.Open(ctx, name, version)
	if err != nil {
		return nil, imagestore.ObjectRef{}, imageStoreError("download", name, version, err)
	}
	if ref.SHA256 != image.Spec.SHA256 {
		if cerr := reader.Close(); cerr != nil {
			err = errors.Join(fmt.Errorf("apiserver: image store sha256 %q does not match Image metadata sha256 %q", ref.SHA256, image.Spec.SHA256), cerr)
			return nil, imagestore.ObjectRef{}, conflictErr(err)
		}
		return nil, imagestore.ObjectRef{}, conflictErr(fmt.Errorf("apiserver: image store sha256 %q does not match Image metadata sha256 %q", ref.SHA256, image.Spec.SHA256))
	}
	return reader, ref, nil
}

func (s *Server) imageMetadataForDownload(ctx context.Context, name, version string) (imagev1.Image, *apiError) {
	raw, err := s.store.Get(ctx, storeKey(metav1.KindImage, name))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return imagev1.Image{}, notFound(fmt.Errorf("apiserver: Image %q metadata: %w", name, err))
		}
		return imagev1.Image{}, internalErr(fmt.Errorf("apiserver: read Image %q metadata: %w", name, err))
	}
	obj, err := decodeObjectByKind(metav1.KindImage, raw.Value)
	if err != nil {
		return imagev1.Image{}, internalErr(fmt.Errorf("apiserver: decode Image %q metadata: %w", name, err))
	}
	image, ok := obj.(imagev1.Image)
	if !ok {
		return imagev1.Image{}, internalErr(fmt.Errorf("apiserver: decoded Image %q has type %T", name, obj))
	}
	if image.DeletionTimestamp != "" {
		return imagev1.Image{}, conflictErr(fmt.Errorf("apiserver: cannot download deleting Image %q", name))
	}
	if image.Spec.Source.Type != imagev1.ImageSourceUpload {
		return imagev1.Image{}, conflictErr(fmt.Errorf("apiserver: Image %q source.type %q is not upload", name, image.Spec.Source.Type))
	}
	if image.Spec.Version != version {
		return imagev1.Image{}, conflictErr(fmt.Errorf("apiserver: Image %q version %q does not match requested version %q", name, image.Spec.Version, version))
	}
	if image.Spec.Source.Location != s.imageStoreURL(name, version) {
		return imagev1.Image{}, conflictErr(fmt.Errorf("apiserver: Image %q source.location %q does not match canonical store URL", name, image.Spec.Source.Location))
	}
	return image, nil
}

func (s *Server) getExistingObject(ctx context.Context, kind metav1.Kind, key string) (store.RawObject, any, error) {
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.RawObject{}, nil, nil
		}
		return store.RawObject{}, nil, fmt.Errorf("apiserver: read existing %s %q: %w", kind, key, err)
	}
	obj, err := decodeObjectByKind(kind, raw.Value)
	if err != nil {
		return store.RawObject{}, nil, fmt.Errorf("apiserver: decode existing %s %q: %w", kind, key, err)
	}
	return raw, obj, nil
}

func (s *Server) imageStoreURL(name, version string) string {
	return s.imageStorePublicURL + "/apis/Image/" + name + "/store/" + version
}

func imageStoreError(operation, name, version string, err error) *apiError {
	wrapped := fmt.Errorf("apiserver: image store %s %s/%s: %w", operation, name, version, err)
	switch {
	case errors.Is(err, imagestore.ErrInvalidRequest), errors.Is(err, imagestore.ErrChecksumMismatch), errors.Is(err, imagestore.ErrUnsafePath):
		return badRequest(wrapped)
	case errors.Is(err, imagestore.ErrNotFound):
		return notFound(wrapped)
	case errors.Is(err, imagestore.ErrConflict):
		return conflictErr(wrapped)
	default:
		return internalErr(wrapped)
	}
}
