package flags

import "testing"

func TestCanonicalFormFallbackWhenNoRegistry(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ctx-size", "--ctx-size"},
		{"m", "-m"},
		{"hf", "-hf"},
		{"alias", "--alias"},
		{"host", "--host"},
		{"port", "--port"},
	}
	for _, c := range cases {
		if got := CanonicalForm(c.in, nil); got != c.want {
			t.Errorf("CanonicalForm(%q, nil) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalFormRegistryWins(t *testing.T) {
	// Registry's Form wins over the fallback short-set when both apply.
	reg := Registry{
		"m":   {Name: "m", Form: "--model"}, // contrived: pretend --help renamed it
		"foo": {Name: "foo", Form: "-foo"},
	}
	if got := CanonicalForm("m", reg); got != "--model" {
		t.Errorf("CanonicalForm(m) with registry = %q, want --model", got)
	}
	if got := CanonicalForm("foo", reg); got != "-foo" {
		t.Errorf("CanonicalForm(foo) with registry = %q, want -foo", got)
	}
}

func TestCanonicalFormFallbackWhenKeyMissing(t *testing.T) {
	// Registry is non-nil but doesn't know the key → fall back to Canonical.
	reg := Registry{"other": {Name: "other", Form: "--other"}}
	if got := CanonicalForm("ctx-size", reg); got != "--ctx-size" {
		t.Errorf("CanonicalForm(ctx-size) with partial registry = %q, want --ctx-size", got)
	}
	if got := CanonicalForm("ngl", reg); got != "-ngl" {
		t.Errorf("CanonicalForm(ngl) with partial registry = %q, want -ngl", got)
	}
}
