// xbot Web Channel — File upload/download handlers

package channel

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	log "xbot/logger"

	"github.com/google/uuid"
)

const (
	maxFileSize = 10 << 20 // 10MB
)

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// handleFileUpload handles POST /api/files/upload
func (wc *WebChannel) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+1024) // +1KB for multipart overhead

	if err := r.ParseMultipartForm(maxFileSize); err != nil {
		http.Error(w, "file too large (max 10MB)", http.StatusRequestEntityTooLarge)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Read file content
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	if int64(len(data)) > maxFileSize {
		http.Error(w, "file too large (max 10MB)", http.StatusRequestEntityTooLarge)
		return
	}

	// Validate MIME type — block dangerous file types
	ext := strings.ToLower(filepath.Ext(header.Filename))
	detectedMIME := http.DetectContentType(data)
	allowedExtensions := map[string]bool{
		".txt": true, ".md": true, ".csv": true, ".json": true, ".xml": true, ".yaml": true, ".yml": true,
		".log": true, ".py": true, ".js": true, ".ts": true, ".go": true, ".rs": true, ".java": true,
		".c": true, ".cpp": true, ".h": true, ".sh": true, ".bash": true, ".zsh": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
		".zip": true, ".tar": true, ".gz": true, ".7z": true, ".rar": true,
		".mp3": true, ".mp4": true, ".wav": true, ".webm": true, ".ogg": true,
		".toml": true, ".cfg": true, ".ini": true, ".env": true, ".sql": true,
	}
	blockedMIMEs := map[string]bool{
		"text/html":               true,
		"application/xhtml+xml":   true,
		"application/x-httpd-php": true,
	}
	if !allowedExtensions[ext] {
		http.Error(w, "file type not allowed", http.StatusBadRequest)
		return
	}
	if blockedMIMEs[detectedMIME] {
		log.WithFields(log.Fields{
			"filename":  header.Filename,
			"mime_type": detectedMIME,
		}).Warn("Blocked file upload with dangerous MIME type")
		http.Error(w, "file type not allowed", http.StatusBadRequest)
		return
	}

	// Generate unique ID with extension
	fileID := uuid.New().String() + ext

	// Ensure upload directory exists
	uploadDir := filepath.Join(wc.uploadDir, "web")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.WithError(err).Error("Failed to create upload directory")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Write file
	filePath := filepath.Join(uploadDir, fileID)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		log.WithError(err).Error("Failed to write uploaded file")
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}

	// Detect MIME type
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	// JSON response
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"file_id": fileID,
		"name":    header.Filename,
		"size":    len(data),
		"mime":    mimeType,
	})
}

// handleFileDownload handles GET /api/files/{id}
func (wc *WebChannel) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	// Extract file ID from path: /api/files/{id}
	fileID := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if fileID == "" || strings.ContainsAny(fileID, "/\\") || strings.Contains(fileID, "..") {
		http.Error(w, "invalid file id", http.StatusBadRequest)
		return
	}

	// Clean and validate path to prevent traversal
	filePath := filepath.Join(wc.uploadDir, "web", filepath.Base(fileID))

	// Ensure the resolved path is within the upload directory
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		http.Error(w, "invalid file id", http.StatusBadRequest)
		return
	}
	absUploadDir, err := filepath.Abs(filepath.Join(wc.uploadDir, "web"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !strings.HasPrefix(absPath, absUploadDir+string(os.PathSeparator)) {
		http.Error(w, "invalid file id", http.StatusBadRequest)
		return
	}

	// Stat to check existence and get size
	info, err := os.Stat(filePath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	// Detect MIME type
	ext := filepath.Ext(fileID)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Set headers
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", "inline; filename=\""+fileID+"\"")

	// Serve file
	http.ServeFile(w, r, filePath)
}
