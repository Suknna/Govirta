package types

// Node represents a compute host managed by Govirta.
type Node struct {
	Name string
}

// VirtualMachine represents a virtual machine managed by Govirta.
type VirtualMachine struct {
	Name string
}

// ResourceList describes coarse compute capacity or demand.
type ResourceList struct {
	CPU    int
	Memory int64
}
