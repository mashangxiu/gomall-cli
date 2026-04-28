package gomallapi

import "testing"

func TestEnvelopeDecodeData(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Code:      200,
		Message:   "success",
		RequestID: "rid-1",
		Data:      []byte(`{"token":"abc","expireTime":123,"username":"gomall"}`),
	}

	var out struct {
		Token      string `json:"token"`
		ExpireTime int64  `json:"expireTime"`
		Username   string `json:"username"`
	}

	if err := env.DecodeData(&out); err != nil {
		t.Fatalf("DecodeData() error = %v", err)
	}
	if out.Token != "abc" || out.ExpireTime != 123 || out.Username != "gomall" {
		t.Fatalf("DecodeData() = %+v", out)
	}
}
