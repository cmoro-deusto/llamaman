package flags

import "testing"

func TestCanonicalShort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"m", "-m"},
		{"ngl", "-ngl"},
		{"ctk", "-ctk"},
		{"fa", "-fa"},
		{"alias", "--alias"},
		{"ctx-size", "--ctx-size"},
		{"top-p", "--top-p"},
		{"no-mmproj", "--no-mmproj"},
	}
	for _, c := range cases {
		if got := Canonical(c.in); got != c.want {
			t.Errorf("Canonical(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
