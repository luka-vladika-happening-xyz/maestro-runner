package cli

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/urfave/cli/v2"
)

// writeSuite writes a suite file (two runFlows of files, nothing else) and
// its parts to dir and returns the suite's path.
func writeSuite(t *testing.T, dir string) string {
	t.Helper()
	for name, body := range map[string]string{
		"suite.yaml": "appId: com.example\n---\n- runFlow: a.yaml\n- runFlow: b.yaml\n",
		"a.yaml":     "appId: com.example\n---\n- tapOn: a\n",
		"b.yaml":     "appId: com.example\n---\n- tapOn: b\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "suite.yaml")
}

// flowsInReport returns the names of the flows in a report folder's report.json.
func flowsInReport(t *testing.T, reportDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(reportDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Flows []struct {
			Name string `json:"name"`
		} `json:"flows"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range index.Flows {
		names = append(names, f.Name)
	}
	return names
}

// test --expand-suites splits a suite file into a flow per runFlow; without
// it the file is one flow.
func TestExpandSuitesFlag(t *testing.T) {
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "suite"},
		{[]string{"--expand-suites"}, "a,b"},
	} {
		dir := t.TempDir()
		suite := writeSuite(t, dir)
		app := &cli.App{Name: "test-app", Flags: GlobalFlags, Commands: []*cli.Command{testCommand}}
		args := append([]string{"test-app", "-p", "mock", "--device", "mock-device", "test", "--output", filepath.Join(dir, "reports"), "--flatten"}, c.args...)
		if err := app.Run(append(args, suite)); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got := strings.Join(flowsInReport(t, filepath.Join(dir, "reports")), ","); got != c.want {
			t.Errorf("%v: flows %q, want %q", c.args, got, c.want)
		}
	}
}

// A library caller's RunConfig.ExpandSuites reaches ExecuteFlowWithDriver's
// runner.
func TestExecuteFlowWithDriverExpandSuites(t *testing.T) {
	oldStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = oldStdout }()

	for _, expand := range []bool{false, true} {
		dir := t.TempDir()
		f, err := flow.ParseFile(writeSuite(t, dir))
		if err != nil {
			t.Fatal(err)
		}
		cfg := &RunConfig{Platform: "mock", Devices: []string{"mock-device"}, OutputDir: filepath.Join(dir, "reports"), ExpandSuites: expand}
		driver, cleanup, err := CreateDriver(cfg)
		if err != nil {
			t.Fatalf("CreateDriver: %v", err)
		}
		result, err := ExecuteFlowWithDriver(driver, cfg, *f)
		cleanup()
		if err != nil {
			t.Fatalf("ExecuteFlowWithDriver: %v", err)
		}
		want := 1
		if expand {
			want = 2
		}
		if result.TotalFlows != want {
			t.Errorf("ExpandSuites=%v: %d flows, want %d", expand, result.TotalFlows, want)
		}
	}
}

// Every executor.RunnerConfig the CLI builds passes RunConfig.ExpandSuites
// on: the sequential run, the library entry point, the Appium run and the
// parallel runner. A config that leaves it out splits nothing in that mode.
func TestExpandSuitesReachesEveryRunnerConfig(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	sites := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RunnerConfig" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "executor" {
				return true
			}
			sites++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "ExpandSuites" {
					if val, ok := kv.Value.(*ast.SelectorExpr); ok && val.Sel.Name == "ExpandSuites" {
						return true
					}
				}
			}
			t.Errorf("%s: executor.RunnerConfig without ExpandSuites: cfg.ExpandSuites", fset.Position(lit.Pos()))
			return true
		})
	}
	if sites < 4 {
		t.Errorf("found %d executor.RunnerConfig literals, want the 4 known (sequential, library, Appium, parallel)", sites)
	}
}
