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
package acceptance
