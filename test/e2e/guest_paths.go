//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"

	"github.com/suknna/govirta/internal/node/identity"
)

const (
	// guestStateRoot 是 e2e 约定的 guest 状态根。e2e.sh 的 guest_state_root 必须
	// 等于此值（跨语言契约，见 spec 组件 5）。
	guestStateRoot   = "/var/lib/govirta"
	guestRuntimeRoot = guestStateRoot + "/runtime"
	// guestBlockStorageRoot 是 block StoragePool 的 storageRoot，必须等于
	// 01-storagepool-block.json 的 spec.storageRoot。local.Driver 在其下拼
	// pool/<poolName>/...，所以它是 guestQcowPath 的权威输入。
	guestBlockStorageRoot = guestStateRoot + "/block"
)

// guestQcowPath 对齐 local.Driver 的 qcow2 布局：
//
//	<storageRoot>/pool/<poolName>/<vmUID>/<vmName>-disk-<diskIndex>.qcow2
//
// 这与 local.Driver.pathForCreate 的布局一致（该函数 unexported，无法直接复用，
// 故在测试侧重建；storageRoot 以 StoragePool manifest 的 spec.storageRoot 为权威输入）。
func guestQcowPath(storageRoot, poolName, vmUID, vmName string, diskIndex int) string {
	return filepath.Join(
		storageRoot, "pool", poolName, vmUID,
		fmt.Sprintf("%s-disk-%d.qcow2", vmName, diskIndex),
	)
}

// guestRuntimeDir 是 vmm 以 VM uid 为名的 runtime 目录。
func guestRuntimeDir(vmUID string) string {
	return filepath.Join(guestRuntimeRoot, vmUID)
}

// guestTAPName 复用控制器的权威派生，绝不重抄 sha256 逻辑。
func guestTAPName(vmUID string, nicIndex int) string {
	return string(identity.DeriveNICIdentity(vmUID, nicIndex).TapName)
}

// guestAntiSpoofChain 复用控制器派生的 NIC 反欺骗链名（gv-as-<tap>）。
func guestAntiSpoofChain(vmUID string, nicIndex int) string {
	return string(identity.DeriveNICIdentity(vmUID, nicIndex).AntiSpoofChain)
}

// guestMasqueradeChain 复用控制器派生的网络 masquerade 链名（gv-masq-<network>）。
func guestMasqueradeChain(network string) string {
	return string(identity.DeriveNetworkIdentity(network).MasqueradeChain)
}

// guestForwardChain 复用控制器派生的网络 forward-accept 链名（gv-fwd-<network>）。
func guestForwardChain(network string) string {
	return string(identity.DeriveNetworkIdentity(network).ForwardChain)
}
