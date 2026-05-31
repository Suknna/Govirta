#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
instance_name="govirta-acceptance"
repo_parent=$(dirname -- "$repo_root")
repo_key=$(printf '%s' "$repo_root" | cksum | cut -d ' ' -f 1)
lima_home="${GOVIRTA_LIMA_HOME:-$repo_parent/.l/$repo_key}"
cache_dir="$repo_root/.lima/cache"
generated_config="$cache_dir/govirta.generated.yaml"
cirros_url="https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-aarch64-disk.img"
cirros_image="$cache_dir/images/cirros-aarch64.qcow2"

usage() {
	cat <<EOF
Usage: scripts/acceptance.sh [mode]

Modes:
  full         Prepare cache, start a fresh Lima VM, run acceptance tests, then delete it.
  linux        Same as full.
  check-tools  Verify local tools required by this script without starting a VM.
  help         Show this help.

Default mode: full
EOF
}

require_tool() {
	if ! command -v "$1" >/dev/null 2>&1; then
		printf 'missing required tool: %s\n' "$1" >&2
		exit 1
	fi
}

check_tools() {
	require_tool limactl
	require_tool curl
	limactl --version
}

prepare_cache() {
	mkdir -p \
		"$lima_home" \
		"$cache_dir/images" \
		"$cache_dir/toolchain" \
		"$cache_dir/gocache" \
		"$cache_dir/gomodcache"

	sed \
		-e "s#{{.Dir}}/\.\.#$repo_root#g" \
		-e "s#{{.Dir}}/\.\./\.lima/cache#$cache_dir#g" \
		"$repo_root/lima/govirta.yaml" >"$generated_config"

	if [ ! -f "$cirros_image" ]; then
		cirros_tmp="$cirros_image.download.$$"
		rm -f "$cirros_tmp"
		if ! curl -fsSL "$cirros_url" -o "$cirros_tmp"; then
			rm -f "$cirros_tmp"
			exit 1
		fi
		mv "$cirros_tmp" "$cirros_image"
	fi
}

delete_instance() {
	LIMA_HOME="$lima_home" limactl delete --force "$instance_name" >/dev/null 2>&1 || true
}

run_acceptance() {
	check_tools
	prepare_cache

	trap delete_instance EXIT INT TERM
	delete_instance

	LIMA_HOME="$lima_home" limactl start --name="$instance_name" --yes "$generated_config"

	LIMA_HOME="$lima_home" limactl shell --workdir /govirta-src "$instance_name" -- sh -eu -c '
		sudo -E env \
			PATH="$HOME/.local/go/bin:$PATH" \
			GOCACHE=/govirta-cache/gocache \
			GOMODCACHE=/govirta-cache/gomodcache \
			GOVIRTA_ACCEPTANCE=1 \
			GOVIRTA_ACCEPTANCE_QEMU=/usr/bin/qemu-system-aarch64 \
			GOVIRTA_ACCEPTANCE_QEMU_IMG=/usr/bin/qemu-img \
			GOVIRTA_ACCEPTANCE_FIRMWARE=/usr/share/AAVMF/AAVMF_CODE.fd \
			GOVIRTA_ACCEPTANCE_CIRROS=/govirta-cache/images/cirros-aarch64.qcow2 \
			go test -v -tags acceptance -count=1 ./test/acceptance/...
	'
}

mode="${1:-full}"

case "$mode" in
	full | linux)
		run_acceptance
		;;
	check-tools)
		check_tools
		;;
	help | -h | --help)
		usage
		;;
	*)
		printf 'unknown mode: %s\n\n' "$mode" >&2
		usage >&2
		exit 2
		;;
esac
