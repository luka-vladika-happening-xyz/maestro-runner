package appium

import (
	"reflect"
	"testing"

	"github.com/devicelab-dev/maestro-runner/pkg/flow"
)

func TestIOSLaunchArguments(t *testing.T) {
	args, env := iosLaunchArguments(&flow.LaunchAppStep{
		Arguments:   map[string]any{"e2e-skip-onboarding": "true", "b": false},
		Environment: map[string]string{"FOO": "bar"},
	})
	if want := []string{"-b", "false", "-e2e-skip-onboarding", "true"}; !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if env["FOO"] != "bar" || env["e2e-skip-onboarding"] != "true" {
		t.Errorf("env = %v", env)
	}
}
