package buildctx

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"

	"github.com/davecgh/go-spew/spew"
)

func ImportAll(dir string, compiler string, packages []string) ([]*Package, error) {
	pkgs, err := internalImportAll(dir, compiler, packages, true)
	if err != nil {
		return nil, err
	}

	paths := map[string]struct{}{}
	for _, pkg := range pkgs {
		paths[pkg.ImportPath] = struct{}{}
	}

	missing := map[string]struct{}{}
	for _, pkg := range pkgs {
		for _, path := range pkg.TestImports {
			if _, ok := paths[path]; !ok {
				missing[path] = struct{}{}
			}
		}
		for _, path := range pkg.XTestImports {
			if _, ok := paths[path]; !ok {
				missing[path] = struct{}{}
			}
		}
	}

	for len(missing) > 0 {
		missingPackages := []string{}
		for path := range missing {
			missingPackages = append(missingPackages, path)
		}

		newPkgs, err := internalImportAll(dir, compiler, missingPackages, false)
		if err != nil {
			return nil, err
		}

		for _, pkg := range newPkgs {
			if _, ok := paths[pkg.ImportPath]; !ok {
				paths[pkg.ImportPath] = struct{}{}
				pkgs = append(pkgs, pkg)
			}
		}

		missing = map[string]struct{}{}
		for _, pkg := range pkgs {
			for _, path := range pkg.Imports {
				if strings.Contains(path, "vendor/") {
					spew.Dump(pkg)
				}
				if _, ok := paths[path]; !ok {
					missing[path] = struct{}{}
				}
			}
		}
	}

	return pkgs, nil
}

func internalImportAll(dir string, compiler string, packages []string, test bool) ([]*Package, error) {
	if len(packages) == 0 {
		return nil, nil
	}

	testStr := "-deps"
	if test {
		//testStr = "-test"
	}

	stdout := &bytes.Buffer{}
	cmd := exec.Command("go", append([]string{"list", "-e", "-json", testStr, "-compiler", compiler, "--"}, packages...)...)
	cmd.Stdout = stdout
	cmd.Dir = dir
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	pkgs := []*Package{}
	decoder := json.NewDecoder(stdout)
	for decoder.More() {
		pkg := &Package{}
		err = decoder.Decode(pkg)
		if err != nil {
			return nil, err
		}
		if test {
			pkg.ImportPath = strings.Split(pkg.ImportPath, " ")[0]
		}
		if pkg.ImportMap != nil {
			inverted := map[string]string{}
			for orig, replaced := range pkg.ImportMap {
				inverted[replaced] = orig
			}
			for k, v := range pkg.Imports {
				if orig, ok := inverted[v]; ok {
					pkg.Imports[k] = orig
				}
			}
			for k, v := range pkg.TestImports {
				if orig, ok := inverted[v]; ok {
					pkg.TestImports[k] = orig
				}
			}
			for k, v := range pkg.XTestImports {
				if orig, ok := inverted[v]; ok {
					pkg.XTestImports[k] = orig
				}
			}
		}

		pkgs = append(pkgs, pkg)
	}

	return pkgs, nil
}
