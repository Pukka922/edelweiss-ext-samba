package jobs_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// uiTexts is what the UI words for the jobs: kinds.<kind> for every kind of a job and steps.<label> for every label of a
// step. ui/src/test/job-texts.json lists them.
type uiTexts struct {
	Kinds  []string `json:"kinds"`
	Labels []string `json:"labels"`
}

const (
	uiTextsFile = "../../ui/src/test/job-texts.json"
	localesDir  = "../../ui/public/locales"
)

// planners are the packages that name the kinds and the labels of the steps.
var planners = []string{"../actions", "../setup"}

var (
	labelName = regexp.MustCompile(`^label[A-Z]`)
	kindName  = regexp.MustCompile(`^kind[A-Z]`)
)

// parsed are the non-test files of the package in dir.
func parsed(t *testing.T, dir string) []*ast.File {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s: %v", dir, err)
	}
	var out []*ast.File
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		out = append(out, f)
	}
	return out
}

// stringConsts are the values of the string constants of the files whose name matches.
func stringConsts(t *testing.T, files []*ast.File, name *regexp.Regexp) []string {
	t.Helper()
	var out []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, id := range spec.Names {
				if !name.MatchString(id.Name) || i >= len(spec.Values) {
					continue
				}
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: %v", id.Name, err)
					}
					out = append(out, s)
				}
			}
			return true
		})
	}
	return out
}

// literalLabels are the string literals that name a label where no constant does: the Label of a step, and the argument
// of a parameter named label. A label written as a literal has to have a text like one of a constant.
func literalLabels(t *testing.T, files []*ast.File) []string {
	t.Helper()
	labeled := map[string]int{} // the functions with a parameter label, and its position
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if fd, ok := n.(*ast.FuncDecl); ok {
				i := 0
				for _, field := range fd.Type.Params.List {
					for _, name := range field.Names {
						if name.Name == "label" {
							labeled[fd.Name.Name] = i
						}
						i++
					}
					if len(field.Names) == 0 {
						i++
					}
				}
			}
			return true
		})
	}
	var out []string
	add := func(e ast.Expr) {
		if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Label" {
					add(n.Value)
				}
			case *ast.CallExpr:
				var name string
				switch fn := n.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if i, ok := labeled[name]; ok && i < len(n.Args) {
					add(n.Args[i])
				}
			}
			return true
		})
	}
	return out
}

func sortedUnique(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}

// texts are the keys of the section of a locale of the UI that have a text.
func texts(t *testing.T, lang, section string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(localesDir, lang+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var locale map[string]json.RawMessage
	if err := json.Unmarshal(b, &locale); err != nil {
		t.Fatalf("%s.json: %v", lang, err)
	}
	var keys map[string]string
	if err := json.Unmarshal(locale[section], &keys); err != nil {
		t.Fatalf("%s.json %s: %v", lang, section, err)
	}
	out := map[string]bool{}
	for k, v := range keys {
		out[k] = strings.TrimSpace(v) != ""
	}
	return out
}

// TestUITextsMatchTheCode keeps ui/src/test/job-texts.json equal to the kinds and the step labels of the plans, and fails
// for a kind or a label that has no text in a locale of the UI (kinds.<kind>, steps.<label>). After a new kind or label, run
// it with UPDATE_JOB_TEXTS=1 to write the file, and add the texts to both locales.
func TestUITextsMatchTheCode(t *testing.T) {
	var want uiTexts
	for _, dir := range planners {
		files := parsed(t, dir)
		want.Labels = append(want.Labels, stringConsts(t, files, labelName)...)
		want.Labels = append(want.Labels, literalLabels(t, files)...)
		if dir == "../actions" {
			want.Kinds = stringConsts(t, files, kindName)
		}
	}
	want.Kinds, want.Labels = sortedUnique(want.Kinds), sortedUnique(want.Labels)
	if len(want.Kinds) < 10 || len(want.Labels) < 10 {
		t.Fatalf("found only %d kinds and %d labels: the constants moved?", len(want.Kinds), len(want.Labels))
	}
	if os.Getenv("UPDATE_JOB_TEXTS") != "" {
		b, err := json.MarshalIndent(want, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(uiTextsFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(uiTextsFile)
	if err != nil {
		t.Fatalf("%v (UPDATE_JOB_TEXTS=1 writes it)", err)
	}
	var got uiTexts
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%s: %v", uiTextsFile, err)
	}
	if !slices.Equal(got.Kinds, want.Kinds) {
		t.Errorf("kinds in %s:\n got %v\nwant %v\n(UPDATE_JOB_TEXTS=1 go test ./internal/jobs writes it)", uiTextsFile, got.Kinds, want.Kinds)
	}
	if !slices.Equal(got.Labels, want.Labels) {
		t.Errorf("labels in %s:\n got %v\nwant %v\n(UPDATE_JOB_TEXTS=1 go test ./internal/jobs writes it)", uiTextsFile, got.Labels, want.Labels)
	}
	for _, lang := range []string{"en", "de"} {
		kinds, steps := texts(t, lang, "kinds"), texts(t, lang, "steps")
		for _, k := range want.Kinds {
			if !kinds[k] {
				t.Errorf("%s.json has no text kinds.%s", lang, k)
			}
		}
		for _, l := range want.Labels {
			if !steps[l] {
				t.Errorf("%s.json has no text steps.%s", lang, l)
			}
		}
	}
}
