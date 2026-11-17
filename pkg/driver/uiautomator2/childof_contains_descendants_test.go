package uiautomator2

import (
	"net/http"
	"strings"
	"testing"

	"github.com/devicelab-dev/maestro-runner/pkg/flow"
)

// Three rows of buttons: the first row's are all off, the second's all on,
// the third's first is off and its second on.
const rowsOfButtons = `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
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
// counts those rows only.
func TestChildOfAnchorContainsDescendants(t *testing.T) {
	server := setupRelativeSelectorServer(t, rowsOfButtons, func(w http.ResponseWriter, r *http.Request) {})
	defer server.Close()
	driver := New(newMockHTTPClient(server.URL).Client, nil, nil)
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
	for _, tc := range []struct{ row, want string }{
		{"0", "1.1"},
		{"1", "2.1"},
		{"", "1.1"},
		{"-1", "2.1"},
	} {
		result := driver.Execute(&flow.CopyTextFromStep{Selector: sel(tc.row, "button_on")})
		if !result.Success || result.Data != tc.want {
			t.Errorf("copyTextFrom, row %q: copied %q (success %v, %v); want %q", tc.row, result.Data, result.Success, result.Error, tc.want)
		}
		_, info, err := driver.findElementRelativeWithElements(sel(tc.row, "button_on"), all)
		if err != nil || info == nil || info.Text != tc.want {
			t.Errorf("findElementRelativeWithElements, row %q = %+v, %v; want %s", tc.row, info, err, tc.want)
		}
	}
	if _, info, err := driver.findElementRelativeWithElements(sel("2", "button_on"), all); err == nil || !strings.Contains(err.Error(), "no element at index 2: 2 match(es)") {
		t.Errorf("row 2 of the 2 rows holding a button on = %+v, %v; want an index error", info, err)
	}
	if _, info, err := driver.findElementRelativeWithElements(sel("0", "button_gone"), all); err == nil {
		t.Errorf("no row holds the descendant, got %+v; want an error", info)
	}
}
