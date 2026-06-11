package vmm

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/firmware"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
)

// deriveBuilder 据 SpecSummary（per-VM 配置权威）+ NodeEnv（节点级运行时环境）
// 确定性构造「配置好但未 Build」的 qemu.Builder。SpecSummary 决定 VM 自身配置
// （vcpu/内存/磁盘/网卡/MAC），NodeEnv 决定本节点所有 VM 共享的环境（QEMU 二进制
// 路径、guest 固件路径）——两轴分离：节点级环境不污染 per-VM 的 SpecSummary。
// 这是 json→qemu flag 单向映射的唯一实现：相同 (SpecSummary, NodeEnv) 永远产生
// 相同 builder。设施 flag（pidfile/QMP/serial/vnc/daemonize）由 injectFacilityFlags
// 在其后注入。
//
// API 形态（已核对 pkg/virt/qemu 真实代码）：流式构造用 qemu.NewVM(arch) +
// .Name()/.Binary()/.Machine()/.CPU()/.SMP()/.Memory()；磁盘/网卡用 AddBlockdev+AddDevice /
// AddNetdev+AddDevice 成对添加；MAC 落在 device.VirtioNetPCI.Mac（netdev.Tap 无 MAC 字段）。
func deriveBuilder(spec SpecSummary, env NodeEnv) (*qemu.Builder, error) {
	arch, profile, err := mapArch(spec.Arch)
	if err != nil {
		return nil, err
	}

	b := qemu.NewVM(arch).
		Name(spec.Name).
		Binary(env.QEMUBinary).
		Machine(profile).
		CPU(cpu.Model(spec.CPUModel)).
		SMP(qemu.SMP{CPUs: spec.VCPUs, Cores: spec.VCPUs, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(spec.MemoryMiB))

	// 节点级固件：aarch64 virt 磁盘引导需 edk2 固件（memory 868）；x86_64 q35
	// 自带 SeaBIOS，Firmware 空字符串 = 不渲染 -bios（对应「该 arch 无需显式固件」
	// 的真实语义，非行为推断）。
	if env.Firmware != "" {
		b = b.BIOS(firmware.BIOS{Path: env.Firmware})
	}

	for i, disk := range spec.Disks {
		node := fmt.Sprintf("disk%d", i)
		b = b.AddBlockdev(blockdev.Qcow2{
			NodeName: node,
			File:     blockdev.FileProtocol{Filename: disk.Path},
			Cache:    blockdev.Cache{Direct: qemu.Off},
			AIO:      blockdev.AIOThreads,
		}).AddDevice(device.VirtioBlkPCI{
			ID:    fmt.Sprintf("blk%d", i),
			Drive: blockdev.Ref(node),
		})
	}

	for i, nic := range spec.NICs {
		netID := fmt.Sprintf("net%d", i)
		b = b.AddNetdev(netdev.Tap{
			ID:         netID,
			IfName:     nic.TapName,
			Script:     netdev.ScriptNo,
			DownScript: netdev.ScriptNo,
			Vhost:      qemu.On,
		}).AddDevice(device.VirtioNetPCI{
			ID:      fmt.Sprintf("nic%d", i),
			Netdev:  netdev.Ref(netID),
			Mac:     device.MAC(nic.MAC), // 控制面分配的 MAC 原样贯穿（memory 698）
			RomFile: qflag.String(""),    // 显式禁用 PXE option ROM（本项目不支持 PXE 引导）
		})
	}

	return b, nil
}

// mapArch maps an arch string to typed qemu.Arch + KVM machine profile.
// 未知 arch 是永久配置错误（项目铁律：禁止裸 string 推断）。从控制器下沉而来。
func mapArch(arch string) (qemu.Arch, machine.Profile, error) {
	switch arch {
	case "x86_64":
		return qemu.ArchX86_64, machine.ProfileX86_64Q35KVM, nil
	case "aarch64":
		return qemu.ArchAArch64, machine.ProfileAArch64VirtKVM, nil
	default:
		return "", "", fmt.Errorf("%w: unsupported arch %q", ErrInvalidRequest, arch)
	}
}
