//go:build e2e

// Package e2e holds Govirta's end-to-end distributed-spine acceptance test. It
// is host-driven: scripts/e2e.sh starts etcd (container), govirtad (host), and
// govirtlet (Lima guest dialing back to the host), then runs this test, which
// drives the govirtctl binary to apply the six first-class resources and waits
// for the VM to reach Running. It exercises the real Linux execution plane
// (netlink/nftables/CoreDHCP/QEMU) reconciled from a real master over watch —
// the proof the unit and per-package acceptance tests cannot give.
package e2e
