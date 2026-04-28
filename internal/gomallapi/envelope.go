package gomallapi

import (
	"encoding/json"
	"fmt"
)

// Envelope is the common response schema returned by gomall APIs.
type Envelope struct {
	Code       int             `json:"code"`
	Message    string          `json:"message"`
	RequestID  string          `json:"request_id"`
	RequestID2 string          `json:"requestId"`
	Timestamp  int64           `json:"timestamp"`
	Data       json.RawMessage `json:"data"`
}

func (e Envelope) EffectiveRequestID() string {
	if e.RequestID != "" {
		return e.RequestID
	}
	return e.RequestID2
}

func (e Envelope) DecodeData(out any) error {
	if out == nil {
		return nil
	}
	if len(e.Data) == 0 || string(e.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(e.Data, out); err != nil {
		return fmt.Errorf("decode response data: %w", err)
	}
	return nil
}
