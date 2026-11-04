// Package mail sends plain-text e-mail through SMTP; LogMailer/MemMailer serve development and tests.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	gomail "github.com/wneessen/go-mail"
)

type Message struct{ To, Subject, TextBody string }

type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// SMTPMailer uses STARTTLS when the server supports it (mandatory unless the host is local).
type SMTPMailer struct {
	Host, User, Pass, From string
	Port                   int
	Plain                  bool // true: no TLS (local Mailpit only)
}

func (s *SMTPMailer) Send(ctx context.Context, m Message) error {
	msg := gomail.NewMsg()
	if err := msg.From(s.From); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	if err := msg.To(m.To); err != nil {
		return fmt.Errorf("mail: to: %w", err)
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, m.TextBody)
	opts := []gomail.Option{gomail.WithPort(s.Port), gomail.WithTimeout(10 * time.Second)}
	if s.Plain {
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	} else {
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if s.User != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover), gomail.WithUsername(s.User), gomail.WithPassword(s.Pass))
	}
	c, err := gomail.NewClient(s.Host, opts...)
	if err != nil {
		return fmt.Errorf("mail: client: %w", err)
	}
	return c.DialAndSendWithContext(ctx, msg)
}

// LogMailer logs recipient and subject only; the body only when LogBody is set (dev).
type LogMailer struct{ LogBody bool }

func (l LogMailer) Send(_ context.Context, m Message) error {
	attrs := []any{"to", m.To, "subject", m.Subject}
	if l.LogBody {
		attrs = append(attrs, "body", m.TextBody)
	}
	slog.Info("mail (log only)", attrs...)
	return nil
}

// MemMailer keeps sent messages in memory (tests).
type MemMailer struct {
	mu   sync.Mutex
	Sent []Message
	Err  error
}

func (m *MemMailer) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.Sent = append(m.Sent, msg)
	return nil
}

func (m *MemMailer) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.Sent...)
}
