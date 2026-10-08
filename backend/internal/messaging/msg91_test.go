package messaging

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
}

// MSG91 can answer HTTP 200 and still refuse the message; those replies must count as failures.
func TestMSG91Result(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		fail   bool
	}{
		{"otp sent", 200, `{"type":"success","request_id":"3461"}`, false},
		{"otp refused with 200", 200, `{"type":"error","message":"Invalid template"}`, true},
		{"whatsapp sent", 200, `{"status":"success","hasError":false,"data":"queued"}`, false},
		{"whatsapp refused with 200", 200, `{"status":"fail","hasError":true,"errors":"template not approved"}`, true},
		{"http error", 401, `{"message":"Unauthorized"}`, true},
		{"plain text ok", 200, `ok`, false},
	}
	for _, c := range cases {
		err := msg91Result(reply(c.status, c.body))
		if (err != nil) != c.fail {
			t.Errorf("%s: got error %v, want failure %v", c.name, err, c.fail)
		}
	}
}
