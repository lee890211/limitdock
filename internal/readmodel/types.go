package readmodel

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type ReadModel struct {
	Snapshots map[string]*Snapshot `json:"snapshots"`
	Raw       map[string]any       `json:"-"`
}

type Snapshot struct {
	ProviderID string            `json:"provider_id"`
	AccountID  string            `json:"account_id"`
	Status     string            `json:"status"`
	Message    string            `json:"message"`
	Metrics    map[string]Metric `json:"metrics"`
	Resets     map[string]any    `json:"resets"`
	Attributes map[string]any    `json:"attributes"`
	Raw        map[string]any    `json:"raw"`
}

type Metric struct {
	Used      *float64       `json:"used,omitempty"`
	Limit     *float64       `json:"limit,omitempty"`
	Remaining *float64       `json:"remaining,omitempty"`
	Unit      string         `json:"unit,omitempty"`
	Window    any            `json:"window,omitempty"`
	Raw       map[string]any `json:"-"`
}

func (m *Metric) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Raw = raw
	m.Unit = stringValue(raw["unit"])
	m.Window = raw["window"]
	m.Used = floatPtr(raw["used"])
	m.Limit = floatPtr(raw["limit"])
	m.Remaining = floatPtr(raw["remaining"])
	return nil
}

func (m Metric) MarshalJSON() ([]byte, error) {
	raw := map[string]any{}
	for k, v := range m.Raw {
		raw[k] = v
	}
	if m.Unit != "" {
		raw["unit"] = m.Unit
	}
	if m.Window != nil {
		raw["window"] = m.Window
	}
	if m.Used != nil {
		raw["used"] = *m.Used
	}
	if m.Limit != nil {
		raw["limit"] = *m.Limit
	}
	if m.Remaining != nil {
		raw["remaining"] = *m.Remaining
	}
	return json.Marshal(raw)
}

func (rm *ReadModel) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	type alias ReadModel
	var decoded alias
	if err := json.Unmarshal(b, &decoded); err != nil {
		return err
	}
	*rm = ReadModel(decoded)
	rm.Raw = raw
	if rm.Snapshots == nil {
		rm.Snapshots = map[string]*Snapshot{}
	}
	return nil
}

func (s *Snapshot) ResetTime(metric string) (time.Time, bool) {
	if s == nil || s.Resets == nil {
		return time.Time{}, false
	}
	v, ok := s.Resets[metric]
	if !ok {
		return time.Time{}, false
	}
	switch x := v.(type) {
	case string:
		return parseTime(x)
	case fmt.Stringer:
		return parseTime(x.String())
	default:
		return parseTime(fmt.Sprint(x))
	}
}

func String(v any) string {
	return stringValue(v)
}

func AttrString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(m[key]))
}

func floatPtr(v any) *float64 {
	switch x := v.(type) {
	case nil:
		return nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return &x
	case float32:
		f := float64(x)
		return &f
	case int:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	case json.Number:
		f, err := x.Float64()
		if err == nil {
			return &f
		}
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil {
			return &f
		}
	}
	return nil
}

func stringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
