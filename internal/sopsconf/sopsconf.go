// Package sopsconf reads and edits a .sops.yaml: the key anchors and the
// creation rules that say which keys encrypt which files. Edits are made
// line by line so comments, anchors and layout survive, and every edit is
// checked by parsing the result again.
package sopsconf

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Key is a "- &anchor value" entry under keys:.
type Key struct {
	Anchor string
	Value  string
	line   int // 0-based
}

// Rule is a creation rule. PGP and Age are the anchors its single key group
// refers to.
type Rule struct {
	PathRegex string
	PGP       []string
	Age       []string
	line      int // the "- path_regex" line, 0-based
	ageLine   int // the "age: [...]" line, or -1
}

type Config struct {
	Keys  []Key
	Rules []Rule
	lines []string
}

// Parse reads a .sops.yaml.
func Parse(b []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf(".sops.yaml: %w", err)
	}
	c := &Config{lines: strings.Split(string(b), "\n")}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New(".sops.yaml: expected a mapping at the top")
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		switch top.Content[i].Value {
		case "keys":
			for _, n := range top.Content[i+1].Content {
				if n.Kind == yaml.ScalarNode && n.Anchor != "" {
					c.Keys = append(c.Keys, Key{Anchor: n.Anchor, Value: n.Value, line: n.Line - 1})
				}
			}
		case "creation_rules":
			for _, r := range top.Content[i+1].Content {
				c.Rules = append(c.Rules, parseRule(r))
			}
		}
	}
	return c, nil
}

func parseRule(n *yaml.Node) Rule {
	r := Rule{line: n.Line - 1, ageLine: -1}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "path_regex":
			r.PathRegex = v.Value
		case "key_groups":
			for _, g := range v.Content {
				for j := 0; j+1 < len(g.Content); j += 2 {
					names := aliases(g.Content[j+1])
					switch g.Content[j].Value {
					case "pgp":
						r.PGP = append(r.PGP, names...)
					case "age":
						r.Age = append(r.Age, names...)
						if g.Content[j+1].Style&yaml.FlowStyle != 0 && g.Content[j+1].Line == g.Content[j].Line {
							r.ageLine = g.Content[j].Line - 1
						}
					}
				}
			}
		}
	}
	return r
}

func aliases(seq *yaml.Node) []string {
	var out []string
	for _, n := range seq.Content {
		if n.Kind == yaml.AliasNode {
			out = append(out, n.Value)
		}
	}
	return out
}

// Bytes is the file as edited.
func (c *Config) Bytes() []byte { return []byte(strings.Join(c.lines, "\n")) }

func (c *Config) Key(anchor string) (Key, bool) {
	i := slices.IndexFunc(c.Keys, func(k Key) bool { return k.Anchor == anchor })
	if i < 0 {
		return Key{}, false
	}
	return c.Keys[i], true
}

func (c *Config) Rule(pathRegex string) (Rule, bool) {
	i := slices.IndexFunc(c.Rules, func(r Rule) bool { return r.PathRegex == pathRegex })
	if i < 0 {
		return Rule{}, false
	}
	return c.Rules[i], true
}

// SetKey sets anchor's value, adding the entry after the last key when
// it's new.
func (c *Config) SetKey(anchor, value string) error {
	if k, ok := c.Key(anchor); ok {
		if k.Value == value {
			return nil
		}
		line := c.lines[k.line]
		i := strings.LastIndex(line, k.Value)
		if i < 0 {
			return fmt.Errorf(".sops.yaml line %d: can't find %s's value to replace", k.line+1, anchor)
		}
		c.lines[k.line] = line[:i] + value + line[i+len(k.Value):]
	} else {
		if len(c.Keys) == 0 {
			return errors.New(".sops.yaml has no keys: entries to add to")
		}
		last := c.Keys[len(c.Keys)-1]
		c.insert(last.line+1, indentOf(c.lines[last.line])+"- &"+anchor+" "+value)
	}
	return c.reparse(func(n *Config) bool { k, ok := n.Key(anchor); return ok && k.Value == value })
}

// AddRule adds a creation rule encrypting pathRegex to pgp and age
// anchors. It goes before the first rule in before, else at the end, laid
// out like the rules already there.
func (c *Config) AddRule(pathRegex string, pgp, age []string, before []string) error {
	if _, ok := c.Rule(pathRegex); ok {
		return nil
	}
	if len(c.Rules) == 0 {
		return errors.New(".sops.yaml has no creation_rules: to add to")
	}
	indent := indentOf(c.lines[c.Rules[0].line])
	block := []string{
		indent + "- path_regex: " + pathRegex,
		indent + "  key_groups:",
		indent + "    - pgp: " + flow(pgp),
		indent + "      age: " + flow(age),
		"",
	}
	at := -1
	for _, r := range c.Rules {
		if slices.Contains(before, r.PathRegex) {
			at = r.line
			break
		}
	}
	if at < 0 {
		at = len(c.lines)
		for at > 0 && strings.TrimSpace(c.lines[at-1]) == "" {
			at--
		}
		block = append([]string{""}, block[:len(block)-1]...)
	}
	c.insert(at, block...)
	return c.reparse(func(n *Config) bool {
		r, ok := n.Rule(pathRegex)
		return ok && slices.Equal(r.PGP, pgp) && slices.Equal(r.Age, age)
	})
}

// JoinRule adds anchor to the age keys of the rule for pathRegex. Only a
// one-line "age: [*a, *b]" list can be edited.
func (c *Config) JoinRule(pathRegex, anchor string) error {
	r, ok := c.Rule(pathRegex)
	if !ok {
		return fmt.Errorf(".sops.yaml has no rule for %s", pathRegex)
	}
	if slices.Contains(r.Age, anchor) {
		return nil
	}
	if r.ageLine < 0 {
		return fmt.Errorf(".sops.yaml: the rule for %s doesn't use a one-line age: [...] list; add *%s to it by hand", pathRegex, anchor)
	}
	line := c.lines[r.ageLine]
	end := strings.LastIndex(line, "]")
	sep := ", "
	if len(r.Age) == 0 {
		sep = ""
	}
	c.lines[r.ageLine] = line[:end] + sep + "*" + anchor + line[end:]
	return c.reparse(func(n *Config) bool { nr, ok := n.Rule(pathRegex); return ok && slices.Contains(nr.Age, anchor) })
}

func (c *Config) insert(at int, lines ...string) {
	c.lines = slices.Insert(c.lines, at, lines...)
}

// reparse replaces c with a fresh parse of its lines, and fails if the
// edit didn't produce what check expects.
func (c *Config) reparse(check func(*Config) bool) error {
	n, err := Parse(c.Bytes())
	if err != nil {
		return fmt.Errorf("edit made .sops.yaml invalid: %w", err)
	}
	if !check(n) {
		return errors.New("edit didn't apply to .sops.yaml as expected; make it by hand")
	}
	*c = *n
	return nil
}

func indentOf(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " "))] }

func flow(anchors []string) string {
	var b bytes.Buffer
	b.WriteString("[")
	for i, a := range anchors {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("*" + a)
	}
	b.WriteString("]")
	return b.String()
}
