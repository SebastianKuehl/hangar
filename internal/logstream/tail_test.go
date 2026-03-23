package logstream

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTailFileLoadsBacklogFollowsAppendsAndHandlesTruncation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	tailer := NewTailer(20 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan LogEvent, 16)
	go func() {
		_ = tailer.TailFile(ctx, logPath, true, 2, events)
	}()

	first := waitForEvent(t, events)
	if !first.Reset {
		t.Fatalf("expected first event to be a reset, got %#v", first)
	}
	if !reflect.DeepEqual(first.Lines, []string{"two", "three"}) {
		t.Fatalf("first backlog = %#v, want %#v", first.Lines, []string{"two", "three"})
	}

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile returned error: %v", err)
	}
	if _, err := file.WriteString("four\n"); err != nil {
		t.Fatalf("WriteString returned error: %v", err)
	}
	_ = file.Close()

	second := waitForEvent(t, events)
	if second.Reset {
		t.Fatalf("expected append event, got %#v", second)
	}
	if !reflect.DeepEqual(second.Lines, []string{"four"}) {
		t.Fatalf("appended lines = %#v, want %#v", second.Lines, []string{"four"})
	}

	if err := os.WriteFile(logPath, []byte("fresh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile truncate returned error: %v", err)
	}

	third := waitForEvent(t, events)
	if !third.Reset {
		t.Fatalf("expected reset event after truncation, got %#v", third)
	}
	if !reflect.DeepEqual(third.Lines, []string{"fresh"}) {
		t.Fatalf("reset lines = %#v, want %#v", third.Lines, []string{"fresh"})
	}
}

func waitForEvent(t *testing.T, events <-chan LogEvent) LogEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.Err != nil {
			t.Fatalf("unexpected tail error: %v", event.Err)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log event")
		return LogEvent{}
	}
}
