package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/devicelab-dev/maestro-runner/pkg/report"
)

// A failing tapOn pauses the flow; a step run in the debugger sees the flow's
// variables, and a retry with a corrected selector lets the flow pass.
func TestDebuggerPausesAndRetries(t *testing.T) {
	t.Setenv("MAESTRO_RUNNER_DEBUG_ADDR", "127.0.0.1:0")
	d := activeDebugger()
	if d == nil {
		t.Fatal("no debugger")
	}
	base := "http://" + d.addr

	driver := &mockDriver{executeFunc: func(step flow.Step) *core.CommandResult {
		if tap, ok := step.(*flow.TapOnStep); ok && tap.Selector.Text == "Log in" {
			return &core.CommandResult{Success: false, Message: "Element not found: Log in"}
		}
		return &core.CommandResult{Success: true}
	}}
	f, err := flow.Parse([]byte("appId: x\n---\n- evalScript: ${output.user = 'qa'}\n- tapOn: Log in\n- tapOn: Done\n"), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *RunResult, 1)
	go func() {
		runner := New(driver, RunnerConfig{OutputDir: t.TempDir(), Artifacts: ArtifactNever,
			Device: report.Device{ID: "test", Platform: "android"}})
		r, _ := runner.Run(context.Background(), []flow.Flow{*f})
		done <- r
	}()

	var st debugState
	for i := 0; i < 100 && st.State != "paused"; i++ {
		time.Sleep(20 * time.Millisecond)
		getJSON(t, base+"/debug/state", &st)
	}
	if st.State != "paused" || st.Step == nil || !strings.Contains(st.Step.Error, "Log in") {
		t.Fatalf("state = %+v, want paused on tapOn Log in", st)
	}

	var exec map[string]any
	postJSON(t, base+"/debug/exec", "- assertTrue: ${output.user == 'qa'}", &exec)
	if exec["ok"] != true {
		t.Fatalf("exec in the flow's context = %v", exec)
	}

	var retry map[string]any
	postJSON(t, base+"/debug/retry", "- tapOn: Sign in", &retry)
	if retry["ok"] != true || retry["resumed"] != true {
		t.Fatalf("retry = %v", retry)
	}

	select {
	case r := <-done:
		if r.Status != report.StatusPassed {
			t.Fatalf("flow = %s, want passed after the retry", r.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flow did not resume")
	}
}

// A step whose ${...} threw pauses even when optional, since it fails the
// flow; once a step run in the debugger sets the value, an empty retry runs
// the step again with it.
func TestDebuggerPausesOnAThrowingExpression(t *testing.T) {
	t.Setenv("MAESTRO_RUNNER_DEBUG_ADDR", "127.0.0.1:0")
	d := activeDebugger()
	if d == nil {
		t.Fatal("no debugger")
	}
	base := "http://" + d.addr

	var tapped []string
	driver := &mockDriver{executeFunc: func(step flow.Step) *core.CommandResult {
		if tap, ok := step.(*flow.TapOnStep); ok {
			tapped = append(tapped, tap.Selector.ID)
		}
		return &core.CommandResult{Success: true}
	}}
	f, err := flow.Parse([]byte("appId: x\n---\n- tapOn:\n    id: ${output.user.id}\n    optional: true\n"), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *RunResult, 1)
	go func() {
		runner := New(driver, RunnerConfig{OutputDir: t.TempDir(), Artifacts: ArtifactNever,
			Device: report.Device{ID: "test", Platform: "android"}})
		r, _ := runner.Run(context.Background(), []flow.Flow{*f})
		done <- r
	}()

	var st debugState
	for i := 0; i < 100 && st.State != "paused"; i++ {
		time.Sleep(20 * time.Millisecond)
		getJSON(t, base+"/debug/state", &st)
	}
	if st.State != "paused" || st.Step == nil || !strings.Contains(st.Step.Error, "TypeError") {
		t.Fatalf("state = %+v, want paused on the TypeError", st)
	}

	var exec map[string]any
	postJSON(t, base+"/debug/exec", `- evalScript: "${output.user = {id: 'login'}}"`, &exec)
	if exec["ok"] != true {
		t.Fatalf("exec = %v", exec)
	}
	var retry map[string]any
	postJSON(t, base+"/debug/retry", "", &retry)
	if retry["ok"] != true || retry["resumed"] != true {
		t.Fatalf("retry = %v", retry)
	}

	select {
	case r := <-done:
		if r.Status != report.StatusPassed || len(tapped) != 1 || tapped[0] != "login" {
			t.Fatalf("flow = %s, taps = %q; want passed after one tap on id login", r.Status, tapped)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flow did not resume")
	}
}

// A runFlow whose own env: throws pauses on the runFlow, since nothing inside
// it ran; an inner step whose ${...} threw pauses once, and failing it there
// does not pause the runFlow again.
func TestDebuggerPausesOnARunFlowsOwnExpression(t *testing.T) {
	t.Setenv("MAESTRO_RUNNER_DEBUG_ADDR", "127.0.0.1:0")
	d := activeDebugger()
	if d == nil {
		t.Fatal("no debugger")
	}
	base := "http://" + d.addr

	var typed []string
	driver := &mockDriver{executeFunc: func(step flow.Step) *core.CommandResult {
		if in, ok := step.(*flow.InputTextStep); ok {
			typed = append(typed, in.Text)
		}
		return &core.CommandResult{Success: true}
	}}
	f, err := flow.Parse([]byte(`appId: x
---
- runFlow:
    env:
      USERNAME: ${output.user.name}
    commands:
      - inputText: ${USERNAME}
- runFlow:
    commands:
      - inputText: ${output.other.name}
`), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *RunResult, 1)
	go func() {
		runner := New(driver, RunnerConfig{OutputDir: t.TempDir(), Artifacts: ArtifactNever,
			Device: report.Device{ID: "test", Platform: "android"}})
		r, _ := runner.Run(context.Background(), []flow.Flow{*f})
		done <- r
	}()
	waitPaused := func(want string) {
		t.Helper()
		var st debugState
		for i := 0; i < 100 && st.State != "paused"; i++ {
			time.Sleep(20 * time.Millisecond)
			getJSON(t, base+"/debug/state", &st)
		}
		if st.State != "paused" || st.Step == nil || st.Step.Type != want || !strings.Contains(st.Step.Error, "TypeError") {
			t.Fatalf("state = %+v, want paused on a %s's TypeError", st, want)
		}
	}

	waitPaused("runFlow")
	var reply map[string]any
	postJSON(t, base+"/debug/exec", "- evalScript: \"${output.user = {name: 'qa'}}\"", &reply)
	if reply["ok"] != true {
		t.Fatalf("exec = %v", reply)
	}
	postJSON(t, base+"/debug/retry", "", &reply)
	if reply["ok"] != true || reply["resumed"] != true {
		t.Fatalf("retry = %v", reply)
	}

	waitPaused("inputText")
	postJSON(t, base+"/debug/fail", "", &reply)
	select {
	case r := <-done:
		if r.Status != report.StatusFailed || len(typed) != 1 || typed[0] != "qa" {
			t.Fatalf("flow = %s, typed %q; want failed after typing qa once", r.Status, typed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flow did not end after the fail: the runFlow paused again?")
	}
}

// A step paused inside a runFlow is in the report as running, and what the
// debugger runs while it is paused stays out of the runFlow's steps, live and
// at the end: a tool that drives the runner shows it under the paused step.
func TestDebuggerStepsStayOutOfTheReport(t *testing.T) {
	t.Setenv("MAESTRO_RUNNER_DEBUG_ADDR", "127.0.0.1:0")
	d := activeDebugger()
	if d == nil {
		t.Fatal("no debugger")
	}
	base := "http://" + d.addr

	driver := &mockDriver{executeFunc: func(step flow.Step) *core.CommandResult {
		if tap, ok := step.(*flow.TapOnStep); ok && tap.Selector.Text == "Log in" {
			return &core.CommandResult{Success: false, Message: "Element not found: Log in"}
		}
		return &core.CommandResult{Success: true}
	}}
	f, err := flow.Parse([]byte("appId: x\n---\n- runFlow:\n    commands:\n      - tapOn: First\n      - tapOn: Log in\n      - tapOn: Done\n"), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	done := make(chan *RunResult, 1)
	go func() {
		runner := New(driver, RunnerConfig{OutputDir: dir, Artifacts: ArtifactNever,
			Device: report.Device{ID: "test", Platform: "android"}})
		r, _ := runner.Run(context.Background(), []flow.Flow{*f})
		done <- r
	}()
	steps := func() string {
		t.Helper()
		d, err := report.ReadFlowDetail(filepath.Join(dir, "flows", "flow-000.json"))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range d.Commands[0].SubCommands {
			out = append(out, c.YAML+" "+string(c.Status))
		}
		return strings.Join(out, ", ")
	}

	var st debugState
	for i := 0; i < 100 && st.State != "paused"; i++ {
		time.Sleep(20 * time.Millisecond)
		getJSON(t, base+"/debug/state", &st)
	}
	if st.State != "paused" {
		t.Fatalf("state = %+v, want paused", st)
	}
	paused := `tapOn: text="First" passed, tapOn: text="Log in" running`
	if got := steps(); got != paused {
		t.Errorf("paused, the runFlow's steps are %q, want %q", got, paused)
	}
	var reply map[string]any
	postJSON(t, base+"/debug/exec", "- tapOn: Other", &reply)
	if reply["ok"] != true {
		t.Fatalf("exec = %v", reply)
	}
	if got := steps(); got != paused {
		t.Errorf("after an exec, the runFlow's steps are %q, want %q", got, paused)
	}
	postJSON(t, base+"/debug/retry", "- tapOn: Sign in", &reply)
	if reply["ok"] != true || reply["resumed"] != true {
		t.Fatalf("retry = %v", reply)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the flow did not resume")
	}
	if got, want := steps(), `tapOn: text="First" passed, tapOn: text="Log in" passed, tapOn: text="Done" passed`; got != want {
		t.Errorf("at the end the runFlow's steps are %q, want %q", got, want)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(v)
}

func postJSON(t *testing.T, url, body string, v any) {
	t.Helper()
	resp, err := http.Post(url, "application/yaml", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %s", url, b)
	}
}
