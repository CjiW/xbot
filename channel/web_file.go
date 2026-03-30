// xbot Web Channel - File upload handlers

package channel

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	log "xbot/logger"

	"github.com/google/uuid"
)

const (
	maxFileSize = 10 << 20 // 10MB
)

// handleFileUpload handles POST /api/files/upload
func (wc *WebChannel) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+1024)

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

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	if int64(len(data)) > maxFileSize {
		http.Error(w, "file too large (max 10MB)", http.StatusRequestEntityTooLarge)
		return
	}

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

	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	// Web uploads MUST go to cloud OSS - local storage is never allowed for security
	if wc.ossProvider == nil || wc.ossProvider.Name() == "local" {
		log.Error("Web file upload rejected: no cloud OSS provider configured (local storage is forbidden for web uploads)")
		http.Error(w, "file storage not configured", http.StatusServiceUnavailable)
		return
	}

	wc.handleCloudUpload(w, r, header.Filename, ext, data, mimeType)
}

// handleCloudUpload uploads a file to cloud OSS (e.g., Qiniu) and returns the upload key.
func (wc *WebChannel) handleCloudUpload(w http.ResponseWriter, r *http.Request, filename, ext string, data []byte, mimeType string) {
	userID := "anonymous"
	if si := wc.validateSession(r); si != nil {
		userID = fmt.Sprintf("%d", si.userID)
	}

	key := fmt.Sprintf("uploads/%s/%s%s", userID, uuid.New().String(), ext)

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

	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":         true,
		"upload_key": key,
		"name":       filename,
		"size":       len(data),
		"mime":       mimeType,
	})
}

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

func isBlockedMIME(mimeType string) bool {
	blocked := map[string]bool{
		"text/html":               true,
		"application/xhtml+xml":   true,
		"application/x-httpd-php": true,
	}
	return blocked[mimeType]
}
