package gateway

import "testing"

func TestLiveTrackerMarksStreaming(t *testing.T) {
	tracker := newLiveTracker()
	tracker.start("req1", "smart", "gpt-4o", "mr-sk", "127.0.0.1", "p1", "OpenAI")
	tracker.setStatus("req1", "streaming")

	snap := tracker.snapshot()
	if len(snap.Active) != 1 {
		t.Fatalf("expected one active request, got %d", len(snap.Active))
	}
	if snap.Active[0].Status != "streaming" {
		t.Fatalf("expected streaming status, got %q", snap.Active[0].Status)
	}

	tracker.finish("req1", "ok")
	snap = tracker.snapshot()
	if len(snap.Active) != 0 {
		t.Fatalf("expected no active requests after finish, got %d", len(snap.Active))
	}
	if len(snap.Recent) != 1 || snap.Recent[0].Status != "ok" {
		t.Fatalf("expected a finished ok request, got %+v", snap.Recent)
	}
}
