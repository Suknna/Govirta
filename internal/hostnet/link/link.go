package link

import (
	"context"
	"net"
)

// Manager owns host network link lifecycle operations.
//
// Implementations must require callers to pass every behavior-affecting
// parameter explicitly through the request structs. A nil context is an invalid
// request. A canceled context must be returned as the context error before an
// implementation performs live host networking work.
type Manager interface {
	// EnsureBridge creates or reconciles a bridge link to exactly match spec.
	// Implementations return linkerr.ErrInvalidRequest for invalid specs,
	// linkerr.ErrConflict when an existing non-bridge link has the same name, and
	// linkerr.ErrPermission when host privileges are insufficient.
	EnsureBridge(ctx context.Context, spec BridgeSpec) (LinkInfo, error)

	// EnsureTap creates or reconciles a TAP link to exactly match spec.
	// Implementations return linkerr.ErrInvalidRequest for invalid specs,
	// linkerr.ErrConflict when the requested bridge or existing link conflicts
	// with spec, and linkerr.ErrPermission when host privileges are insufficient.
	EnsureTap(ctx context.Context, spec TapSpec) (LinkInfo, error)

	// Delete removes the named host link.
	// Implementations return nil when the link is absent after the operation, and
	// return context or host errors when removal cannot be completed.
	Delete(ctx context.Context, name Name) error

	// Exists reports whether the named host link currently exists.
	// Implementations must return host lookup errors instead of silently treating
	// every lookup failure as a missing link.
	Exists(ctx context.Context, name Name) (bool, error)

	// Get returns the current host link state for name.
	// Implementations return linkerr.ErrNotFound when the link does not exist.
	Get(ctx context.Context, name Name) (LinkInfo, error)

	// List returns host links that match filter.
	// Implementations may return partial results with linkerr.ErrIncompleteList
	// when the platform reports that enumeration was incomplete.
	List(ctx context.Context, filter ListFilter) ([]LinkInfo, error)
}

// BridgeSpec describes the complete desired bridge state.
//
// GatewayCIDR must be an explicit CIDR string. MTU and MAC are required; zero
// MTU or nil/empty MAC values are invalid because the manager must not infer
// behavior-affecting bridge parameters.
type BridgeSpec struct {
	Name        Name
	GatewayCIDR string
	MTU         int
	MAC         net.HardwareAddr
}

// TapSpec describes the complete desired TAP state.
//
// OwnerUID and OwnerGID must have Set=true. UID/GID value 0 is valid only when
// passed explicitly, so callers that want root ownership must use ExplicitUID(0)
// and ExplicitGID(0). VNetHeader must be explicitly set to
// VNetHeaderEnabled or VNetHeaderDisabled. MTU and MAC are required; zero MTU or
// nil/empty MAC values are invalid.
type TapSpec struct {
	Name       Name
	BridgeName Name
	OwnerUID   UID
	OwnerGID   GID
	MTU        int
	MAC        net.HardwareAddr
	VNetHeader VNetHeaderMode
}

// ListFilter selects which host link kinds List returns.
//
// Kind must be explicitly set to KindAny, KindBridge, or KindTap. The empty
// value is invalid because list behavior must not rely on implicit defaults.
type ListFilter struct{ Kind Kind }

// LinkInfo reports observed host link state.
//
// Addresses contains stable CIDR strings. Linux implementations must sort this
// field before returning it so callers receive deterministic snapshots.
type LinkInfo struct {
	Name       Name
	Kind       Kind
	Index      int
	MTU        int
	MAC        net.HardwareAddr
	AdminState AdminState
	MasterName Name
	Addresses  []string
}
