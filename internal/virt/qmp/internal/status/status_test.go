package status

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/suknna/govirta/internal/virt/qmp/internal/monitor"
	"github.com/suknna/govirta/internal/virt/qmp/internal/protocol"
)

func TestQuerySendsQueryStatusCommand(t *testing.T) {
	mon := &fakeMonitor{response: []byte(`{"return":{"running":true,"singlestep":false,"status":"running"}}`)}

	_, err := Query(context.Background(), mon)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	want := []byte(`{"execute":"query-status"}`)
	if !reflect.DeepEqual(mon.command, want) {
		t.Fatalf("command = %s, want %s", mon.command, want)
	}
}

func TestParseStatusResponse(t *testing.T) {
	got, err := Parse([]byte(`{"return":{"running":true,"singlestep":true,"status":"paused"}}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := Result{Running: true, Singlestep: true, State: "paused"}
	if got != want {
		t.Fatalf("Parse() = %+v, want %+v", got, want)
	}
}

func TestParsePreservesUnknownStatus(t *testing.T) {
	got, err := Parse([]byte(`{"return":{"status":"future-state"}}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.State != "future-state" {
		t.Fatalf("State = %q, want future-state", got.State)
	}
}

func TestParseMalformedJSON(t *testing.T) {
	_, err := Parse([]byte(`{"return":`))
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
}

func TestParseQMPError(t *testing.T) {
	_, err := Parse([]byte(`{"error":{"class":"GenericError","desc":"bad command"}}`))
	var responseErr *protocol.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("Parse() error = %v, want *ResponseError", err)
	}
	if responseErr.Class != "GenericError" || responseErr.Description != "bad command" {
		t.Fatalf("ResponseError = %+v, want class and description", responseErr)
	}
}

func TestQueryCanceledContextDoesNotRunCommand(t *testing.T) {
	mon := &fakeMonitor{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Query(ctx, mon)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Query() error = %v, want %v", err, context.Canceled)
	}
	if mon.called {
		t.Fatalf("monitor Run() called for canceled context")
	}
}

type fakeMonitor struct {
	called   bool
	command  []byte
	response []byte
	err      error
}

func (m *fakeMonitor) Connect(ctx context.Context) error                        { return nil }
func (m *fakeMonitor) Disconnect() error                                        { return nil }
func (m *fakeMonitor) Events(ctx context.Context) (<-chan monitor.Event, error) { return nil, nil }
func (m *fakeMonitor) Run(ctx context.Context, command []byte) ([]byte, error) {
	m.called = true
	m.command = append([]byte(nil), command...)
	return m.response, m.err
}
