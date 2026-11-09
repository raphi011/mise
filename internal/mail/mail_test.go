package mail

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/mise/internal/config"
)

// fakeSMTP accepts one message on a local port and returns its DATA. With
// tlsCfg it offers STARTTLS.
func fakeSMTP(t *testing.T, tlsCfg *tls.Config) (port int, data <-chan string) {
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
		tlsDone := false
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
				if tlsCfg != nil && !tlsDone {
					say("250-STARTTLS")
				}
				say("250 8BITMIME")
			case cmd == "STARTTLS" && tlsCfg != nil:
				say("220 ready")
				tc := tls.Server(conn, tlsCfg)
				if err := tc.HandshakeContext(t.Context()); err != nil {
					return
				}
				conn, r, tlsDone = tc, bufio.NewReader(tc), true
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
	port, data := fakeSMTP(t, nil)
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

// selfSigned returns a server config with a throwaway self-signed
// certificate, like an in-cluster relay's.
func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func TestSMTPStartTLSVerify(t *testing.T) {
	from, _ := mail.ParseAddress("mise@example.com")
	msg := Message{To: "anna@example.com", Subject: "s", Text: "t", HTML: "<p>t</p>"}

	port, _ := fakeSMTP(t, selfSigned(t))
	m := NewSMTP(config.SMTP{Host: "127.0.0.1", Port: port, TLS: config.TLSStartTLS, From: from})
	if err := m.Send(t.Context(), msg); err == nil {
		t.Error("sent to a server with an untrusted certificate")
	}

	port, data := fakeSMTP(t, selfSigned(t))
	m = NewSMTP(config.SMTP{Host: "127.0.0.1", Port: port, TLS: config.TLSStartTLS, From: from, SkipVerify: true})
	if err := m.Send(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if got := <-data; !strings.Contains(got, "To: <anna@example.com>") {
		t.Errorf("message:\n%s", got)
	}
}
