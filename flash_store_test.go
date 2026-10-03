package inertia

import (
	"context"
	"sync"
	"testing"
)

func withSession(id string) context.Context {
	return context.WithValue(context.Background(), "sessionID", id)
}

// An anonymous visitor has no session, which means they have no flash data.
// That is a fact, not a failure. GetFlash was the only one of the six
// FlashProvider methods that reported it as an error, and since
// Provider.Provide installs this store for every Inertia application, the
// result was a logged warning on every single anonymous request.
func TestGetFlashWithoutASessionIsNotAnError(t *testing.T) {
	store := NewFlash()

	flash, err := store.GetFlash(context.Background())
	if err != nil {
		t.Fatalf("GetFlash without a session = %v, want no error", err)
	}
	if len(flash) != 0 {
		t.Errorf("GetFlash returned %+v, want no flash data", flash)
	}
}

// The other five already behave this way; this pins the consistency so the
// odd one out cannot come back.
func TestFlashProviderIsConsistentWithoutASession(t *testing.T) {
	store := NewFlash()
	ctx := context.Background()

	if err := store.Flash(ctx, Flash{}); err != nil {
		t.Errorf("Flash: %v", err)
	}
	if err := store.FlashErrors(ctx, ValidationErrors{}); err != nil {
		t.Errorf("FlashErrors: %v", err)
	}
	if err := store.FlashClearHistory(ctx); err != nil {
		t.Errorf("FlashClearHistory: %v", err)
	}
	if _, err := store.GetErrors(ctx); err != nil {
		t.Errorf("GetErrors: %v", err)
	}
	if _, err := store.ShouldClearHistory(ctx); err != nil {
		t.Errorf("ShouldClearHistory: %v", err)
	}
	if _, err := store.GetFlash(ctx); err != nil {
		t.Errorf("GetFlash: %v", err)
	}
}

// The store is shared by every request in the process and holds three plain
// maps. Without synchronisation, two concurrent requests are a concurrent map
// write — which is a fatal runtime error, not a panic any recoverer can
// catch. Run with -race.
func TestFlashStoreIsSafeForConcurrentRequests(t *testing.T) {
	store := NewFlash()

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct sessions, as distinct visitors would have: the race is
			// on the map itself, not on one entry.
			ctx := withSession(string(rune('a' + i%26)))
			_ = store.Flash(ctx, Flash{"message": "saved"})
			_ = store.FlashErrors(ctx, ValidationErrors{"email": "taken"})
			_ = store.FlashClearHistory(ctx)
			_, _ = store.GetFlash(ctx)
			_, _ = store.GetErrors(ctx)
			_, _ = store.ShouldClearHistory(ctx)
		}(i)
	}
	wg.Wait()
}

// Flash data is read once and consumed, for each of the two carriers.
func TestFlashIsConsumedOnRead(t *testing.T) {
	store := NewFlash()
	ctx := withSession("s1")

	if err := store.Flash(ctx, Flash{"message": "saved"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.GetFlash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first["message"] != "saved" {
		t.Fatalf("first read = %+v", first)
	}
	second, err := store.GetFlash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("second read = %+v, want it consumed", second)
	}
}
