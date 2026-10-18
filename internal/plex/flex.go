package plex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Int decodes from a JSON number or a quoted number. Plex is inconsistent
// about which it emits, particularly in /status/sessions.
type Int int64

func (i *Int) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) || bytes.Equal(b, []byte(`""`)) {
		*i = 0
		return nil
	}
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("plex: bad integer %s", b)
	}
	*i = Int(f)
	return nil
}

// Bool decodes from a JSON bool, 0/1, or the strings "0"/"1"/"true"/"false".
type Bool bool

func (v *Bool) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	switch x := raw.(type) {
	case nil:
		*v = false
	case bool:
		*v = Bool(x)
	case float64:
		*v = Bool(x != 0)
	case string:
		*v = Bool(x == "1" || strings.EqualFold(x, "true"))
	default:
		return fmt.Errorf("plex: bad boolean %s", b)
	}
	return nil
}
