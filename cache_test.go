package main

import (
	"testing"
	"time"
)

func TestDocCache_GetMissOnEmpty(t *testing.T) {
	c := NewDocCache(10, time.Minute)
	if _, ok := c.Get("nope"); ok {
		t.Fatal("expected miss on empty cache")
	}
}

func TestDocCache_PutThenGet(t *testing.T) {
	c := NewDocCache(10, time.Minute)
	c.Put("k", "v")
	v, ok := c.Get("k")
	if !ok || v != "v" {
		t.Fatalf("want v/true, got %q/%v", v, ok)
	}
}

func TestDocCache_Expiry(t *testing.T) {
	c := NewDocCache(10, 5*time.Millisecond)
	c.Put("k", "v")
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected expired entry to miss")
	}
}

func TestDocCache_SizeEvictsOldest(t *testing.T) {
	c := NewDocCache(2, time.Minute)
	c.Put("a", "1")
	time.Sleep(2 * time.Millisecond)
	c.Put("b", "2")
	time.Sleep(2 * time.Millisecond)
	c.Put("c", "3") // evicts "a"

	if _, ok := c.Get("a"); ok {
		t.Error("expected 'a' to be evicted")
	}
	if v, ok := c.Get("b"); !ok || v != "2" {
		t.Errorf("expected b=2, got %q/%v", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != "3" {
		t.Errorf("expected c=3, got %q/%v", v, ok)
	}
}

func TestDocCache_ExpiredEntryIsPurgedOnGet(t *testing.T) {
	c := NewDocCache(10, 5*time.Millisecond)
	c.Put("k", "v")
	time.Sleep(15 * time.Millisecond)
	_, _ = c.Get("k") // triggers purge
	// The entry should be gone from the map now, not just marked expired.
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, present := c.entries["k"]; present {
		t.Fatal("expected Get to purge expired entry from the map")
	}
}

func TestDocCache_OverwriteRefreshesTimestamp(t *testing.T) {
	c := NewDocCache(10, time.Minute)
	c.Put("k", "v1")
	time.Sleep(5 * time.Millisecond)
	c.Put("k", "v2")
	v, ok := c.Get("k")
	if !ok || v != "v2" {
		t.Fatalf("want v2/true, got %q/%v", v, ok)
	}
}
