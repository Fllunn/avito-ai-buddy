package agent

import (
	"bytes"
	"encoding/json"
	"errors"
)

var errSchema = errors.New("INVALID_LLM_SCHEMA")

func (i *Intent) UnmarshalJSON(data []byte) error {
	var wire struct {
		Name       *string  `json:"name"`
		Slots      *Slots   `json:"slots"`
		Confidence *float64 `json:"confidence"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.Name == nil || wire.Slots == nil || wire.Confidence == nil {
		return errSchema
	}
	*i = Intent{Name: *wire.Name, Slots: *wire.Slots, Confidence: *wire.Confidence}
	return nil
}
func (s *FragmentSelection) UnmarshalJSON(data []byte) error {
	var wire struct {
		DocumentID *string `json:"document_id"`
		FragmentID *string `json:"fragment_id"`
		Start      *int    `json:"start"`
		End        *int    `json:"end"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.DocumentID == nil || wire.FragmentID == nil || wire.Start == nil || wire.End == nil {
		return errSchema
	}
	*s = FragmentSelection{*wire.DocumentID, *wire.FragmentID, *wire.Start, *wire.End}
	return nil
}
