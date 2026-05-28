package volume

// AttachmentKind describes the storage object shape exposed to VM runtime code.
type AttachmentKind string

const (
	// AttachmentFile exposes the volume as a filesystem path to a disk image.
	AttachmentFile AttachmentKind = "file"
	// AttachmentBlockDevice exposes the volume as an existing host block device path.
	AttachmentBlockDevice AttachmentKind = "blockDevice"
	// AttachmentDirectory exposes the volume as a directory-style backend mount point.
	AttachmentDirectory AttachmentKind = "directory"
	// AttachmentNetwork exposes the volume through backend-specific network addressing.
	AttachmentNetwork AttachmentKind = "network"
)

// DiskFormat names the disk payload format when an attachment represents an image file.
type DiskFormat string

const (
	// DiskFormatQCOW2 identifies qcow2 images managed by the local block backend.
	DiskFormatQCOW2 DiskFormat = "qcow2"
	// DiskFormatRaw identifies raw disk payloads for backends that expose unwrapped bytes.
	DiskFormatRaw DiskFormat = "raw"
)

// Attachment is the runtime-facing handle returned after publishing a volume.
type Attachment struct {
	Kind       AttachmentKind
	Format     DiskFormat
	Path       string
	ReadOnly   bool
	Attributes map[string]string
}
