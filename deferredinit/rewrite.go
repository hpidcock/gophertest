package deferredinit

import (
	"context"
	"fmt"
	"go/ast"
	gobuild "go/build"
	"go/format"
	"go/token"
	"go/types"
	"log"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"

	"github.com/go-toolsmith/astcopy"
	"github.com/gophertest/build"
	"github.com/hpidcock/gophertest/dag"
	"github.com/pkg/errors"
)

type Logger interface {
	Infof(format string, args ...interface{})
}

type initAssign struct {
	order     int
	statement ast.Stmt
	srcFile   *ast.File
}

type DeferredIniter struct {
	Logger   Logger
	BuildCtx gobuild.Context
	Tools    build.Tools

	WorkDir   string
	SourceDir string

	mutex        sync.Mutex
	testPackages map[string]*packages.Package
	nodes        map[string]*dag.Node
}

func (d *DeferredIniter) CollectPackages(ctx context.Context, node *dag.Node) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if d.nodes == nil {
		d.nodes = make(map[string]*dag.Node)
	}
	d.nodes[node.ImportPath] = node
	if node.Shlib != "" {
		return nil
	}
	if !node.Tests {
		return nil
	}
	if d.testPackages == nil {
		d.testPackages = make(map[string]*packages.Package)
	}
	if _, ok := d.testPackages[node.ImportPath]; ok {
		return fmt.Errorf("package %q already collected", node.ImportPath)
	}
	d.testPackages[node.ImportPath] = nil
	return nil
}

func (d *DeferredIniter) LoadPackages() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	dedupe := map[string]struct{}{}
	importPaths := []string(nil)
	for k := range d.testPackages {
		importPath := strings.TrimSuffix(k, "_test")
		if _, ok := dedupe[importPath]; ok {
			continue
		}
		dedupe[importPath] = struct{}{}
		importPaths = append(importPaths, importPath)
	}

	config := &packages.Config{
		Mode: packages.NeedTypesInfo |
			packages.NeedTypes |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedSyntax |
			packages.NeedName,
		Tests: true,
		Dir:   d.SourceDir,
	}

	pkgs, err := packages.Load(config, importPaths...)
	if err != nil {
		return errors.WithStack(err)
	}
	for _, pkg := range pkgs {
		if !strings.HasSuffix(pkg.ID, ".test]") {
			continue
		}
		if oldPkg, ok := d.testPackages[pkg.PkgPath]; !ok {
			continue
		} else if oldPkg != nil {
			return fmt.Errorf("package %q already loaded", pkg.PkgPath)
		}
		d.testPackages[pkg.PkgPath] = pkg
	}

	return nil
}

func (d *DeferredIniter) Rewrite(ctx context.Context, node *dag.Node) error {
	if !node.Tests {
		return nil
	}
	if node.Shlib != "" {
		return nil
	}

	d.Logger.Infof("rewrite %q", node.ImportPath)

	pkg, ok := d.testPackages[node.ImportPath]
	if !ok || pkg == nil {
		return fmt.Errorf("package %q missing", node.ImportPath)
	}

	missingTests := true
	for _, f := range pkg.Syntax {
		// Skip non-test packages
		file := pkg.Fset.File(f.Package)
		if strings.HasSuffix(file.Name(), "_test.go") {
			missingTests = false
		}
	}
	if missingTests {
		return fmt.Errorf("package %q missing test files", node.ImportPath)
	}

	outDir := path.Join(d.WorkDir, "rewrite", node.ImportPath)
	err := os.MkdirAll(outDir, 0777)
	if err != nil {
		return errors.WithStack(err)
	}

	node.Mutex.Unlock()
	newFiles, testImports, err := d.transformPkg(ctx, pkg, outDir)
	node.Mutex.Lock()
	if err != nil {
		return errors.WithStack(err)
	}

	// Patch file paths for changed files. Add missing ones.
	filesToAdd := map[string]struct{}{}
	for _, v := range newFiles {
		filesToAdd[v] = struct{}{}
	}
	for k, goFile := range node.GoFiles {
		if _, ok := filesToAdd[goFile.Filename]; !ok {
			continue
		}
		delete(filesToAdd, goFile.Filename)
		goFile.Dir = outDir
		node.GoFiles[k] = goFile
	}
	for filename := range filesToAdd {
		goFile := dag.GoFile{
			Dir:      outDir,
			Filename: filename,
			Test:     strings.HasSuffix(filename, "_test.go"),
		}
		node.GoFiles = append(node.GoFiles, goFile)
	}

	importsToAdd := map[string]struct{}{}
	for _, v := range testImports {
		if v == node.ImportPath {
			// Never try to import ourselves.
			continue
		}
		importsToAdd[v] = struct{}{}
	}
	for _, imp := range node.Imports {
		delete(importsToAdd, imp.ImportPath)
	}

	for importPath := range importsToAdd {
		imp, ok := d.nodes[importPath]
		if !ok || imp == nil {
			return fmt.Errorf("could not find import %q", importPath)
		}
		imp.Mutex.Lock()
		imp.Deps = append(imp.Deps, node)
		node.Imports = append(node.Imports, dag.Import{
			Node: imp,
			Test: true,
		})
		imp.Mutex.Unlock()
	}

	return nil
}

type transformState struct {
	Pkg    *packages.Package
	OutDir string

	uniqueFuncSuffix   int
	uniqueFuncMutex    sync.Mutex
	uniqueImportSuffix int
	uniqueImportMutex  sync.Mutex
}

func (s *transformState) NextInitName() string {
	s.uniqueFuncMutex.Lock()
	suffix := s.uniqueFuncSuffix
	s.uniqueFuncSuffix++
	s.uniqueFuncMutex.Unlock()
	return fmt.Sprintf("gopherTestInit%d", suffix)
}

func (s *transformState) NextImportName() string {
	s.uniqueImportMutex.Lock()
	suffix := s.uniqueImportSuffix
	s.uniqueImportSuffix++
	s.uniqueImportMutex.Unlock()
	return fmt.Sprintf("gopherTestImport%d", suffix)
}

type fileTransformState struct {
	*transformState

	imports map[string]string
	names   map[string]string
}

func (d *DeferredIniter) transformPkg(ctx context.Context, pkg *packages.Package, outDir string) ([]string, []string, error) {
	state := &transformState{
		Pkg:    pkg,
		OutDir: outDir,
	}

	mut := sync.Mutex{}
	assignments := make([]initAssign, 0, len(pkg.TypesInfo.InitOrder))
	testImports := map[string]struct{}{}
	newFiles := []string(nil)

	posToOrder := map[token.Pos]int{}
	for i, v := range pkg.TypesInfo.InitOrder {
		for _, lhs := range v.Lhs {
			posToOrder[lhs.Pos()] = i
		}
	}

	identToOrder := map[*ast.Ident]int{}
	for _, f := range pkg.Syntax {
		astutil.Apply(f, func(c *astutil.Cursor) bool {
			if c.Node() == nil {
				return true
			}
			switch t := c.Node().(type) {
			case *ast.Ident:
				if order, ok := posToOrder[t.Pos()]; ok {
					identToOrder[t] = order
				}
			case *ast.FuncDecl:
				return false
			case *ast.StructType:
				return false
			case *ast.InterfaceType:
				return false
			}
			return true
		}, nil)
	}

	slots := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		slots <- struct{}{}
	}
	eg, _ := errgroup.WithContext(ctx)
	for _, f := range pkg.Syntax {
		f := f
		file := pkg.Fset.File(f.Package)
		if !strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}

		fstate := &fileTransformState{
			transformState: state,
		}

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-slots:
		}

		eg.Go(func() error {
			defer func() {
				slots <- struct{}{}
			}()
			addedDecls := []ast.Decl{}
			astutil.Apply(f,
				func(c *astutil.Cursor) bool {
					if c.Node() == f {
						// Descend into file
						return true
					}
					if c.Node() == nil {
						// Descend from root
						return true
					}
					switch n := c.Node().(type) {
					case *ast.FuncDecl:
						if n.Name.Name != "init" {
							break
						}
						if n.Recv != nil {
							break
						}
						if n.Type.Params != nil && len(n.Type.Params.List) > 0 {
							break
						}
						if n.Type.Results != nil && len(n.Type.Results.List) > 0 {
							break
						}
						newName := state.NextInitName()
						c.Replace(&ast.FuncDecl{
							Name: &ast.Ident{
								Name: newName,
							},
							Body: n.Body,
							Doc:  n.Doc,
							Recv: n.Recv,
							Type: n.Type,
						})
						mut.Lock()
						assignments = append(assignments, initAssign{
							order:   math.MaxInt64,
							srcFile: f,
							statement: &ast.ExprStmt{
								X: &ast.CallExpr{
									Fun: &ast.Ident{
										Name: newName,
									},
								},
							},
						})
						mut.Unlock()
					case *ast.GenDecl:
						if n.Tok == token.VAR {
							return true
						}
					case *ast.ValueSpec:
						initIndex := math.MaxInt64
						for _, name := range n.Names {
							if index, ok := identToOrder[name]; ok {
								if index < initIndex {
									initIndex = index
								}
							}
						}
						assignment := &ast.AssignStmt{
							Tok: token.ASSIGN,
						}
						for _, lhs := range n.Names {
							assignment.Lhs = append(assignment.Lhs, lhs)
						}
						for _, rhs := range n.Values {
							assignment.Rhs = append(assignment.Rhs, rhs)
						}
						if n.Values != nil {
							value := n.Values[0]
							ti := pkg.TypesInfo
							expr := n.Type
							if expr == nil {
								tv := ti.TypeOf(value)
								expr = resolveType(fstate, tv, f, value.Pos())
							}
							if expr != nil {
								c.Replace(&ast.ValueSpec{
									Names:   n.Names,
									Comment: n.Comment,
									Doc:     n.Doc,
									Type:    expr,
									Values:  nil,
								})

								funcName := state.NextInitName()
								fn := &ast.FuncDecl{
									Name: &ast.Ident{
										Name: funcName,
									},
									Body: &ast.BlockStmt{
										List: []ast.Stmt{
											assignment,
										},
									},
									Type: &ast.FuncType{
										Params:  &ast.FieldList{},
										Results: &ast.FieldList{},
									},
								}
								addedDecls = append(addedDecls, fn)
								mut.Lock()
								assignments = append(assignments, initAssign{
									order:   initIndex,
									srcFile: f,
									statement: &ast.ExprStmt{
										X: &ast.CallExpr{
											Fun: &ast.Ident{
												Name: funcName,
											},
										},
									},
								})
								mut.Unlock()
							}
						}
					}
					// Don't decend deeper than root/file
					return false
				},
				nil,
			)

			if len(assignments) == 0 && len(addedDecls) == 0 {
				return nil
			}

			f.Decls = append(f.Decls, addedDecls...)

			imports := astutil.Imports(pkg.Fset, f)
			for _, para := range imports {
				for _, imp := range para {
					importPath, err := strconv.Unquote(imp.Path.Value)
					if err != nil {
						return errors.WithMessagef(err, "transforming file %q", file.Name())
					}
					mut.Lock()
					testImports[importPath] = struct{}{}
					mut.Unlock()
				}
			}

			newFile := path.Base(file.Name())
			of, err := os.OpenFile(path.Join(outDir, newFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
			if err != nil {
				return errors.WithMessagef(err, "transforming file %q", file.Name())
			}
			err = format.Node(of, pkg.Fset, f)
			if err != nil {
				of.Close()
				log.Printf("failed to format %s for %s", newFile, pkg.PkgPath)
				return errors.WithMessagef(err, "transforming file %q", file.Name())
			}
			err = of.Close()
			if err != nil {
				return errors.WithMessagef(err, "transforming file %q", file.Name())
			}

			mut.Lock()
			newFiles = append(newFiles, newFile)
			mut.Unlock()
			return nil
		})
	}

	err := eg.Wait()
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	if len(assignments) > 0 {
		sort.Slice(assignments, func(i, j int) bool {
			return assignments[i].order < assignments[j].order
		})

		g := &ast.File{
			Name: &ast.Ident{
				Name: pkg.Name,
			},
		}
		block := &ast.BlockStmt{}
		for _, v := range assignments {
			block.List = append(block.List, v.statement)
		}

		fn := &ast.FuncDecl{
			Name: &ast.Ident{
				Name: "GopherTestInit",
			},
			Body: block,
			Type: &ast.FuncType{
				Params:  &ast.FieldList{},
				Results: &ast.FieldList{},
			},
		}
		g.Decls = append(g.Decls, fn)

		newFile := fmt.Sprintf("gophertest_generated_%s_test.go", pkg.Name)
		imports := astutil.Imports(pkg.Fset, g)
		for _, para := range imports {
			for _, imp := range para {
				importPath, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return nil, nil, err
				}
				testImports[importPath] = struct{}{}
			}
		}

		of, err := os.OpenFile(path.Join(outDir, newFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}
		err = format.Node(of, pkg.Fset, g)
		if err != nil {
			of.Close()
			return nil, nil, errors.WithStack(err)
		}
		err = of.Close()
		if err != nil {
			return nil, nil, errors.WithStack(err)
		}

		newFiles = append(newFiles, newFile)
	}

	var uniqueTestImports []string
	for k := range testImports {
		uniqueTestImports = append(uniqueTestImports, k)
	}

	return newFiles, uniqueTestImports, nil
}

func resolveType(state *fileTransformState, decl types.Type, f *ast.File, pos token.Pos) ast.Expr {
	switch t := decl.(type) {
	case *types.Basic:
		return &ast.Ident{
			Name: t.Name(),
		}
	case *types.Named:
		objPkg := t.Obj().Pkg()
		if objPkg != nil {
			if objPkg.Path() == state.Pkg.PkgPath {
				return &ast.Ident{
					Name: t.Obj().Name(),
					Obj: &ast.Object{
						Kind: ast.Typ,
						Name: t.Obj().Name(),
					},
				}
			}
		} else {
			// Probably a builtin.
			return &ast.Ident{
				Name: t.Obj().Name(),
			}
		}

		name := findOrAddImport(state, f, objPkg.Path())
		return &ast.SelectorExpr{
			X: &ast.Ident{
				Name: name,
			},
			Sel: &ast.Ident{
				Name: t.Obj().Name(),
			},
		}
	case *types.Pointer:
		return &ast.StarExpr{
			X: resolveType(state, t.Elem(), f, pos),
		}
	case *types.Slice:
		return &ast.ArrayType{
			Elt: resolveType(state, t.Elem(), f, pos),
		}
	case *types.Array:
		return &ast.ArrayType{
			Elt: resolveType(state, t.Elem(), f, pos),
			Len: &ast.BasicLit{
				Kind:  token.INT,
				Value: strconv.FormatInt(t.Len(), 10),
			},
		}
	case *types.Map:
		return &ast.MapType{
			Key:   resolveType(state, t.Key(), f, pos),
			Value: resolveType(state, t.Elem(), f, pos),
		}
	case *types.Chan:
		v := &ast.ChanType{
			Value: resolveType(state, t.Elem(), f, pos),
		}
		switch t.Dir() {
		case types.SendRecv:
			v.Dir = ast.SEND | ast.RECV
		case types.SendOnly:
			v.Dir = ast.SEND
		case types.RecvOnly:
			v.Dir = ast.RECV
		}
		return v
	case *types.Interface:
		v := &ast.InterfaceType{
			Methods: &ast.FieldList{},
		}
		for i := 0; i < t.NumMethods(); i++ {
			fn := t.Method(i)
			v.Methods.List = append(v.Methods.List, &ast.Field{
				Names: []*ast.Ident{{
					Name: fn.Name(),
				}},
				Type: resolveType(state, fn.Type(), f, pos),
			})
		}
		return v
	case *types.Struct:
		v := &ast.StructType{
			Fields: &ast.FieldList{},
		}
		for i := 0; i < t.NumFields(); i++ {
			field := t.Field(i)
			names := []*ast.Ident{{
				Name: field.Name(),
			}}
			if field.Embedded() {
				names = nil
			}
			v.Fields.List = append(v.Fields.List, &ast.Field{
				Names: names,
				Type:  resolveType(state, field.Type(), f, pos),
			})
		}
		return v
	case *types.Signature:
		v := &ast.FuncType{
			Params:  &ast.FieldList{},
			Results: &ast.FieldList{},
		}
		params := t.Params()
		for i := 0; i < params.Len(); i++ {
			e := params.At(i)
			if t.Variadic() && i+1 == params.Len() {
				var elem types.Type
				switch collectionType := e.Type().(type) {
				case *types.Array:
					elem = collectionType.Elem()
				case *types.Slice:
					elem = collectionType.Elem()
				default:
					log.Fatalf("invalid type for %#v", e.Type())
				}
				name := e.Name()
				if name == "" {
					name = "_"
				}
				v.Params.List = append(v.Params.List, &ast.Field{
					Names: []*ast.Ident{{
						Name: name,
					}},
					Type: &ast.Ellipsis{
						Elt: resolveType(state, elem, f, pos),
					},
				})
			} else {
				name := e.Name()
				if name == "" {
					name = "_"
				}
				v.Params.List = append(v.Params.List, &ast.Field{
					Names: []*ast.Ident{{
						Name: name,
					}},
					Type: resolveType(state, e.Type(), f, pos),
				})
			}
		}
		results := t.Results()
		for i := 0; i < results.Len(); i++ {
			e := results.At(i)
			v.Results.List = append(v.Results.List, &ast.Field{
				Names: []*ast.Ident{{
					Name: e.Name(),
				}},
				Type: resolveType(state, e.Type(), f, pos),
			})
		}
		return v
	default:
		log.Fatalf("unhandled @ %s %#v", state.Pkg.Fset.Position(pos), decl)
	}
	return nil
}

func maybeLoadImports(state *fileTransformState, f *ast.File) {
	if state.imports == nil {
		state.imports = map[string]string{}
		state.names = map[string]string{}
		imports := astutil.Imports(state.Pkg.Fset, f)
		for _, para := range imports {
			for _, imp := range para {
				importPath, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					panic(err)
				}
				name := ""
				if imp.Name != nil {
					name = imp.Name.Name
				} else if importPkg, ok := state.Pkg.Imports[importPath]; ok {
					name = importPkg.Name
				}
				if name != "" {
					state.imports[importPath] = name
					state.names[name] = importPath
				}
			}
		}
	}
}

func findOrAddImport(state *fileTransformState, f *ast.File, path string) string {
	maybeLoadImports(state, f)
	if name, ok := state.imports[path]; ok {
		return name
	}
	name := state.NextImportName()
	astutil.AddNamedImport(state.Pkg.Fset, f, name, path)
	state.imports[path] = name
	state.names[name] = path
	return name
}

func pathOfImport(state *fileTransformState, f *ast.File, name string) string {
	maybeLoadImports(state, f)
	return state.names[name]
}

func rewriteStatement(state *fileTransformState, srcFile *ast.File, destFile *ast.File, stmt *ast.AssignStmt) *ast.AssignStmt {
	copy := astcopy.AssignStmt(stmt)
	astutil.Apply(copy, func(c *astutil.Cursor) bool {
		node := c.Node()
		if node != nil {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				if _, ok := c.Parent().(*ast.SelectorExpr); ok {
					// Only deal with root X which is either a package or a variable.
					return false
				}
				ident, ok := n.X.(*ast.Ident)
				if !ok {
					return true
				}
				path := pathOfImport(state, srcFile, ident.Name)
				if path != "" {
					n.X = &ast.Ident{
						Name: findOrAddImport(state, destFile, path),
					}
					return false
				}
			}
		}
		return true
	}, nil)
	return copy
}
