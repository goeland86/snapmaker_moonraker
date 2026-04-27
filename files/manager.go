package files

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manager handles local gcode file storage.
type Manager struct {
	gcodeDir  string
	configDir string
}

// NewManager creates a file manager with the given gcode and config directories.
func NewManager(gcodeDir, configDir string) (*Manager, error) {
	if err := os.MkdirAll(gcodeDir, 0755); err != nil {
		return nil, fmt.Errorf("creating gcode dir %s: %w", gcodeDir, err)
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("creating config dir %s: %w", configDir, err)
	}
	return &Manager{gcodeDir: gcodeDir, configDir: configDir}, nil
}

// GetRootPath returns the absolute path for a file root.
func (m *Manager) GetRootPath(root string) string {
	switch root {
	case "config":
		return m.configDir
	default:
		return m.gcodeDir
	}
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
			"filename":    relPath,
			"modified":    float64(info.ModTime().UnixNano()) / 1e9,
			"size":        info.Size(),
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

// SaveFromReader streams src to the file storage location and returns the
// number of bytes written. Used for large uploads where loading the entire
// content into memory would risk OOM (a 250 MB gcode upload was a common
// trigger on the Pi).
func (m *Manager) SaveFromReader(root, filename string, src io.Reader) (int64, error) {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return 0, fmt.Errorf("creating directory: %w", err)
	}

	dst, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("creating file: %w", err)
	}
	closeOK := false
	defer func() {
		if !closeOK {
			dst.Close()
			os.Remove(path)
		}
	}()

	bw := bufio.NewWriterSize(dst, 256*1024)
	n, err := io.Copy(bw, src)
	if err != nil {
		return n, err
	}
	if err := bw.Flush(); err != nil {
		return n, err
	}
	if err := dst.Close(); err != nil {
		return n, err
	}
	closeOK = true
	return n, nil
}

// FilePath returns the absolute filesystem path for a file in a root.
// Used by the printer client to stream uploads without loading the file.
func (m *Manager) FilePath(root, filename string) string {
	return filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))
}

// ReadFile reads a file from storage.
func (m *Manager) ReadFile(root, filename string) ([]byte, error) {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))
	return os.ReadFile(path)
}

// StatFile returns os.FileInfo for a file in storage.
func (m *Manager) StatFile(root, filename string) (os.FileInfo, error) {
	path := filepath.Join(m.GetRootPath(root), filepath.FromSlash(filename))
	return os.Stat(path)
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
// Both paths must resolve within a known root directory (gcodes or config).
func (m *Manager) MoveFile(source, dest string) error {
	absSrc, _ := filepath.Abs(source)
	absDst, _ := filepath.Abs(dest)
	absGcode, _ := filepath.Abs(m.gcodeDir)
	absConfig, _ := filepath.Abs(m.configDir)

	srcOk := strings.HasPrefix(absSrc, absGcode+string(filepath.Separator)) || strings.HasPrefix(absSrc, absConfig+string(filepath.Separator))
	dstOk := strings.HasPrefix(absDst, absGcode+string(filepath.Separator)) || strings.HasPrefix(absDst, absConfig+string(filepath.Separator))
	if !srcOk || !dstOk {
		return fmt.Errorf("invalid path")
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}
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

// ParseFilamentByLine reads a gcode file and returns cumulative filament extruded (mm)
// indexed by line number (0-based). Handles both absolute (M82) and relative (M83) extrusion.
func ParseFilamentByLine(path string) ([]float64, error) {
	perTool, err := ParseFilamentByLinePerTool(path)
	if err != nil {
		return nil, err
	}
	// Sum both tools into a single cumulative array.
	n := len(perTool[0])
	result := make([]float64, n)
	for i := 0; i < n; i++ {
		result[i] = perTool[0][i] + perTool[1][i]
	}
	return result, nil
}

// ParseFilamentByLinePerTool reads a gcode file and returns per-tool cumulative filament
// extruded (mm) indexed by line number (0-based). Returns [2][]float64 for T0 and T1.
// Handles tool changes (T0/T1), absolute (M82) and relative (M83) extrusion modes.
func ParseFilamentByLinePerTool(path string) ([2][]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return [2][]float64{}, err
	}
	defer f.Close()

	var result [2][]float64
	var cumulative [2]float64
	var lastAbsE [2]float64
	currentTool := 0
	relative := false // default is absolute extrusion

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		// Strip comments.
		if idx := strings.IndexByte(line, ';'); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)

		upper := strings.ToUpper(line)

		// Tool change commands.
		if len(upper) >= 2 && upper[0] == 'T' && upper[1] >= '0' && upper[1] <= '9' {
			if t, err := strconv.Atoi(upper[1:]); err == nil {
				currentTool = t % 2 // Remap T2/T3 -> T0/T1 consistent with gcode processor
			}
			result[0] = append(result[0], cumulative[0])
			result[1] = append(result[1], cumulative[1])
			continue
		}

		if upper == "M83" {
			relative = true
			result[0] = append(result[0], cumulative[0])
			result[1] = append(result[1], cumulative[1])
			continue
		}
		if upper == "M82" {
			relative = false
			result[0] = append(result[0], cumulative[0])
			result[1] = append(result[1], cumulative[1])
			continue
		}

		// Only parse G0/G1 moves.
		if !strings.HasPrefix(upper, "G0 ") && !strings.HasPrefix(upper, "G1 ") &&
			!strings.HasPrefix(upper, "G0\t") && !strings.HasPrefix(upper, "G1\t") &&
			upper != "G0" && upper != "G1" {
			result[0] = append(result[0], cumulative[0])
			result[1] = append(result[1], cumulative[1])
			continue
		}

		// Find E parameter.
		eVal := math.NaN()
		for _, field := range strings.Fields(line)[1:] {
			if len(field) > 1 && (field[0] == 'E' || field[0] == 'e') {
				if v, err := strconv.ParseFloat(field[1:], 64); err == nil {
					eVal = v
				}
			}
		}

		if !math.IsNaN(eVal) {
			t := currentTool
			if relative {
				if eVal > 0 {
					cumulative[t] += eVal
				}
			} else {
				if eVal > lastAbsE[t] {
					cumulative[t] += eVal - lastAbsE[t]
				}
				lastAbsE[t] = eVal
			}
		}

		result[0] = append(result[0], cumulative[0])
		result[1] = append(result[1], cumulative[1])
	}

	return result, scanner.Err()
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
