package mail

import (
	"bufio"
	"net"
	"net/mail"
	"strings"
	"testing"

	"github.com/raphi011/mise/internal/config"
)

// fakeSMTP accepts one message on a local port and returns its DATA.
func fakeSMTP(t *testing.T) (port int, data <-chan string) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var body strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					out <- body.String()
					say("250 queued")
					continue
				}
				body.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250-fake")
				say("250 8BITMIME")
			case cmd == "DATA":
				inData = true
				say("354 go ahead")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}

func TestSMTPSend(t *testing.T) {
	port, data := fakeSMTP(t)
	from, _ := mail.ParseAddress("mise <mise@example.com>")
	m := NewSMTP(config.SMTP{Host: "127.0.0.1", Port: port, TLS: config.TLSNone, From: from})
	err := m.Send(t.Context(), Message{
		To: "Anna <anna@example.com>", Subject: "Passwort zurücksetzen",
		Text: "Hallo Anna", HTML: "<p>Hallo Anna</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := <-data
	for _, want := range []string{
		"From: \"mise\" <mise@example.com>", "To: \"Anna\" <anna@example.com>",
		"Subject: =?UTF-8?", "multipart/alternative", "text/plain", "text/html",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message has no %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "text/plain") > strings.Index(got, "text/html") {
		t.Error("HTML part comes before the text part")
	}
}
