package main

import (
	"go/ast"
	"strings"
)

func isTest(fd *ast.FuncDecl) bool {
	if !strings.HasPrefix(fd.Name.String(), "Test") {
		return false
	}
	if fd.Recv != nil {
		return false
	}
	if fd.Type.Results != nil {
		return false
	}
	params := fd.Type.Params.List
	if len(params) != 1 {
		return false
	}
	first := params[0]
	if len(first.Names) > 1 {
		return false
	}
	argExp, ok := first.Type.(*ast.StarExpr)
	if ok == false {
		return false
	}
	switch argType := argExp.X.(type) {
	case *ast.SelectorExpr:
		if argType.Sel.Name == "T" {
			return true
		}
	case *ast.Ident:
		if argType.Name == "T" {
			return true
		}
	}
	return false
}

func isBenchmark(fd *ast.FuncDecl) bool {
	if !strings.HasPrefix(fd.Name.String(), "Benchmark") {
		return false
	}
	if fd.Recv != nil {
		return false
	}
	if fd.Type.Results != nil {
		return false
	}
	params := fd.Type.Params.List
	if len(params) != 1 {
		return false
	}
	first := params[0]
	if len(first.Names) > 1 {
		return false
	}
	argExp, ok := first.Type.(*ast.StarExpr)
	if ok == false {
		return false
	}
	switch argType := argExp.X.(type) {
	case *ast.SelectorExpr:
		if argType.Sel.Name == "B" {
			return true
		}
	case *ast.Ident:
		if argType.Name == "B" {
			return true
		}
	}
	return false
}

func isTestMain(fd *ast.FuncDecl) bool {
	if fd.Name.String() != "TestMain" {
		return false
	}
	if fd.Recv != nil {
		return false
	}
	if fd.Type.Results != nil {
		return false
	}
	params := fd.Type.Params.List
	if len(params) != 1 {
		return false
	}
	first := params[0]
	if len(first.Names) > 1 {
		return false
	}
	argExp, ok := first.Type.(*ast.StarExpr)
	if ok == false {
		return false
	}
	switch argType := argExp.X.(type) {
	case *ast.SelectorExpr:
		if argType.Sel.Name == "M" {
			return true
		}
	case *ast.Ident:
		if argType.Name == "M" {
			return true
		}
	}
	return false
}

func isGopherTestInit(fd *ast.FuncDecl) bool {
	if fd.Name.String() != "GopherTestInit" {
		return false
	}
	if fd.Recv != nil {
		return false
	}
	if fd.Type.Results != nil {
		params := fd.Type.Results.List
		if len(params) != 0 {
			return false
		}
	}
	if fd.Type.Params != nil {
		params := fd.Type.Params.List
		if len(params) != 0 {
			return false
		}
	}
	return true
}
