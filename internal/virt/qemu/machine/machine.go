package machine

// Profile identifies a Govirta-supported QEMU machine profile.
type Profile string

const (
	// ProfileX86_64Q35KVM is the supported x86_64 KVM profile based on q35.
	ProfileX86_64Q35KVM Profile = "x86_64-q35-kvm"
	// ProfileAArch64VirtKVM is the supported aarch64 KVM profile based on virt.
	ProfileAArch64VirtKVM Profile = "aarch64-virt-kvm"
)

// Config renders a supported QEMU -machine profile.
type Config struct{ Profile Profile }

// New returns a machine config for a predefined Govirta profile.
func New(profile Profile) Config { return Config{Profile: profile} }

// IsSupported reports whether the profile is predefined by Govirta.
func (p Profile) IsSupported() bool {
	switch p {
	case ProfileX86_64Q35KVM, ProfileAArch64VirtKVM:
		return true
	default:
		return false
	}
}

// Arg renders the QEMU -machine value for the profile.
func (c Config) Arg() string {
	switch c.Profile {
	case ProfileX86_64Q35KVM:
		return "type=q35,accel=kvm,kernel-irqchip=split"
	case ProfileAArch64VirtKVM:
		return "type=virt,accel=kvm"
	default:
		return ""
	}
}
