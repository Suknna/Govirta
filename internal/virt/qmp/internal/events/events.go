package events

import (
	"context"
	"time"

	"github.com/suknna/govirta/internal/virt/qmp/internal/monitor"
)

// Event is a project-owned QMP event after filtering and timestamp conversion.
type Event struct {
	Name      string
	Data      map[string]any
	Timestamp time.Time
}

// Stream converts and filters internal monitor events.
func Stream(ctx context.Context, source <-chan monitor.Event, names ...string) <-chan Event {
	filters := filterSet(names)
	out := make(chan Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-source:
				if !ok {
					return
				}
				converted := Convert(event)
				if len(filters) > 0 && !filters[converted.Name] {
					continue
				}
				select {
				case out <- converted:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// Convert maps a monitor event to the event representation used outside transport code.
func Convert(event monitor.Event) Event {
	return Event{
		Name:      event.Name,
		Data:      event.Data,
		Timestamp: time.Unix(event.Seconds, event.Microseconds*1000),
	}
}

func filterSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	filters := make(map[string]bool, len(names))
	for _, name := range names {
		filters[name] = true
	}
	return filters
}
