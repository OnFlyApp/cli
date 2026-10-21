package resolve

import (
	"testing"

	"github.com/axitdev/onfly/internal/parser"
)

func ch(file, content string, meta map[string]string) parser.Chunk {
	return parser.Chunk{FilePath: file, Content: content, Metadata: meta}
}

const goSrc = `package skeleton

import (
	"fmt"
	"strings"

	"github.com/axitdev/onfly/internal/tokens"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/acme/lib/v2"
)
`

func TestGoResolution(t *testing.T) {
	fn := func(file, name string) parser.Chunk { return ch(file, "", map[string]string{"name": name}) }
	method := func(file, name, recv string) parser.Chunk {
		return ch(file, "", map[string]string{"name": name, "receiver": recv})
	}
	tests := []struct {
		name    string
		content string
		def     parser.Chunk
		want    bool
	}{
		{"strings.Split is not the repo's Split", "parts := strings.Split(s, \",\")", fn("internal/eval/eval.go", "Split"), false},
		{"imported package function", "n, _ := tokens.Count(s)", fn("internal/tokens/tokens.go", "Count"), true},
		{"imported under an alias", "sdkmcp.AddTool(srv)", fn("vendor/x/mcp/mcp.go", "AddTool"), false},
		{"bare call stays in the package", "return indentOf(line)", fn("internal/skeleton/skeleton.go", "indentOf"), true},
		{"bare call does not cross packages", "return indentOf(line)", fn("internal/other/x.go", "indentOf"), false},
		{"method of a package the caller neither lives in nor imports", "h.store.GetComponents(ctx, n)", method("internal/vectorstore/postgres.go", "GetComponents", "Postgres"), false},
		{"method of an imported package", "c.Count(s)", method("internal/tokens/tokens.go", "Count", "Counter"), true},
		{"method of the caller's own package", "sc.line(x)", method("internal/skeleton/scan.go", "line", "scanner"), true},
		{"predeclared append is not a method", "out = append(out, x)", method("internal/usage/usage.go", "append", "Log"), false},
		{"a field read is not a method call", "srcs.text[rel] = src", method("internal/rules/rules.go", "text", "Rule"), false},
		{"String on a value does not cross files", "b.String()", method("internal/rules/rules.go", "String", "parserNames"), false},
		{"String on a value in the same file", "b.String()", method("internal/skeleton/skeleton.go", "String", "scanner"), true},
		{"a same-named method of an unrelated package is not a caller", "h.record(x)", method("internal/mcp/server.go", "record", "tools"), false},
		{"a function is not called through a value", "x.Count()", fn("internal/tokens/tokens.go", "Count"), false},
		{"name not in the text", "return 1", fn("internal/skeleton/skeleton.go", "Collapse"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := ch("internal/skeleton/skeleton.go", tc.content, map[string]string{"name": "F"})
			if got := Accepts(goSrc, caller, tc.def); got != tc.want {
				t.Fatalf("Accepts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGoImportNames(t *testing.T) {
	got := goImports(goSrc + "import single \"example.com/one/two\"\n")
	want := map[string]string{
		"fmt": "fmt", "strings": "strings", "tokens": "github.com/axitdev/onfly/internal/tokens",
		"sdkmcp": "github.com/modelcontextprotocol/go-sdk/mcp", "lib": "github.com/acme/lib/v2", "single": "example.com/one/two",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("import %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestAcceptsEverythingWithoutSource(t *testing.T) {
	def := ch("a/b.go", "", map[string]string{"name": "Split"})
	if !Accepts("", ch("c/d.go", "strings.Split(x)", nil), def) {
		t.Fatal("an unreadable caller file must not hide a candidate")
	}
	if !Accepts("x", ch("c/d.rb", "Split(x)", nil), def) {
		t.Fatal("languages without rules accept")
	}
}

const phpSrc = `<?php
namespace App\Http;

use App\Models\Story;
use App\Support\Money as Cash;
`

func TestPHPResolution(t *testing.T) {
	story := ch("app/Models/Story.php", "", map[string]string{"name": "Story", "fqn": `App\Models\Story`, "namespace": `App\Models`})
	method := func(name, recv string) parser.Chunk {
		return ch("x.php", "", map[string]string{"name": name, "receiver": recv, "namespace": "N"})
	}
	tests := []struct {
		name    string
		content string
		def     parser.Chunk
		want    bool
	}{
		{"new of an imported class", "$s = new Story();", story, true},
		{"static call on an imported class", "Story::query()", method("query", `App\Models\Story`), true},
		{"static call on a different class of that name", "Story::query()", method("query", `Other\Story`), false},
		{"aliased import", "Cash::format(1)", method("format", `App\Support\Money`), true},
		{"method on a variable", "$this->save()", method("save", `App\Models\Story`), true},
		{"a method is not called bare", "save()", method("save", `App\Models\Story`), false},
		{"handle() on a variable does not cross files", "$job->handle()", method("handle", `App\Jobs\Send`), false},
		{"count() is not a user method", "count($a)", method("count", `App\Models\Story`), false},
		{"a value's method is not an unrelated class's method of that name", "$this->getName()", ch("src/Auth/SessionGuard.php", "", map[string]string{"name": "getName", "receiver": `Auth\SessionGuard`, "namespace": `Auth`}), false},
		{"a method of a class in the caller's namespace", "$this->boot()", ch("app/Http/Base.php", "", map[string]string{"name": "boot", "receiver": `App\Http\Base`, "namespace": `App\Http`}), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caller := ch("app/Http/C.php", tc.content, map[string]string{"name": "show", "namespace": `App\Http`})
			if got := Accepts(phpSrc, caller, tc.def); got != tc.want {
				t.Fatalf("Accepts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPHPPropertyChainIsACallOnAValue(t *testing.T) {
	src := "<?php\nnamespace App\\Queue;\nuse App\\Cache\\RateLimiter;\n"
	def := ch("src/Cache/RateLimiter.php", "", map[string]string{"name": "tooManyAttempts", "receiver": "App\\Cache\\RateLimiter", "namespace": "App\\Cache"})
	caller := ch("src/Queue/RateLimited.php", "return $this->limiter->tooManyAttempts($key, 1);", map[string]string{"name": "handle", "namespace": "App\\Queue"})
	if !Accepts(src, caller, def) {
		t.Fatal("a call through a property of a class the caller imports is a caller")
	}
	stranger := ch("src/Queue/Other.php", "return $this->limiter->tooManyAttempts($key, 1);", map[string]string{"name": "handle", "namespace": "App\\Queue"})
	if Accepts("<?php\nnamespace App\\Queue;\n", stranger, def) {
		t.Fatal("without importing the class or sharing its namespace it is not")
	}
}

func TestPHPPropertyTypeDecidesBeforeTheNamespace(t *testing.T) {
	src := "<?php\nnamespace App\\Queue;\nuse App\\Cache\\RateLimiter;\nclass RateLimited {\n    protected $limiter;\n    function __construct() { $this->limiter = Container::make(RateLimiter::class); }\n}\n"
	call := "return $this->limiter->tooManyAttempts($key, 1);"
	caller := ch("src/Queue/RateLimited.php", call, map[string]string{"name": "handleJob", "namespace": "App\\Queue"})
	cache := ch("src/Cache/RateLimiter.php", "", map[string]string{"name": "tooManyAttempts", "receiver": "App\\Cache\\RateLimiter", "namespace": "App\\Cache"})
	neighbour := ch("src/Queue/RateLimitedWithRedis.php", "", map[string]string{"name": "tooManyAttempts", "receiver": "App\\Queue\\RateLimitedWithRedis", "namespace": "App\\Queue"})
	if !Accepts(src, caller, cache) {
		t.Fatal("the property holds a RateLimiter")
	}
	if Accepts(src, caller, neighbour) {
		t.Fatal("a class in the same namespace is not what the property holds")
	}
	// Without a type the namespace still counts, as before.
	untyped := "<?php\nnamespace App\\Queue;\nclass A { protected $limiter; }\n"
	if !Accepts(untyped, caller, neighbour) {
		t.Fatal("an untyped property keeps the namespace rule")
	}
	for name, typed := range map[string]string{
		"docblock": "<?php\nnamespace App\\Queue;\nuse App\\Cache\\RateLimiter;\nclass A {\n    /**\n     * The limiter.\n     *\n     * @var \\App\\Cache\\RateLimiter\n     */\n    protected $limiter;\n}\n",
		"promoted": "<?php\nnamespace App\\Queue;\nuse App\\Cache\\RateLimiter;\nclass A { function __construct(protected RateLimiter $limiter) {} }\n",
	} {
		if !Accepts(typed, caller, cache) || Accepts(typed, caller, neighbour) {
			t.Fatalf("%s: the declared type decides", name)
		}
	}
}

func TestPHPCallOnAVariableNamedForTheClass(t *testing.T) {
	src := "<?php\nnamespace App\\Application;\nclass Handler {}\n"
	call := "$order->cancelOrder($id);"
	caller := ch("src/Application/Handler.php", call, map[string]string{"name": "__invoke", "namespace": "App\\Application"})
	def := ch("src/Domain/Order.php", "", map[string]string{"name": "cancelOrder", "receiver": "App\\Domain\\Order", "namespace": "App\\Domain"})
	other := ch("src/Domain/Other.php", "", map[string]string{"name": "cancelOrder", "receiver": "App\\Domain\\Other", "namespace": "App\\Domain"})
	if !Accepts(src, caller, def) {
		t.Fatal("$order is a Order, imported or not")
	}
	if Accepts(src, caller, other) {
		t.Fatal("and not an Other")
	}
}

func TestPHPParameterTypeDecidesForAVariable(t *testing.T) {
	src := "<?php\nnamespace App\\Application;\nuse App\\Domain\\Account;\nclass Handler { function run(Account $acc) { $acc->close(); } }\n"
	caller := ch("src/Application/Handler.php", "$acc->close();", map[string]string{"name": "run", "namespace": "App\\Application"})
	account := ch("src/Domain/Account.php", "", map[string]string{"name": "close", "receiver": "App\\Domain\\Account", "namespace": "App\\Domain"})
	door := ch("src/Application/Door.php", "", map[string]string{"name": "close", "receiver": "App\\Application\\Door", "namespace": "App\\Application"})
	if !Accepts(src, caller, account) || Accepts(src, caller, door) {
		t.Fatal("the parameter is an Account, so the neighbour in the namespace is not the target")
	}
}

func TestPHPNameThatOneClassDefinesIsThatClassesEvenOnAnUntypedValue(t *testing.T) {
	src := "<?php\nnamespace App\\Application;\nclass Handler {}\n"
	caller := ch("src/Application/Handler.php", "$store->findByIban($iban);", map[string]string{"name": "run", "namespace": "App\\Application"})
	def := ch("src/Infra/Repo.php", "", map[string]string{"name": "findByIban", "receiver": "App\\Infra\\Repo", "namespace": "App\\Infra"})
	r := Rules{}
	if r.Accepts(src, caller, def, 3) {
		t.Fatal("three classes define it: the call could be to any of them")
	}
	if !r.Accepts(src, caller, def, 1) {
		t.Fatal("one class defines it: the call is to that one")
	}
	if (Rules{MaxUnambiguous: -1}).Accepts(src, caller, def, 1) {
		t.Fatal("the rule can be turned off")
	}
	if !(Rules{MaxUnambiguous: 3}).Accepts(src, caller, def, 3) {
		t.Fatal("and widened")
	}
	if r.Accepts(src, caller, ch("src/Infra/Repo.php", "", map[string]string{"name": "__construct", "receiver": "App\\Infra\\Repo"}), 1) {
		t.Fatal("a name every class has is never unambiguous")
	}
}

func TestConstructsTiesTheCodeThatMakesACommandToIt(t *testing.T) {
	cmd := ch("src/Domain/CancelOrder.php", "", map[string]string{"name": "CancelOrder", "fqn": "App\\Domain\\CancelOrder", "namespace": "App\\Domain"})
	src := "<?php\nnamespace App\\Api;\nuse App\\Domain\\CancelOrder;\nclass Controller {}\n"
	ns := map[string]string{"name": "post", "namespace": "App\\Api"}
	r := Rules{}
	for call, want := range map[string]bool{
		"$bus->dispatch(new CancelOrder($id));":             true,
		"$bus->dispatch(CancelOrder::fromRequest($r));":     true,
		"$bus->dispatch(new \\App\\Domain\\CancelOrder());": true,
		"function f(CancelOrder $c) {}":                     false, // names the type, makes none
		"$x = CancelOrder::class;":                          false,
		"$bus->dispatch(new Other());":                      false,
	} {
		caller := ch("src/Api/Controller.php", call, ns)
		if got := r.Constructs(src, caller, cmd); got != want {
			t.Errorf("%q: got %v, want %v", call, got, want)
		}
	}
	elsewhere := ch("src/Api/Controller.php", "new CancelOrder()", ns)
	if r.Constructs("<?php\nnamespace App\\Api;\nclass Controller {}\n", elsewhere, cmd) {
		t.Error("a CancelOrder of another namespace, not imported, is not this one")
	}
}
