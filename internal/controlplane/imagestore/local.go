package imagestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	imageFileName            = "image"
	metadataFileName         = "metadata.json"
	recoveryMetadataFileName = "metadata.recover.json"
)

var safeSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var _ Store = (*LocalStore)(nil)

// LocalStore stores image objects under an explicit local filesystem root.
type LocalStore struct {
	mu   sync.Mutex
	root string
}

// NewLocal creates a local image store rooted under an absolute directory.
func NewLocal(root string) (*LocalStore, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: root must be an absolute path", ErrInvalidRequest)
	}
	clean := filepath.Clean(root)
	if err := ensureNoSymlinkLeaf(clean); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return nil, fmt.Errorf("imagestore: create root %q: %w", clean, err)
	}
	if err := ensureExistingDir(clean); err != nil {
		return nil, err
	}
	return &LocalStore{root: clean}, nil
}

// Put stores one object atomically after validating its declared size and SHA256.
func (s *LocalStore) Put(ctx context.Context, req PutRequest) (ObjectRef, error) {
	if ctx == nil {
		return ObjectRef{}, fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return ObjectRef{}, err
	}
	if err := validatePutRequest(req); err != nil {
		return ObjectRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	objectDir, imagePath, metadataPath, recoveryPath, err := s.objectPaths(req.Name, req.Version)
	if err != nil {
		return ObjectRef{}, err
	}
	ref := ObjectRef{
		Name:      req.Name,
		Version:   req.Version,
		Format:    req.Format,
		SizeBytes: req.DeclaredSizeBytes,
		SHA256:    strings.ToLower(req.SHA256),
		Path:      imagePath,
	}

	if existing, err := s.readMetadata(objectDir, metadataPath); err == nil {
		if existing == ref {
			return existing, nil
		}
		return ObjectRef{}, fmt.Errorf("%w: image %s/%s already exists with different metadata", ErrConflict, req.Name, req.Version)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ObjectRef{}, err
	}

	if err := s.ensureObjectDir(objectDir); err != nil {
		return ObjectRef{}, err
	}
	if recovered, err := s.recoverMissingMetadata(imagePath, metadataPath, recoveryPath, ref); err != nil {
		return ObjectRef{}, err
	} else if recovered {
		return ref, nil
	}

	tmp, err := os.CreateTemp(objectDir, "upload.tmp.*")
	if err != nil {
		return ObjectRef{}, fmt.Errorf("imagestore: create upload temp: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	actualSHA, copied, copyErr := copyWithHash(ctx, tmp, req.Reader)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		return ObjectRef{}, errors.Join(copyErr, closeErr)
	}
	if copied != req.DeclaredSizeBytes {
		return ObjectRef{}, fmt.Errorf("%w: declared size %d does not match copied size %d", ErrInvalidRequest, req.DeclaredSizeBytes, copied)
	}
	if actualSHA != ref.SHA256 {
		return ObjectRef{}, fmt.Errorf("%w: expected %s got %s", ErrChecksumMismatch, ref.SHA256, actualSHA)
	}

	if err := writeMetadata(recoveryPath, ref); err != nil {
		return ObjectRef{}, err
	}
	if err := s.commitImage(tmpPath, imagePath); err != nil {
		return ObjectRef{}, err
	}
	if err := os.Remove(tmpPath); err != nil {
		return ObjectRef{}, fmt.Errorf("imagestore: remove upload temp %q: %w", tmpPath, err)
	}
	committed = true

	if err := writeMetadata(metadataPath, ref); err != nil {
		return ObjectRef{}, err
	}
	return ref, nil
}

// Get returns committed metadata for a named image object version.
func (s *LocalStore) Get(ctx context.Context, name, version string) (ObjectRef, error) {
	if err := validateContext(ctx); err != nil {
		return ObjectRef{}, err
	}
	objectDir, _, metadataPath, _, err := s.objectPaths(name, version)
	if err != nil {
		return ObjectRef{}, err
	}
	ref, err := s.readMetadata(objectDir, metadataPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ObjectRef{}, fmt.Errorf("%w: %s/%s", ErrNotFound, name, version)
		}
		return ObjectRef{}, err
	}
	return ref, nil
}

// Open opens committed image bytes and returns their metadata.
func (s *LocalStore) Open(ctx context.Context, name, version string) (io.ReadCloser, ObjectRef, error) {
	if err := validateContext(ctx); err != nil {
		return nil, ObjectRef{}, err
	}
	objectDir, imagePath, metadataPath, _, err := s.objectPaths(name, version)
	if err != nil {
		return nil, ObjectRef{}, err
	}
	ref, err := s.readMetadata(objectDir, metadataPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ObjectRef{}, fmt.Errorf("%w: %s/%s", ErrNotFound, name, version)
		}
		return nil, ObjectRef{}, err
	}
	if ref.Path != imagePath {
		return nil, ObjectRef{}, fmt.Errorf("%w: metadata path %q does not match expected path %q", ErrUnsafePath, ref.Path, imagePath)
	}
	if err := s.ensureExistingObjectDir(objectDir); err != nil {
		return nil, ObjectRef{}, err
	}
	if err := ensureNoSymlinkLeaf(imagePath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ObjectRef{}, fmt.Errorf("%w: image file missing %q: %w", ErrNotFound, imagePath, err)
		}
		return nil, ObjectRef{}, err
	}
	file, err := os.Open(imagePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ObjectRef{}, fmt.Errorf("%w: %s/%s", ErrNotFound, name, version)
		}
		return nil, ObjectRef{}, fmt.Errorf("imagestore: open image %q: %w", imagePath, err)
	}
	return file, ref, nil
}

// Delete removes an image object only when the caller supplies the matching SHA256.
func (s *LocalStore) Delete(ctx context.Context, name, version string, sha256 string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	objectDir, _, metadataPath, _, err := s.objectPaths(name, version)
	if err != nil {
		return err
	}
	wantSHA, err := validateSHA256(sha256)
	if err != nil {
		return err
	}
	ref, err := s.readMetadata(objectDir, metadataPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if ref.SHA256 != wantSHA {
		return fmt.Errorf("%w: delete sha256 %s does not match stored sha256 %s", ErrConflict, wantSHA, ref.SHA256)
	}
	if err := s.ensureExistingObjectDir(objectDir); err != nil {
		return err
	}
	if err := os.RemoveAll(objectDir); err != nil {
		return fmt.Errorf("imagestore: delete object %q: %w", objectDir, err)
	}
	return nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	return ctx.Err()
}

func validatePutRequest(req PutRequest) error {
	if req.Reader == nil {
		return fmt.Errorf("%w: nil reader", ErrInvalidRequest)
	}
	if !safeSegment(req.Name) || !safeSegment(req.Version) || !safeSegment(req.Format) || req.DeclaredSizeBytes <= 0 {
		return ErrInvalidRequest
	}
	_, err := validateSHA256(req.SHA256)
	return err
}

func validateSHA256(value string) (string, error) {
	lower := strings.ToLower(value)
	decoded, err := hex.DecodeString(lower)
	if err != nil || len(decoded) != sha256.Size || value != lower {
		return "", fmt.Errorf("%w: invalid sha256", ErrInvalidRequest)
	}
	return lower, nil
}

func safeSegment(segment string) bool {
	if segment == "." || segment == ".." || strings.ContainsAny(segment, `/\`) {
		return false
	}
	return safeSegmentPattern.MatchString(segment)
}

func (s *LocalStore) objectPaths(name, version string) (string, string, string, string, error) {
	if !safeSegment(name) || !safeSegment(version) {
		return "", "", "", "", ErrInvalidRequest
	}
	objectDir := filepath.Join(s.root, "images", name, version)
	imagePath := filepath.Join(objectDir, imageFileName)
	metadataPath := filepath.Join(objectDir, metadataFileName)
	recoveryPath := filepath.Join(objectDir, recoveryMetadataFileName)
	for _, path := range []string{objectDir, imagePath, metadataPath, recoveryPath} {
		if !pathWithin(s.root, path) {
			return "", "", "", "", fmt.Errorf("%w: %q escapes root %q", ErrUnsafePath, path, s.root)
		}
	}
	return objectDir, imagePath, metadataPath, recoveryPath, nil
}

func (s *LocalStore) ensureObjectDir(objectDir string) error {
	imagesRoot := filepath.Join(s.root, "images")
	if err := s.mkdirSafe(imagesRoot); err != nil {
		return err
	}
	nameDir := filepath.Dir(objectDir)
	if err := s.mkdirSafe(nameDir); err != nil {
		return err
	}
	return s.mkdirSafe(objectDir)
}

func (s *LocalStore) recoverMissingMetadata(imagePath, metadataPath, recoveryPath string, ref ObjectRef) (bool, error) {
	if err := ensureNoSymlinkLeaf(imagePath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	recoveryRef, err := s.readMetadata(filepath.Dir(recoveryPath), recoveryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("%w: image exists without recovery metadata: %s", ErrConflict, imagePath)
		}
		return false, err
	}
	if recoveryRef != ref {
		return false, fmt.Errorf("%w: recovery metadata does not match request: %s", ErrConflict, recoveryPath)
	}
	actualSHA, size, err := hashExistingFile(imagePath)
	if err != nil {
		return false, err
	}
	if size != ref.SizeBytes || actualSHA != ref.SHA256 {
		return false, fmt.Errorf("%w: image exists without matching metadata: %s", ErrConflict, imagePath)
	}
	if err := writeMetadata(metadataPath, ref); err != nil {
		return false, err
	}
	return true, nil
}

func (s *LocalStore) commitImage(tmpPath, imagePath string) error {
	if err := ensureNoSymlinkLeaf(imagePath); err == nil {
		return fmt.Errorf("%w: image file already exists: %s", ErrConflict, imagePath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Link(tmpPath, imagePath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: image file already exists: %s", ErrConflict, imagePath)
		}
		return fmt.Errorf("imagestore: commit image %q: %w", imagePath, err)
	}
	return nil
}

func (s *LocalStore) mkdirSafe(path string) error {
	if err := s.ensureNoSymlinkPathComponents(filepath.Dir(path)); err != nil {
		return err
	}
	if err := ensureNoSymlinkLeaf(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ensureExistingDir(path)
		}
		return fmt.Errorf("imagestore: create directory %q: %w", path, err)
	}
	return ensureExistingDir(path)
}

func (s *LocalStore) ensureExistingObjectDir(objectDir string) error {
	if err := s.ensureNoSymlinkPathComponents(objectDir); err != nil {
		return err
	}
	return ensureExistingDir(objectDir)
}

func (s *LocalStore) ensureNoSymlinkPathComponents(path string) error {
	clean := filepath.Clean(path)
	if !pathWithin(s.root, clean) {
		return fmt.Errorf("%w: %q escapes root %q", ErrUnsafePath, clean, s.root)
	}
	if err := ensureExistingDir(s.root); err != nil {
		return err
	}
	rel, err := filepath.Rel(s.root, clean)
	if err != nil {
		return fmt.Errorf("%w: relate %q to root %q: %v", ErrUnsafePath, clean, s.root, err)
	}
	if rel == "." {
		return nil
	}
	current := s.root
	for _, component := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("imagestore: inspect path component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink path component %q", ErrUnsafePath, current)
		}
	}
	return nil
}

func ensureExistingDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink directory %q", ErrUnsafePath, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: not a directory %q", ErrUnsafePath, path)
	}
	return nil
}

func ensureNoSymlinkLeaf(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink leaf %q", ErrUnsafePath, path)
	}
	return nil
}

func (s *LocalStore) readMetadata(objectDir, path string) (ObjectRef, error) {
	if !pathWithin(s.root, path) {
		return ObjectRef{}, fmt.Errorf("%w: metadata path %q escapes root %q", ErrUnsafePath, path, s.root)
	}
	if err := s.ensureExistingObjectDir(objectDir); err != nil {
		return ObjectRef{}, err
	}
	if err := ensureNoSymlinkLeaf(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return ObjectRef{}, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ObjectRef{}, err
	}
	var ref ObjectRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return ObjectRef{}, fmt.Errorf("imagestore: decode metadata %q: %w", path, err)
	}
	if !pathWithin(s.root, ref.Path) {
		return ObjectRef{}, fmt.Errorf("%w: stored path %q escapes root %q", ErrUnsafePath, ref.Path, s.root)
	}
	return ref, nil
}

func writeMetadata(path string, ref ObjectRef) error {
	if err := ensureNoSymlinkLeaf(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(ref, "", "  ")
	if err != nil {
		return fmt.Errorf("imagestore: encode metadata: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "metadata.tmp.*")
	if err != nil {
		return fmt.Errorf("imagestore: create metadata temp: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		closeErr := tmp.Close()
		return errors.Join(fmt.Errorf("imagestore: write metadata temp: %w", err), closeErr)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("imagestore: close metadata temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("imagestore: commit metadata %q: %w", path, err)
	}
	committed = true
	return nil
}

func copyWithHash(ctx context.Context, dst io.Writer, src io.Reader) (string, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return "", copied, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			if _, err := dst.Write(chunk); err != nil {
				return "", copied, fmt.Errorf("imagestore: write upload temp: %w", err)
			}
			if _, err := hash.Write(chunk); err != nil {
				return "", copied, fmt.Errorf("imagestore: hash upload bytes: %w", err)
			}
			copied += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), copied, nil
		}
		if readErr != nil {
			return "", copied, fmt.Errorf("imagestore: read upload bytes: %w", readErr)
		}
	}
}

func hashExistingFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("imagestore: open existing image %q: %w", path, err)
	}
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("imagestore: hash existing image %q: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

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
