package volume

// ID is the stable identifier assigned by storage to a volume.
type ID string

// State describes whether a volume is free for offline mutation or published to a VM.
type State string

const (
	// StateAvailable means the volume is not published and may be used for cold operations.
	StateAvailable State = "available"
	// StatePublished means the volume has an active runtime attachment contract.
	StatePublished State = "published"
)

// Role describes how VM orchestration intends to use a volume.
type Role string

const (
	// RoleRoot identifies the VM boot/root disk volume.
	RoleRoot Role = "root"
	// RoleData identifies an additional non-boot data disk volume.
	RoleData Role = "data"
)

// Spec is the caller-supplied contract for creating a VM-scoped block volume.
type Spec struct {
	Name          string
	Role          Role
	DiskIndex     int
	CapacityBytes int64
	ReadOnly      bool
}

// Volume is the storage-owned metadata snapshot for a single block volume.
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

// AttachmentState records the current publish result while a volume is in use.
type AttachmentState struct {
	VMID       string
	ReadOnly   bool
	Attachment Attachment
}

// PublishedVolume is returned to orchestration after storage prepares runtime access.
type PublishedVolume struct {
	VolumeID   ID
	VMID       string
	PoolName   string
	Attachment Attachment
}

// Snapshot names an offline point-in-time copy associated with a volume.
type Snapshot struct {
	ID       string
	Name     string
	VolumeID ID
}
