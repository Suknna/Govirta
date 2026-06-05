package link

type Name string
type Kind string
type AdminState string
type VNetHeaderMode string

type UID struct {
	Value uint32
	Set   bool
}

type GID struct {
	Value uint32
	Set   bool
}

const (
	KindAny    Kind = "any"
	KindBridge Kind = "bridge"
	KindTap    Kind = "tap"

	AdminStateUp   AdminState = "up"
	AdminStateDown AdminState = "down"

	VNetHeaderEnabled  VNetHeaderMode = "enabled"
	VNetHeaderDisabled VNetHeaderMode = "disabled"

	MaxInterfaceNameLength = 15
)

func ExplicitUID(value uint32) UID { return UID{Value: value, Set: true} }
func ExplicitGID(value uint32) GID { return GID{Value: value, Set: true} }
