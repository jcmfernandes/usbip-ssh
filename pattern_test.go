package main

import "testing"

const specKbd = "1-1.4  03f0:e111  001/004  ABC123 HP Keyboard\t030101 030102"

func TestMkDevPattern(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{"1-1.4", true}, // busid, anchored
		{"1-1", false},  // busid must match exactly
		{"03f0:e111", true},
		{"3f0:e111", true}, // leading zeros optional
		{"3F0:E111", true}, // hex case-insensitive
		{"03f0:", true},    // vid only
		{":e111", true},    // pid only
		{":1111", false},
		{"Keyboard", true},
		{"keyboard", false}, // free text is case-sensitive
		{"HP Keyboard", true},
		{"030102", true},     // interface spec
		{"!Keyboard", false}, // negation
		{"!Mouse", true},
		{"", true}, // empty pattern matches everything
	}
	for _, c := range cases {
		p, err := mkDevPattern(c.pattern)
		if err != nil {
			t.Errorf("mkDevPattern(%q): %v", c.pattern, err)
			continue
		}
		if got := p.match(specKbd); got != c.want {
			t.Errorf("pattern %q match = %v, want %v", c.pattern, got, c.want)
		}
	}
}

func TestMkDevPatternBad(t *testing.T) {
	if _, err := mkDevPattern("("); err == nil {
		t.Error("mkDevPattern(\"(\") should fail")
	}
}

func TestDevPatternString(t *testing.T) {
	p, _ := mkDevPattern("!Mouse")
	if p.String() != "!Mouse" {
		t.Errorf("String() = %q", p.String())
	}
}
