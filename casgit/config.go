// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package casgit

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/google/abfs/shared/entities"
)

type GitConfig struct {
	sections []*configSection
}

type configSection struct {
	name       string
	subsection string
	hasSub     bool
	header     string
	entries    []*configEntry
	trailing   []string
}

type configEntry struct {
	key   string
	value string
	raw   string
}

func ParseGitConfig(r io.Reader) (*GitConfig, error) {
	cfg := &GitConfig{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	var current *configSection
	var pendingComments []string
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			if current == nil {
				pendingComments = append(pendingComments, raw)
			} else {
				current.trailing = append(current.trailing, raw)
			}
			continue
		}

		if strings.HasPrefix(line, "[") {
			sec, err := parseSectionHeader(line)
			if err != nil {
				return nil, fmt.Errorf("config line %d: %w", lineNo, err)
			}
			sec.header = raw
			if current != nil {
				moveTrailingToNext(current)
			}
			if len(pendingComments) > 0 {
				cfg.sections = append(cfg.sections, &configSection{trailing: pendingComments})
				pendingComments = nil
			}
			cfg.sections = append(cfg.sections, sec)
			current = sec
			continue
		}

		if current == nil {
			return nil, fmt.Errorf("config line %d: variable outside any section", lineNo)
		}

		key, value, err := parseEntryLine(raw, scanner)
		if err != nil {
			return nil, fmt.Errorf("config line %d: %w", lineNo, err)
		}
		ent := &configEntry{
			key:   strings.ToLower(key),
			value: value,
			raw:   raw,
		}
		if len(current.trailing) > 0 {
			ent.raw = strings.Join(current.trailing, "\n") + "\n" + raw
			current.trailing = nil
		}
		current.entries = append(current.entries, ent)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func moveTrailingToNext(_ *configSection) {}

func parseSectionHeader(line string) (*configSection, error) {
	if !strings.HasSuffix(line, "]") {
		return nil, fmt.Errorf("unterminated section header: %q", line)
	}
	inner := strings.TrimSpace(line[1 : len(line)-1])
	if inner == "" {
		return nil, fmt.Errorf("empty section header")
	}

	sec := &configSection{}
	if i := strings.IndexAny(inner, " \t"); i >= 0 {
		sec.name = strings.ToLower(strings.TrimSpace(inner[:i]))
		sub := strings.TrimSpace(inner[i:])
		if !strings.HasPrefix(sub, `"`) || !strings.HasSuffix(sub, `"`) || len(sub) < 2 {
			return nil, fmt.Errorf("invalid subsection: %q", sub)
		}
		decoded, err := unquoteSubsection(sub[1 : len(sub)-1])
		if err != nil {
			return nil, err
		}
		sec.subsection = decoded
		sec.hasSub = true
	} else if dot := strings.Index(inner, "."); dot >= 0 {
		sec.name = strings.ToLower(inner[:dot])
		sec.subsection = strings.ToLower(inner[dot+1:])
		sec.hasSub = true
	} else {
		sec.name = strings.ToLower(inner)
	}

	if !validSectionName(sec.name) {
		return nil, fmt.Errorf("invalid section name: %q", sec.name)
	}
	return sec, nil
}

func validSectionName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '.' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func unquoteSubsection(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			if i+1 >= len(s) {
				return "", fmt.Errorf("trailing backslash in subsection")
			}
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

func parseEntryLine(raw string, scanner *bufio.Scanner) (string, string, error) {
	line := raw
	stripped := stripCommentOutsideQuotes(line)
	stripped = strings.TrimLeft(stripped, " \t")

	eq := -1
	for i := 0; i < len(stripped); i++ {
		c := stripped[i]
		if c == '=' {
			eq = i
			break
		}
		if !isNameByte(c) && c != ' ' && c != '\t' {
			return "", "", fmt.Errorf("invalid character in name: %q", c)
		}
	}

	var key, valuePart string
	if eq < 0 {
		key = strings.TrimSpace(stripped)
		return key, "true", nil
	}
	key = strings.TrimSpace(stripped[:eq])
	valuePart = stripped[eq+1:]

	value, cont, err := decodeValue(valuePart)
	if err != nil {
		return "", "", err
	}
	for cont {
		if !scanner.Scan() {
			return "", "", fmt.Errorf("unexpected EOF in continued value")
		}
		next := stripCommentOutsideQuotes(scanner.Text())
		var more string
		more, cont, err = decodeValue(next)
		if err != nil {
			return "", "", err
		}
		value += more
	}
	return key, value, nil
}

func isNameByte(c byte) bool {
	return c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func stripCommentOutsideQuotes(line string) string {
	inQuote := false
	esc := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && (c == '#' || c == ';') {
			return line[:i]
		}
	}
	return line
}

func decodeValue(s string) (string, bool, error) {
	s = strings.TrimLeft(s, " \t")
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			if i+1 >= len(s) {
				return strings.TrimRight(b.String(), " \t"), true, nil
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				return "", false, fmt.Errorf("invalid escape \\%c", s[i])
			}
			continue
		}
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		b.WriteByte(c)
	}
	if inQuote {
		return "", false, fmt.Errorf("unterminated quoted value")
	}
	out := b.String()
	if !strings.Contains(s, `"`) {
		out = strings.TrimRight(out, " \t")
	}
	return out, false, nil
}

func (c *GitConfig) section(name, subsection string, hasSub bool) *configSection {
	name = strings.ToLower(name)
	for _, s := range c.sections {
		if s.name == "" {
			continue
		}
		if s.name == name && s.hasSub == hasSub && s.subsection == subsection {
			return s
		}
	}
	return nil
}

func splitConfigKey(key string) (section, subsection, name string, hasSub bool, err error) {
	first := strings.Index(key, ".")
	last := strings.LastIndex(key, ".")
	if first < 0 || first == len(key)-1 {
		return "", "", "", false, fmt.Errorf("key %q does not contain a section", key)
	}
	section = strings.ToLower(key[:first])
	name = strings.ToLower(key[last+1:])
	if first != last {
		subsection = key[first+1 : last]
		hasSub = true
	}
	if !validSectionName(section) {
		return "", "", "", false, fmt.Errorf("invalid section in key %q", key)
	}
	if !validVariableName(name) {
		return "", "", "", false, fmt.Errorf("invalid variable name in key %q", key)
	}
	return section, subsection, name, hasSub, nil
}

func validVariableName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func (c *GitConfig) GetAll(key string) ([]string, error) {
	section, subsection, name, hasSub, err := splitConfigKey(key)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range c.sections {
		if s.name != section || s.hasSub != hasSub || s.subsection != subsection {
			continue
		}
		for _, e := range s.entries {
			if e.key == name {
				out = append(out, e.value)
			}
		}
	}
	return out, nil
}

func (c *GitConfig) Get(key string) (string, bool, error) {
	vals, err := c.GetAll(key)
	if err != nil {
		return "", false, err
	}
	if len(vals) == 0 {
		return "", false, nil
	}
	return vals[len(vals)-1], true, nil
}

func (c *GitConfig) KeysWithPrefix(prefix string) []string {
	var prefixParts []string
	if prefix != "" {
		prefixParts = strings.Split(prefix, ".")
	}
	var out []string
	for _, s := range c.sections {
		if s.name == "" {
			continue
		}
		for _, e := range s.entries {
			full := s.name
			if s.hasSub {
				full += "." + s.subsection
			}
			full += "." + e.key
			if hasDotPrefix(full, prefixParts) {
				out = append(out, full)
			}
		}
	}
	return out
}

type ConfigGroup struct {
	Name   string
	Keys   []string
	Values []string
}

func (c *GitConfig) Group(prefix string) []ConfigGroup {
	var prefixParts []string
	if prefix != "" {
		prefixParts = strings.Split(prefix, ".")
	}

	groups := make([]ConfigGroup, 0)
	groupsMap := make(map[string]*ConfigGroup)

	for _, s := range c.sections {
		if s.name == "" {
			continue
		}
		for _, e := range s.entries {
			full := s.name
			if s.hasSub {
				full += "." + s.subsection
			}
			full += "." + e.key
			if hasDotPrefix(full, prefixParts) {
				parts := strings.SplitN(full, ".", 3)
				name := parts[1]
				var subKey string
				if len(parts) == 3 {
					subKey = parts[2]
				}
				g, ok := groupsMap[name]
				if !ok {
					groups = append(groups, ConfigGroup{Name: name})
					g = &groups[len(groups)-1]
					groupsMap[name] = g
				}
				g.Keys = append(g.Keys, subKey)
				g.Values = append(g.Values, e.value)
			}
		}
	}

	return groups
}

func hasDotPrefix(key string, prefixParts []string) bool {
	if len(prefixParts) == 0 {
		return true
	}
	parts := strings.Split(key, ".")
	if len(prefixParts) > len(parts) {
		return false
	}
	for i, p := range prefixParts {
		if parts[i] != p {
			return false
		}
	}
	return true
}

func (c *GitConfig) Add(key, value string) error {
	section, subsection, name, hasSub, err := splitConfigKey(key)
	if err != nil {
		return err
	}
	sec := c.section(section, subsection, hasSub)
	if sec == nil {
		sec = &configSection{name: section, subsection: subsection, hasSub: hasSub}
		sec.header = formatSectionHeader(section, subsection, hasSub)
		c.sections = append(c.sections, sec)
	}
	sec.entries = append(sec.entries, &configEntry{
		key:   name,
		value: value,
		raw:   "\t" + name + " = " + encodeValue(value),
	})
	return nil
}

func (c *GitConfig) Set(key, value string) error {
	section, subsection, name, hasSub, err := splitConfigKey(key)
	if err != nil {
		return err
	}
	sec := c.section(section, subsection, hasSub)
	if sec == nil {
		return c.Add(key, value)
	}
	for i := len(sec.entries) - 1; i >= 0; i-- {
		if sec.entries[i].key == name {
			sec.entries[i].value = value
			sec.entries[i].raw = "\t" + name + " = " + encodeValue(value)
			return nil
		}
	}
	return c.Add(key, value)
}

func (c *GitConfig) Unset(key string) error {
	section, subsection, name, hasSub, err := splitConfigKey(key)
	if err != nil {
		return err
	}
	sec := c.section(section, subsection, hasSub)
	if sec == nil {
		return nil
	}
	out := sec.entries[:0]
	for _, e := range sec.entries {
		if e.key != name {
			out = append(out, e)
		}
	}
	sec.entries = out
	return nil
}

func formatSectionHeader(name, subsection string, hasSub bool) string {
	if !hasSub {
		return "[" + name + "]"
	}
	return "[" + name + ` "` + escapeSubsection(subsection) + `"]`
}

func escapeSubsection(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == '"' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

func encodeValue(v string) string {
	needQuote := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '"' || c == '\\' || c == '\n' || c == '\t' || c == '\b' || c == '#' || c == ';' {
			needQuote = true
			break
		}
	}
	if !needQuote && (strings.HasPrefix(v, " ") || strings.HasSuffix(v, " ") || strings.HasPrefix(v, "\t") || strings.HasSuffix(v, "\t")) {
		needQuote = true
	}
	if !needQuote {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (c *GitConfig) Write(w io.Writer) error {
	bw := bufio.NewWriter(w)
	for _, s := range c.sections {
		if s.name != "" {
			if s.header != "" {
				if _, err := fmt.Fprintln(bw, s.header); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(bw, formatSectionHeader(s.name, s.subsection, s.hasSub)); err != nil {
					return err
				}
			}
		}
		for _, e := range s.entries {
			if _, err := fmt.Fprintln(bw, e.raw); err != nil {
				return err
			}
		}
		for _, t := range s.trailing {
			if _, err := fmt.Fprintln(bw, t); err != nil {
				return err
			}
		}
	}
	return bw.Flush()
}

func ReadGitConfig(gitDir string) (*GitConfig, error) {
	p := path.Join(gitDir, "config")
	data, err := os.ReadFile(p)
	if err != nil {
		if entities.IsNotExist(err) {
			return &GitConfig{}, nil
		}
		return nil, err
	}
	return ParseGitConfig(bytes.NewReader(data))
}

func WriteGitConfig(gitDir string, cfg *GitConfig) error {
	var buf bytes.Buffer
	if err := cfg.Write(&buf); err != nil {
		return err
	}
	return WriteFileAtomic(path.Join(gitDir, "config"), buf.Bytes(), 0644)
}
