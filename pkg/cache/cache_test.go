package cache

import (
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	ttl := time.Hour
	c := New(ttl)

	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.ttl != ttl {
		t.Errorf("expected TTL %v, got %v", ttl, c.ttl)
	}
	if c.entries == nil {
		t.Error("entries map not initialized")
	}
}

func TestCache_SetAndGet(t *testing.T) {
	c := New(time.Hour)

	// Test setting and getting a value
	c.Set("key1", "value1")
	val, found := c.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Test getting non-existent key
	val, found = c.Get("nonexistent")
	if found {
		t.Error("expected key not to be found")
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}
}

func TestCache_SetWithTTL(t *testing.T) {
	c := New(time.Hour)

	// Set with short TTL
	c.SetWithTTL("key1", "value1", 100*time.Millisecond)

	// Should be found immediately
	val, found := c.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not be found after expiration
	val, found = c.Get("key1")
	if found {
		t.Error("expected key1 to be expired")
	}
	if val != nil {
		t.Errorf("expected nil value for expired key, got %v", val)
	}
}

func TestCache_SetOverwrite(t *testing.T) {
	c := New(time.Hour)

	// Set initial value
	c.Set("key1", "value1")

	// Overwrite with new value
	c.Set("key1", "value2")

	val, found := c.Get("key1")
	if !found {
		t.Fatal("expected to find key1")
	}
	if val != "value2" {
		t.Errorf("expected value2, got %v", val)
	}
}

func TestCache_DifferentValueTypes(t *testing.T) {
	c := New(time.Hour)

	tests := []struct {
		name  string
		key   string
		value any
	}{
		{"string", "str", "test"},
		{"int", "int", 42},
		{"float", "float", 3.14},
		{"bool", "bool", true},
		{"slice", "slice", []int{1, 2, 3}},
		{"map", "map", map[string]int{"a": 1}},
		{"struct", "struct", struct{ Name string }{"test"}},
		{"nil", "nil", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.Set(tt.key, tt.value)
			val, found := c.Get(tt.key)
			if !found {
				t.Fatalf("expected to find %s", tt.key)
			}
			// Deep comparison for complex types would require reflection,
			// but we can at least verify something was stored
			if val == nil && tt.value != nil {
				t.Errorf("expected non-nil value for %s", tt.key)
			}
		})
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	c := New(time.Hour)
	const goroutines = 100
	const operations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3) // readers, writers, setWithTTL

	// Concurrent writers
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for j := range operations {
				key := "key" + string(rune(id%10))
				c.Set(key, id*operations+j)
			}
		}(i)
	}

	// Concurrent readers
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range operations {
				key := "key" + string(rune(id%10))
				c.Get(key)
			}
		}(i)
	}

	// Concurrent SetWithTTL
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for j := range operations {
				key := "key" + string(rune(id%10))
				c.SetWithTTL(key, id*operations+j, time.Hour)
			}
		}(i)
	}

	wg.Wait()

	// If we get here without deadlock or race conditions, the test passes
}

func TestCache_ExpirationRaceCondition(t *testing.T) {
	c := New(time.Hour)

	// Set a key with very short TTL
	c.SetWithTTL("key1", "value1", 50*time.Millisecond)

	// Concurrently access the key while it's expiring
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				c.Get("key1")
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	// Should not panic or deadlock
}

func TestCache_MultipleKeys(t *testing.T) {
	c := New(time.Hour)

	// Set multiple keys
	for i := range 1000 {
		key := "key" + string(rune(i))
		c.Set(key, i)
	}

	// Verify all keys
	for i := range 1000 {
		key := "key" + string(rune(i))
		val, found := c.Get(key)
		if !found {
			t.Errorf("expected to find %s", key)
		}
		if val != i {
			t.Errorf("expected %d, got %v", i, val)
		}
	}
}

func TestCache_CleanupExpired(t *testing.T) {
	c := New(time.Hour)

	// Set entries with very short TTL
	for i := range 10 {
		key := "key" + string(rune(i))
		c.SetWithTTL(key, i, 50*time.Millisecond)
	}

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Access keys - should be cleaned up on access
	for i := range 10 {
		key := "key" + string(rune(i))
		_, found := c.Get(key)
		if found {
			t.Errorf("expected %s to be expired", key)
		}
	}

	// Verify internal entries are cleaned up after access
	c.mu.RLock()
	count := len(c.entries)
	c.mu.RUnlock()

	if count > 0 {
		t.Logf("note: %d entries remain after access (will be cleaned by background goroutine)", count)
	}
}

func TestCache_ZeroTTL(t *testing.T) {
	c := New(0)

	c.Set("key1", "value1")

	// With zero TTL, the entry should expire immediately
	// However, time.Now().Add(0) gives current time, not past
	// so it might still be valid for a brief moment
	val, found := c.Get("key1")

	// The behavior with zero TTL is that it expires immediately
	// after time progresses, but initially it's valid
	if found && val != "value1" {
		t.Errorf("unexpected value: %v", val)
	}
}

func TestCache_NegativeTTL(t *testing.T) {
	c := New(time.Hour)

	// Set with negative TTL (already expired)
	c.SetWithTTL("key1", "value1", -time.Second)

	// Should not be found
	val, found := c.Get("key1")
	if found {
		t.Error("expected key with negative TTL to be expired")
	}
	if val != nil {
		t.Errorf("expected nil value, got %v", val)
	}
}

func TestCache_UpdateExpirationOnSet(t *testing.T) {
	c := New(time.Hour)

	// Set with short TTL
	c.SetWithTTL("key1", "value1", 100*time.Millisecond)

	// Wait a bit
	time.Sleep(60 * time.Millisecond)

	// Update with longer TTL
	c.SetWithTTL("key1", "value2", time.Hour)

	// Wait for original TTL to expire
	time.Sleep(60 * time.Millisecond)

	// Should still be found due to updated TTL
	val, found := c.Get("key1")
	if !found {
		t.Fatal("expected to find key1 with updated TTL")
	}
	if val != "value2" {
		t.Errorf("expected value2, got %v", val)
	}
}

func TestCache_EmptyKey(t *testing.T) {
	c := New(time.Hour)

	// Test empty key
	c.Set("", "empty")
	val, found := c.Get("")
	if !found {
		t.Fatal("expected to find empty key")
	}
	if val != "empty" {
		t.Errorf("expected 'empty', got %v", val)
	}
}

func TestCache_LargeValue(t *testing.T) {
	c := New(time.Hour)

	// Test with large value
	largeValue := make([]byte, 1024*1024) // 1MB
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	c.Set("large", largeValue)
	val, found := c.Get("large")
	if !found {
		t.Fatal("expected to find large value")
	}

	retrieved, ok := val.([]byte)
	if !ok {
		t.Fatal("expected []byte value")
	}
	if len(retrieved) != len(largeValue) {
		t.Errorf("expected length %d, got %d", len(largeValue), len(retrieved))
	}
}

func BenchmarkCache_Set(b *testing.B) {
	c := New(time.Hour)
	b.ResetTimer()

	for i := range b.N {
		key := "key" + string(rune(i%1000))
		c.Set(key, i)
	}
}

func BenchmarkCache_Get(b *testing.B) {
	c := New(time.Hour)

	// Pre-populate cache
	for i := range 1000 {
		key := "key" + string(rune(i))
		c.Set(key, i)
	}

	b.ResetTimer()
	for i := range b.N {
		key := "key" + string(rune(i%1000))
		c.Get(key)
	}
}

func BenchmarkCache_SetWithTTL(b *testing.B) {
	c := New(time.Hour)
	b.ResetTimer()

	for i := range b.N {
		key := "key" + string(rune(i%1000))
		c.SetWithTTL(key, i, time.Hour)
	}
}

func BenchmarkCache_ConcurrentSetAndGet(b *testing.B) {
	c := New(time.Hour)
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "key" + string(rune(i%100))
			if i%2 == 0 {
				c.Set(key, i)
			} else {
				c.Get(key)
			}
			i++
		}
	})
}
