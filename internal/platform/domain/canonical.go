package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// CanonicalJSON 只处理JSON结构；调用方必须先按严格schema校验并规范化默认值、时间及金额。
func CanonicalJSON(body []byte) ([]byte, error) {
	check := json.NewDecoder(bytes.NewReader(body))
	check.UseNumber()
	if err := rejectDuplicateKeys(check); err != nil {
		return nil, ErrValidation
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, ErrValidation
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrValidation
	}
	return json.Marshal(object)
}

func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrValidation
			}
			seen[name] = true
			if err = rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err = rejectDuplicateKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return ErrValidation
	}
	_, err = decoder.Token()
	return err
}
