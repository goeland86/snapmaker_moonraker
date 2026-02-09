package database

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Database provides a simple JSON-file backed key-value store,
// compatible with Moonraker's server/database API.
// Data is organized by namespace, with each namespace stored in a separate JSON file.
type Database struct {
	mu      sync.RWMutex
	dataDir string
	cache   map[string]map[string]interface{} // namespace -> key -> value
}

// New creates a new database with the given data directory.
func New(dataDir string) (*Database, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	db := &Database{
		dataDir: dataDir,
		cache:   make(map[string]map[string]interface{}),
	}

	// Load existing namespaces
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading database directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		namespace := strings.TrimSuffix(entry.Name(), ".json")
		if err := db.loadNamespace(namespace); err != nil {
			// Log but don't fail - corrupted files can be recreated
			fmt.Printf("Warning: failed to load namespace %s: %v\n", namespace, err)
		}
	}

	return db, nil
}

// loadNamespace loads a namespace from disk into cache.
func (db *Database) loadNamespace(namespace string) error {
	path := filepath.Join(db.dataDir, namespace+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var ns map[string]interface{}
	if err := json.Unmarshal(data, &ns); err != nil {
		return err
	}

	db.cache[namespace] = ns
	return nil
}

// saveNamespace persists a namespace to disk.
func (db *Database) saveNamespace(namespace string) error {
	ns, ok := db.cache[namespace]
	if !ok {
		return nil
	}

	data, err := json.MarshalIndent(ns, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(db.dataDir, namespace+".json")
	return os.WriteFile(path, data, 0644)
}

// GetItem retrieves a value by namespace and key.
// Key can use dot notation for nested access (e.g., "printer.id").
func (db *Database) GetItem(namespace, key string) (interface{}, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	ns, ok := db.cache[namespace]
	if !ok {
		return nil, false
	}

	return db.getNestedValue(ns, key)
}

// GetNamespace returns all items in a namespace.
func (db *Database) GetNamespace(namespace string) (map[string]interface{}, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	ns, ok := db.cache[namespace]
	if !ok {
		return nil, false
	}

	// Return a copy to prevent external modification
	result := make(map[string]interface{})
	for k, v := range ns {
		result[k] = v
	}
	return result, true
}

// SetItem stores a value by namespace and key.
// Key can use dot notation for nested access.
func (db *Database) SetItem(namespace, key string, value interface{}) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	ns, ok := db.cache[namespace]
	if !ok {
		ns = make(map[string]interface{})
		db.cache[namespace] = ns
	}

	db.setNestedValue(ns, key, value)
	return db.saveNamespace(namespace)
}

// DeleteItem removes a value by namespace and key.
func (db *Database) DeleteItem(namespace, key string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	ns, ok := db.cache[namespace]
	if !ok {
		return nil
	}

	db.deleteNestedValue(ns, key)
	return db.saveNamespace(namespace)
}

// ListNamespaces returns all available namespaces.
func (db *Database) ListNamespaces() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	namespaces := make([]string, 0, len(db.cache))
	for ns := range db.cache {
		namespaces = append(namespaces, ns)
	}
	return namespaces
}

// getNestedValue retrieves a value using dot notation.
func (db *Database) getNestedValue(m map[string]interface{}, key string) (interface{}, bool) {
	parts := strings.Split(key, ".")
	current := interface{}(m)

	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = v[part]
			if !ok {
				return nil, false
			}
		default:
			return nil, false
		}
	}

	return current, true
}

// setNestedValue sets a value using dot notation, creating intermediate maps as needed.
func (db *Database) setNestedValue(m map[string]interface{}, key string, value interface{}) {
	parts := strings.Split(key, ".")

	if len(parts) == 1 {
		m[key] = value
		return
	}

	current := m
	for i, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok {
			next = make(map[string]interface{})
			current[part] = next
		}

		nextMap, ok := next.(map[string]interface{})
		if !ok {
			// Overwrite non-map value with a new map
			nextMap = make(map[string]interface{})
			current[part] = nextMap
		}

		if i == len(parts)-2 {
			nextMap[parts[len(parts)-1]] = value
		} else {
			current = nextMap
		}
	}
}

// deleteNestedValue removes a value using dot notation.
func (db *Database) deleteNestedValue(m map[string]interface{}, key string) {
	parts := strings.Split(key, ".")

	if len(parts) == 1 {
		delete(m, key)
		return
	}

	current := m
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok {
			return
		}

		nextMap, ok := next.(map[string]interface{})
		if !ok {
			return
		}
		current = nextMap
	}

	delete(current, parts[len(parts)-1])
}
