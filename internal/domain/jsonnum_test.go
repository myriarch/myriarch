// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"encoding/json"
	"testing"
)

func TestUnmarshalJSONPreservingInts(t *testing.T) {
	min := 20.0
	in := Blueprint{
		ObjectMeta: ObjectMeta{Name: "b"},
		BlueprintSpec: BlueprintSpec{
			Parameters: []ParameterSpec{{Name: "p", Type: ParamInt, Default: 75, Enum: []any{1, 2.5}, Min: &min}},
			Config:     map[string]any{"a": 10000000, "b": []any{1, 2.5, "x"}, "c": map[string]any{"d": 3}},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Blueprint
	if err := UnmarshalJSONPreservingInts(raw, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Parameters[0].Default.(int); !ok {
		t.Fatalf("default type %T", out.Parameters[0].Default)
	}
	if _, ok := out.Parameters[0].Enum[1].(float64); !ok {
		t.Fatalf("enum[1] type %T", out.Parameters[0].Enum[1])
	}
	if v, ok := out.Config["a"].(int); !ok || v != 10000000 {
		t.Fatalf("config.a %T %v", out.Config["a"], out.Config["a"])
	}
	if _, ok := out.Config["c"].(map[string]any)["d"].(int); !ok {
		t.Fatalf("nested type %T", out.Config["c"].(map[string]any)["d"])
	}
	if *out.Parameters[0].Min != 20 {
		t.Fatal("typed float field broken")
	}

	var f Fleet
	if err := UnmarshalJSONPreservingInts([]byte(`{"name":"f","parameters":{"n":3,"s":"0123"},"update":{"params":{"x":1}}}`), &f); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Parameters["n"].(int); !ok || f.Parameters["s"] != "0123" {
		t.Fatalf("fleet params %#v", f.Parameters)
	}
	if _, ok := f.Update.Params["x"].(int); !ok {
		t.Fatalf("update params %#v", f.Update.Params)
	}
}
