package lint

import (
	"testing"

	"github.com/axitdev/onfly/internal/rules"
)

const sampleDiff = `diff --git a/cmd/a.go b/cmd/a.go
index 111..222 100644
--- a/cmd/a.go
+++ b/cmd/a.go
@@ -10,0 +11,3 @@ func f() {
+	fmt.Println("debug")
+	// fmt.Println("in a comment")
+	log.Printf("fmt.Println(x) in a string")
@@ -30 +33 @@ func g() {
-	old()
+	dd($x)
diff --git a/old.go b/old.go
deleted file mode 100644
--- a/old.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package old
-fmt.Println("gone")
diff --git a/web/app.ts b/web/app.ts
--- a/web/app.ts
+++ b/web/app.ts
@@ -1,0 +2,2 @@
+import { x } from "lodash/fp";
+console.log(x);
`

func TestParseDiffKeepsAddedLinesAndTheirNumbers(t *testing.T) {
	files := ParseDiff(sampleDiff)
	if len(files) != 2 || files[0].Path != "cmd/a.go" || files[1].Path != "web/app.ts" {
		t.Fatalf("a deleted file has no added lines: %+v", files)
	}
	if got := files[0].Lines; len(got) != 4 || got[0].Line != 11 || got[2].Line != 13 || got[3].Line != 33 || got[3].Text != "\tdd($x)" {
		t.Fatalf("%+v", got)
	}
}

func rule(name string, calls, imports []string, scope ...string) rules.Rule {
	return rules.Rule{Name: name, ForbiddenCalls: calls, ForbiddenImports: imports, Scope: scope, Message: ""}
}

func TestCheckFindsForbiddenCallsAndImports(t *testing.T) {
	rs := []rules.Rule{
		rule("no prints", []string{"fmt.Println", "dd", "console.log"}, nil),
		rule("no lodash", nil, []string{"lodash"}, "**/*.ts"),
	}
	got := Check(rs, ParseDiff(sampleDiff))
	type at struct {
		file string
		line int
		rule string
	}
	var have []at
	for _, v := range got {
		have = append(have, at{v.File, v.Line, v.Rule})
	}
	want := []at{{"cmd/a.go", 11, "no prints"}, {"cmd/a.go", 33, "no prints"}, {"web/app.ts", 2, "no lodash"}, {"web/app.ts", 3, "no prints"}}
	if len(have) != len(want) {
		t.Fatalf("a comment and a string are not violations: %+v", got)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("%d: %v, want %v", i, have[i], want[i])
		}
	}
	if got[0].Severity != "error" || got[0].Message != "forbidden call fmt.Println" || got[0].Match != "fmt.Println(" {
		t.Fatalf("%+v", got[0])
	}
}

func TestBareNamesAreNotMethodsOrQualifiedCalls(t *testing.T) {
	rs := []rules.Rule{rule("r", []string{"dd", "eval"}, nil)}
	lines := []Added{
		{1, "dd($x);"}, {2, "$this->dd($x);"}, {3, "Foo::dd($x);"}, {4, "function dd($x) {"}, {5, "add(1)"}, {6, "  eval ($code)"}, {7, "retrieval(x)"},
	}
	got := Check(rs, []File{{Path: "a.php", Lines: lines}})
	var nums []int
	for _, v := range got {
		nums = append(nums, v.Line)
	}
	// `function dd(` declares it; the check is on calls, and a declaration looks like one.
	if len(nums) != 3 || nums[0] != 1 || nums[1] != 4 || nums[2] != 6 {
		t.Fatalf("%v", nums)
	}
}

func TestImportFormsAndScope(t *testing.T) {
	rs := []rules.Rule{
		rule("go", nil, []string{"github.com/pkg/errors"}, "**/*.go"),
		rule("php", nil, []string{`Illuminate\Support\Facades\DB`}, "**/*.php"),
		rule("py", nil, []string{"requests"}, "**/*.py"),
	}
	files := []File{
		{Path: "a.go", Lines: []Added{{3, `	"github.com/pkg/errors"`}, {4, `	e "github.com/pkg/errors/x"`}, {5, `	"github.com/pkg/errorsx"`}}},
		{Path: "a.php", Lines: []Added{{1, `use Illuminate\Support\Facades\DB;`}, {2, `use Illuminate\Support\Facades\Cache;`}}},
		{Path: "a.py", Lines: []Added{{1, "import requests"}, {2, "from requests.auth import X"}, {3, "import requests_mock"}}},
		{Path: "b.txt", Lines: []Added{{1, `import requests`}}},
	}
	var got []string
	for _, v := range Check(rs, files) {
		got = append(got, v.File+":"+itoa(v.Line))
	}
	want := []string{"a.go:3", "a.go:4", "a.php:1", "a.py:1", "a.py:2"}
	if len(got) != len(want) {
		t.Fatalf("%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v, want %v", got, want)
		}
	}
}

func TestIgnoreAndSeverity(t *testing.T) {
	r := rules.Rule{Name: "w", ForbiddenCalls: []string{"print"}, Ignore: []string{"**/*_test.go"}, Severity: "warning", Message: "use the logger"}
	got := Check([]rules.Rule{r}, []File{
		{Path: "a_test.go", Lines: []Added{{1, "print(x)"}}},
		{Path: "a.go", Lines: []Added{{1, "print(x)"}}},
	})
	if len(got) != 1 || got[0].File != "a.go" || got[0].Severity != "warning" || got[0].Message != "use the logger" {
		t.Fatalf("%+v", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
