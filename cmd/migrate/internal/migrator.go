package migrate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Migrator struct {
	DryRun bool
	Dir    string
	Config Config
}

func (m *Migrator) updateFileImports(filePath string) error {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parsing %q: %w", filePath, err)
	}

	var fileChanged bool

	ast.Inspect(astFile, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			val := strings.Trim(x.Path.Value, `"`)
			// we take the first matching prefix, so you need to make sure you don't have ambiguous mappings
			for from, to := range m.Config.ImportPaths {
				if strings.HasPrefix(val, from) {
					suffix := strings.TrimPrefix(val, from)
					newVal := to + suffix
					fmt.Printf("changing %s => %s in %s\n", x.Path.Value, newVal, filePath)
					if !m.DryRun {
						x.Path.Value = fmt.Sprintf(`"%s"`, newVal)
						fileChanged = true
					}
				}
			}
		}
		return true
	})

	if !fileChanged {
		return nil
	}

	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	err = format.Node(f, fset, astFile)
	if err != nil {
		f.Close()
		return fmt.Errorf("formatting %q: %w", filePath, err)
	}
	err = f.Close()
	if err != nil {
		return fmt.Errorf("closing %q: %w", filePath, err)
	}

	return nil
}

func (m *Migrator) run(cmdName string, args ...string) (int, string, string, error) {
	cmd := exec.Command(cmdName, args...)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Dir = m.Dir
	err := cmd.Start()
	if err != nil {
		return 0, "", "", fmt.Errorf("running %s %v: %w", cmdName, args, err)
	}
	state, err := cmd.Process.Wait()
	if err != nil {
		return 0, "", "", fmt.Errorf("waiting for %s %v: %w", cmdName, args, err)
	}
	return state.ExitCode(), strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), nil
}

func (m *Migrator) currentModule() (string, error) {
	exitCode, stdout, stderr, err := m.run("go", "list", "-m")
	if err != nil {
		return "", fmt.Errorf("finding current module: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("non-zero exit code %d finding current module, stderr:\n%s\n", exitCode, stderr)
	}
	return strings.TrimSpace(stdout), nil

}

// FindMigratedDependencies returns a list of dependent module versions like 'module v0.1.0' that have been migrated to go-libipfs.
func (m *Migrator) FindMigratedDependencies() ([]string, error) {
	var modVersions []string
	for _, mod := range m.Config.Modules {
		exitCode, stdout, stderr, err := m.run("go", "list", "-m", mod)
		if err != nil {
			return nil, err
		}
		if exitCode == 0 {
			scanner := bufio.NewScanner(strings.NewReader(stdout))
			for scanner.Scan() {
				modVersions = append(modVersions, scanner.Text())
			}
		} else {
			if !strings.Contains(stderr, "not a known dependency") {
				return nil, fmt.Errorf("non-zero exit code %d finding if current module depends on %q, stderr:\n%s\n", exitCode, mod, stderr)
			}
		}
	}
	return modVersions, nil
}

func (m *Migrator) findSourceFiles() ([]string, error) {
	exitCode, stdout, stderr, err := m.run("go", "list", "-json", "./...")
	if err != nil {
		return nil, fmt.Errorf("finding source files: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("non-zero exit code %d finding source files, stderr:\n%s\n", exitCode, stderr)
	}

	var files []string
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var pkg pkgJSON
		err = dec.Decode(&pkg)
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decoding 'go list' JSON: %w", err)
		}
		files = append(files, pkg.allSourceFiles()...)
	}
}

// UpdateImports rewrites the imports of the current module for any import paths that have been migrated to go-libipfs.
func (m *Migrator) UpdateImports() error {
	sourceFiles, err := m.findSourceFiles()
	if err != nil {
		return err
	}
	for _, sourceFile := range sourceFiles {
		err := m.updateFileImports(sourceFile)
		if err != nil {
			return fmt.Errorf("updating imports in %q: %w", sourceFile, err)
		}
	}
	return nil
}