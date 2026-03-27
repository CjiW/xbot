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

	// Generate unique ID with extension
	ext := filepath.Ext(header.Filename)
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
	if fileID == "" || strings.Contains(fileID, "/") {
		http.Error(w, "invalid file id", http.StatusBadRequest)
		return
	}

	filePath := filepath.Join(wc.uploadDir, "web", fileID)

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
