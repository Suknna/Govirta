package version

const (
	// Name is the project name.
	Name = "govirta"

	// Version is the development version for the initial skeleton.
	Version = "0.1.0-dev"
)

// String returns the human-readable project version.
func String() string {
	return Name + " " + Version
}
