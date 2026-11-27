// Package organs implements Frog Bot V2's 24-hour zero-token assistant organs:
//   - IMAP IDLE two-stage email funnel (local marketing filter + pointerized tainted wake-up)
//   - Anti-bot random Jitter Cron + Morning/Evening Digest Queue
//   - On-demand cold-start, idle-reap Playwright browser pool (anti-cookie-export guard)
//   - Ready-to-Verify remote CDP Screencast captcha handshake + website selector auto-heal
package organs

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"frogbot/internal/host"
	"frogbot/internal/memory"
	"frogbot/internal/vault"
)

// IncomingEmail represents one email that survived Stage-1 zero-token local filtering.
type IncomingEmail struct {
	SeqNum      string    `json:"seq_num"`
	From        string    `json:"from"`
	Subject     string    `json:"subject"`
	Date        string    `json:"date"`
	Summary     string    `json:"summary"`
	PointerFile string    `json:"pointer_file,omitempty"`
	ReceivedAt  time.Time `json:"received_at"`
}

// IMAPDialer allows injecting TLS or in-process net.Pipe connections for testing.
type IMAPDialer func(ctx context.Context, addr string) (net.Conn, error)

// IMAPFunnel maintains an IMAP4rev1 IDLE listener for one Bot with two-stage filtering:
//   Stage 1 (0 Token): Drops blacklisted senders & marketing mails carrying List-Unsubscribe.
//   Stage 2 (Tainted): Pointerizes long bodies (>1200 chars), extracts concise summary,
//                      checks Bot event rate limit, and wakes Bot Actor with Tainted=true.
type IMAPFunnel struct {
	BotID       string
	Config      host.BotIMAPConfig
	Vault       *vault.Vault
	Pointerizer *memory.Pointerizer
	Budget      *host.BudgetGuard
	Actor       *host.BotActor
	Dial        IMAPDialer
	OnEmail     func(email IncomingEmail) // optional hook for tests/telemetry
	OnDropped   func(from, subject, reason string)
	ReIdleEvery time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewIMAPFunnel(botID string, cfg host.BotIMAPConfig, vlt *vault.Vault, ptr *memory.Pointerizer, budget *host.BudgetGuard, actor *host.BotActor) *IMAPFunnel {
	if cfg.Port <= 0 {
		cfg.Port = 993
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	return &IMAPFunnel{
		BotID:       botID,
		Config:      cfg,
		Vault:       vlt,
		Pointerizer: ptr,
		Budget:      budget,
		Actor:       actor,
		ReIdleEvery: 25 * time.Minute,
	}
}

func (f *IMAPFunnel) Start(parent context.Context) {
	if !f.Config.Enabled || f.Config.Host == "" {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	f.mu.Lock()
	f.cancel = cancel
	f.mu.Unlock()

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		backoff := 2 * time.Second
		for ctx.Err() == nil {
			err := f.runSession(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				select {
				case <-time.After(backoff):
					if backoff < time.Minute {
						backoff *= 2
					}
				case <-ctx.Done():
					return
				}
			} else {
				backoff = 2 * time.Second
			}
		}
	}()
}

func (f *IMAPFunnel) Stop() {
	f.mu.Lock()
	c := f.cancel
	f.mu.Unlock()
	if c != nil {
		c()
	}
	f.wg.Wait()
}

var secretHandleExtractRe = regexp.MustCompile(`\{\{secret:(?://)?([A-Za-z][A-Za-z0-9_.-]{0,63})\}\}`)

func (f *IMAPFunnel) resolvePassword() (string, error) {
	ref := strings.TrimSpace(f.Config.PasswordRef)
	if ref == "" {
		return "", errors.New("未配置 IMAP password_ref")
	}
	if m := secretHandleExtractRe.FindStringSubmatch(ref); len(m) == 2 {
		if f.Vault == nil {
			return "", errors.New("未配置 Blind Vault")
		}
		vals, err := f.Vault.Values([]string{m[1]})
		if err != nil {
			return "", err
		}
		return vals[m[1]], nil
	}
	if strings.HasPrefix(ref, "secret://") {
		name := strings.TrimPrefix(ref, "secret://")
		if f.Vault == nil {
			return "", errors.New("未配置 Blind Vault")
		}
		vals, err := f.Vault.Values([]string{name})
		if err != nil {
			return "", err
		}
		return vals[name], nil
	}
	return ref, nil
}

// RunOnce connects, checks UNSEEN messages, optionally runs one IDLE cycle, and disconnects.
func (f *IMAPFunnel) RunOnce(ctx context.Context) error {
	return f.runSessionInternal(ctx, true)
}

func (f *IMAPFunnel) runSession(ctx context.Context) error {
	return f.runSessionInternal(ctx, false)
}

func (f *IMAPFunnel) runSessionInternal(ctx context.Context, singleCycle bool) error {
	pass, err := f.resolvePassword()
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(f.Config.Host, strconv.Itoa(f.Config.Port))
	var conn net.Conn
	if f.Dial != nil {
		conn, err = f.Dial(ctx, addr)
	} else {
		d := &net.Dialer{Timeout: 15 * time.Second}
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: f.Config.Host, MinVersion: tls.VersionTLS12})
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	ic := &imapConn{conn: conn, r: bufio.NewReaderSize(conn, 32*1024)}
	if _, err := ic.readLine(); err != nil { // server greeting
		return err
	}
	if _, err := ic.cmd(fmt.Sprintf("LOGIN %s %s", imapQuote(f.Config.Username), imapQuote(pass))); err != nil {
		return fmt.Errorf("IMAP LOGIN failed: %w", err)
	}
	if _, err := ic.cmd("SELECT " + imapQuote(f.Config.Mailbox)); err != nil {
		return fmt.Errorf("IMAP SELECT failed: %w", err)
	}

	seenSeq := map[string]bool{}
	fetchUnseen := func() error {
		lines, err := ic.cmd("SEARCH UNSEEN")
		if err != nil {
			return err
		}
		var ids []string
		for _, ln := range lines {
			if strings.HasPrefix(ln, "* SEARCH") {
				for _, tok := range strings.Fields(strings.TrimPrefix(ln, "* SEARCH")) {
					if !seenSeq[tok] {
						ids = append(ids, tok)
						seenSeq[tok] = true
					}
				}
			}
		}
		for _, id := range ids {
			rawMsg, err := ic.fetchMessage(id)
			if err != nil {
				continue
			}
			f.processRawEmail(ctx, id, rawMsg)
		}
		return nil
	}

	if err := fetchUnseen(); err != nil {
		return err
	}
	if singleCycle {
		_, _ = ic.cmd("LOGOUT")
		return nil
	}

	// RFC 2177 IDLE loop
	for ctx.Err() == nil {
		tag := ic.nextTag()
		if _, err := fmt.Fprintf(conn, "%s IDLE\r\n", tag); err != nil {
			return err
		}
		cont, err := ic.readLine()
		if err != nil {
			return err
		}
		if !strings.HasPrefix(cont, "+") {
			// Server doesn't support IDLE; fall back to 60s poll
			select {
			case <-time.After(60 * time.Second):
				if err := fetchUnseen(); err != nil {
					return err
				}
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Wait for EXISTS notification or ReIdleEvery keepalive timeout
		reidle := f.ReIdleEvery
		if reidle <= 0 {
			reidle = 25 * time.Minute
		}
		_ = conn.SetReadDeadline(time.Now().Add(reidle))
		hasNew := false
		for {
			ln, err := ic.readLine()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					break
				}
				return err
			}
			if strings.Contains(ln, "EXISTS") || strings.Contains(ln, "RECENT") {
				hasNew = true
				break
			}
		}
		_ = conn.SetReadDeadline(time.Time{})
		if _, err := fmt.Fprintf(conn, "DONE\r\n"); err != nil {
			return err
		}
		if _, err := ic.readUntilTagged(tag); err != nil {
			return err
		}
		if hasNew {
			if err := fetchUnseen(); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// FilterStage1 applies zero-token local filtering (blacklist + List-Unsubscribe marketing filter).
func (f *IMAPFunnel) FilterStage1(from string, headers mail.Header) (drop bool, reason string) {
	lowerFrom := strings.ToLower(strings.TrimSpace(from))
	for _, w := range f.Config.Whitelist {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" && strings.Contains(lowerFrom, w) {
			return false, ""
		}
	}
	for _, b := range f.Config.Blacklist {
		b = strings.ToLower(strings.TrimSpace(b))
		if b != "" && strings.Contains(lowerFrom, b) {
			return true, "命中发件人黑名单: " + b
		}
	}
	if f.Config.FilterMarketing && strings.TrimSpace(headers.Get("List-Unsubscribe")) != "" {
		return true, "含 List-Unsubscribe 营销邮件头（0 Token 拦截）"
	}
	return false, ""
}

func (f *IMAPFunnel) processRawEmail(ctx context.Context, seqNum, rawMsg string) {
	msg, err := mail.ReadMessage(strings.NewReader(rawMsg))
	var from, subject, date, body string
	var hdr mail.Header
	if err == nil && msg != nil {
		hdr = msg.Header
		from = strings.TrimSpace(hdr.Get("From"))
		subject = strings.TrimSpace(hdr.Get("Subject"))
		date = strings.TrimSpace(hdr.Get("Date"))
		b, _ := io.ReadAll(io.LimitReader(msg.Body, 512<<10))
		body = strings.TrimSpace(string(b))
	} else {
		body = strings.TrimSpace(rawMsg)
	}

	// Stage 1: zero-token local filter
	if drop, reason := f.FilterStage1(from, hdr); drop {
		if f.OnDropped != nil {
			f.OnDropped(from, subject, reason)
		}
		return
	}

	// Stage 2: pointerize long body (>1200 chars), rate-limit, and wake Bot Actor with Tainted=true
	summary := body
	pointerFile := ""
	if f.Pointerizer != nil {
		summary, pointerFile, _ = f.Pointerizer.Intercept(ctx, body)
	}
	if f.Vault != nil {
		summary = f.Vault.Scrub(summary)
	}

	email := IncomingEmail{
		SeqNum:      seqNum,
		From:        from,
		Subject:     subject,
		Date:        date,
		Summary:     summary,
		PointerFile: pointerFile,
		ReceivedAt:  time.Now(),
	}
	if f.OnEmail != nil {
		f.OnEmail(email)
	}
	if f.Budget != nil {
		if ok, reason := f.Budget.AllowBackgroundEvent("imap:" + from); !ok {
			if f.OnDropped != nil {
				f.OnDropped(from, subject, reason)
			}
			return
		}
	}
	if f.Actor != nil {
		_ = f.Actor.Submit(host.ActorEvent{
			Type:         host.EventTaskCallback,
			BotID:        f.BotID,
			Source:       "email:" + from,
			Text:         fmt.Sprintf("[新邮件] 发件人：%s\n主题：%s\n内容：%s", from, subject, summary),
			Payload:      email,
			Tainted:      true,
			TaintSources: []string{"email:" + from},
		})
	}
}

type imapConn struct {
	conn net.Conn
	r    *bufio.Reader
	seq  int
}

func (c *imapConn) nextTag() string {
	c.seq++
	return fmt.Sprintf("A%04d", c.seq)
}

func (c *imapConn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *imapConn) cmd(command string) ([]string, error) {
	tag := c.nextTag()
	if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, command); err != nil {
		return nil, err
	}
	return c.readUntilTagged(tag)
}

func (c *imapConn) readUntilTagged(tag string) ([]string, error) {
	var lines []string
	prefix := tag + " "
	for {
		ln, err := c.readLine()
		if err != nil {
			return lines, err
		}
		if strings.HasPrefix(ln, prefix) {
			rest := strings.TrimPrefix(ln, prefix)
			if strings.HasPrefix(strings.ToUpper(rest), "OK") {
				return lines, nil
			}
			return lines, errors.New(rest)
		}
		lines = append(lines, ln)
	}
}

var literalSizeRe = regexp.MustCompile(`\{(\d+)\}$`)

func (c *imapConn) fetchMessage(seqID string) (string, error) {
	tag := c.nextTag()
	if _, err := fmt.Fprintf(c.conn, "%s FETCH %s (BODY.PEEK[])\r\n", tag, seqID); err != nil {
		return "", err
	}
	var body strings.Builder
	prefix := tag + " "
	for {
		ln, err := c.readLine()
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(ln, prefix) {
			if strings.HasPrefix(strings.ToUpper(strings.TrimPrefix(ln, prefix)), "OK") {
				return body.String(), nil
			}
			return "", errors.New(ln)
		}
		if m := literalSizeRe.FindStringSubmatch(ln); len(m) == 2 {
			n, _ := strconv.Atoi(m[1])
			if n > 0 && n <= 2<<20 {
				buf := make([]byte, n)
				if _, err := io.ReadFull(c.r, buf); err != nil {
					return "", err
				}
				body.Write(buf)
			}
		}
	}
}

func imapQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
