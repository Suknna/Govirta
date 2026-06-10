package vmm

import (
	"fmt"

	"github.com/suknna/govirta/pkg/virt/qemu"
	"github.com/suknna/govirta/pkg/virt/qemu/blockdev"
	"github.com/suknna/govirta/pkg/virt/qemu/cpu"
	"github.com/suknna/govirta/pkg/virt/qemu/device"
	"github.com/suknna/govirta/pkg/virt/qemu/machine"
	"github.com/suknna/govirta/pkg/virt/qemu/netdev"
	"github.com/suknna/govirta/pkg/virt/qemu/qflag"
)

// deriveBuilder 据 SpecSummary 确定性构造「配置好但未 Build」的 qemu.Builder。
// 这是 json→qemu flag 单向映射的唯一实现：相同 SpecSummary 永远产生相同 builder。
// 设施 flag（pidfile/QMP/serial/vnc/daemonize）由 injectFacilityFlags 在其后注入。
//
// API 形态（已核对 pkg/virt/qemu 真实代码）：流式构造用 qemu.NewVM(arch) +
// .Name()/.Machine()/.CPU()/.SMP()/.Memory()；磁盘/网卡用 AddBlockdev+AddDevice /
// AddNetdev+AddDevice 成对添加；MAC 落在 device.VirtioNetPCI.Mac（netdev.Tap 无 MAC 字段）。
func deriveBuilder(spec SpecSummary) (*qemu.Builder, error) {
	arch, profile, err := mapArch(spec.Arch)
	if err != nil {
		return nil, err
	}

	b := qemu.NewVM(arch).
		Name(spec.Name).
		Machine(profile).
		CPU(cpu.Model(spec.CPUModel)).
		SMP(qemu.SMP{CPUs: spec.VCPUs, Cores: spec.VCPUs, Threads: 1, Sockets: 1}).
		Memory(qemu.MiB(spec.MemoryMiB))

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
