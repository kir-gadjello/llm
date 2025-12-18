package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// SymbolType defines the category of the code entity.
type SymbolType string

const (
	SymbolFunction SymbolType = "function"
	SymbolMethod   SymbolType = "method"
	SymbolClass    SymbolType = "class"
	SymbolTypeDef  SymbolType = "type"
	SymbolVariable SymbolType = "variable"
)

// Symbol represents a high-level code entity found in a file.
type Symbol struct {
	Name      string     `json:"name"`
	Type      SymbolType `json:"type"`
	Signature string     `json:"signature"` // The clean definition line
	Line      int        `json:"line"`      // 1-based line number
}

// FileSkeleton represents the "Map" of a single file.
type FileSkeleton struct {
	Path    string   `json:"path"`
	Symbols []Symbol `json:"symbols"`
}

// String returns a compact, LLM-friendly representation of the skeleton.
func (fs *FileSkeleton) String() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<file path=\"%s\">\n", fs.Path))
	for _, s := range fs.Symbols {
		b.WriteString(fmt.Sprintf("%s\n", s.Signature))
	}
	b.WriteString("</file>\n")
	return b.String()
}

// Skeletonizer is the service responsible for parsing files.
type Skeletonizer struct {
	mu        sync.Mutex
	parsers   map[string]*sitter.Parser
	queries   map[string]*sitter.Query
	languages map[string]*sitter.Language
}

// NewSkeletonizer initializes the parser pool.
func NewSkeletonizer() *Skeletonizer {
	return &Skeletonizer{
		parsers:   make(map[string]*sitter.Parser),
		queries:   make(map[string]*sitter.Query),
		languages: initLanguages(),
	}
}

// Skeletonize parses the given content and returns its structural skeleton.
func (s *Skeletonizer) Skeletonize(ctx context.Context, path string, content []byte) (*FileSkeleton, error) {
	ext := filepath.Ext(path)
	lang, ok := s.languages[ext]
	if !ok {
		return nil, fmt.Errorf("unsupported language extension: %s", ext)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Lazy load parser for this language
	parser, ok := s.parsers[ext]
	if !ok {
		parser = sitter.NewParser()
		parser.SetLanguage(lang)
		s.parsers[ext] = parser
	}

	// Lazy load query for this language
	query, ok := s.queries[ext]
	if !ok {
		qStr := getQueryForExt(ext)
		q, err := sitter.NewQuery([]byte(qStr), lang)
		if err != nil {
			return nil, fmt.Errorf("invalid query for %s: %w", ext, err)
		}
		s.queries[ext] = q
		query = q
	}

	// Parse
	tree, err := parser.ParseCtx(ctx, nil, content)
	if err != nil {
		return nil, fmt.Errorf("parsing failed: %w", err)
	}
	defer tree.Close()

	root := tree.RootNode()
	cursor := sitter.NewQueryCursor()
	defer cursor.Close()

	cursor.Exec(query, root)

	skeleton := &FileSkeleton{
		Path:    path,
		Symbols: make([]Symbol, 0),
	}

	// Iterate over matches
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}

		var sym Symbol
		var node *sitter.Node

		for _, c := range match.Captures {
			captureName := query.CaptureNameForId(c.Index)

			if captureName == "def" {
				node = c.Node
				sym.Line = int(node.StartPoint().Row) + 1
				sym.Type = SymbolFunction
			}
			if captureName == "name" {
				sym.Name = c.Node.Content(content)
			}
			if captureName == "type_tag" {
				sym.Type = SymbolType(c.Node.Content(content))
			}
		}

		if node != nil && sym.Name != "" {
			sym.Signature = cleanSignature(content, node)
			skeleton.Symbols = append(skeleton.Symbols, sym)
		}
	}

	return skeleton, nil
}

// cleanSignature extracts the definition line but strips the body to save tokens.
func cleanSignature(content []byte, node *sitter.Node) string {
	startByte := node.StartByte()
	endByte := node.EndByte()

	if startByte >= uint32(len(content)) || endByte > uint32(len(content)) {
		return ""
	}

	raw := content[startByte:endByte]

	// Split by newline to get just the header
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	if scanner.Scan() {
		line := scanner.Text()
		// If it contains a brace, cut it there
		if idx := strings.Index(line, "{"); idx != -1 {
			line = line[:idx]
		}
		return strings.TrimSpace(line)
	}

	return strings.TrimSpace(string(raw))
}

func initLanguages() map[string]*sitter.Language {
	return map[string]*sitter.Language{
		".go":  golang.GetLanguage(),
		".py":  python.GetLanguage(),
		".js":  javascript.GetLanguage(),
		".jsx": javascript.GetLanguage(),
		".ts":  typescript.GetLanguage(),
		".tsx": typescript.GetLanguage(),
	}
}

func getQueryForExt(ext string) string {
	switch ext {
	case ".go":
		return `
(function_declaration name: (identifier) @name) @def
(method_declaration name: (field_identifier) @name) @def
(type_declaration (type_spec name: (type_identifier) @name)) @def
`
	case ".py":
		return `
(function_definition name: (identifier) @name) @def
(class_definition name: (identifier) @name) @def
`
	case ".ts", ".tsx":
		return `
(function_declaration name: (identifier) @name) @def
(class_declaration name: (type_identifier) @name) @def
(interface_declaration name: (type_identifier) @name) @def
(variable_declarator 
    name: (identifier) @name 
    value: [(arrow_function) (function_expression)]
) @def
`
	case ".js", ".jsx":
		return `
(function_declaration name: (identifier) @name) @def
(class_declaration name: (identifier) @name) @def
(variable_declarator 
    name: (identifier) @name 
    value: [(arrow_function) (function_expression)]
) @def
`
	default:
		return ""
	}
}

// RepoIndexer generates repository maps
type RepoIndexer struct {
	skeletonizer *Skeletonizer
	ignoredDirs  []string
	maxFiles     int
	verbose      bool
}

// NewRepoIndexer creates a new repo indexer
func NewRepoIndexer(ignoredDirs []string, maxFiles int, verbose bool) *RepoIndexer {
	if maxFiles <= 0 {
		maxFiles = 1000
	}
	if len(ignoredDirs) == 0 {
		ignoredDirs = []string{".git", "node_modules", "dist", "vendor", "__pycache__"}
	}
	return &RepoIndexer{
		skeletonizer: NewSkeletonizer(),
		ignoredDirs:  ignoredDirs,
		maxFiles:     maxFiles,
		verbose:      verbose,
	}
}

// shouldIgnore checks if a directory should be ignored
func (ri *RepoIndexer) shouldIgnore(name string) bool {
	for _, ignore := range ri.ignoredDirs {
		if name == ignore {
			return true
		}
	}
	return false
}

// getFirstLines reads the first N lines from a file
func getFirstLines(path string, n int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for i := 0; i < n && scanner.Scan(); i++ {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return strings.Join(lines, "\n"), nil
}

// GenerateRepoMap creates a structural map of the repository
func (ri *RepoIndexer) GenerateRepoMap(root string) (string, error) {
	var builder strings.Builder
	fileCount := 0

	// Supported code extensions
	codeExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	}

	// Text file extensions to show headers for
	textExts := map[string]bool{
		".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true,
		".toml": true, ".ini": true, ".conf": true,
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if ri.verbose {
				fmt.Fprintf(os.Stderr, "Warning: error accessing %s: %v\n", path, err)
			}
			return nil
		}

		// Skip ignored directories
		if info.IsDir() {
			if ri.shouldIgnore(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		// Check file count limit
		if fileCount >= ri.maxFiles {
			return fmt.Errorf("reached max file limit (%d)", ri.maxFiles)
		}

		ext := filepath.Ext(path)
		relPath, _ := filepath.Rel(root, path)

		// Handle code files with skeletonizer
		if codeExts[ext] {
			// Check size limit (1MB hard limit for indexer to avoid OOM)
			info, err := os.Stat(path)
			if err != nil {
				return nil
			}
			if info.Size() > 1024*1024 {
				if ri.verbose {
					fmt.Fprintf(os.Stderr, "Skipping large file > 1MB: %s\n", path)
				}
				return nil
			}

			content, err := os.ReadFile(path)
			if err != nil {
				if ri.verbose {
					fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", path, err)
				}
				return nil
			}

			skeleton, err := ri.skeletonizer.Skeletonize(context.Background(), relPath, content)
			if err != nil {
				if ri.verbose {
					fmt.Fprintf(os.Stderr, "Warning: cannot parse %s: %v\n", path, err)
				}
				return nil
			}

			if len(skeleton.Symbols) > 0 {
				builder.WriteString(skeleton.String())
				fileCount++
			}
			return nil
		}

		// Handle text files with headers
		if textExts[ext] {
			header, err := getFirstLines(path, 5)
			if err != nil {
				if ri.verbose {
					fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", path, err)
				}
				return nil
			}

			builder.WriteString(fmt.Sprintf("<file path=\"%s\">\n", relPath))
			for _, line := range strings.Split(header, "\n") {
				builder.WriteString(fmt.Sprintf("  %s\n", line))
			}
			builder.WriteString("</file>\n")
			fileCount++
			return nil
		}

		// For other files, just list the name
		// (optional, can be commented out to only show code/text files)
		// builder.WriteString(fmt.Sprintf("%s\n", relPath))

		return nil
	})

	if err != nil {
		// If we hit the file limit, that's okay
		if strings.Contains(err.Error(), "reached max file limit") {
			if ri.verbose {
				fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
			}
		} else {
			return "", fmt.Errorf("failed to walk directory: %w", err)
		}
	}

	return builder.String(), nil
}
