package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
