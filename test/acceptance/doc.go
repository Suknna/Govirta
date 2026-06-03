//go:build acceptance

// Package acceptance contains gated end-to-end checks for Govirta against a
// real QEMU environment.
//
// Acceptance tests are skipped unless GOVIRTA_ACCEPTANCE=1 is set. When enabled,
// the following environment variables are required and must point to existing
// non-directory paths:
//
//   - GOVIRTA_ACCEPTANCE_QEMU: qemu-system binary to execute.
//   - GOVIRTA_ACCEPTANCE_QEMU_IMG: qemu-img binary to execute.
//   - GOVIRTA_ACCEPTANCE_FIRMWARE: firmware image passed to QEMU.
//   - GOVIRTA_ACCEPTANCE_CIRROS: Cirros guest image used as test input.
//   - GOVIRTA_ACCEPTANCE_CIRROS_KERNEL: Cirros kernel used for direct boot.
//   - GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS: Cirros initramfs used for direct boot.
//
// scripts/acceptance.sh runs the suite inside a Lima guest, explicitly enables
// IPv4 forwarding with sysctl before go test, and additionally sets
// GOVIRTA_ACCEPTANCE_LIMA_GUEST=1. Host networking acceptance covers real Linux
// bridge/TAP, route, firewall, and DHCP static-binding primitive lifecycle
// behavior. It does not yet verify full VM internet egress because guest
// default route, DNS, NAT, and orchestration-level connectivity are validated
// in a later end-to-end flow.
// Host-side acceptance logs are archived under test/log/*.log.
package acceptance
