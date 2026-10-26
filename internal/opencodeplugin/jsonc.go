package opencodeplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Span is a byte range [Start, End) of a JSONC document.
type Span struct{ Start, End int }

// Element is one element of a plugin array: its bytes and its plugin spec,
// the string itself or the first string of a [spec, options] tuple.
type Element struct {
	Span
	Spec string
}

// Array is the top-level plugin array of a config file: the offsets of its
// brackets and its elements.
type Array struct {
	Open, Close int
	Elements    []Element
}

// ReadPluginArray reads a JSONC config file and finds the plugin array of its
// top-level object. It returns a nil array when the file is absent or has no
// plugin key.
func ReadPluginArray(path string) ([]byte, *Array, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read OpenCode config %s: %w", path, err)
	}
	array, err := FindPluginArray(data)
	if err != nil {
		return nil, nil, fmt.Errorf("read OpenCode config %s: %w", path, err)
	}
	return data, array, nil
}

// FindPluginArray finds the plugin array of a JSONC document's top-level
// object, or nil when it has no plugin key.
func FindPluginArray(data []byte) (*Array, error) {
	s := &scanner{data: data}
	if err := s.skip(); err != nil {
		return nil, err
	}
	if !s.at('{') {
		return nil, s.fail("expected an object")
	}
	s.pos++
	var plugin *Array
	for {
		if err := s.skip(); err != nil {
			return nil, err
		}
		if s.at('}') {
			s.pos++
			break
		}
		key, _, err := s.str()
		if err != nil {
			return nil, err
		}
		if err := s.skip(); err != nil {
			return nil, err
		}
		if !s.at(':') {
			return nil, s.fail("expected ':'")
		}
		s.pos++
		if err := s.skip(); err != nil {
			return nil, err
		}
		if key == "plugin" {
			if !s.at('[') {
				return nil, s.fail("plugin is not an array")
			}
			if plugin, err = s.pluginArray(); err != nil {
				return nil, err
			}
		} else if _, err := s.value(); err != nil {
			return nil, err
		}
		if err := s.skip(); err != nil {
			return nil, err
		}
		if s.at(',') {
			s.pos++
			continue
		}
		if !s.at('}') {
			return nil, s.fail("expected ',' or '}'")
		}
	}
	if err := s.skip(); err != nil {
		return nil, err
	}
	if s.pos != len(data) {
		return nil, s.fail("unexpected content after the object")
	}
	return plugin, nil
}

// RemoveArrayElement returns data without element index of array, its
// separating comma and the comments on its own lines. Comments that belong to
// other entries, trailing the previous entry's line or on lines above the next
// entry, stay byte for byte.
func RemoveArrayElement(data []byte, array Array, index int) []byte {
	elements := array.Elements
	element := elements[index]
	comma := -1
	if s := (&scanner{data: data, pos: element.End}); s.skip() == nil && s.at(',') {
		comma = s.pos
	}
	var cuts []Span
	if start, end, ok := ownLines(data, element.Span, comma); ok {
		cuts = append(cuts, Span{start, end})
	} else if comma >= 0 {
		end := comma + 1
		for end < len(data) && (data[end] == ' ' || data[end] == '\t') {
			end++
		}
		cuts = append(cuts, Span{element.Start, end})
	} else {
		start := element.Start
		if index > 0 {
			for start > elements[index-1].End && (data[start-1] == ' ' || data[start-1] == '\t') {
				start--
			}
		}
		cuts = append(cuts, Span{start, element.End})
	}
	// The last element has no comma of its own: the one before it separates it.
	if comma < 0 && index > 0 {
		if s := (&scanner{data: data, pos: elements[index-1].End}); s.skip() == nil && s.at(',') {
			cuts = append([]Span{{s.pos, s.pos + 1}}, cuts...)
		}
	}
	out := []byte{}
	previous, removed := 0, 0
	for _, cut := range cuts {
		out = append(out, data[previous:cut.Start]...)
		previous = cut.End
		removed += cut.End - cut.Start
	}
	out = append(out, data[previous:]...)
	if len(elements) == 1 {
		closing := array.Close - removed
		if strings.TrimSpace(string(out[array.Open+1:closing])) == "" {
			out = append(append([]byte{}, out[:array.Open+1]...), out[closing:]...)
		}
	}
	return out
}

// ownLines returns the whole lines an element occupies when nothing but
// whitespace precedes it on its first line and nothing but its comma,
// whitespace and comments follows it on its last line.
func ownLines(data []byte, element Span, comma int) (int, int, bool) {
	start := element.Start
	for start > 0 && (data[start-1] == ' ' || data[start-1] == '\t') {
		start--
	}
	if start > 0 && data[start-1] != '\n' {
		return 0, 0, false
	}
	pos := element.End
	if comma >= 0 {
		if strings.Contains(string(data[element.End:comma]), "\n") {
			return 0, 0, false
		}
		pos = comma + 1
	}
	for pos < len(data) {
		switch c := data[pos]; {
		case c == '\n':
			return start, pos + 1, true
		case c == ' ' || c == '\t' || c == '\r':
			pos++
		case c == '/' && pos+1 < len(data) && data[pos+1] == '/':
			for pos < len(data) && data[pos] != '\n' {
				pos++
			}
		case c == '/' && pos+1 < len(data) && data[pos+1] == '*':
			end := strings.Index(string(data[pos+2:]), "*/")
			if end < 0 || strings.Contains(string(data[pos+2:pos+2+end]), "\n") {
				return 0, 0, false
			}
			pos += end + 4
		default:
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// scanner walks JSON with comments and trailing commas, the format OpenCode
// reads its config in.
type scanner struct {
	data []byte
	pos  int
}

func (s *scanner) at(c byte) bool { return s.pos < len(s.data) && s.data[s.pos] == c }

func (s *scanner) fail(message string) error {
	return fmt.Errorf("invalid JSONC at byte %d: %s", s.pos, message)
}

// skip moves past whitespace and comments.
func (s *scanner) skip() error {
	for s.pos < len(s.data) {
		switch c := s.data[s.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.pos++
		case c == '/' && s.pos+1 < len(s.data) && s.data[s.pos+1] == '/':
			for s.pos < len(s.data) && s.data[s.pos] != '\n' {
				s.pos++
			}
		case c == '/' && s.pos+1 < len(s.data) && s.data[s.pos+1] == '*':
			end := strings.Index(string(s.data[s.pos+2:]), "*/")
			if end < 0 {
				return s.fail("unterminated comment")
			}
			s.pos += end + 4
		default:
			return nil
		}
	}
	return nil
}

// str reads a string and returns its value.
func (s *scanner) str() (string, Span, error) {
	if !s.at('"') {
		return "", Span{}, s.fail("expected a string")
	}
	start := s.pos
	for s.pos++; s.pos < len(s.data); s.pos++ {
		switch s.data[s.pos] {
		case '\\':
			s.pos++
		case '"':
			s.pos++
			var value string
			if err := json.Unmarshal(s.data[start:s.pos], &value); err != nil {
				return "", Span{}, s.fail(err.Error())
			}
			return value, Span{start, s.pos}, nil
		}
	}
	return "", Span{}, s.fail("unterminated string")
}

// value moves past any value and returns its span.
func (s *scanner) value() (Span, error) {
	start := s.pos
	if s.pos >= len(s.data) {
		return Span{}, s.fail("expected a value")
	}
	switch s.data[s.pos] {
	case '"':
		_, span, err := s.str()
		return span, err
	case '{', '[':
		closing := byte('}')
		if s.data[s.pos] == '[' {
			closing = ']'
		}
		object := closing == '}'
		s.pos++
		for {
			if err := s.skip(); err != nil {
				return Span{}, err
			}
			if s.at(closing) {
				s.pos++
				return Span{start, s.pos}, nil
			}
			if object {
				if _, _, err := s.str(); err != nil {
					return Span{}, err
				}
				if err := s.skip(); err != nil {
					return Span{}, err
				}
				if !s.at(':') {
					return Span{}, s.fail("expected ':'")
				}
				s.pos++
				if err := s.skip(); err != nil {
					return Span{}, err
				}
			}
			if _, err := s.value(); err != nil {
				return Span{}, err
			}
			if err := s.skip(); err != nil {
				return Span{}, err
			}
			if s.at(',') {
				s.pos++
			} else if !s.at(closing) {
				return Span{}, s.fail("expected ',' or a closing bracket")
			}
		}
	}
	for s.pos < len(s.data) && !strings.ContainsRune(",]} \t\r\n/", rune(s.data[s.pos])) {
		s.pos++
	}
	if s.pos == start {
		return Span{}, s.fail("expected a value")
	}
	var literal any
	if err := json.Unmarshal(s.data[start:s.pos], &literal); err != nil {
		return Span{}, s.fail("invalid literal")
	}
	return Span{start, s.pos}, nil
}

// pluginArray reads the plugin array and records each element's span and
// spec.
func (s *scanner) pluginArray() (*Array, error) {
	array := &Array{Open: s.pos}
	s.pos++
	for {
		if err := s.skip(); err != nil {
			return nil, err
		}
		if s.at(']') {
			array.Close = s.pos
			s.pos++
			return array, nil
		}
		span, err := s.value()
		if err != nil {
			return nil, err
		}
		element := Element{Span: span}
		inner := &scanner{data: s.data, pos: span.Start}
		if inner.at('[') {
			inner.pos++
			if err := inner.skip(); err != nil {
				return nil, err
			}
		}
		if inner.at('"') {
			if element.Spec, _, err = inner.str(); err != nil {
				return nil, err
			}
		}
		array.Elements = append(array.Elements, element)
		if err := s.skip(); err != nil {
			return nil, err
		}
		if s.at(',') {
			s.pos++
		} else if !s.at(']') {
			return nil, s.fail("expected ',' or ']'")
		}
	}
}
