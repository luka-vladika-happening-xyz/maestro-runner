package wda

import (
	"strings"
	"testing"

	"github.com/devicelab-dev/maestro-runner/pkg/flow"
)

// Three rows of buttons: the first row's are all off, the second's all on,
// the third's first is off and its second on.
const rowsOfButtons = `<?xml version="1.0" encoding="UTF-8"?>
<AppiumAUT>
  <XCUIElementTypeApplication type="XCUIElementTypeApplication" name="App" enabled="true" visible="true" x="0" y="0" width="390" height="844">
    <XCUIElementTypeOther type="XCUIElementTypeOther" name="row" enabled="true" visible="true" x="0" y="100" width="390" height="100">
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="button_off" label="0.1" enabled="true" visible="true" x="10" y="150" width="120" height="40"/>
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="button_off" label="0.2" enabled="true" visible="true" x="140" y="150" width="120" height="40"/>
    </XCUIElementTypeOther>
    <XCUIElementTypeOther type="XCUIElementTypeOther" name="row" enabled="true" visible="true" x="0" y="250" width="390" height="100">
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="button_on" label="1.1" enabled="true" visible="true" x="10" y="300" width="120" height="40"/>
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="button_on" label="1.2" enabled="true" visible="true" x="140" y="300" width="120" height="40"/>
    </XCUIElementTypeOther>
    <XCUIElementTypeOther type="XCUIElementTypeOther" name="row" enabled="true" visible="true" x="0" y="400" width="390" height="100">
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="button_off" label="2.1" enabled="true" visible="true" x="10" y="450" width="120" height="40"/>
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="button_on" label="2.2" enabled="true" visible="true" x="140" y="450" width="120" height="40"/>
    </XCUIElementTypeOther>
  </XCUIElementTypeApplication>
</AppiumAUT>`

// childOf's anchor keeps its own containsDescendants, as in Maestro: the
// anchors are the rows that hold a button that is on, and the anchor's index
// counts those rows only.
func TestChildOfAnchorContainsDescendants(t *testing.T) {
	elements, err := ParsePageSource(rowsOfButtons)
	if err != nil {
		t.Fatal(err)
	}
	d := &Driver{}
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
		info, err := d.resolveRelativeSelector(sel(tc.row, "button_on"), elements)
		if err != nil || info == nil || info.Text != tc.want {
			t.Errorf("row %q = %+v, %v; want %s", tc.row, info, err, tc.want)
		}
	}
	if info, err := d.resolveRelativeSelector(sel("2", "button_on"), elements); err == nil || !strings.Contains(err.Error(), "no element at index 2: 2 match(es)") {
		t.Errorf("row 2 of the 2 rows holding a button on = %+v, %v; want an index error", info, err)
	}
	if info, err := d.resolveRelativeSelector(sel("0", "button_gone"), elements); err == nil {
		t.Errorf("no row holds the descendant, got %+v; want an error", info)
	}
}
