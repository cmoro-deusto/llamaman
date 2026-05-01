package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParamsPreservesOrder(t *testing.T) {
	src := `{"ngl":99,"ctx-size":262144,"fa":"on","temp":0.6,"top-p":0.95,"jinja":true,"no-mmproj":true}`
	var p Params
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{"ngl", "ctx-size", "fa", "temp", "top-p", "jinja", "no-mmproj"}
	if len(p) != len(wantKeys) {
		t.Fatalf("len = %d, want %d", len(p), len(wantKeys))
	}
	for i, k := range wantKeys {
		if p[i].Key != k {
			t.Fatalf("p[%d].Key = %q, want %q", i, p[i].Key, k)
		}
	}

	// Round-trip preserves order verbatim (no whitespace difference because
	// our marshaller is compact).
	round, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(round) != src {
		t.Fatalf("round-trip mismatch:\n got: %s\nwant: %s", round, src)
	}
}

func TestParamsValueTypes(t *testing.T) {
	src := `{"ngl":99,"temp":0.6,"fa":"on","jinja":true,"metrics":false}`
	var p Params
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cases := map[string]any{
		"ngl":     json.Number("99"),
		"temp":    json.Number("0.6"),
		"fa":      "on",
		"jinja":   true,
		"metrics": false,
	}
	for k, want := range cases {
		got, ok := p.Get(k)
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		if got != want {
			t.Errorf("p[%q] = %#v, want %#v", k, got, want)
		}
	}
}

func TestParamsRejectsObjectAndArrayValues(t *testing.T) {
	cases := []struct {
		name, in, wantSubstr string
	}{
		{"object value", `{"a":{"x":1}}`, "object values are not supported"},
		{"array value", `{"a":[1,2,3]}`, "array values are not supported"},
		{"null value", `{"a":null}`, "null is not a valid value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Params
			err := json.Unmarshal([]byte(tc.in), &p)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %v missing substring %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestParamsNumericStaysVerbatim(t *testing.T) {
	// "0.0", "0.00", and "0" are distinct in source — preserve them so users
	// can write --presence-penalty 0.0 vs 0.
	src := `{"a":0,"b":0.0,"c":0.00,"d":1e2}`
	var p Params
	if err := json.Unmarshal([]byte(src), &p); err != nil {
		t.Fatal(err)
	}
	want := []string{"0", "0.0", "0.00", "1e2"}
	for i, w := range want {
		num, ok := p[i].Value.(json.Number)
		if !ok {
			t.Fatalf("p[%d].Value = %T, want json.Number", i, p[i].Value)
		}
		if num.String() != w {
			t.Fatalf("p[%d] = %q, want %q", i, num.String(), w)
		}
	}
}

func TestParamsSetReplacesInPlace(t *testing.T) {
	var p Params
	p.Set("a", json.Number("1"))
	p.Set("b", json.Number("2"))
	p.Set("a", json.Number("99")) // replace, must not move
	if len(p) != 2 || p[0].Key != "a" || p[0].Value != json.Number("99") {
		t.Fatalf("Set should replace in place, got %v", p)
	}
}

func TestParamsDelete(t *testing.T) {
	p := Params{{Key: "a", Value: true}, {Key: "b", Value: false}}
	if !p.Delete("a") || len(p) != 1 || p[0].Key != "b" {
		t.Fatalf("Delete failed, got %v", p)
	}
	if p.Delete("zz") {
		t.Fatalf("Delete should return false for missing key")
	}
}

func TestParamsEmptyAndNull(t *testing.T) {
	var p Params
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatal(err)
	}
	if len(p) != 0 {
		t.Fatalf("empty object should yield empty Params, got %v", p)
	}

	if err := json.Unmarshal([]byte(`null`), &p); err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("null should yield nil Params, got %v", p)
	}
}
