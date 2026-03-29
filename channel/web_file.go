// xbot Web Channel — File upload/download handlers

package channel

import (
	"encoding/json"
	"fmt"
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
	if !isAllowedExtension(ext) {
		http.Error(w, "file type not allowed", http.StatusBadRequest)
		return
	}
	if isBlockedMIME(detectedMIME) {
		log.WithFields(log.Fields{
			"filename":  header.Filename,
			"mime_type": detectedMIME,
		}).Warn("Blocked file upload with dangerous MIME type")
		http.Error(w, "file type not allowed", http.StatusBadRequest)
		return
	}

	// Detect MIME type
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	// Check if cloud OSS provider is configured
	if wc.ossProvider != nil && wc.ossProvider.Name() != "local" {
		wc.handleCloudUpload(w, r, header.Filename, ext, data, mimeType)
		return
	}

	// Local storage mode — uploadDir is required
	if wc.uploadDir == "" {
		log.Error("File upload rejected: uploadDir is not configured and no cloud OSS provider available")
		http.Error(w, "file storage not configured", http.StatusServiceUnavailable)
		return
	}

	// Local storage mode
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

	// JSON response
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"file_id": fileID,
		"name":    header.Filename,
		"size":    len(data),
		"mime":    mimeType,
	})
}

// handleCloudUpload uploads a file to cloud OSS (e.g., Qiniu) and returns the upload key.
func (wc *WebChannel) handleCloudUpload(w http.ResponseWriter, r *http.Request, filename, ext string, data []byte, mimeType string) {
	// Get user ID for the object key
	userID := "anonymous"
	if si := wc.validateSession(r); si != nil {
		userID = fmt.Sprintf("%d", si.userID)
	}

	// Generate object key
	key := fmt.Sprintf("uploads/%s/%s%s", userID, uuid.New().String(), ext)

	// Upload to cloud OSS
	if err := wc.ossProvider.Upload(key, data); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"key":      key,
			"filename": filename,
		}).Error("Failed to upload file to cloud OSS")
		http.Error(w, "failed to upload to cloud storage", http.StatusInternalServerError)
		return
	}

	log.WithFields(log.Fields{
		"key":      key,
		"filename": filename,
		"size":     len(data),
		"provider": wc.ossProvider.Name(),
	}).Info("File uploaded to cloud OSS")

	// JSON response — return upload_key so frontend can reference it when sending messages
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":         true,
		"upload_key": key,
		"name":       filename,
		"size":       len(data),
		"mime":       mimeType,
	})
}

// isAllowedExtension checks if the file extension is in the allowed list.
func isAllowedExtension(ext string) bool {
	allowed := map[string]bool{
		".txt": true, ".md": true, ".csv": true, ".json": true, ".xml": true, ".yaml": true, ".yml": true,
		".log": true, ".py": true, ".js": true, ".ts": true, ".go": true, ".rs": true, ".java": true,
		".c": true, ".cpp": true, ".h": true, ".sh": true, ".bash": true, ".zsh": true,
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
		".zip": true, ".tar": true, ".gz": true, ".7z": true, ".rar": true,
		".mp3": true, ".mp4": true, ".wav": true, ".webm": true, ".ogg": true,
		".toml": true, ".cfg": true, ".ini": true, ".env": true, ".sql": true,
	}
	return allowed[ext]
}

// isBlockedMIME checks if the MIME type is blocked.
func isBlockedMIME(mimeType string) bool {
	blocked := map[string]bool{
		"text/html":               true,
		"application/xhtml+xml":   true,
		"application/x-httpd-php": true,
	}
	return blocked[mimeType]
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
