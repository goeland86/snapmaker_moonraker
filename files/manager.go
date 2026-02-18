package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manager handles local gcode file storage.
type Manager struct {
	gcodeDir string
}

// NewManager creates a file manager with the given gcode directory.
func NewManager(gcodeDir string) (*Manager, error) {
	if err := os.MkdirAll(gcodeDir, 0755); err != nil {
		return nil, fmt.Errorf("creating gcode dir %s: %w", gcodeDir, err)
	}
	return &Manager{gcodeDir: gcodeDir}, nil
}

// GetRootPath returns the absolute path for a file root.
func (m *Manager) GetRootPath(root string) string {
	if root == "gcodes" {
		return m.gcodeDir
	}
	return m.gcodeDir
}

// ListFiles returns file metadata for all files in the given root.
func (m *Manager) ListFiles(root string) []map[string]interface{} {
	dir := m.GetRootPath(root)
	var result []map[string]interface{}

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		relPath, _ := filepath.Rel(dir, path)
		// Use forward slashes for consistency.
		relPath = filepath.ToSlash(relPath)

		result = append(result, map[string]interface{}{
			"path":     relPath,
			"modified": float64(info.ModTime().UnixNano()) / 1e9,
			"size":     info.Size(),
			"permissions": "rw",
		})
		return nil
	})

	if result == nil {
		result = []map[string]interface{}{}
	}
	return result
}

// GetDirectory returns directory listing in Moonraker's get_directory format.
func (m *Manager) GetDirectory(root, path string) map[string]interface{} {
	dir := m.GetRootPath(root)
	if path != "" && path != root {
		// Strip root prefix if present (e.g., "gcodes/subdir" -> "subdir").
		cleanPath := path
		if strings.HasPrefix(cleanPath, root+"/") {
			cleanPath = strings.TrimPrefix(cleanPath, root+"/")
		} else if cleanPath == root {
			cleanPath = ""
		}
		if cleanPath != "" {
			dir = filepath.Join(dir, filepath.FromSlash(cleanPath))
		}
	}

	var files []map[string]interface{}
	var dirs []map[string]interface{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		files = []map[string]interface{}{}
		dirs = []map[string]interface{}{}
	} else {
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if entry.IsDir() {
				dirs = append(dirs, map[string]interface{}{
					"dirname":  entry.Name(),
					"modified": float64(info.ModTime().UnixNano()) / 1e9,
					"size":     info.Size(),
					"permissions": "rw",
				})
			} else {
				files = append(files, map[string]interface{}{
					"filename":    entry.Name(),
					"modified":    float64(info.ModTime().UnixNano()) / 1e9,
					"size":        info.Size(),
					"permissions": "rw",
				})
			}
		}
	}

	if files == nil {
		files = []map[string]interface{}{}
	}
	if dirs == nil {
		dirs = []map[string]interface{}{}
	}

	// Get disk usage
	diskUsage := m.getDiskUsage(m.GetRootPath(root))

	return map[string]interface{}{
		"dirs":  dirs,
		"files": files,
		"disk_usage": diskUsage,
		"root_info": map[string]interface{}{
			"name":        root,
			"permissions": "rw",
		},
	}
}

// getDiskUsage returns disk usage stats for the given path.
func (m *Manager) getDiskUsage(path string) map[string]interface{} {
	total, free := diskUsage(path)
	return map[string]interface{}{
		"total": total,
		"used":  total - free,
		"free":  free,
	}
}

// GetMetadata returns metadata for a specific file.
func (m *Manager) GetMetadata(root, filename string) (map[string]interface{}, error) {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", filename)
	}

	meta := map[string]interface{}{
		"filename":       filename,
		"size":           info.Size(),
		"modified":       float64(info.ModTime().UnixNano()) / 1e9,
		"print_start_time": nil,
		"job_id":         nil,
		"slicer":         "",
		"slicer_version": "",
		"estimated_time": nil,
		"filament_total": 0.0,
		"first_layer_height": nil,
		"layer_height":   nil,
		"object_height":  nil,
	}

	// Try to extract metadata from gcode comments.
	if strings.HasSuffix(filename, ".gcode") || strings.HasSuffix(filename, ".g") {
		extractGCodeMeta(path, meta)
	}

	return meta, nil
}

// SaveFile writes data to the file storage.
func (m *Manager) SaveFile(root, filename string, data []byte) error {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))

	// Create parent directories if needed.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// ReadFile reads a file from storage.
func (m *Manager) ReadFile(root, filename string) ([]byte, error) {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))
	return os.ReadFile(path)
}

// CreateDirectory creates a directory within a root.
func (m *Manager) CreateDirectory(root, dirPath string) error {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(dirPath))
	return os.MkdirAll(path, 0755)
}

// DeleteDirectory removes an empty directory within a root.
func (m *Manager) DeleteDirectory(root, dirPath string) error {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(dirPath))

	absRoot, _ := filepath.Abs(m.GetRootPath(root))
	absPath, _ := filepath.Abs(path)
	if !strings.HasPrefix(absPath, absRoot) {
		return fmt.Errorf("invalid path: %s", dirPath)
	}

	return os.Remove(path)
}

// MoveFile moves/renames a file or directory.
func (m *Manager) MoveFile(source, dest string) error {
	return os.Rename(source, dest)
}

// ResolvePath resolves a root/path pair to an absolute filesystem path.
func (m *Manager) ResolvePath(rootAndPath string) string {
	// Moonraker paths look like "gcodes/subdir/file.gcode"
	parts := strings.SplitN(rootAndPath, "/", 2)
	root := parts[0]
	path := ""
	if len(parts) > 1 {
		path = parts[1]
	}
	return filepath.Join(m.GetRootPath(root), filepath.FromSlash(path))
}

// DeleteFile removes a file from storage.
func (m *Manager) DeleteFile(root, filename string) error {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))

	// Ensure the path is within the root to prevent directory traversal.
	absRoot, _ := filepath.Abs(m.GetRootPath(root))
	absPath, _ := filepath.Abs(path)
	if !strings.HasPrefix(absPath, absRoot) {
		return fmt.Errorf("invalid path: %s", filename)
	}

	return os.Remove(path)
}

// extractGCodeMeta reads the first few lines of a gcode file to extract slicer metadata.
func extractGCodeMeta(path string, meta map[string]interface{}) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	// Only scan the first 8KB and last 8KB for metadata comments.
	content := string(data)
	scanRegion := content
	if len(content) > 16384 {
		scanRegion = content[:8192] + "\n" + content[len(content)-8192:]
	}

	for _, line := range strings.Split(scanRegion, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ";") {
			continue
		}
		line = strings.TrimPrefix(line, "; ")
		line = strings.TrimPrefix(line, ";")

		if kv := strings.SplitN(line, "=", 2); len(kv) == 2 {
			key := strings.TrimSpace(strings.ToLower(kv[0]))
			val := strings.TrimSpace(kv[1])

			switch key {
			case "generated by", "slicer":
				meta["slicer"] = val
			case "slicer_version", "slicer version":
				meta["slicer_version"] = val
			case "estimated printing time (normal mode)", "estimated_time":
				meta["estimated_time"] = parseDuration(val)
			case "filament used [mm]", "filament_total":
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					meta["filament_total"] = f
				} else {
					meta["filament_total"] = val
				}
			case "first_layer_height":
				meta["first_layer_height"] = val
			case "layer_height":
				meta["layer_height"] = val
			case "max_print_height", "object_height":
				meta["object_height"] = val
			}
		}
	}
}

// parseDuration parses a human-readable duration like "1h 30m 15s" to seconds.
func parseDuration(s string) float64 {
	d, err := time.ParseDuration(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "d", "h"))
	if err != nil {
		return 0
	}
	return d.Seconds()
}
