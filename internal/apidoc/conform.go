package apidoc

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	docOnce sync.Once
	docMap  map[string]any
	docErr  error
)

func loadedDoc() (map[string]any, error) {
	docOnce.Do(func() { docMap, docErr = Document() })
	return docMap, docErr
}

func Response(operationID string, status int, body []byte) error {
	op, err := findOp(operationID)
	if err != nil {
		return err
	}
	resp := lookupMap(op, "responses", strconv.Itoa(status))
	if resp == nil {
		return fmt.Errorf("%s: the spec has no %d response", operationID, status)
	}
	if ref, ok := resp["$ref"].(string); ok {
		r, err := resolve(ref)
		if err != nil {
			return err
		}
		resp, _ = r.(map[string]any)
	}
	schema := lookupMap(resp, "content", "application/json", "schema")
	if schema == nil {
		return nil // no JSON body described
	}
	return check(operationID+" "+strconv.Itoa(status), schema, body)
}

func Request(operationID string, body []byte) error {
	op, err := findOp(operationID)
	if err != nil {
		return err
	}
	schema := lookupMap(op, "requestBody", "content", "application/json", "schema")
	if schema == nil {
		return fmt.Errorf("%s: the spec has no JSON request body", operationID)
	}
	return check(operationID+" request", schema, body)
}

func check(label string, schema map[string]any, body []byte) error {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("%s: body is not JSON: %w", label, err)
	}
	var errs []string
	validate(schema, v, "$", &errs)
	if len(errs) == 0 {
		return nil
	}
	if len(errs) > 12 {
		errs = append(errs[:12], fmt.Sprintf("… and %d more", len(errs)-12))
	}
	return fmt.Errorf("%s does not match the spec:\n  %s", label, strings.Join(errs, "\n  "))
}

func findOp(id string) (map[string]any, error) {
	doc, err := loadedDoc()
	if err != nil {
		return nil, err
	}
	for _, item := range doc["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			if !httpMethods[method] {
				continue
			}
			op := raw.(map[string]any)
			if op["operationId"] == id {
				return op, nil
			}
		}
	}
	return nil, fmt.Errorf("no operationId %q in the spec", id)
}

func lookupMap(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func resolve(ref string) (any, error) {
	doc, err := loadedDoc()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("bad $ref %s", ref)
	}
	var cur any = doc
	for _, p := range strings.Split(ref[2:], "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolved $ref %s", ref)
		}
		if cur, ok = m[p]; !ok {
			return nil, fmt.Errorf("unresolved $ref %s", ref)
		}
	}
	return cur, nil
}

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	dtRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
)

func flatten(s map[string]any) (map[string]any, error) {
	if ref, ok := s["$ref"].(string); ok {
		r, err := resolve(ref)
		if err != nil {
			return nil, err
		}
		rm, _ := r.(map[string]any)
		return flatten(rm)
	}
	all, ok := s["allOf"].([]any)
	if !ok {
		return s, nil
	}
	merged := map[string]any{}
	for k, v := range s {
		if k != "allOf" {
			merged[k] = v
		}
	}
	props := map[string]any{}
	var required []any
	for _, part := range all {
		pm, _ := part.(map[string]any)
		f, err := flatten(pm)
		if err != nil {
			return nil, err
		}
		if ps, ok := f["properties"].(map[string]any); ok {
			for k, v := range ps {
				props[k] = v
			}
		}
		if rq, ok := f["required"].([]any); ok {
			required = append(required, rq...)
		}
		if t, ok := f["type"]; ok && merged["type"] == nil {
			merged["type"] = t
		}
		if ap, ok := f["additionalProperties"]; ok {
			merged["additionalProperties"] = ap
		}
	}
	if ps, ok := merged["properties"].(map[string]any); ok {
		for k, v := range ps {
			props[k] = v
		}
	}
	merged["properties"] = props
	merged["required"] = required
	return merged, nil
}

func typesOf(s map[string]any) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func typeOK(want []string, v any) bool {
	got := jsonKind(v)
	for _, w := range want {
		if w == got || (w == "number" && got == "integer") {
			return true
		}
	}
	return false
}

func validate(schema map[string]any, v any, path string, errs *[]string) {
	s, err := flatten(schema)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: %v", path, err))
		return
	}
	if one, ok := s["oneOf"].([]any); ok {
		matches := 0
		for _, alt := range one {
			am, _ := alt.(map[string]any)
			var sub []string
			validate(am, v, path, &sub)
			if len(sub) == 0 {
				matches++
			}
		}
		if matches != 1 {
			*errs = append(*errs, fmt.Sprintf("%s: matches %d of %d oneOf alternatives (value %s)", path, matches, len(one), short(v)))
		}
		return
	}
	if types := typesOf(s); len(types) > 0 && !typeOK(types, v) {
		*errs = append(*errs, fmt.Sprintf("%s: is %s, spec says %s", path, jsonKind(v), strings.Join(types, "|")))
		return
	}
	if c, ok := s["const"]; ok && fmt.Sprint(c) != fmt.Sprint(v) {
		*errs = append(*errs, fmt.Sprintf("%s: %s, spec says const %v", path, short(v), c))
	}
	if en, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range en {
			if fmt.Sprint(e) == fmt.Sprint(v) || (e == nil && v == nil) {
				found = true
			}
		}
		if !found {
			*errs = append(*errs, fmt.Sprintf("%s: %s is not in the enum %v", path, short(v), en))
		}
	}
	switch x := v.(type) {
	case string:
		switch s["format"] {
		case "uuid":
			if !uuidRe.MatchString(x) {
				*errs = append(*errs, fmt.Sprintf("%s: %q is not a uuid", path, x))
			}
		case "date":
			if !dateRe.MatchString(x) {
				*errs = append(*errs, fmt.Sprintf("%s: %q is not a date", path, x))
			}
		case "date-time":
			if !dtRe.MatchString(x) {
				*errs = append(*errs, fmt.Sprintf("%s: %q is not a date-time", path, x))
			}
		}
	case []any:
		if n, ok := num(s["minItems"]); ok && len(x) < n {
			*errs = append(*errs, fmt.Sprintf("%s: %d items, spec says at least %d", path, len(x), n))
		}
		if n, ok := num(s["maxItems"]); ok && len(x) > n {
			*errs = append(*errs, fmt.Sprintf("%s: %d items, spec says at most %d", path, len(x), n))
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, el := range x {
				validate(items, el, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, has := x[r.(string)]; !has {
					*errs = append(*errs, fmt.Sprintf("%s: missing required key %q", path, r))
				}
			}
		}
		open := s["additionalProperties"] == true
		var extra []string
		for k, el := range x {
			ps, known := props[k].(map[string]any)
			if !known {
				if !open && props != nil {
					extra = append(extra, k)
				}
				continue
			}
			validate(ps, el, path+"."+k, errs)
		}
		sort.Strings(extra)
		for _, k := range extra {
			*errs = append(*errs, fmt.Sprintf("%s: key %q is not in the spec", path, k))
		}
	}
}

func num(v any) (int, bool) {
	f, ok := v.(float64)
	return int(f), ok
}

func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 60 {
		return string(b[:60]) + "…"
	}
	return string(b)
}
