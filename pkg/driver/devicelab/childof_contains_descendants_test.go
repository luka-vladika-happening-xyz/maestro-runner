package devicelab

import (
	"strings"
	"testing"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
)

// Three rows of buttons: the first row's are all off, the second's all on,
// the third's first is off and its second on.
const rowsOfButtons = `<hierarchy rotation="0">
<node index="0" text="" resource-id="" class="android.widget.FrameLayout" bounds="[0,0][1080,2000]" displayed="true" enabled="true">
<node index="0" text="" resource-id="row" class="android.view.ViewGroup" bounds="[0,100][1080,300]" displayed="true" enabled="true" clickable="true">
<node index="0" text="0.1" resource-id="button_off" class="android.view.View" bounds="[20,200][340,280]" displayed="true" enabled="true" clickable="true"/>
<node index="1" text="0.2" resource-id="button_off" class="android.view.View" bounds="[380,200][700,280]" displayed="true" enabled="true" clickable="true"/>
</node>
<node index="1" text="" resource-id="row" class="android.view.ViewGroup" bounds="[0,400][1080,600]" displayed="true" enabled="true" clickable="true">
<node index="0" text="1.1" resource-id="button_on" class="android.view.View" bounds="[20,500][340,580]" displayed="true" enabled="true" clickable="true"/>
<node index="1" text="1.2" resource-id="button_on" class="android.view.View" bounds="[380,500][700,580]" displayed="true" enabled="true" clickable="true"/>
</node>
<node index="2" text="" resource-id="row" class="android.view.ViewGroup" bounds="[0,700][1080,900]" displayed="true" enabled="true" clickable="true">
<node index="0" text="2.1" resource-id="button_off" class="android.view.View" bounds="[20,800][340,880]" displayed="true" enabled="true" clickable="true"/>
<node index="1" text="2.2" resource-id="button_on" class="android.view.View" bounds="[380,800][700,880]" displayed="true" enabled="true" clickable="true"/>
</node>
</node>
</hierarchy>`

// childOf's anchor keeps its own containsDescendants, as in Maestro: the
// anchors are the rows that hold a button that is on, and the anchor's index
// counts those rows only. The button itself is any button of that row.
func TestChildOfAnchorContainsDescendants(t *testing.T) {
	c := &splitClient{trackingClient: newTrackingClient(), snapshot: rowsOfButtons, source: rowsOfButtons}
	d := New(c, &core.PlatformInfo{}, &mockShell{})
	all, err := ParsePageSource(rowsOfButtons)
	if err != nil {
		t.Fatal(err)
	}
	sel := func(row, holds string) flow.Selector {
		return flow.Selector{
			ID: "button_.*",
			ChildOf: &flow.Selector{
				ID:                  "row",
				ContainsDescendants: []*flow.Selector{{ID: holds}},
				Index:               row,
			},
			Index: "0",
		}
	}
	resolvers := map[string]func(flow.Selector) (*core.ElementInfo, error){
		"resolveRelativeSelector": d.resolveRelativeSelector,
		"findElementRelativeWithElements": func(s flow.Selector) (*core.ElementInfo, error) {
			_, info, err := d.findElementRelativeWithElements(s, all)
			return info, err
		},
	}
	for name, resolve := range resolvers {
		for _, tc := range []struct{ row, want string }{
			{"0", "1.1"},
			{"1", "2.1"},
			{"", "1.1"},
			{"-1", "2.1"},
		} {
			info, err := resolve(sel(tc.row, "button_on"))
			if err != nil || info == nil || info.Text != tc.want {
				t.Errorf("%s: row %q = %+v, %v; want %s", name, tc.row, info, err, tc.want)
			}
		}
		if info, err := resolve(sel("2", "button_on")); err == nil || !strings.Contains(err.Error(), "no element at index 2: 2 match(es)") {
			t.Errorf("%s: row 2 of the 2 rows holding a button on = %+v, %v; want an index error", name, info, err)
		}
		if info, err := resolve(sel("0", "button_gone")); err == nil {
			t.Errorf("%s: no row holds the descendant, got %+v; want an error", name, info)
		}
	}
}
