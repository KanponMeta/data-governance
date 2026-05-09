package notification

import (
	"context"
	"fmt"

	mail "github.com/wneessen/go-mail"
)

// SMTPChannel sends an email via wneessen/go-mail with mandatory TLS
// (T-05-05-05 mitigation — no plaintext fallback). User / password are loaded
// from env vars at startup time (see configs/notifications.example.yaml docs);
// the channel does NOT log credentials (T-05-05-04 mitigation).
type SMTPChannel struct {
	Host        string
	Port        int
	Username    string
	Password    string
	From        string
	UseSTARTTLS bool // default true; encoded as TLSMandatory below
}

// NewSMTPChannel constructs an SMTPChannel with STARTTLS=true.
func NewSMTPChannel(host string, port int, user, pass, from string) *SMTPChannel {
	return &SMTPChannel{
		Host:        host,
		Port:        port,
		Username:    user,
		Password:    pass,
		From:        from,
		UseSTARTTLS: true,
	}
}

// Name implements Channel.
func (s *SMTPChannel) Name() string { return "smtp" }

// Send implements Channel. Recipient(s) are pulled from p.Vars["recipient"]
// (single) or p.Vars["recipients"] (comma-separated) — both forms supported
// because notifications.yaml templates may resolve either way.
func (s *SMTPChannel) Send(ctx context.Context, p SendPayload) error {
	if s.From == "" {
		return fmt.Errorf("smtp: From is empty")
	}
	to := p.Vars["recipient"]
	if to == "" {
		to = p.Vars["recipients"]
	}
	if to == "" {
		return fmt.Errorf("smtp: no recipient resolved from vars")
	}

	m := mail.NewMsg()
	if err := m.From(s.From); err != nil {
		return fmt.Errorf("smtp: from: %w", err)
	}
	if err := m.To(to); err != nil {
		return fmt.Errorf("smtp: to: %w", err)
	}
	m.Subject(p.Subject)
	m.SetBodyString(mail.TypeTextPlain, p.BodyText)
	if p.BodyHTML != "" {
		m.AddAlternativeString(mail.TypeTextHTML, p.BodyHTML)
	}

	c, err := mail.NewClient(s.Host,
		mail.WithPort(s.Port),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(s.Username),
		mail.WithPassword(s.Password),
		mail.WithTLSPolicy(mail.TLSMandatory),
	)
	if err != nil {
		return fmt.Errorf("smtp: new client: %w", err)
	}
	if err := c.DialAndSendWithContext(ctx, m); err != nil {
		return fmt.Errorf("smtp: send: %w", err)
	}
	return nil
}
