package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/devicelab-dev/maestro-runner/pkg/logger"
)

// The debugger pauses a flow where a step fails, so a person (or an agent, or a
// tool such as simector) can look at the live device, try steps in the flow's
// own context (its variables, output.*, copiedText) and then retry, skip or fail
// the step, instead of reading a screenshot after the run has ended.
//
// It is on when MAESTRO_RUNNER_DEBUG_ADDR names a host:port, and serves:
//
//	GET  /debug/state       {"state": "running"|"paused", "flow", "step": {...}, "pausedAt"}
//	GET  /debug/screenshot  the device's screen now, PNG or JPEG (paused only)
//	GET  /debug/hierarchy   the driver's hierarchy now, as the driver gives it (paused only)
//	POST /debug/exec        body: YAML steps, run in the flow's context; the results (paused only)
//	POST /debug/retry       body: YAML steps to run instead of the failed step, or empty to run the
//	                        step again; on success the flow continues, on failure it stays paused
//	POST /debug/skip        the step counts as passed and the flow continues
//	POST /debug/fail        the step fails as it would have without the debugger
//
// It pauses only on a failure that would fail the flow: not on an optional
// step, not on an attempt a retry: block will repeat, and not on the runFlow,
// retry or repeat that only passes on a failure inside it (the inner step
// paused already). The device is driven from the flow's goroutine only: an HTTP
// request hands it a command and waits for the answer.
type Debugger struct {
	addr string

	mu    sync.Mutex
	state debugState
	cmds  chan debugCmd // non-nil while paused
	step  flow.Step     // the failed step, while paused
}

type debugState struct {
	State    string     `json:"state"`
	Flow     string     `json:"flow,omitempty"`
	Step     *debugStep `json:"step,omitempty"`
	PausedAt string     `json:"pausedAt,omitempty"`
}

type debugStep struct {
	Type     string `json:"type"`
	Describe string `json:"describe"`
	Label    string `json:"label,omitempty"`
	Error    string `json:"error"`
	Depth    int    `json:"depth"`
}

type debugCmd struct {
	run   func(fr *FlowRunner) (body any, resume *core.CommandResult)
	reply chan debugReply
}

type debugReply struct {
	body any
	err  error
}

var (
	debuggerOnce sync.Once
	theDebugger  *Debugger
)

// activeDebugger returns the debugger MAESTRO_RUNNER_DEBUG_ADDR asks for, or
// nil. The listener is opened once per process.
func activeDebugger() *Debugger {
	debuggerOnce.Do(func() {
		addr := strings.TrimSpace(os.Getenv("MAESTRO_RUNNER_DEBUG_ADDR"))
		if addr == "" {
			return
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Warn("debugger: cannot listen on %s: %v", addr, err)
			return
		}
		d := &Debugger{addr: ln.Addr().String(), state: debugState{State: "running"}}
		go func() { _ = http.Serve(ln, d.handler()) }()
		fmt.Printf("  ⏯  Debugger on http://%s/debug/state (a failing step pauses the flow)\n", d.addr)
		theDebugger = d
	})
	return theDebugger
}

// maybePause hands a failed step to the debugger when one is on and the
// failure would fail the flow; it returns the result the flow goes on with.
func (fr *FlowRunner) maybePause(step flow.Step, result *core.CommandResult) *core.CommandResult {
	if result == nil || result.Success || step.IsOptional() || fr.retryNotLast > 0 || fr.debugBusy {
		return result
	}
	switch step.(type) {
	case *flow.RunFlowStep, *flow.RetryStep, *flow.RepeatStep:
		return result
	}
	if os.Getenv("MAESTRO_RUNNER_DEBUG_ADDR") == "" {
		return result
	}
	d := activeDebugger()
	if d == nil || fr.ctx.Err() != nil {
		return result
	}
	return d.pause(fr, step, result)
}

func (d *Debugger) pause(fr *FlowRunner, step flow.Step, failed *core.CommandResult) *core.CommandResult {
	cmds := make(chan debugCmd)
	errText := failed.Message
	if failed.Error != nil && !strings.Contains(errText, failed.Error.Error()) {
		errText = strings.TrimSpace(errText + " (" + failed.Error.Error() + ")")
	}
	_, label := fr.stepNames(step)
	d.mu.Lock()
	d.state = debugState{
		State: "paused",
		Flow:  fr.flow.SourcePath,
		Step: &debugStep{Type: string(step.Type()), Describe: step.Describe(), Label: label,
			Error: errText, Depth: fr.depth},
		PausedAt: time.Now().Format(time.RFC3339),
	}
	d.cmds = cmds
	d.step = step
	d.mu.Unlock()
	fmt.Printf("  ⏸  Paused: %s — %s\n     http://%s/debug/state  (retry, skip or fail it there)\n",
		step.Describe(), errText, d.addr)

	defer func() {
		d.mu.Lock()
		d.state = debugState{State: "running", Flow: fr.flow.SourcePath}
		d.cmds = nil
		d.step = nil
		d.mu.Unlock()
	}()
	for {
		select {
		case <-fr.ctx.Done():
			return failed
		case c := <-cmds:
			fr.debugBusy = true
			body, resume := c.run(fr)
			fr.debugBusy = false
			c.reply <- debugReply{body: body}
			if resume != nil {
				return resume
			}
			if s, ok := body.(map[string]any); ok {
				if e, ok := s["error"].(string); ok && e != "" {
					d.mu.Lock()
					d.state.Step.Error = e
					d.mu.Unlock()
				}
			}
		}
	}
}

// send runs fn on the paused flow's goroutine and waits for its answer.
func (d *Debugger) send(fn func(fr *FlowRunner) (any, *core.CommandResult)) (any, error) {
	d.mu.Lock()
	cmds := d.cmds
	d.mu.Unlock()
	if cmds == nil {
		return nil, fmt.Errorf("the flow is not paused")
	}
	c := debugCmd{run: fn, reply: make(chan debugReply, 1)}
	select {
	case cmds <- c:
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("the flow did not take the command")
	}
	r := <-c.reply
	return r.body, r.err
}

// parseSteps reads YAML steps the way a flow file holds them, with the paused
// flow's appId and source path, so relative files resolve as in the flow.
func parseSteps(fr *FlowRunner, yamlSteps string) ([]flow.Step, error) {
	yamlSteps = strings.TrimSpace(yamlSteps)
	if !strings.HasPrefix(yamlSteps, "-") {
		yamlSteps = "- " + yamlSteps
	}
	header := "appId: " + fr.flow.Config.EffectiveAppID() + "\n---\n"
	f, err := flow.Parse([]byte(header+yamlSteps+"\n"), fr.flow.SourcePath)
	if err != nil {
		return nil, err
	}
	return f.Steps, nil
}

type stepOutcome struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// runSteps runs steps in the flow's context and stops at the first failure.
func runSteps(fr *FlowRunner, steps []flow.Step) ([]stepOutcome, bool) {
	var out []stepOutcome
	for _, s := range steps {
		r := fr.executeNestedStep(s)
		o := stepOutcome{Step: s.Describe(), Success: r.Success, Message: r.Message}
		if r.Error != nil {
			o.Error = r.Error.Error()
		}
		out = append(out, o)
		if !r.Success && !s.IsOptional() {
			return out, false
		}
	}
	return out, true
}

func (d *Debugger) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/state", func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		st := d.state
		d.mu.Unlock()
		writeDebugJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /debug/screenshot", func(w http.ResponseWriter, _ *http.Request) {
		body, err := d.send(func(fr *FlowRunner) (any, *core.CommandResult) {
			png, err := fr.driver.Screenshot()
			if err != nil {
				return err, nil
			}
			return png, nil
		})
		if img, ok := body.([]byte); ok && err == nil {
			w.Header().Set("Content-Type", http.DetectContentType(img)) // PNG or JPEG, by driver
			_, _ = w.Write(img)
			return
		}
		debugError(w, err, body)
	})
	mux.HandleFunc("GET /debug/hierarchy", func(w http.ResponseWriter, _ *http.Request) {
		body, err := d.send(func(fr *FlowRunner) (any, *core.CommandResult) {
			h, err := fr.driver.Hierarchy()
			if err != nil {
				return err, nil
			}
			return h, nil
		})
		if h, ok := body.([]byte); ok && err == nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(h)
			return
		}
		debugError(w, err, body)
	})
	mux.HandleFunc("POST /debug/exec", func(w http.ResponseWriter, r *http.Request) {
		src, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		body, err := d.send(func(fr *FlowRunner) (any, *core.CommandResult) {
			steps, err := parseSteps(fr, string(src))
			if err != nil {
				return map[string]any{"ok": false, "parseError": err.Error()}, nil
			}
			out, ok := runSteps(fr, steps)
			return map[string]any{"ok": ok, "results": out}, nil
		})
		debugAnswer(w, err, body)
	})
	mux.HandleFunc("POST /debug/retry", func(w http.ResponseWriter, r *http.Request) {
		src, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		body, err := d.send(func(fr *FlowRunner) (any, *core.CommandResult) {
			var steps []flow.Step
			if strings.TrimSpace(string(src)) != "" {
				parsed, err := parseSteps(fr, string(src))
				if err != nil {
					return map[string]any{"ok": false, "parseError": err.Error()}, nil
				}
				steps = parsed
			} else {
				d.mu.Lock()
				steps = []flow.Step{d.step}
				d.mu.Unlock()
			}
			out, ok := runSteps(fr, steps)
			if !ok {
				last := out[len(out)-1]
				return map[string]any{"ok": false, "results": out, "error": strings.TrimSpace(last.Message + " " + last.Error)}, nil
			}
			return map[string]any{"ok": true, "results": out, "resumed": true},
				&core.CommandResult{Success: true, Message: "Passed in the debugger after a retry"}
		})
		debugAnswer(w, err, body)
	})
	mux.HandleFunc("POST /debug/skip", func(w http.ResponseWriter, _ *http.Request) {
		body, err := d.send(func(*FlowRunner) (any, *core.CommandResult) {
			return map[string]any{"ok": true, "resumed": true},
				&core.CommandResult{Success: true, Message: "Skipped in the debugger"}
		})
		debugAnswer(w, err, body)
	})
	mux.HandleFunc("POST /debug/fail", func(w http.ResponseWriter, _ *http.Request) {
		body, err := d.send(func(fr *FlowRunner) (any, *core.CommandResult) {
			d.mu.Lock()
			msg := d.state.Step.Error
			d.mu.Unlock()
			return map[string]any{"ok": true, "resumed": true},
				&core.CommandResult{Success: false, Error: fmt.Errorf("%s", msg), Message: msg}
		})
		debugAnswer(w, err, body)
	})
	return mux
}

func writeDebugJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func debugAnswer(w http.ResponseWriter, err error, body any) {
	if err != nil {
		writeDebugJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeDebugJSON(w, http.StatusOK, body)
}

func debugError(w http.ResponseWriter, err error, body any) {
	if err == nil {
		if e, ok := body.(error); ok {
			err = e
		}
	}
	writeDebugJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprint(err)})
}
