// Package messaging sends email (SMTP) and WhatsApp and SMS one-time codes (MSG91), and logs every message.
// When a provider is not configured the message is only logged, so the rest of the app keeps working.
package messaging

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	SMTPHost, SMTPPort, SMTPUser, SMTPPassword, SMTPFrom string
	MSG91AuthKey, MSG91WhatsAppNumber, MSG91OTPTemplate  string
}

type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

type Sender struct {
	DB   *pgxpool.Pool
	Cfg  Config
	HTTP *http.Client
}

func New(db *pgxpool.Pool, cfg Config) *Sender {
	return &Sender{DB: db, Cfg: cfg, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Sender) EmailReady() bool { return s.Cfg.SMTPHost != "" && s.Cfg.SMTPFrom != "" }
func (s *Sender) WhatsAppReady() bool {
	return s.Cfg.MSG91AuthKey != "" && s.Cfg.MSG91WhatsAppNumber != ""
}
func (s *Sender) SMSOTPReady() bool { return s.Cfg.MSG91AuthKey != "" && s.Cfg.MSG91OTPTemplate != "" }

func (s *Sender) record(ctx context.Context, channel, to, subject, body, status, errText, ref string, by *int64) {
	_, err := s.DB.Exec(ctx, `INSERT INTO message_log(channel,recipient,subject,body,status,error,ref,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		channel, to, subject, body, status, errText, ref, by)
	if err != nil {
		log.Printf("message log: %v", err)
	}
}

// Email sends a message with optional attachments. It returns "sent" or "logged".
func (s *Sender) Email(ctx context.Context, to, subject, body string, atts []Attachment, ref string, by *int64) (string, error) {
	if !s.EmailReady() {
		s.record(ctx, "email", to, subject, body, "logged", "SMTP is not configured", ref, by)
		return "logged", nil
	}
	msg := buildMIME(s.Cfg.SMTPFrom, to, subject, body, atts)
	err := s.smtpSend(to, msg)
	if err != nil {
		s.record(ctx, "email", to, subject, body, "failed", err.Error(), ref, by)
		return "failed", err
	}
	s.record(ctx, "email", to, subject, body, "sent", "", ref, by)
	return "sent", nil
}

func (s *Sender) smtpSend(to string, msg []byte) error {
	addr := net.JoinHostPort(s.Cfg.SMTPHost, s.Cfg.SMTPPort)
	var auth smtp.Auth
	if s.Cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.Cfg.SMTPUser, s.Cfg.SMTPPassword, s.Cfg.SMTPHost)
	}
	if s.Cfg.SMTPPort == "465" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Cfg.SMTPHost})
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, s.Cfg.SMTPHost)
		if err != nil {
			return err
		}
		defer c.Close()
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
		if err := c.Mail(s.Cfg.SMTPFrom); err != nil {
			return err
		}
		if err := c.Rcpt(to); err != nil {
			return err
		}
		wc, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := wc.Write(msg); err != nil {
			return err
		}
		if err := wc.Close(); err != nil {
			return err
		}
		return c.Quit()
	}
	return smtp.SendMail(addr, auth, s.Cfg.SMTPFrom, []string{to}, msg)
}

func buildMIME(from, to, subject, body string, atts []Attachment) []byte {
	var b bytes.Buffer
	boundary := fmt.Sprintf("pd-%d", time.Now().UnixNano())
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", from, to, mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, body)
	for _, a := range atts {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=%q\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=%q\r\n\r\n", boundary, a.ContentType, a.Name, a.Name)
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}

func indianNumber(mobile string) string {
	m := strings.TrimPrefix(strings.ReplaceAll(mobile, " ", ""), "+")
	if len(m) == 10 {
		return "91" + m
	}
	return m
}

// WhatsApp sends an approved MSG91 WhatsApp template. params fill the template's body variables in order.
// The template name must match a template approved on the MSG91 WhatsApp sender.
func (s *Sender) WhatsApp(ctx context.Context, mobile, template string, params []string, preview, ref string, by *int64) (string, error) {
	if !s.WhatsAppReady() {
		s.record(ctx, "whatsapp", mobile, template, preview, "logged", "MSG91 is not configured", ref, by)
		return "logged", nil
	}
	comps := map[string]any{}
	for i, p := range params {
		comps[fmt.Sprintf("body_%d", i+1)] = map[string]string{"type": "text", "value": p}
	}
	payload := map[string]any{
		"integrated_number": s.Cfg.MSG91WhatsAppNumber,
		"content_type":      "template",
		"payload": map[string]any{
			"messaging_product": "whatsapp",
			"type":              "template",
			"template": map[string]any{
				"name":     template,
				"language": map[string]string{"code": "en", "policy": "deterministic"},
				"to_and_components": []map[string]any{{
					"to":         []string{indianNumber(mobile)},
					"components": comps,
				}},
			},
		},
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.msg91.com/api/v5/whatsapp/whatsapp-outbound-message/bulk/", bytes.NewReader(b))
	req.Header.Set("authkey", s.Cfg.MSG91AuthKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err == nil {
		err = msg91Result(res)
	}
	if err != nil {
		s.record(ctx, "whatsapp", mobile, template, preview, "failed", err.Error(), ref, by)
		return "failed", err
	}
	s.record(ctx, "whatsapp", mobile, template, preview, "sent", "", ref, by)
	return "sent", nil
}

// SMSOTP sends a sign-in code by SMS through the MSG91 OTP API.
func (s *Sender) SMSOTP(ctx context.Context, mobile, code string) error {
	if !s.SMSOTPReady() {
		return fmt.Errorf("SMS is not configured")
	}
	q := url.Values{"template_id": {s.Cfg.MSG91OTPTemplate}, "mobile": {indianNumber(mobile)}, "otp": {code}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://control.msg91.com/api/v5/otp?"+q.Encode(), nil)
	req.Header.Set("authkey", s.Cfg.MSG91AuthKey)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	if err := msg91Result(res); err != nil {
		s.record(ctx, "sms", mobile, "Sign-in code", "One-time code not sent", "failed", err.Error(), "otp", nil)
		return err
	}
	s.record(ctx, "sms", mobile, "Sign-in code", "One-time code sent", "sent", "", "otp", nil)
	return nil
}

// msg91Result reads MSG91's reply and closes it. MSG91 can answer HTTP 200 and still refuse the message,
// so the body is checked too: {"type":"error"} (OTP API) or {"hasError":true} / {"status":"fail"} (WhatsApp API).
func msg91Result(res *http.Response) error {
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 300 {
		return fmt.Errorf("MSG91 replied %s: %s", res.Status, body)
	}
	var reply struct {
		Type     string `json:"type"`
		Status   string `json:"status"`
		HasError bool   `json:"hasError"`
		Message  any    `json:"message"`
		Errors   any    `json:"errors"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return nil // not JSON: trust the HTTP status
	}
	if strings.EqualFold(reply.Type, "error") || reply.HasError || strings.EqualFold(reply.Status, "fail") || strings.EqualFold(reply.Status, "error") {
		return fmt.Errorf("MSG91 refused the message: %s", body)
	}
	return nil
}
