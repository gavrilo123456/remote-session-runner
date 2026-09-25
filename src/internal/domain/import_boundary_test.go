package domain

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type p005ImportBoundary struct {
	importerRoot  string
	forbiddenRoot string
}

func p005ImportBoundaries(modulePath string) []p005ImportBoundary {
	internal := modulePath + "/src/internal/"
	domainRoot := internal + "domain"
	localAPIRoot := internal + "localapi"
	bridgeRoot := internal + "sshbridge"

	boundaries := make([]p005ImportBoundary, 0, 8)
	for _, forbidden := range []string{"httpsapi", "localapi", "mailbox", "sshbridge", "store", "runtime"} {
		boundaries = append(boundaries, p005ImportBoundary{
			importerRoot:  domainRoot,
			forbiddenRoot: internal + forbidden,
		})
	}
	boundaries = append(boundaries,
		p005ImportBoundary{importerRoot: localAPIRoot, forbiddenRoot: internal + "sshbridge"},
		p005ImportBoundary{importerRoot: localAPIRoot, forbiddenRoot: internal + "runtime"},
		p005ImportBoundary{importerRoot: bridgeRoot, forbiddenRoot: internal + "execution"},
	)
	return boundaries
}

func p005ImportGraph(repositoryRoot, modulePath string) (map[string][]string, error) {
	sourceRoot := filepath.Join(repositoryRoot, "src")
	graph := make(map[string][]string)

	err := filepath.WalkDir(sourceRoot, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(filePath) != ".go" || strings.HasSuffix(filePath, "_test.go") {
			return nil
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), filePath, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", filePath, err)
		}
		relativeDir, err := filepath.Rel(repositoryRoot, filepath.Dir(filePath))
		if err != nil {
			return fmt.Errorf("resolve package for %s: %w", filePath, err)
		}
		packagePath := modulePath + "/" + filepath.ToSlash(relativeDir)
		imports := graph[packagePath]
		for _, importSpec := range parsed.Imports {
			importPath, err := strconv.Unquote(importSpec.Path.Value)
			if err != nil {
				return fmt.Errorf("decode import in %s: %w", filePath, err)
			}
			if strings.HasPrefix(importPath, modulePath+"/") {
				imports = append(imports, importPath)
			}
		}
		graph[packagePath] = imports
		return nil
	})
	if err != nil {
		return nil, err
	}
	for packagePath := range graph {
		sort.Strings(graph[packagePath])
	}
	return graph, nil
}

func p005WithinPackageRoot(packagePath, root string) bool {
	return packagePath == root || strings.HasPrefix(packagePath, root+"/")
}

func p005PathToForbiddenImport(graph map[string][]string, start, forbiddenRoot string) []string {
	queue := [][]string{{start}}
	visited := map[string]bool{start: true}

	for head := 0; head < len(queue); head++ {
		currentPath := queue[head]
		current := currentPath[len(currentPath)-1]
		for _, imported := range graph[current] {
			path := append(append([]string(nil), currentPath...), imported)
			if p005WithinPackageRoot(imported, forbiddenRoot) {
				return path
			}
			if _, hasSource := graph[imported]; hasSource && !visited[imported] {
				visited[imported] = true
				queue = append(queue, path)
			}
		}
	}
	return nil
}

func p005ImportBoundaryViolations(graph map[string][]string, boundaries []p005ImportBoundary) []string {
	packages := make([]string, 0, len(graph))
	for packagePath := range graph {
		packages = append(packages, packagePath)
	}
	sort.Strings(packages)

	var violations []string
	for _, boundary := range boundaries {
		for _, packagePath := range packages {
			if !p005WithinPackageRoot(packagePath, boundary.importerRoot) {
				continue
			}
			if path := p005PathToForbiddenImport(graph, packagePath, boundary.forbiddenRoot); path != nil {
				violations = append(violations, fmt.Sprintf("%s must not depend on %s (path: %s)", boundary.importerRoot, boundary.forbiddenRoot, strings.Join(path, " -> ")))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func p005ModulePath(repositoryRoot string) (string, error) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\""), nil
		}
	}
	return "", fmt.Errorf("module directive not found in %s", filepath.Join(repositoryRoot, "go.mod"))
}

func p005RepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate repository go.mod")
		}
		directory = parent
	}
}

func TestP005ImportBoundaries(t *testing.T) {
	repositoryRoot := p005RepositoryRoot(t)
	modulePath, err := p005ModulePath(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := p005ImportGraph(repositoryRoot, modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if violations := p005ImportBoundaryViolations(graph, p005ImportBoundaries(modulePath)); len(violations) > 0 {
		t.Fatalf("D-12 import boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestP005ImportBoundaryScannerFindsNewPackages(t *testing.T) {
	repositoryRoot := t.TempDir()
	modulePath := "example.test/remote-session-runner"
	files := map[string]string{
		"src/internal/domain/future/identities.go": `package future
import _ "example.test/remote-session-runner/src/internal/execution/future"`,
		"src/internal/execution/future/shared.go": `package future
import _ "example.test/remote-session-runner/src/internal/runtime"`,
		"src/internal/localapi/future/client.go": `package future
import _ "example.test/remote-session-runner/src/internal/sshbridge"`,
		"src/internal/sshbridge/future/forwarder.go": `package future
import _ "example.test/remote-session-runner/src/internal/execution"`,
	}
	for name, contents := range files {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	graph, err := p005ImportGraph(repositoryRoot, modulePath)
	if err != nil {
		t.Fatal(err)
	}
	violations := p005ImportBoundaryViolations(graph, p005ImportBoundaries(modulePath))
	if len(violations) != 3 {
		t.Fatalf("found %d boundary violations, want 3: %v", len(violations), violations)
	}
}
