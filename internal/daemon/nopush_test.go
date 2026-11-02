package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Jody's context repo (github.com/jodydadescott/jody) is private on GitHub Free,
// which cannot protect main: nothing on the server stops a push to it (#11).
// Until it can, doppelganger may only ever open pull requests there, and today
// it writes to no git repository at all. These tests hold that: any git or
// GitHub write path added to doppelganger fails them, and must come with a test
// of its own proving it only pushes review branches and opens pull requests,
// never updates main (DOPPELGANGER-STATE.md, "Review = one PR per recorded answer").

// Imports that can push to a git repository or update a GitHub ref.
var gitWriterImports = []string{
	"os/exec", // git, gh
	"github.com/go-git/",
	"gopkg.in/src-d/go-git",
	"github.com/libgit2/",
	"github.com/google/go-github",
	"github.com/cli/go-gh",
	"github.com/shurcooL/githubv4",
	"github.com/xanzy/go-gitlab",
	"gitlab.com/gitlab-org/api/client-go",
}

// String literals that name a git push, a ref, or GitHub's API.
var gitWriterLiterals = []string{
	"git push",
	"refs/heads/",
	"/git/refs",
	"api.github.com",
	"jodydadescott/jody",
}

func TestNeverPushesToContextRepoMain(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	files := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			for _, msg := range gitWrites(f) {
				t.Errorf("%s: %s", path, msg)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files == 0 {
		t.Fatal("no Go sources found under cmd/ and internal/")
	}
}

func TestNoGitWriterModule(t *testing.T) {
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		for _, p := range gitWriterImports {
			if strings.Contains(p, ".") && strings.Contains(strings.TrimSpace(line), p) {
				t.Errorf("go.mod requires a git or GitHub writer: %s", strings.TrimSpace(line))
			}
		}
	}
}

// gitWrites reports each import and string literal in f that could push to a
// git repository.
func gitWrites(f *ast.File) []string {
	var found []string
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		for _, p := range gitWriterImports {
			if path == p || strings.HasPrefix(path, p) {
				found = append(found, "imports "+path)
			}
		}
	}
	for _, decl := range f.Decls {
		if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			s = strings.ToLower(s)
			for _, w := range gitWriterLiterals {
				if strings.Contains(s, w) {
					found = append(found, "string literal names "+strconv.Quote(w))
				}
			}
			return true
		})
	}
	return found
}

func TestGitWritesCatchesAPush(t *testing.T) {
	src := `package p
import "os/exec"
func push() { exec.Command("git", "push", "origin", "HEAD:refs/heads/main").Run() }
const api = "https://api.github.com/repos/jodydadescott/jody/git/refs/heads/main"
`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(gitWrites(f), "\n")
	for _, want := range []string{"imports os/exec", `"refs/heads/"`, `"api.github.com"`, `"/git/refs"`, `"jodydadescott/jody"`} {
		if !strings.Contains(got, want) {
			t.Errorf("gitWrites missed %s; found:\n%s", want, got)
		}
	}
}
