#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
instance_name="govirta-acceptance"
# 始终基于主仓库根目录计算 Lima 短路径，避免 worktree 内运行时路径漂移。
# `git rev-parse --git-common-dir` 在主仓库可能返回相对 `.git`，在 worktree
# 返回主仓库的绝对 `.git` 路径；先规范化 common dir，再取其父目录。
git_common_dir=$(cd -- "$repo_root" && git rev-parse --git-common-dir)
case "$git_common_dir" in
  /*) ;;
  *)  git_common_dir=$(cd -- "$repo_root" && cd -- "$git_common_dir" && pwd) ;;
esac
main_repo_root=$(dirname -- "$git_common_dir")
repo_parent=$(dirname -- "$main_repo_root")
repo_key=$(printf '%s' "$main_repo_root" | cksum | cut -d ' ' -f 1)
lima_home="${GOVIRTA_LIMA_HOME:-$repo_parent/.l/$repo_key}"
cache_dir="$repo_root/.lima/cache"
generated_config="$cache_dir/govirta.generated.yaml"
cirros_base_url="https://download.cirros-cloud.net/0.6.2"
cirros_md5_url="$cirros_base_url/MD5SUMS"
cirros_md5_file="$cache_dir/images/cirros-0.6.2-MD5SUMS"
cirros_url="$cirros_base_url/cirros-0.6.2-aarch64-disk.img"
cirros_kernel_url="$cirros_base_url/cirros-0.6.2-aarch64-kernel"
cirros_initramfs_url="$cirros_base_url/cirros-0.6.2-aarch64-initramfs"
cirros_image="$cache_dir/images/cirros-aarch64.qcow2"
cirros_kernel="$cache_dir/images/cirros-0.6.2-aarch64-kernel"
cirros_initramfs="$cache_dir/images/cirros-0.6.2-aarch64-initramfs"
log_dir="$repo_root/test/log"

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
	require_tool awk
	if ! command -v md5sum >/dev/null 2>&1 && \
		! command -v md5 >/dev/null 2>&1 && \
		! command -v openssl >/dev/null 2>&1; then
		printf 'missing required tool: md5sum, md5, or openssl\n' >&2
		exit 1
	fi
	limactl --version
}

md5_of() {
	if command -v md5sum >/dev/null 2>&1; then
		md5sum "$1" | awk '{print $1}'
		return
	fi
	if command -v md5 >/dev/null 2>&1; then
		md5 -q "$1"
		return
	fi
	openssl dgst -md5 -r "$1" | awk '{print $1}'
}

download_file() {
	url="$1"
	target="$2"
	if [ -f "$target" ]; then
		return 0
	fi
	tmp="$target.download.$$"
	rm -f "$tmp"
	if ! curl -fsSL "$url" -o "$tmp"; then
		rm -f "$tmp"
		exit 1
	fi
	mv "$tmp" "$target"
}

ensure_md5sums() {
	download_file "$cirros_md5_url" "$cirros_md5_file"
}

verify_md5() {
	target="$1"
	upstream_name="$2"
	expected=$(awk -v name="$upstream_name" '$2 == name {print $1; found = 1} END {if (!found) exit 1}' "$cirros_md5_file") || {
		printf 'missing MD5 checksum for %s in %s\n' "$upstream_name" "$cirros_md5_file" >&2
		exit 1
	}
	actual=$(md5_of "$target")
	if [ "$actual" != "$expected" ]; then
		printf 'MD5 checksum mismatch for %s: expected %s, got %s\n' "$target" "$expected" "$actual" >&2
		rm -f "$target"
		exit 1
	fi
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

	ensure_md5sums
	download_file "$cirros_url" "$cirros_image"
	verify_md5 "$cirros_image" "cirros-0.6.2-aarch64-disk.img"
	download_file "$cirros_kernel_url" "$cirros_kernel"
	verify_md5 "$cirros_kernel" "cirros-0.6.2-aarch64-kernel"
	download_file "$cirros_initramfs_url" "$cirros_initramfs"
	verify_md5 "$cirros_initramfs" "cirros-0.6.2-aarch64-initramfs"
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
		if ! command -v ip >/dev/null 2>&1; then
			printf "missing required guest tool: ip\n" >&2
			exit 1
		fi
		if ! command -v ping >/dev/null 2>&1; then
			printf "missing required guest tool: ping\n" >&2
			exit 1
		fi
		if ! command -v nft >/dev/null 2>&1; then
			echo "nft command is required for hostnet firewall acceptance" >&2
			exit 1
		fi
		if [ ! -x "$HOME/.local/go/bin/go" ]; then
			printf "missing required guest tool: %s\n" "$HOME/.local/go/bin/go" >&2
			exit 1
		fi
		old_ip_forward=$(sysctl -n net.ipv4.ip_forward)
		printf "previous net.ipv4.ip_forward = %s\n" "$old_ip_forward"
		restore_ipv4_forwarding() {
			sudo sysctl -w "net.ipv4.ip_forward=$old_ip_forward" >/dev/null || \
				printf "failed to restore net.ipv4.ip_forward=%s\n" "$old_ip_forward" >&2
		}
		trap restore_ipv4_forwarding EXIT
		sudo sysctl -w net.ipv4.ip_forward=1
		sudo -E env \
			GOCACHE=/govirta-cache/gocache \
			GOMODCACHE=/govirta-cache/gomodcache \
			GOVIRTA_ACCEPTANCE=1 \
			GOVIRTA_ACCEPTANCE_LIMA_GUEST=1 \
			GOVIRTA_ACCEPTANCE_QEMU=/usr/bin/qemu-system-aarch64 \
			GOVIRTA_ACCEPTANCE_QEMU_IMG=/usr/bin/qemu-img \
			GOVIRTA_ACCEPTANCE_FIRMWARE=/usr/share/AAVMF/AAVMF_CODE.fd \
			GOVIRTA_ACCEPTANCE_CIRROS=/govirta-cache/images/cirros-aarch64.qcow2 \
			GOVIRTA_ACCEPTANCE_CIRROS_KERNEL=/govirta-cache/images/cirros-0.6.2-aarch64-kernel \
			GOVIRTA_ACCEPTANCE_CIRROS_INITRAMFS=/govirta-cache/images/cirros-0.6.2-aarch64-initramfs \
			"$HOME/.local/go/bin/go" test -v -tags acceptance -count=1 ./test/acceptance/...
	'
}

timestamp() {
	date '+%Y-%m-%d-%H%M%S'
}

run_acceptance_logged() {
	mkdir -p "$log_dir"
	log_file="$log_dir/$(timestamp)-acceptance-$mode.log"
	printf 'writing acceptance log to %s\n' "$log_file"
	set +e
	run_acceptance >"$log_file" 2>&1
	status=$?
	set -e
	cat "$log_file" || true
	return "$status"
}

mode="${1:-full}"

case "$mode" in
	full | linux)
		run_acceptance_logged
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
