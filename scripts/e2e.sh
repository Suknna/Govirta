#!/bin/sh
set -eu

# scripts/e2e.sh — Govirta 分布式脊柱端到端验收编排（三节点真实拓扑）。
#
# 拓扑（spec §7，node-initiated 拨号）：
#   - etcd     → OrbStack Docker 容器，host 发布到 127.0.0.1:<etcd_port>
#   - govirtad → macOS host 直接运行，连本机 etcd，监听 0.0.0.0:<api_port>
#   - govirtlet→ Lima KVM guest 内运行，拨回 host.lima.internal:<api_port>
#   - e2e test → host 上运行，驱动 govirtctl，断言 VM 到达 Running
#
# 只需 Lima guest → host 单向可达（host.lima.internal），无需 host 反向入站
# guest，也无需 guest 直连 etcd（仅 master 碰 etcd）。
#
# 所有中间产物写入项目 .tmp/e2e/（pidfile/日志/socket），绝不用全局 /tmp。

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
instance_name="govirta-e2e"

# 始终基于主仓库根目录计算 Lima 短路径，避免 worktree 内运行时路径漂移
# （与 acceptance.sh 同款处理：git common dir 规范化后取父目录）。
git_common_dir=$(cd -- "$repo_root" && git rev-parse --git-common-dir)
case "$git_common_dir" in
  /*) ;;
  *)  git_common_dir=$(cd -- "$repo_root" && cd -- "$git_common_dir" && pwd) ;;
esac
main_repo_root=$(dirname -- "$git_common_dir")
repo_parent=$(dirname -- "$main_repo_root")
repo_key=$(printf '%s' "$main_repo_root" | cksum | cut -d ' ' -f 1)
lima_home="${GOVIRTA_LIMA_HOME:-$repo_parent/.l/${repo_key}e}"

cache_dir="$repo_root/.lima/cache"
generated_config="$cache_dir/govirta.e2e.generated.yaml"
tmp_dir="$repo_root/.tmp/e2e"
log_dir="$repo_root/test/log"
image_store_root="$tmp_dir/image-store"
host_image_cache_root="$tmp_dir/image-cache"

cirros_base_url="https://download.cirros-cloud.net/0.6.2"
cirros_md5_url="$cirros_base_url/MD5SUMS"
cirros_md5_file="$cache_dir/images/cirros-0.6.2-MD5SUMS"
cirros_url="$cirros_base_url/cirros-0.6.2-aarch64-disk.img"
cirros_image="$cache_dir/images/cirros-aarch64.qcow2"

# 拓扑端口与身份（显式，无隐藏默认）。
etcd_port="${GOVIRTA_E2E_ETCD_PORT:-23790}"
api_port="${GOVIRTA_E2E_API_PORT:-18080}"
node_name="${GOVIRTA_E2E_NODE:-node0}"
etcd_image="quay.io/coreos/etcd:v3.6.12"
etcd_container="govirta-e2e-etcd"
mac_prefix="02:00:00"
mac_suffix_start="1"
mac_suffix_end="65535"

# guest 内的固定路径（manifest 里的 storageRoot / image source 必须与此一致）。
# guest_state_root 必须等于 Go 常量 guestStateRoot（test/e2e/guest_paths.go），
# 见 spec 组件 5 跨语言路径契约。
guest_state_root="/var/lib/govirta"
guest_image_root="$guest_state_root/images"
guest_runtime_root="$guest_state_root/runtime"
guest_image_cache_root="$guest_state_root/image-cache"
guest_cirros="$guest_image_root/cirros-aarch64.qcow2"
govirtad_pidfile="$tmp_dir/govirtad.pid"
govirtad_log="$tmp_dir/govirtad.log"
govirtctl_bin="$tmp_dir/govirtctl"
govirtad_bin="$tmp_dir/govirtad"

usage() {
	cat <<EOF
Usage: scripts/e2e.sh [mode]

Modes:
  full         Stand up etcd + host govirtad + Lima govirtlet, run the closure test, tear down.
  check-tools  Verify local tools (docker, limactl, go) without starting anything.
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
	require_tool docker
	require_tool limactl
	require_tool go
	require_tool curl
	require_tool awk
	if ! command -v md5sum >/dev/null 2>&1 && \
		! command -v md5 >/dev/null 2>&1 && \
		! command -v openssl >/dev/null 2>&1; then
		printf 'missing required tool: md5sum, md5, or openssl\n' >&2
		exit 1
	fi
	docker version --format '{{.Server.Version}}' >/dev/null 2>&1 || {
		printf 'docker engine not reachable\n' >&2
		exit 1
	}
	limactl --version
	go version
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

# ensure_md5sums downloads the upstream checksum manifest. It is a separate step
# (not folded into verify_md5) because download_file assigns the global `target`
# variable in POSIX sh; calling it from inside verify_md5 would clobber that
# function's own `target`, making md5_of check the wrong file. Keeping the
# download out of verify_md5 keeps each function's `target` intact.
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
		"$cache_dir/gomodcache" \
		"$image_store_root" \
		"$host_image_cache_root" \
		"$tmp_dir"

	sed \
		-e "s#{{.Dir}}/\.\.#$repo_root#g" \
		-e "s#{{.Dir}}/\.\./\.lima/cache#$cache_dir#g" \
		"$repo_root/lima/govirta.yaml" >"$generated_config"

	ensure_md5sums
	download_file "$cirros_url" "$cirros_image"
	verify_md5 "$cirros_image" "cirros-0.6.2-aarch64-disk.img"
}

cleanup() {
	# guest govirtlet 由 guest 内的 trap 负责；这里收 host 侧资源。
	if [ -f "$govirtad_pidfile" ]; then
		pid=$(cat "$govirtad_pidfile" 2>/dev/null || true)
		if [ -n "${pid:-}" ]; then
			kill "$pid" >/dev/null 2>&1 || true
		fi
		rm -f "$govirtad_pidfile"
	fi
	docker rm -f "$etcd_container" >/dev/null 2>&1 || true
	LIMA_HOME="$lima_home" limactl delete --force "$instance_name" >/dev/null 2>&1 || true
	# `limactl delete` can leave a partially-created instance directory after a
	# failed start. Remove this fixed test instance path so the next run starts
	# from a coherent Lima state instead of reusing corrupt metadata.
	rm -rf "$lima_home/$instance_name"
}

start_etcd() {
	docker rm -f "$etcd_container" >/dev/null 2>&1 || true
	docker run -d --name "$etcd_container" \
		-p "127.0.0.1:$etcd_port:2379" \
		"$etcd_image" \
		/usr/local/bin/etcd \
		--name e2e \
		--data-dir /etcd-data \
		--listen-client-urls http://0.0.0.0:2379 \
		--advertise-client-urls "http://127.0.0.1:$etcd_port" >/dev/null

	# 等 etcd 接受客户端连接。
	i=0
	while [ "$i" -lt 30 ]; do
		if docker exec "$etcd_container" etcdctl --endpoints=http://127.0.0.1:2379 endpoint health >/dev/null 2>&1; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	printf 'etcd did not become healthy\n' >&2
	exit 1
}

start_govirtad() {
	( cd "$repo_root" && go build -o "$govirtad_bin" ./cmd/govirtad )
	( cd "$repo_root" && go build -o "$govirtctl_bin" ./cmd/govirtctl )

	"$govirtad_bin" \
		--etcd-endpoint "http://127.0.0.1:$etcd_port" \
		--node-name "$node_name" \
		--listen-addr "0.0.0.0:$api_port" \
		--mac-prefix "$mac_prefix" \
		--mac-suffix-start "$mac_suffix_start" \
		--mac-suffix-end "$mac_suffix_end" \
		--image-store-root "$image_store_root" \
		--image-store-public-url "http://host.lima.internal:$api_port" \
		--image-cache-root "$guest_image_cache_root" \
		--image-controller-sync-period "1s" \
		--phase-one-node-task-name "phase-one-node-task-$node_name" \
		--phase-one-node-task-node "$node_name" \
		--phase-one-cluster-task-name "phase-one-cluster-task" \
		--phase-one-task-owner-name "phase-one-owner" \
		--phase-one-task-owner-uid "phase-one-owner-uid" \
		--phase-one-task-executor-id "govirtad" \
		--phase-one-task-noop-marker "phase-one" \
		>"$govirtad_log" 2>&1 &
	echo $! >"$govirtad_pidfile"

	# 等 apiserver 监听。
	i=0
	while [ "$i" -lt 30 ]; do
		if curl -fsS "http://127.0.0.1:$api_port/apis/VM" >/dev/null 2>&1; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	printf 'govirtad apiserver did not start listening; log:\n' >&2
	cat "$govirtad_log" >&2 || true
	exit 1
}

start_lima_govirtlet() {
	# The distributed-spine test needs mounts, provisioning, guest agent, and
	# nested KVM, but not Lima's built-in containerd integration. Disable it so
	# instance readiness is not gated by an unrelated optional runtime.
	LIMA_HOME="$lima_home" limactl start --containerd=none --timeout=20m --name="$instance_name" --yes "$generated_config"

	# guest 内：装备状态目录、放镜像、开转发、构建并后台启动 govirtlet 拨回 host。
	# host.lima.internal 是 Lima guest 访问 host 的标准地址。
	LIMA_HOME="$lima_home" limactl shell --workdir /govirta-src "$instance_name" -- sh -eu -c '
		api_port="'"$api_port"'"
		node_name="'"$node_name"'"
		state_root="'"$guest_state_root"'"
		image_root="'"$guest_image_root"'"
		runtime_root="'"$guest_runtime_root"'"
		image_cache_root="'"$guest_image_cache_root"'"
		guest_cirros="'"$guest_cirros"'"

		i=0
		while [ "$i" -lt 180 ] && [ ! -x "$HOME/.local/go/bin/go" ]; do
			i=$((i + 1))
			sleep 1
		done
		if [ ! -x "$HOME/.local/go/bin/go" ]; then
			printf "missing guest go toolchain after wait\n" >&2
			exit 1
		fi
		for tool in ip nft; do
			command -v "$tool" >/dev/null 2>&1 || { printf "missing guest tool: %s\n" "$tool" >&2; exit 1; }
		done

		sudo mkdir -p "$state_root/block" "$state_root/file" "$image_root" "$runtime_root" "$image_cache_root"
		sudo cp /govirta-cache/images/cirros-aarch64.qcow2 "$guest_cirros"

		# route 原语要求 ip_forward；node-prep 责任（route 包只读不改）。
		sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null

		# guest 内构建 govirtlet（/govirta-src 只读，输出到可写 cache）。
		export GOCACHE=/govirta-cache/gocache
		export GOMODCACHE=/govirta-cache/gomodcache
		govirtlet_bin=/govirta-cache/govirtlet
		"$HOME/.local/go/bin/go" build -o "$govirtlet_bin" ./cmd/govirtlet

		owner_uid=$(id -u)
		owner_gid=$(id -g)

		# 后台启动 govirtlet（root：netlink/nftables/TAP）。pidfile + 日志写 cache。
		sudo -b sh -c "
			$govirtlet_bin \
				--master-url http://host.lima.internal:$api_port \
				--node-name $node_name \
				--runtime-root $runtime_root \
				--image-cache-root $image_cache_root \
				--owner-uid $owner_uid \
				--owner-gid $owner_gid \
				--guest-cpu host \
				--qemu-binary /usr/bin/qemu-system-aarch64 \
				--firmware /usr/share/AAVMF/AAVMF_CODE.fd \
				>/govirta-cache/govirtlet.log 2>&1 &
			echo \$! >/govirta-cache/govirtlet.pid
		"
		sleep 2
		printf "govirtlet started in guest (pid %s)\n" "$(cat /govirta-cache/govirtlet.pid 2>/dev/null || echo unknown)"
	'
}

run_closure() {
	GOVIRTA_E2E=1 \
	GOVIRTA_E2E_SERVER="http://127.0.0.1:$api_port" \
	GOVIRTA_E2E_GOVIRTCTL="$govirtctl_bin" \
	GOVIRTA_E2E_MANIFESTS="$repo_root/test/e2e/manifests" \
	GOVIRTA_E2E_NODE="$node_name" \
	GOVIRTA_E2E_LIMA_INSTANCE="$instance_name" \
	GOVIRTA_E2E_LIMA_HOME="$lima_home" \
		go test -v -tags e2e -count=1 "$repo_root/test/e2e/..."
}

run_full() {
	check_tools
	prepare_cache

	trap cleanup EXIT INT TERM
	cleanup

	start_etcd
	start_govirtad
	start_lima_govirtlet
	run_closure
}

run_full_logged() {
	mkdir -p "$log_dir" "$tmp_dir"
	log_file="$log_dir/$(date '+%Y-%m-%d-%H%M%S')-e2e-full.log"
	printf 'writing e2e log to %s\n' "$log_file"
	set +e
	(set -e; run_full) >"$log_file" 2>&1
	status=$?
	set -e
	cat "$log_file" || true
	return "$status"
}

mode="${1:-full}"

case "$mode" in
	full)
		run_full_logged
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
