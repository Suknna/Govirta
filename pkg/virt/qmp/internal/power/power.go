package power

import (
	"context"
	"encoding/json"

	"github.com/suknna/govirta/pkg/virt/qmp/internal/monitor"
)

type commandName string

const (
	commandSystemPowerdown commandName = "system_powerdown"
	commandQuit            commandName = "quit"
)

// SystemPowerdown asks QEMU to initiate guest shutdown through QMP.
func SystemPowerdown(ctx context.Context, mon monitor.Monitor) error {
	return run(ctx, mon, commandSystemPowerdown)
}

// Quit asks QEMU to terminate the VM process through QMP.
func Quit(ctx context.Context, mon monitor.Monitor) error {
	return run(ctx, mon, commandQuit)
}

func run(ctx context.Context, mon monitor.Monitor, command commandName) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Execute commandName `json:"execute"`
	}{Execute: command})
	if err != nil {
		return err
	}
	_, err = mon.Run(ctx, payload)
	return err
}
