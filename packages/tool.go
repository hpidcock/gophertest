package packages

import (
	"bytes"
	"encoding/json"
	"go/build"
	"os/exec"
)

func ImportAll(buildCtx build.Context, dir string, packages []string) ([]*Package, error) {
	testPackage := map[string]struct{}{}
	for _, pkg := range packages {
		testPackage[pkg] = struct{}{}
	}

	pkgs, err := internalImportAll(buildCtx, dir, packages, true)
	if err != nil {
		return nil, err
	}

	paths := map[string]struct{}{}
	for _, pkg := range pkgs {
		paths[pkg.Dir] = struct{}{}
	}

	missing := map[string]struct{}{}
	for _, pkg := range pkgs {
		if _, ok := testPackage[pkg.ImportPath]; !ok {
			continue
		}
		for _, path := range pkg.Imports {
			if _, ok := paths[path]; !ok {
				missing[path] = struct{}{}
			}
		}
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

	missingPackages := []string{}
	for pkg := range missing {
		missingPackages = append(missingPackages, pkg)
	}

	newPkgs, err := internalImportAll(buildCtx, dir, missingPackages, false)
	if err != nil {
		return nil, err
	}

	for _, pkg := range newPkgs {
		if _, ok := paths[pkg.Dir]; !ok {
			paths[pkg.Dir] = struct{}{}
			pkgs = append(pkgs, pkg)
		}
	}

	return pkgs, nil
}

func internalImportAll(buildCtx build.Context, dir string, packages []string, test bool) ([]*Package, error) {
	if len(packages) == 0 {
		return nil, nil
	}

	args := []string{"list", "-e", "-json", "-compiler", buildCtx.Compiler}
	if !test {
		args = append(args, "-deps")
	}

	stdout := &bytes.Buffer{}
	cmd := exec.Command("go", append(append(args, "--"), packages...)...)
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
		pkgs = append(pkgs, pkg)
	}

	return pkgs, nil
}
