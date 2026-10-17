package envscan

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func TestParseKeysNeverKeepsValues(t *testing.T) {
	got := ParseKeys("# comment\nAPP_KEY=base64:secret\nexport DB_HOST=localhost\n\nEMPTY=\nbad line\nWITH SPACE=1\nQUOTED=\"a b\"\nAPP_KEY=again\n")
	if want := []string{"APP_KEY", "DB_HOST", "EMPTY", "QUOTED"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestFindReadsInEveryLanguage(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []Hit
	}{
		{"go", "func f() { a := os.Getenv(\"DB_URL\"); b, ok := os.LookupEnv(\"PORT\") }", []Hit{{"DB_URL", 1, false}, {"PORT", 1, false}}},
		{"php with and without default", "$a = env('APP_KEY');\n$b = env('APP_ENV', 'prod');\n$c = getenv('HOME');\n$d = $_ENV['SECRET'];", []Hit{{"APP_KEY", 1, false}, {"APP_ENV", 2, true}, {"HOME", 3, false}, {"SECRET", 4, false}}},
		{"php method named env is not a read", "$this->env('X');\nFoo::env('Y');", nil},
		{"javascript", "const a = process.env.API_URL; const b = process.env['TOKEN']; const c = import.meta.env.VITE_MODE", []Hit{{"API_URL", 1, false}, {"TOKEN", 1, false}, {"VITE_MODE", 1, false}}},
		{"python", "a = os.getenv('A')\nb = os.getenv('B', 'x')\nc = os.environ['C']\nd = os.environ.get('D')", []Hit{{"A", 1, false}, {"B", 2, true}, {"C", 3, false}, {"D", 4, false}}},
		{"rust", `let a = std::env::var("R1"); let b = env!("R2");`, []Hit{{"R1", 1, false}, {"R2", 1, false}}},
		{"ruby", "a = ENV['K1']; b = ENV.fetch('K2', 'd')", []Hit{{"K1", 1, false}, {"K2", 1, true}}},
		{"comments are skipped", "// os.Getenv(\"OLD\")\n# env('OLD2')\n * process.env.OLD3", nil},
		{"a computed name is not a variable name", `os.Getenv(prefix + "X")`, nil},
	} {
		got := Find(tc.src)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

type fakeSource map[string][]parser.Chunk

func (f fakeSource) SearchKeyword(_ context.Context, kw string, _ int, _ string) ([]parser.Chunk, error) {
	return f[kw], nil
}

func TestCollectCrossReferencesFilesAndCode(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".env.example", "APP_KEY=\nDB_HOST=127.0.0.1\nOLD_FLAG=1\n")
	write(".env", "APP_KEY=secret\nDB_HOST=db\n")
	chunk := func(file, content string, start string) parser.Chunk {
		return parser.Chunk{FilePath: file, Content: content, Metadata: map[string]string{"start_line": start, "name": "f"}}
	}
	src := fakeSource{
		"env(": {
			chunk("config/app.php", "return [\n 'key' => env('APP_KEY'),\n 'debug' => env('APP_DEBUG', false),\n 'mail' => env('MAIL_HOST'),\n];", "10"),
			chunk("tests/AppTest.php", "env('ONLY_IN_TESTS')", "1"),
		},
		"Getenv": {chunk("cmd/db.go", "func f() {\n\treturn os.Getenv(\"DB_HOST\")\n}", "40")},
	}
	rep, err := Collect(context.Background(), root, src)
	if err != nil {
		t.Fatal(err)
	}
	names := func(vs []Var) []string {
		var n []string
		for _, v := range vs {
			n = append(n, v.Name)
		}
		return n
	}
	if want := []string{"MAIL_HOST"}; !reflect.DeepEqual(names(rep.Missing), want) {
		t.Fatalf("used but defined nowhere (APP_DEBUG has a default, test files do not count): %v", names(rep.Missing))
	}
	if want := []string{"OLD_FLAG"}; !reflect.DeepEqual(names(rep.Unused), want) {
		t.Fatalf("defined but never read: %v", names(rep.Unused))
	}
	var db Var
	for _, v := range rep.Vars {
		if v.Name == "DB_HOST" {
			db = v
		}
	}
	if !reflect.DeepEqual(db.Defined, []string{".env", ".env.example"}) || len(db.Used) != 1 || db.Used[0] != (Ref{File: "cmd/db.go", Line: 41}) {
		t.Fatalf("%+v", db)
	}
	if rep.Files[".env"] != 2 || rep.Files[".env.example"] != 3 {
		t.Fatalf("%v", rep.Files)
	}
}

func TestWithoutEnvFilesNothingIsMissing(t *testing.T) {
	rep, err := Collect(context.Background(), t.TempDir(), fakeSource{"Getenv": {{FilePath: "a.go", Content: `os.Getenv("X")`, Metadata: map[string]string{"start_line": "1"}}}})
	if err != nil || len(rep.Missing) != 0 || len(rep.Vars) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}
