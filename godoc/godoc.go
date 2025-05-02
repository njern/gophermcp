// Package godoc provides a tool for getting Go documentation for a package, type, function, or method.
// Adapted from https://github.com/mrjoshuak/godoc-mcp
// Licensed under the MIT license (see original LICENSE file in this directory)
package godoc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	mcp "github.com/metoro-io/mcp-golang"
)

const tempDirPattern = "gophermcp-*"

var (
	mutex    sync.RWMutex
	tempDirs []string
)

const ToolDescription = `
Get Go documentation for a package, type, function, or method. 
This is the preferred and most efficient way to understand Go packages, providing official package documentation in a concise format. 
Use this before attempting to read source files directly.
This tool is a great way to avoid hallucinations. If you generate code and are seeing errors about missing packages, try using this tool to verify that the package or symbol within the package exists.

Best Practices:
1. ALWAYS try this tool first before reading package source code.
2. Start with basic package documentation before looking at source code or specific symbols.
3. Use the show_all_documentation option when you need comprehensive package documentation.
4. Only look up specific symbols after understanding the package overview.

Common Usage Patterns:
- Standard library: Use just the package name (e.g., "io", "net/http")
- External packages: Use full import path (e.g., "github.com/user/repo")
- Local packages: Use relative path (e.g., "./pkg") or absolute path

The documentation is cached for 5 minutes to improve performance.`

type Arguments struct {
	Path                 string `json:"path" jsonschema:"required,description=Path to the Go package or file. This can be an import path (e.g., 'io', 'github.com/user/repo') or a local file path."`
	Target               string `json:"target" jsonschema:"description=Specific symbol to get documentation for (e.g., function name, type name, interface name). Leave empty to get full package documentation."`
	ShowAllDocumentation bool   `json:"show_all_documentation" jsonschema:"description=Show all documentation for the package in the response"`
	IncludeSource        bool   `json:"include_source" jsonschema:"description=Include the source code in the response"`
	IncludeUnexported    bool   `json:"include_unexported" jsonschema:"description=Include unexported symbols in the response"`
	WorkingDir           string `json:"working_dir" jsonschema:"description=Working directory to execute go doc from. Required for relative paths (including '.') to resolve the correct module context. Optional for absolute paths and standard library packages."`
}

// Tool implements the godoc tool.
func Tool(ctx context.Context, arguments Arguments) (*mcp.ToolResponse, error) {
	// Check working directory
	if wd := arguments.WorkingDir; wd != "" {
		if info, err := os.Stat(wd); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("invalid working directory: %w", err)
		}
	}

	// Validate and resolve the path
	resolvedPath, subDirs, err := validatePath(arguments.Path, arguments.WorkingDir)
	if err != nil {
		if subDirs != nil {
			// Return a special response indicating available subdirectories
			return mcp.NewToolResponse(mcp.NewTextContent(fmt.Sprintf("No Go files found in %s, but found Go packages in the following subdirectories: %v", arguments.Path, subDirs))), nil
		}

		return nil, fmt.Errorf("failed to validate path: %w", err)
	}

	// Use the resolved path for documentation
	arguments.Path = resolvedPath

	// Create temporary project if needed
	if arguments.WorkingDir == "" {
		var err error
		arguments.WorkingDir, err = createTempProject(arguments.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary project: %w", err)
		}
	}

	// Build command arguments
	var cmdArgs []string

	if arguments.ShowAllDocumentation {
		cmdArgs = append(cmdArgs, "-all")
	}

	if arguments.IncludeSource {
		cmdArgs = append(cmdArgs, "-src")
	}

	if arguments.IncludeUnexported {
		cmdArgs = append(cmdArgs, "-u")
	}

	// Add the path
	cmdArgs = append(cmdArgs, arguments.Path)

	// Add specific target if provided
	if target := arguments.Target; target != "" {
		cmdArgs = append(cmdArgs, target)
	}

	// Run go doc command with working directory
	doc, err := runGoDoc(arguments.WorkingDir, cmdArgs...)
	if err != nil {
		return nil, fmt.Errorf("error running go doc: %w", err)
	}

	// Create the result with just the documentation
	return mcp.NewToolResponse(mcp.NewTextContent(doc)), nil
}

// createTempProject creates a temporary Go project with the given package
func createTempProject(pkgPath string) (string, error) {
	// For standard library packages, create a minimal temp project
	if isStandardLibrary := !strings.Contains(pkgPath, "."); isStandardLibrary {
		tempDir, err := os.MkdirTemp("", tempDirPattern)
		if err != nil {
			return "", fmt.Errorf("failed to create temp directory: %w", err)
		}

		mutex.Lock()
		tempDirs = append(tempDirs, tempDir)
		mutex.Unlock()

		// Initialize minimal go.mod for std lib access
		cmd := exec.Command("go", "mod", "init", "gophermcp-temp")
		cmd.Dir = tempDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to initialize go.mod: %w\noutput: %s", err, out)
		}

		return tempDir, nil
	}

	// For local paths, try to find the module root first
	if strings.HasPrefix(pkgPath, ".") || strings.HasPrefix(pkgPath, "/") || filepath.IsAbs(pkgPath) {
		absPath, err := filepath.Abs(pkgPath)
		if err == nil {
			// Look for go.mod in current or parent directories
			dir := absPath
			for dir != "/" {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					// Verify this module contains our target package
					if info, err := os.Stat(absPath); err == nil {
						if info.IsDir() || strings.HasSuffix(absPath, ".go") {
							return dir, nil // Found valid module root
						}
					}
				}
				dir = filepath.Dir(dir)
			}
		}
	}

	// For non-local paths or if no module root found, create temp project
	tempDir, err := os.MkdirTemp("", tempDirPattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}

	mutex.Lock()
	tempDirs = append(tempDirs, tempDir)
	mutex.Unlock()

	// Initialize go.mod
	cmd := exec.Command("go", "mod", "init", "gophermcp-temp")
	cmd.Dir = tempDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to initialize go.mod: %w\noutput: %s", err, out)
	}

	// For remote packages, try to get the package
	if pkgPath != "" && !strings.HasPrefix(pkgPath, ".") && !strings.HasPrefix(pkgPath, "/") && !filepath.IsAbs(pkgPath) {
		cmd = exec.Command("go", "get", pkgPath)
		cmd.Dir = tempDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to get package %s: %w\noutput: %s", pkgPath, err, out)
		}
	}

	return tempDir, nil
}

// Cleanup removes all temporary directories
func Cleanup() {
	mutex.Lock()
	defer mutex.Unlock()

	for _, dir := range tempDirs {
		os.RemoveAll(dir)
	}

	tempDirs = nil
}

// runGoDoc executes the go doc command with the given arguments and optional working directory
func runGoDoc(workingDir string, args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"doc"}, args...)...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		// Enhanced error handling with suggestions
		errStr := string(out)
		if strings.Contains(errStr, "no such package") || strings.Contains(errStr, "is not in std") {
			return "", fmt.Errorf("package not found. Suggestions:\n"+
				"1. For standard library packages, use just the package name (e.g., 'io', 'net/http')\n"+
				"2. For external packages, ensure they are imported in the module\n"+
				"3. For local packages, provide the relative path (e.g., './pkg') or absolute path\n"+
				"4. Check for typos in the package name\n"+
				"Error details: %s", errStr)
		}
		if strings.Contains(errStr, "no such symbol") {
			return "", fmt.Errorf("symbol not found. Suggestions:\n"+
				"1. Check if the symbol name is correct (case-sensitive)\n"+
				"2. Use -u flag to see unexported symbols\n"+
				"3. Use -all flag to see all package documentation\n"+
				"Error: %w", err)
		}
		if strings.Contains(errStr, "build constraints exclude all Go files") {
			return "", fmt.Errorf("no Go files found for current platform. Suggestions:\n"+
				"1. Try using -all flag to see all package files\n"+
				"2. Check if you need to set GOOS/GOARCH environment variables\n"+
				"Error: %w", err)
		}
		return "", fmt.Errorf("go doc error: %w\noutput: %s\nTip: Use -h flag to see all available options", err, errStr)
	}

	content := string(out)
	return content, nil
}

// validatePath ensures the path is either a valid local file/directory or appears to be a valid import path
func validatePath(path string, workingDir string) (string, []string, error) {
	// For relative paths, working directory is required
	if strings.HasPrefix(path, ".") {
		if workingDir == "" {
			return "", nil, fmt.Errorf("working_dir is required for relative paths (including '.')")
		}

		// Read go.mod from working directory only
		modPath := filepath.Join(workingDir, "go.mod")
		content, err := os.ReadFile(modPath)
		if err != nil {
			return "", nil, fmt.Errorf("failed to read go.mod in working directory: %w", err)
		}

		// Parse module name from go.mod
		var moduleName string
		for line := range strings.SplitSeq(string(content), "\n") {
			if strings.HasPrefix(line, "module ") {
				moduleName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
				break
			}
		}
		if moduleName == "" {
			return "", nil, fmt.Errorf("no module declaration found in go.mod")
		}

		// If path is ".", use the module name directly
		if path == "." {
			return moduleName, nil, nil
		}

		// For other relative paths, append to module name
		relPath := strings.TrimPrefix(path, "./")
		return filepath.Join(moduleName, relPath), nil, nil
	}

	// Handle absolute paths - must match working directory if provided
	if strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		if workingDir != "" && path != workingDir {
			return "", nil, fmt.Errorf("absolute path must match working directory when provided")
		}

		// Read go.mod from the absolute path
		modPath := filepath.Join(path, "go.mod")
		content, err := os.ReadFile(modPath)
		if err != nil {
			return "", nil, fmt.Errorf("failed to read go.mod: %w", err)
		}

		// Parse module name from go.mod
		var moduleName string
		for line := range strings.SplitSeq(string(content), "\n") {
			if strings.HasPrefix(line, "module ") {
				moduleName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
				return moduleName, nil, nil
			}
		}
		return "", nil, fmt.Errorf("no module declaration found in go.mod")
	}

	// For all other paths, treat as import path
	return path, nil, nil
}
