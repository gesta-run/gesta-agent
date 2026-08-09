package hookinstall

import "encoding/json"

type codexHooksFile struct {
	Hooks map[string][]codexHookGroup
	Extra map[string]json.RawMessage
}

type codexHookGroup struct {
	Matcher string
	Hooks   []codexHookCommand
	Extra   map[string]json.RawMessage
}

type codexHookCommand struct {
	Type           string
	Command        string
	CommandWindows string
	Timeout        int
	StatusMessage  string
	Extra          map[string]json.RawMessage
}

func (value *codexHooksFile) UnmarshalJSON(data []byte) error {
	*value = codexHooksFile{}
	raw, err := decodeJSONObject(data)
	if err != nil {
		return err
	}
	if hooks, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooks, &value.Hooks); err != nil {
			return err
		}
		delete(raw, "hooks")
	}
	value.Extra = raw
	return nil
}

func (value codexHooksFile) MarshalJSON() ([]byte, error) {
	raw := cloneRawFields(value.Extra)
	if err := setRawField(raw, "hooks", value.Hooks); err != nil {
		return nil, err
	}
	return json.Marshal(raw)
}

func (value *codexHookGroup) UnmarshalJSON(data []byte) error {
	*value = codexHookGroup{}
	raw, err := decodeJSONObject(data)
	if err != nil {
		return err
	}
	if matcher, ok := raw["matcher"]; ok {
		if err := json.Unmarshal(matcher, &value.Matcher); err != nil {
			return err
		}
		delete(raw, "matcher")
	}
	if hooks, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooks, &value.Hooks); err != nil {
			return err
		}
		delete(raw, "hooks")
	}
	value.Extra = raw
	return nil
}

func (value codexHookGroup) MarshalJSON() ([]byte, error) {
	raw := cloneRawFields(value.Extra)
	if value.Matcher != "" {
		if err := setRawField(raw, "matcher", value.Matcher); err != nil {
			return nil, err
		}
	}
	if value.Hooks != nil {
		if err := setRawField(raw, "hooks", value.Hooks); err != nil {
			return nil, err
		}
	}
	return json.Marshal(raw)
}

func (value *codexHookCommand) UnmarshalJSON(data []byte) error {
	*value = codexHookCommand{}
	raw, err := decodeJSONObject(data)
	if err != nil {
		return err
	}
	known := []struct {
		name   string
		target interface{}
	}{
		{name: "type", target: &value.Type},
		{name: "command", target: &value.Command},
		{name: "commandWindows", target: &value.CommandWindows},
		{name: "timeout", target: &value.Timeout},
		{name: "statusMessage", target: &value.StatusMessage},
	}
	for _, field := range known {
		if encoded, ok := raw[field.name]; ok {
			if err := json.Unmarshal(encoded, field.target); err != nil {
				return err
			}
			delete(raw, field.name)
		}
	}
	value.Extra = raw
	return nil
}

func (value codexHookCommand) MarshalJSON() ([]byte, error) {
	raw := cloneRawFields(value.Extra)
	if value.Type != "" {
		if err := setRawField(raw, "type", value.Type); err != nil {
			return nil, err
		}
	}
	if value.Command != "" {
		if err := setRawField(raw, "command", value.Command); err != nil {
			return nil, err
		}
	}
	if value.CommandWindows != "" {
		if err := setRawField(raw, "commandWindows", value.CommandWindows); err != nil {
			return nil, err
		}
	}
	if value.Timeout != 0 {
		if err := setRawField(raw, "timeout", value.Timeout); err != nil {
			return nil, err
		}
	}
	if value.StatusMessage != "" {
		if err := setRawField(raw, "statusMessage", value.StatusMessage); err != nil {
			return nil, err
		}
	}
	return json.Marshal(raw)
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

func cloneRawFields(source map[string]json.RawMessage) map[string]json.RawMessage {
	target := make(map[string]json.RawMessage, len(source)+4)
	for key, value := range source {
		target[key] = value
	}
	return target
}

func setRawField(target map[string]json.RawMessage, name string, value interface{}) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	target[name] = encoded
	return nil
}
