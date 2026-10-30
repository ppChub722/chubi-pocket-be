package apierr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// declaredCodes parses codes.go: every `Name Code = "VALUE"` constant.
func declaredCodes(t *testing.T) map[string]Code {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "codes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Code{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Code" {
				continue
			}
			for i, n := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s: a Code constant must be a string literal", n.Name)
				}
				v, _ := strconv.Unquote(lit.Value)
				out[n.Name] = Code(v)
			}
		}
	}
	return out
}

func TestRegistryIsConsistent(t *testing.T) {
	consts := declaredCodes(t)
	if len(consts) != len(registry) {
		t.Errorf("%d Code constants but %d registry rows", len(consts), len(registry))
	}
	inConsts := map[Code]bool{}
	for name, c := range consts {
		inConsts[c] = true
		if _, ok := Lookup(c); !ok {
			t.Errorf("constant %s (%s) is not in the registry", name, c)
		}
	}
	seen := map[Code]bool{}
	for _, d := range registry {
		if seen[d.Code] {
			t.Errorf("%s is registered twice", d.Code)
		}
		seen[d.Code] = true
		if !inConsts[d.Code] {
			t.Errorf("%s has no constant", d.Code)
		}
		if strings.TrimSpace(d.Desc) == "" {
			t.Errorf("%s has no description", d.Code)
		}
		if d.Use == 0 {
			t.Errorf("%s has no Use", d.Code)
		}
		if (d.Use&InEnvelope != 0) != (len(d.Status) > 0) {
			t.Errorf("%s: an envelope code needs its HTTP status(es), other codes none", d.Code)
		}
		for _, s := range d.Status {
			if s < 400 || s > 599 {
				t.Errorf("%s: status %d is not an error status", d.Code, s)
			}
		}
		up := strings.ToUpper(string(d.Code))
		if up != string(d.Code) || strings.ContainsAny(string(d.Code), " -") {
			t.Errorf("%s is not UPPER_SNAKE", d.Code)
		}
	}
}

var httpStatus = map[string]int{
	"StatusBadRequest": http.StatusBadRequest, "StatusUnauthorized": http.StatusUnauthorized,
	"StatusForbidden": http.StatusForbidden, "StatusNotFound": http.StatusNotFound,
	"StatusConflict": http.StatusConflict, "StatusGone": http.StatusGone,
	"StatusUnprocessableEntity": http.StatusUnprocessableEntity,
	"StatusUpgradeRequired":     http.StatusUpgradeRequired,
	"StatusTooManyRequests":     http.StatusTooManyRequests,
	"StatusInternalServerError": http.StatusInternalServerError,
	"StatusBadGateway":          http.StatusBadGateway,
	"StatusServiceUnavailable":  http.StatusServiceUnavailable,
	"StatusGatewayTimeout":      http.StatusGatewayTimeout,
}

// TestEveryReturnedCodeIsRegistered scans the server code (non-test files under internal/ and
// cmd/): every error code it can return must be an apierr constant that is registered for that
// place (and, in the envelope, for that HTTP status). It also bans hand-written error JSON.
func TestEveryReturnedCodeIsRegistered(t *testing.T) {
	consts := declaredCodes(t)
	root := filepath.Join("..", "..", "..")
	fset := token.NewFileSet()
	type file struct {
		path string
		f    *ast.File
	}
	pkgs := map[string][]file{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			pkgs[filepath.Dir(p)] = append(pkgs[filepath.Dir(p)], file{p, f})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// codeOf resolves a code expression: apierr.X, or a package constant declared as apierr.X.
	codeOf := func(e ast.Expr, aliases map[string]string) (Code, bool) {
		if id, ok := e.(*ast.Ident); ok {
			if target, ok := aliases[id.Name]; ok {
				c, ok := consts[target]
				return c, ok
			}
			return "", false
		}
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "apierr" {
			return "", false
		}
		c, ok := consts[sel.Sel.Name]
		return c, ok
	}

	calls := 0
	for _, files := range pkgs {
		aliases := map[string]string{} // const Name = apierr.X
		for _, fl := range files {
			for _, d := range fl.f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, s := range gd.Specs {
					vs := s.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if sel, ok := vs.Values[i].(*ast.SelectorExpr); ok {
								if x, ok := sel.X.(*ast.Ident); ok && x.Name == "apierr" {
									aliases[n.Name] = sel.Sel.Name
								}
							}
						}
					}
				}
			}
		}
		for _, fl := range files {
			if strings.Contains(filepath.ToSlash(fl.path), "platform/apierr/") {
				continue
			}
			// A registered code written as a string literal (an argument or a composite element)
			// bypasses the registry: use the constant.
			strayLit := func(e ast.Expr) {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					e = kv.Value
				}
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return
				}
				v, _ := strconv.Unquote(lit.Value)
				if _, ok := Lookup(Code(v)); ok {
					t.Errorf("%s: %s written as a string literal; use the apierr constant", fset.Position(lit.Pos()), v)
				}
			}
			ast.Inspect(fl.f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if fn := calleeName(x); strings.Contains(fn, "Env") {
						break // getEnv("OCR_TIMEOUT", ...): an env var name, not a code
					}
					for _, a := range x.Args {
						strayLit(a)
					}
				case *ast.CompositeLit:
					for _, el := range x.Elts {
						strayLit(el)
					}
				}
				switch x := n.(type) {
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					recv, ok := sel.X.(*ast.Ident)
					if !ok || recv.Name != "response" {
						return true
					}
					pos := fset.Position(x.Pos())
					var status int
					var codeArg ast.Expr
					switch sel.Sel.Name {
					case "Fail":
						codeArg = x.Args[2]
						st, ok := x.Args[1].(*ast.SelectorExpr)
						if !ok || httpStatus[st.Sel.Name] == 0 {
							t.Errorf("%s: response.Fail needs a constant http.StatusX", pos)
							return true
						}
						status = httpStatus[st.Sel.Name]
					case "BadRequest":
						codeArg, status = x.Args[1], http.StatusBadRequest
					case "NotFound":
						codeArg, status = x.Args[1], http.StatusNotFound
					default:
						return true
					}
					calls++
					code, ok := codeOf(codeArg, aliases)
					if !ok {
						t.Errorf("%s: the code must be a registered apierr constant", pos)
						return true
					}
					d, ok := Lookup(code)
					if !ok || d.Use&InEnvelope == 0 {
						t.Errorf("%s: %s is not registered as an envelope code", pos, code)
						return true
					}
					if !d.AllowsStatus(status) {
						t.Errorf("%s: %s is registered with %v, not %d", pos, code, d.Status, status)
					}
					if code == ValidationError {
						t.Errorf("%s: use response.Validation / ValidationErr / BindError for VALIDATION_ERROR", pos)
					}
				case *ast.CompositeLit:
					typeName := ""
					switch tt := x.Type.(type) {
					case *ast.Ident:
						typeName = tt.Name
					case *ast.SelectorExpr:
						typeName = tt.Sel.Name
					}
					pos := fset.Position(x.Pos())
					var use Use
					switch typeName {
					case "submitErr", "SubmitError":
						use = InPendingLastError
					case "DatasetError":
						use = InSyncDatasetError
					case "H":
						for _, el := range x.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if lit, ok := kv.Key.(*ast.BasicLit); ok && (lit.Value == `"error"` || lit.Value == `"code"`) {
									t.Errorf("%s: hand-written error JSON; use the response package", pos)
								}
							}
						}
						return true
					default:
						return true
					}
					for i, el := range x.Elts {
						var v ast.Expr
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); !ok || !strings.EqualFold(k.Name, "code") {
								continue
							}
							v = kv.Value
						} else if i == 0 {
							v = el
						} else {
							continue
						}
						if sel, ok := v.(*ast.SelectorExpr); ok {
							if r, ok := sel.X.(*ast.Ident); ok && r.Name != "apierr" {
								continue // a field copied from another value (se.code, c.code)
							}
						}
						if id, ok := v.(*ast.Ident); ok && aliases[id.Name] == "" {
							continue // a variable
						}
						code, ok := codeOf(v, aliases)
						if !ok {
							t.Errorf("%s: %s code must be a registered apierr constant", pos, typeName)
							continue
						}
						if d, _ := Lookup(code); d.Use&use == 0 {
							t.Errorf("%s: %s is not registered for %s", pos, code, typeName)
						}
					}
				}
				return true
			})
		}
	}
	if calls < 200 {
		t.Errorf("only %d response calls found: is the scan looking at the right tree?", calls)
	}
}

func TestCodesByUse(t *testing.T) {
	if len(Codes(InEnvelope)) == 0 || len(Codes(InPendingLastError)) == 0 || len(Codes(InSyncDatasetError)) != 1 {
		t.Fatalf("envelope %d, pending %d, sync %d", len(Codes(InEnvelope)), len(Codes(InPendingLastError)), len(Codes(InSyncDatasetError)))
	}
}

func calleeName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
