package objectstorage

import (
	"bytes"
	"encoding/json"
)

func ValidatePolicy(policy string) error {
	if policy == "" {
		return nil
	}
	if len(policy) > MaxPolicyBytes {
		return ErrPolicyInvalid
	}
	var document struct {
		Statement []json.RawMessage `json:"Statement"`
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(policy)))
	if err := decoder.Decode(&document); err != nil || len(document.Statement) == 0 {
		return ErrPolicyInvalid
	}
	return nil
}
