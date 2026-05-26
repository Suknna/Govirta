package status

import (
	"context"
	"encoding/json"

	"github.com/suknna/govirta/internal/virt/qmp/internal/monitor"
	"github.com/suknna/govirta/internal/virt/qmp/internal/protocol"
)

type commandName string

const commandQueryStatus commandName = "query-status"

// Result is the typed query-status response used by the root QMP package.
type Result struct {
	Running    bool
	Singlestep bool
	State      string
}

// Query executes QMP query-status and parses the response.
func Query(ctx context.Context, mon monitor.Monitor) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	command, err := json.Marshal(struct {
		Execute commandName `json:"execute"`
	}{Execute: commandQueryStatus})
	if err != nil {
		return Result{}, err
	}

	raw, err := mon.Run(ctx, command)
	if err != nil {
		return Result{}, err
	}
	return Parse(raw)
}

// Parse decodes a QMP query-status response.
func Parse(raw []byte) (Result, error) {
	var response struct {
		Return struct {
			Running    bool   `json:"running"`
			Singlestep bool   `json:"singlestep"`
			Status     string `json:"status"`
		} `json:"return"`
		Error *struct {
			Class       string `json:"class"`
			Description string `json:"desc"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return Result{}, err
	}
	if response.Error != nil && response.Error.Description != "" {
		return Result{}, &protocol.ResponseError{Class: response.Error.Class, Description: response.Error.Description}
	}
	return Result{Running: response.Return.Running, Singlestep: response.Return.Singlestep, State: response.Return.Status}, nil
}
