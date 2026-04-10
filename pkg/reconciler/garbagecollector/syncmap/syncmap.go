package syncmap

import "sync"

// SyncMap is a type-safe wrapper around sync.Map.
type SyncMap[K comparable, V any] struct {
	m sync.Map
}

// NewSyncMap creates a new SyncMap.
func NewSyncMap[K comparable, V any]() *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m: sync.Map{},
	}
}

// Store stores the given key-value pair in the map.
func (m *SyncMap[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

// StoreIfAbsent stores the given key-value pair in the map only if
// the key does not already exist.
func (m *SyncMap[K, V]) StoreIfAbsent(key K, value V) {
	m.m.LoadOrStore(key, value)
}

// Load retrieves the value for the given key from the map.
func (m *SyncMap[K, V]) Load(key K) (V, bool) {
	value, ok := m.m.Load(key)
	if !ok {
		return *new(V), false
	}
	return value.(V), true
}

// LoadAndDelete retrieves and removes the value for the given key from
// the map.
func (m *SyncMap[K, V]) LoadAndDelete(key K) (V, bool) {
	value, ok := m.m.LoadAndDelete(key)
	if !ok {
		return *new(V), false
	}
	return value.(V), true
}

// Delete removes the given key from the map.
func (m *SyncMap[K, V]) Delete(key K) {
	m.m.Delete(key)
}

// Modify modifies the value for the given key using the provided
// modifyFunc.
// The modifyFunc receives the current value or a valid default value
// and a boolean indicating whether the key exists in the map. It should
// return the new value to be stored.
// Modify retries on CAS failure to ensure the modification is applied.
func (m *SyncMap[K, V]) Modify(key K, modifyFunc func(value V, exists bool) V) {
	for {
		value, exists := m.Load(key)
		if !exists {
			value = *new(V)
		}
		newValue := modifyFunc(value, exists)
		if exists {
			if m.m.CompareAndSwap(key, value, newValue) {
				return
			}
			// CAS failed, another writer modified the value — retry.
			continue
		}
		// Key didn't exist — store the new value.
		m.m.Store(key, newValue)
		return
	}
}

// Range iterates over all key-value pairs in the map, calling the
// provided function for each pair. If the function returns false,
// the iteration stops.
func (m *SyncMap[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(func(k, v any) bool {
		return f(k.(K), v.(V))
	})
}
