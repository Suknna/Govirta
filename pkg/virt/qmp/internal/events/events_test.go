package events

import (
	"context"
	"testing"
	"time"

	"github.com/suknna/govirta/pkg/virt/qmp/internal/monitor"
)

func TestConvertPreservesEventNameDataAndTimestamp(t *testing.T) {
	event := Convert(monitor.Event{
		Name:         "SHUTDOWN",
		Data:         map[string]any{"guest": "cirros"},
		Seconds:      10,
		Microseconds: 123456,
	})

	if event.Name != "SHUTDOWN" {
		t.Fatalf("Name = %q, want SHUTDOWN", event.Name)
	}
	if event.Data["guest"] != "cirros" {
		t.Fatalf("Data = %v, want guest=cirros", event.Data)
	}
	want := time.Unix(10, 123456000)
	if !event.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %v, want %v", event.Timestamp, want)
	}
}

func TestStreamFiltersSelectedEvents(t *testing.T) {
	source := make(chan monitor.Event, 3)
	source <- monitor.Event{Name: "RESET"}
	source <- monitor.Event{Name: "SHUTDOWN"}
	close(source)

	stream := Stream(context.Background(), source, "SHUTDOWN")
	event, ok := <-stream
	if !ok {
		t.Fatalf("stream closed before matching event")
	}
	if event.Name != "SHUTDOWN" {
		t.Fatalf("event name = %q, want SHUTDOWN", event.Name)
	}
	if _, ok := <-stream; ok {
		t.Fatalf("stream should be closed after source closes")
	}
}

func TestStreamWithoutFilterForwardsAllEvents(t *testing.T) {
	source := make(chan monitor.Event, 2)
	source <- monitor.Event{Name: "RESET"}
	source <- monitor.Event{Name: "STOP"}
	close(source)

	stream := Stream(context.Background(), source)
	var names []string
	for event := range stream {
		names = append(names, event.Name)
	}
	if len(names) != 2 || names[0] != "RESET" || names[1] != "STOP" {
		t.Fatalf("names = %v, want [RESET STOP]", names)
	}
}

func TestStreamStopsWhenContextIsCanceled(t *testing.T) {
	source := make(chan monitor.Event)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream := Stream(ctx, source)
	if _, ok := <-stream; ok {
		t.Fatalf("stream should close after context cancellation")
	}
}
