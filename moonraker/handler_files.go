package moonraker

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

// registerFileHandlers sets up /server/files/* routes.
func (s *Server) registerFileHandlers() {
	s.mux.HandleFunc("GET /server/files/list", s.handleFileList)
	s.mux.HandleFunc("GET /server/files/directory", s.handleFileDirectory)
	s.mux.HandleFunc("GET /server/files/metadata", s.handleFileMetadata)
	s.mux.HandleFunc("POST /server/files/upload", s.handleFileUpload)
	s.mux.HandleFunc("POST /server/files/directory", s.handleCreateDirectory)
	s.mux.HandleFunc("DELETE /server/files/directory", s.handleDeleteDirectory)
	s.mux.HandleFunc("POST /server/files/move", s.handleFileMove)
	s.mux.HandleFunc("DELETE /server/files/{root}/{path...}", s.handleFileDelete)
	s.mux.HandleFunc("GET /server/files/{root}/{path...}", s.handleFileDownload)
	s.mux.HandleFunc("GET /server/files/roots", s.handleFileRoots)
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if root == "" {
		root = "gcodes"
	}

	files := s.fileManager.ListFiles(root)

	writeJSON(w, map[string]interface{}{
		"result": files,
	})
}

func (s *Server) handleFileDirectory(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	root := "gcodes"
	if path == "" {
		path = "gcodes"
	}
	// If path starts with a known root, extract it
	if strings.HasPrefix(path, "gcodes") {
		root = "gcodes"
		path = strings.TrimPrefix(path, "gcodes")
		path = strings.TrimPrefix(path, "/")
	}

	writeJSON(w, map[string]interface{}{
		"result": s.fileManager.GetDirectory(root, path),
	})
}

func (s *Server) handleFileMetadata(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{
				"filename": "",
				"size":     0,
				"modified": float64(0),
			},
		})
		return
	}

	meta, err := s.fileManager.GetMetadata("gcodes", filename)
	if err != nil {
		// Return minimal metadata stub for files not in local storage
		// (e.g. prints started from the printer's touchscreen).
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{
				"filename": filename,
				"size":     0,
				"modified": float64(0),
			},
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"result": meta,
	})
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(512 << 20); err != nil { // 512MB max
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	root := r.FormValue("root")
	if root == "" {
		root = "gcodes"
	}

	// In Moonraker's API, "path" is the subdirectory within the root,
	// and the actual filename comes from the multipart file header.
	filename := header.Filename
	if subdir := r.FormValue("path"); subdir != "" {
		filename = subdir + "/" + filename
	}

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	if err := s.fileManager.SaveFile(root, filename, data); err != nil {
		http.Error(w, "failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("File uploaded: %s/%s (%d bytes)", root, filename, len(data))

	// Notify WebSocket clients.
	s.wsHub.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "create_file",
			"item": map[string]interface{}{
				"root":     root,
				"path":     filename,
				"modified": 0,
				"size":     len(data),
			},
		},
	})

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"item": map[string]interface{}{
				"path":     filename,
				"root":     root,
				"modified": 0,
				"size":     len(data),
			},
			"action": "create_file",
		},
	})
}

func (s *Server) handleCreateDirectory(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		if err := r.ParseForm(); err == nil {
			path = r.FormValue("path")
		}
	}
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	// Extract root from path (e.g., "gcodes/subdir" -> root="gcodes", dir="subdir")
	root := "gcodes"
	dirPath := path
	if strings.HasPrefix(path, "gcodes/") {
		dirPath = strings.TrimPrefix(path, "gcodes/")
	} else if path == "gcodes" {
		dirPath = ""
	}

	if err := s.fileManager.CreateDirectory(root, dirPath); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    500,
				"message": err.Error(),
			},
		})
		return
	}

	s.wsHub.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "create_dir",
			"item": map[string]interface{}{
				"root": root,
				"path": dirPath,
			},
		},
	})

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"item": map[string]interface{}{
				"path": dirPath,
				"root": root,
			},
			"action": "create_dir",
		},
	})
}

func (s *Server) handleDeleteDirectory(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}

	root := "gcodes"
	dirPath := path
	if strings.HasPrefix(path, "gcodes/") {
		dirPath = strings.TrimPrefix(path, "gcodes/")
	}

	if err := s.fileManager.DeleteDirectory(root, dirPath); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    404,
				"message": err.Error(),
			},
		})
		return
	}

	s.wsHub.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "delete_dir",
			"item": map[string]interface{}{
				"root": root,
				"path": dirPath,
			},
		},
	})

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"item": map[string]interface{}{
				"path": dirPath,
				"root": root,
			},
			"action": "delete_dir",
		},
	})
}

func (s *Server) handleFileMove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	source := r.FormValue("source")
	dest := r.FormValue("dest")
	if source == "" || dest == "" {
		http.Error(w, "missing source or dest parameter", http.StatusBadRequest)
		return
	}

	srcPath := s.fileManager.ResolvePath(source)
	dstPath := s.fileManager.ResolvePath(dest)

	if err := s.fileManager.MoveFile(srcPath, dstPath); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    500,
				"message": err.Error(),
			},
		})
		return
	}

	s.wsHub.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "move_file",
			"item": map[string]interface{}{
				"path":        dest,
				"root":        "gcodes",
				"source_path": source,
			},
		},
	})

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"item": map[string]interface{}{
				"path":        dest,
				"root":        "gcodes",
				"source_path": source,
			},
			"action": "move_file",
		},
	})
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	root := r.PathValue("root")
	path := r.PathValue("path")

	if err := s.fileManager.DeleteFile(root, path); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    404,
				"message": err.Error(),
			},
		})
		return
	}

	// Notify WebSocket clients.
	s.wsHub.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "delete_file",
			"item": map[string]interface{}{
				"root": root,
				"path": path,
			},
		},
	})

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"item": map[string]interface{}{
				"path": path,
				"root": root,
			},
			"action": "delete_file",
		},
	})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	root := r.PathValue("root")
	path := r.PathValue("path")

	data, err := s.fileManager.ReadFile(root, path)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// Set content type based on extension.
	if strings.HasSuffix(path, ".gcode") || strings.HasSuffix(path, ".g") {
		w.Header().Set("Content-Type", "text/plain")
	} else if strings.HasSuffix(path, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+path)
	w.Write(data)
}

func (s *Server) handleFileRoots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": []map[string]interface{}{
			{
				"name":        "gcodes",
				"path":        s.fileManager.GetRootPath("gcodes"),
				"permissions": "rw",
			},
		},
	})
}

// Ensure json import is used (needed for handleFileUpload body parsing if extended).
var _ = json.Marshal
