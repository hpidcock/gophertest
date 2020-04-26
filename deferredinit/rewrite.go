package deferredinit

import (
	"fmt"
	"go/ast"
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

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"

	"github.com/go-toolsmith/astcopy"
)

type initAssign struct {
	order     int
	statement ast.Stmt
	srcFile   *ast.File
}

type RewritePkg struct {
	Path           string
	TestImports    []string
	AddedTestFiles []string
}

func LoadPackages(dir string, importPaths []string) (map[string][]*packages.Package, error) {
	config := &packages.Config{
		Mode: packages.NeedTypesInfo |
			packages.NeedTypes |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedSyntax |
			packages.NeedName,
		Tests: true,
		Dir:   dir,
	}

	pkgs, err := packages.Load(config, importPaths...)
	if err != nil {
		return nil, err
	}

	result := map[string][]*packages.Package{}
	for _, pkg := range pkgs {
		if !strings.HasSuffix(pkg.ID, ".test]") {
			continue
		}
		importPath := strings.TrimSuffix(pkg.PkgPath, "_test")
		pkgResult := result[importPath]
		pkgResult = append(pkgResult, pkg)
		result[importPath] = pkgResult
	}

	return result, nil
}

func RewriteTestPackages(dir string, pkgs []*packages.Package, outDir string) ([]RewritePkg, error) {
	transformed := []RewritePkg{}
	for _, pkg := range pkgs {
		testPackage := false
		for _, f := range pkg.Syntax {
			// Skip non-test packages
			file := pkg.Fset.File(f.Package)
			if strings.HasSuffix(file.Name(), "_test.go") {
				testPackage = true
			}
		}
		if !testPackage {
			continue
		}

		newFiles, testImports, err := transformPkg(pkg, pkg.Fset, outDir)
		if err != nil {
			return nil, err
		}

		transformed = append(transformed, RewritePkg{
			Path:           pkg.PkgPath,
			TestImports:    testImports,
			AddedTestFiles: newFiles,
		})
	}
	return transformed, nil
}

func transformPkg(pkg *packages.Package, fset *token.FileSet, outDir string) ([]string, []string, error) {
	assignments := make([]initAssign, 0, len(pkg.TypesInfo.InitOrder))

	identToOrder := map[*ast.Ident]int{}
	for i, v := range pkg.TypesInfo.InitOrder {
		for _, lhs := range v.Lhs {
			for _, f := range pkg.Syntax {
				astutil.Apply(f, func(c *astutil.Cursor) bool {
					if c.Node() != nil {
						if ident, ok := c.Node().(*ast.Ident); ok {
							if lhs.Pos() == ident.Pos() {
								identToOrder[ident] = i
							}
						}
					}
					return true
				}, nil)
			}
		}
	}

	var testImports = map[string]struct{}{}
	var newFiles []string
	for _, f := range pkg.Syntax {
		file := pkg.Fset.File(f.Package)
		if !strings.HasSuffix(file.Name(), "_test.go") {
			of, err := os.OpenFile(path.Join(outDir, path.Base(file.Name())), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err != nil {
				return nil, nil, err
			}
			err = format.Node(of, fset, f)
			if err != nil {
				return nil, nil, err
			}
			err = of.Close()
			if err != nil {
				return nil, nil, err
			}
			continue
		}

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
					newName := nextInitName()
					c.Replace(&ast.FuncDecl{
						Name: &ast.Ident{
							Name: newName,
						},
						Body: n.Body,
						Doc:  n.Doc,
						Recv: n.Recv,
						Type: n.Type,
					})
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
							expr = resolveType(tv, pkg, fset, f, fset.Position(value.Pos()))
						}
						if expr != nil {
							c.Replace(&ast.ValueSpec{
								Names:   n.Names,
								Comment: n.Comment,
								Doc:     n.Doc,
								Type:    expr,
								Values:  nil,
							})

							funcName := nextInitName()
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
						}
					}
				}
				// Don't decend deeper than root/file
				return false
			},
			nil,
		)

		f.Decls = append(f.Decls, addedDecls...)

		imports := astutil.Imports(fset, f)
		for _, para := range imports {
			for _, imp := range para {
				importPath, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return nil, nil, err
				}
				testImports[importPath] = struct{}{}
			}
		}

		of, err := os.OpenFile(path.Join(outDir, path.Base(file.Name())), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, err
		}
		err = format.Node(of, fset, f)
		if err != nil {
			log.Printf("failed to format %s for %s", path.Base(file.Name()), pkg.PkgPath)
			return nil, nil, err
		}
		err = of.Close()
		if err != nil {
			return nil, nil, err
		}
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
		imports := astutil.Imports(fset, g)
		for _, para := range imports {
			for _, imp := range para {
				importPath, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return nil, nil, err
				}
				testImports[importPath] = struct{}{}
			}
		}

		of, err := os.OpenFile(path.Join(outDir, newFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, err
		}
		err = format.Node(of, fset, g)
		if err != nil {
			return nil, nil, err
		}
		err = of.Close()
		if err != nil {
			return nil, nil, err
		}

		newFiles = append(newFiles, newFile)
	}

	var uniqueTestImports []string
	for k := range testImports {
		uniqueTestImports = append(uniqueTestImports, k)
	}

	return newFiles, uniqueTestImports, nil
}

func resolveType(decl types.Type, pkg *packages.Package, fset *token.FileSet, f *ast.File, pos token.Position) ast.Expr {
	switch t := decl.(type) {
	case *types.Basic:
		return &ast.Ident{
			Name: t.Name(),
		}
	case *types.Named:
		objPkg := t.Obj().Pkg()
		if objPkg != nil {
			if objPkg.Path() == pkg.PkgPath {
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

		name := findOrAddImport(pkg, fset, f, objPkg.Path())
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
			X: resolveType(t.Elem(), pkg, fset, f, pos),
		}
	case *types.Slice:
		return &ast.ArrayType{
			Elt: resolveType(t.Elem(), pkg, fset, f, pos),
		}
	case *types.Array:
		return &ast.ArrayType{
			Elt: resolveType(t.Elem(), pkg, fset, f, pos),
			Len: &ast.BasicLit{
				Kind:  token.INT,
				Value: strconv.FormatInt(t.Len(), 10),
			},
		}
	case *types.Map:
		return &ast.MapType{
			Key:   resolveType(t.Key(), pkg, fset, f, pos),
			Value: resolveType(t.Elem(), pkg, fset, f, pos),
		}
	case *types.Chan:
		v := &ast.ChanType{
			Value: resolveType(t.Elem(), pkg, fset, f, pos),
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
				Type: resolveType(fn.Type(), pkg, fset, f, pos),
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
				Type:  resolveType(field.Type(), pkg, fset, f, pos),
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
						Elt: resolveType(elem, pkg, fset, f, pos),
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
					Type: resolveType(e.Type(), pkg, fset, f, pos),
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
				Type: resolveType(e.Type(), pkg, fset, f, pos),
			})
		}
		return v
	default:
		log.Fatalf("unhandled @ %s %#v", pos, decl)
	}
	return nil
}

var uniqueImportSuffix = 0
var uniqueImportMutex sync.Mutex

func findOrAddImport(pkg *packages.Package, fset *token.FileSet, f *ast.File, path string) string {
	imports := astutil.Imports(fset, f)
	for _, para := range imports {
		for _, imp := range para {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				panic(err)
			}
			if importPath == path {
				if imp.Name != nil {
					return imp.Name.Name
				}
				if importPkg, ok := pkg.Imports[importPath]; ok {
					return importPkg.Name
				}
			}
		}
	}

	uniqueImportMutex.Lock()
	suffix := uniqueImportSuffix
	uniqueImportSuffix++
	uniqueImportMutex.Unlock()

	name := fmt.Sprintf("gopherTestImport%d", suffix)
	astutil.AddNamedImport(fset, f, name, path)
	return name
}

var uniqueFuncSuffix = 0
var uniqueFuncMutex sync.Mutex

func nextInitName() string {
	uniqueFuncMutex.Lock()
	suffix := uniqueFuncSuffix
	uniqueFuncSuffix++
	uniqueFuncMutex.Unlock()

	return fmt.Sprintf("gopherTestInit%d", suffix)
}

func pathOfImport(pkg *packages.Package, fset *token.FileSet, f *ast.File, name string) string {
	imports := astutil.Imports(fset, f)
	for _, para := range imports {
		for _, imp := range para {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				panic(err)
			}
			if imp.Name != nil {
				if imp.Name.Name != name {
					continue
				}
			} else if importPkg, ok := pkg.Imports[path]; !ok || importPkg.Name != name {
				continue
			}
			return path
		}
	}
	return ""
}

func rewriteStatement(pkg *packages.Package, fset *token.FileSet, srcFile *ast.File, destFile *ast.File, stmt *ast.AssignStmt) *ast.AssignStmt {
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
				path := pathOfImport(pkg, fset, srcFile, ident.Name)
				if path != "" {
					n.X = &ast.Ident{
						Name: findOrAddImport(pkg, fset, destFile, path),
					}
					return false
				}
			}
		}
		return true
	}, nil)
	return copy
}
