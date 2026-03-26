package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// startHTTPServer starts the HTTP server for large file transfers.
// Returns the actual listen port.
func startHTTPServer(addr, authToken, workspace string) (int, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/files", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuthToken(r, authToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			handleFileDownload(w, r, workspace)
		case http.MethodPost:
			handleFileUpload(w, r, workspace)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	server := &http.Server{Addr: addr, Handler: mux}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("HTTP listen: %w", err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
		}
	}()

	return actualPort, nil
}

func checkAuthToken(r *http.Request, expected string) bool {
	token := r.URL.Query().Get("token")
	if token != "" {
		return token == expected
	}
	return r.Header.Get("X-Auth-Token") == expected
}

func handleFileDownload(w http.ResponseWriter, r *http.Request, workspace string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	cleanPath, err := safePath(path, workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	f, err := os.Open(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	info, _ := f.Stat()
	if info != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

func handleFileUpload(w http.ResponseWriter, r *http.Request, workspace string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	cleanPath, err := safePath(path, workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
		http.Error(w, "create directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	f, err := os.Create(cleanPath)
	if err != nil {
		http.Error(w, "create file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, io.LimitReader(r.Body, 500*1024*1024)); err != nil {
		http.Error(w, "write file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
