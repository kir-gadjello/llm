package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// FileContext represents a loaded file with metadata
type FileContext struct {
	Path      string
	Content   string
	IsBinary  bool
	IsImage   bool
	Type      string // file extension or detected type
	SizeBytes int64
}

// FileLoader handles safe file loading with binary detection
type FileLoader struct {
    maxFileSizeKB     int
    maxImageSizeKB    int
    verbose           bool
}

// NewFileLoader creates a new file loader with the given size limit
func NewFileLoader(maxFileSizeKB int, maxImageSizeKB int, verbose bool) *FileLoader {
    if maxFileSizeKB <= 0 {
        maxFileSizeKB = 1024 // default 1MB for non-image files
    }
    // Default image size to 10MB if not provided
    if maxImageSizeKB <= 0 {
        maxImageSizeKB = 10240 // 10 MB
    }
    return &FileLoader{
        maxFileSizeKB:   maxFileSizeKB,
        maxImageSizeKB:  maxImageSizeKB,
        verbose:         verbose,
    }
}

// isBinaryContent checks if content contains binary data
func isBinaryContent(data []byte) bool {
	// Check for NUL bytes (common in binary files)
	if bytes.IndexByte(data, 0) != -1 {
		return true
	}

	// Use http.DetectContentType for MIME type detection
	contentType := http.DetectContentType(data)

	// If it's explicitly an application type (except some text-like ones), consider binary
	if strings.HasPrefix(contentType, "application/") {
		// These are actually text
		textlike := []string{
			"application/json",
			"application/xml",
			"application/javascript",
		}
		for _, t := range textlike {
			if strings.HasPrefix(contentType, t) {
				return false
			}
		}
		return true
	}

	// Images, video, audio are binary
	if strings.HasPrefix(contentType, "image/") ||
		strings.HasPrefix(contentType, "video/") ||
		strings.HasPrefix(contentType, "audio/") {
		return true
	}

	return false
}

// classifyFileType returns a simple type classification based on extension
func classifyFileType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".jsx":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescript"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".txt":
		return "text"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".java":
		return "java"
	default:
		if ext != "" {
			return ext[1:] // return extension without dot
		}
		return "unknown"
	}
}

// ReadFile reads a single file with binary detection and size limiting
func (fl *FileLoader) ReadFile(path string) (*FileContext, error) {
	// Get absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %s: %w", path, err)
	}

	// Check if file exists
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", absPath)
	}

	// Skip directories
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory: %s", absPath)
	}

	ctx := &FileContext{
		Path:      absPath,
		Type:      classifyFileType(absPath),
		SizeBytes: info.Size(),
	}

    // Check size limit (use image-specific limit if the file is an image)
    // We need to detect early if this is likely an image; read header first and detect MIME.
    // To avoid duplicating detection logic, we read header now for binary/image detection.
    file, err := os.Open(absPath)
    if err != nil {
        return nil, fmt.Errorf("failed to open %s: %w", absPath, err)
    }
    defer file.Close()

    // Read first 512 bytes for MIME detection
    header := make([]byte, 512)
    n, err := file.Read(header)
    if err != nil && err != io.EOF {
        return nil, fmt.Errorf("failed to read header from %s: %w", absPath, err)
    }
    header = header[:n]

    // Detect MIME type
    mime := http.DetectContentType(header)
    isImage := strings.HasPrefix(mime, "image/")

    // Apply size limit
    var maxBytes int64
    if isImage {
        maxBytes = int64(fl.maxImageSizeKB) * 1024
        if info.Size() > maxBytes {
            return nil, fmt.Errorf("image too large: %s (%d KB exceeds limit %d KB)",
                absPath, info.Size()/1024, fl.maxImageSizeKB)
        }
    } else {
        maxBytes = int64(fl.maxFileSizeKB) * 1024
        if info.Size() > maxBytes {
            return nil, fmt.Errorf("file too large: %s (%d KB exceeds limit %d KB)",
                absPath, info.Size()/1024, fl.maxFileSizeKB)
        }
    }

	// Open file
    // Handle binary/image vs text
    if isBinaryContent(header) {
        if isImage {
            ctx.IsImage = true
            ctx.IsBinary = false

            // Read rest of file (continue from current offset)
            rest, err := io.ReadAll(file)
            if err != nil {
                return nil, fmt.Errorf("failed to read image %s: %w", absPath, err)
            }
            fullContent := append(header, rest...)

            b64 := base64.StdEncoding.EncodeToString(fullContent)
            ctx.Content = fmt.Sprintf("data:%s;base64,%s", mime, b64)
            return ctx, nil
        }

        // Other binary files are an error in strict mode
        return nil, fmt.Errorf("binary files not allowed: %s", absPath)
    }

    // Read rest of file (text)
    rest, err := io.ReadAll(file)
    if err != nil {
        return nil, fmt.Errorf("failed to read %s: %w", absPath, err)
    }

	// Combine header and rest
	fullContent := append(header, rest...)
	ctx.Content = string(fullContent)
	ctx.IsBinary = false

	return ctx, nil
}

// LoadAll loads multiple files, deduplicating paths
func (fl *FileLoader) LoadAll(paths []string) ([]FileContext, error) {
	// Deduplicate paths
	seen := make(map[string]bool)
	uniquePaths := make([]string, 0, len(paths))

	for _, p := range paths {
		// Resolve to absolute path for deduplication
		absPath, err := filepath.Abs(p)
		if err != nil {
			// Fail immediately on path resolution error
			return nil, fmt.Errorf("failed to resolve path %s: %w", p, err)
		}

		if !seen[absPath] {
			seen[absPath] = true
			uniquePaths = append(uniquePaths, absPath)
		}
	}

	// Load all files
	contexts := make([]FileContext, 0, len(uniquePaths))
	var loadErrors []string

	for _, path := range uniquePaths {
		ctx, err := fl.ReadFile(path)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("%s: %v", path, err))
			// Fail immediately on file read error
			return nil, fmt.Errorf("failed to load %s: %w", path, err)
		}
		contexts = append(contexts, *ctx)
	}

	return contexts, nil
}
