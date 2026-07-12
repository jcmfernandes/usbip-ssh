package main

import (
	"fmt"
	"regexp"
	"strings"
)

type devPattern struct {
	re     *regexp.Regexp
	negate bool
	src    string
}

var (
	busidPatRe     = regexp.MustCompile(`^\d+-\d+(\.\d+)*$`)
	hexBeforeColon = regexp.MustCompile(`(?i)\b0*[0-9a-f]+:`)
	hexAfterColon  = regexp.MustCompile(`(?i):0*[0-9a-f]+\b`)
	wsRun          = regexp.MustCompile(`\s+`)
)

// normHex lowercases a hex token and strips leading zeros (keeping at
// least one character).
func normHex(s string) string {
	s = strings.ToLower(s)
	t := strings.TrimLeft(s, "0")
	if t == "" {
		return s[len(s)-1:]
	}
	return t
}

func mkDevPattern(p string) (*devPattern, error) {
	src := p
	negate := false
	if strings.HasPrefix(p, "!") || strings.HasPrefix(p, "-") {
		negate = true
		p = p[1:]
	}
	if busidPatRe.MatchString(p) {
		p = "^" + regexp.QuoteMeta(p) + " "
	} else {
		p = hexBeforeColon.ReplaceAllStringFunc(p, func(m string) string {
			return `\b0*` + normHex(m[:len(m)-1]) + `\b:`
		})
		p = hexAfterColon.ReplaceAllStringFunc(p, func(m string) string {
			return `:\b0*` + normHex(m[1:]) + `\b`
		})
		p = wsRun.ReplaceAllString(p, " +")
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", src, err)
	}
	return &devPattern{re: re, negate: negate, src: src}, nil
}

func (p *devPattern) match(spec string) bool {
	m := p.re.MatchString(spec)
	if p.negate {
		return !m
	}
	return m
}

func (p *devPattern) String() string { return p.src }

func mustPattern(p string) *devPattern {
	pat, err := mkDevPattern(p)
	if err != nil {
		fatalf("%s", err)
	}
	return pat
}
