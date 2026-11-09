package common

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSMTPServer struct {
	listener          net.Listener
	host              string
	port              int
	cert              tls.Certificate
	advertiseSTARTTLS bool
	authMechanisms    []string
	messages          chan string
	authCommands      chan string
	startTLSCommands  chan string
}

func newFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	return newFakeSMTPServerWithSTARTTLSAdvertisement(t, true)
}

func newFakeSMTPServerWithSTARTTLSAdvertisement(t *testing.T, advertiseSTARTTLS bool) *fakeSMTPServer {
	t.Helper()

	cert, err := newTestTLSCertificate()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	server := &fakeSMTPServer{
		listener:          listener,
		host:              host,
		port:              port,
		cert:              cert,
		advertiseSTARTTLS: advertiseSTARTTLS,
		authMechanisms:    []string{"PLAIN", "LOGIN"},
		messages:          make(chan string, 1),
		authCommands:      make(chan string, 1),
		startTLSCommands:  make(chan string, 1),
	}
	go server.serve()
	return server
}

func newFakeImplicitTLSSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()

	cert, err := newTestTLSCertificate()
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	host, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	server := &fakeSMTPServer{
		listener:          tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}}),
		host:              host,
		port:              port,
		cert:              cert,
		advertiseSTARTTLS: false,
		authMechanisms:    []string{"PLAIN", "LOGIN"},
		messages:          make(chan string, 1),
		authCommands:      make(chan string, 1),
		startTLSCommands:  make(chan string, 1),
	}
	go server.serve()
	return server
}

func (s *fakeSMTPServer) close() {
	_ = s.listener.Close()
}

func (s *fakeSMTPServer) serve() {
	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	if err := writeSMTPLine(rw, "220 fake.smtp.local ESMTP"); err != nil {
		return
	}

	encrypted := false
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimRight(line, "\r\n")
		upperCommand := strings.ToUpper(command)

		switch {
		case strings.HasPrefix(upperCommand, "EHLO"):
			if err := writeSMTPLine(rw, "250-fake.smtp.local"); err != nil {
				return
			}
			if !encrypted && s.advertiseSTARTTLS {
				if err := writeSMTPLine(rw, "250-STARTTLS"); err != nil {
					return
				}
			}
			if len(s.authMechanisms) > 0 {
				if err := writeSMTPLine(rw, "250 AUTH "+strings.Join(s.authMechanisms, " ")); err != nil {
					return
				}
			} else if err := writeSMTPLine(rw, "250 8BITMIME"); err != nil {
				return
			}
		case upperCommand == "STARTTLS":
			if encrypted || !s.advertiseSTARTTLS {
				if err := writeSMTPLine(rw, "502 5.5.1 STARTTLS not supported"); err != nil {
					return
				}
				continue
			}
			select {
			case s.startTLSCommands <- command:
			default:
			}
			if err := writeSMTPLine(rw, "220 2.0.0 Ready to start TLS"); err != nil {
				return
			}
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			rw = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
			encrypted = true
		case strings.HasPrefix(upperCommand, "AUTH"):
			select {
			case s.authCommands <- command:
			default:
			}
			if err := writeSMTPLine(rw, "235 2.7.0 Authentication successful"); err != nil {
				return
			}
		case strings.HasPrefix(upperCommand, "MAIL FROM:"):
			if err := writeSMTPLine(rw, "250 2.1.0 Sender OK"); err != nil {
				return
			}
		case strings.HasPrefix(upperCommand, "RCPT TO:"):
			if err := writeSMTPLine(rw, "250 2.1.5 Recipient OK"); err != nil {
				return
			}
		case upperCommand == "DATA":
			if err := writeSMTPLine(rw, "354 End data with <CR><LF>.<CR><LF>"); err != nil {
				return
			}
			var data strings.Builder
			for {
				dataLine, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				data.WriteString(dataLine)
			}
			s.messages <- data.String()
			if err := writeSMTPLine(rw, "250 2.0.0 Queued"); err != nil {
				return
			}
		case upperCommand == "QUIT":
			_ = writeSMTPLine(rw, "221 2.0.0 Bye")
			return
		default:
			if err := writeSMTPLine(rw, "502 5.5.1 Command not implemented"); err != nil {
				return
			}
		}
	}
}

func writeSMTPLine(rw *bufio.ReadWriter, line string) error {
	_, err := rw.WriteString(line + "\r\n")
	if err != nil {
		return err
	}
	return rw.Flush()
}

func newTestTLSCertificate() (tls.Certificate, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "aixinexchange01.aixin-chip.com",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"aixinexchange01", "aixinexchange01.aixin-chip.com"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func withSMTPSettings(t *testing.T) {
	t.Helper()
	originalSMTPServer := SMTPServer
	originalSMTPPort := SMTPPort
	originalSMTPSSLEnabled := SMTPSSLEnabled
	originalSMTPStartTLSEnabled := SMTPStartTLSEnabled
	originalSMTPInsecureSkipVerify := SMTPInsecureSkipVerify
	originalSMTPForceAuthLogin := SMTPForceAuthLogin
	originalSMTPAccount := SMTPAccount
	originalSMTPFrom := SMTPFrom
	originalSMTPToken := SMTPToken
	originalSystemName := SystemName

	t.Cleanup(func() {
		SMTPServer = originalSMTPServer
		SMTPPort = originalSMTPPort
		SMTPSSLEnabled = originalSMTPSSLEnabled
		SMTPStartTLSEnabled = originalSMTPStartTLSEnabled
		SMTPInsecureSkipVerify = originalSMTPInsecureSkipVerify
		SMTPForceAuthLogin = originalSMTPForceAuthLogin
		SMTPAccount = originalSMTPAccount
		SMTPFrom = originalSMTPFrom
		SMTPToken = originalSMTPToken
		SystemName = originalSystemName
	})
}

func TestSendEmailUsesExplicitStartTLSWithInsecureCertificate(t *testing.T) {
	server := newFakeSMTPServer(t)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = true
	SMTPInsecureSkipVerify = true
	SMTPForceAuthLogin = false
	SMTPAccount = "sender@example.com"
	SMTPFrom = "sender@example.com"
	SMTPToken = "secret"
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.NoError(t, err)

	select {
	case message := <-server.messages:
		require.Contains(t, message, "Subject: =?UTF-8?B?")
		require.Contains(t, message, "<p>123456</p>")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP DATA")
	}
}

func TestSendEmailExplicitStartTLSRequiresServerSupport(t *testing.T) {
	server := newFakeSMTPServerWithSTARTTLSAdvertisement(t, false)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = true
	SMTPInsecureSkipVerify = true
	SMTPForceAuthLogin = false
	SMTPAccount = "sender@example.com"
	SMTPFrom = "sender@example.com"
	SMTPToken = "secret"
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.Error(t, err)
	require.Contains(t, err.Error(), "STARTTLS")
}

func TestSendEmailDoesNotAutoUpgradeWhenStartTLSDisabled(t *testing.T) {
	server := newFakeSMTPServerWithSTARTTLSAdvertisement(t, true)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = false
	SMTPInsecureSkipVerify = false
	SMTPForceAuthLogin = false
	SMTPAccount = "sender@example.com"
	SMTPFrom = "sender@example.com"
	SMTPToken = "secret"
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.NoError(t, err)

	select {
	case command := <-server.startTLSCommands:
		t.Fatalf("unexpected SMTP STARTTLS command: %s", command)
	default:
	}

	select {
	case message := <-server.messages:
		require.Contains(t, message, "<p>123456</p>")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP DATA")
	}
}

func TestSMTPPlainAuthRejectsRemotePlaintextConnection(t *testing.T) {
	server := newFakeSMTPServerWithSTARTTLSAdvertisement(t, false)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = "smtp.example.com"
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = false
	SMTPInsecureSkipVerify = false
	SMTPForceAuthLogin = false
	SMTPAccount = "sender@example.com"
	SMTPFrom = "sender@example.com"
	SMTPToken = "secret"

	conn, err := net.Dial("tcp", net.JoinHostPort(server.host, strconv.Itoa(server.port)))
	require.NoError(t, err)
	client, err := smtp.NewClient(conn, SMTPServer)
	require.NoError(t, err)

	err = client.Auth(getSMTPAuth())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unencrypted connection")

	select {
	case command := <-server.authCommands:
		t.Fatalf("unexpected SMTP auth command: %s", command)
	default:
	}
}

func TestNewSMTPClientHonorsExplicitStartTLSWhenPortIs465(t *testing.T) {
	server := newFakeSMTPServer(t)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = 465
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = true
	SMTPInsecureSkipVerify = true

	client, err := newSMTPClient(fmt.Sprintf("%s:%d", server.host, server.port))
	require.NoError(t, err)
	defer client.Close()

	select {
	case command := <-server.startTLSCommands:
		require.Equal(t, "STARTTLS", command)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP STARTTLS")
	}
}

func TestNewSMTPClientKeepsImplicitTLSForLegacyPort465(t *testing.T) {
	server := newFakeImplicitTLSSMTPServer(t)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = 465
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = false
	SMTPInsecureSkipVerify = true

	client, err := newSMTPClient(fmt.Sprintf("%s:%d", server.host, server.port))
	require.NoError(t, err)
	defer client.Close()
}

func TestSendEmailSkipsAuthWhenCredentialsAreEmpty(t *testing.T) {
	server := newFakeSMTPServerWithSTARTTLSAdvertisement(t, false)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = false
	SMTPInsecureSkipVerify = false
	SMTPForceAuthLogin = false
	SMTPAccount = ""
	SMTPFrom = "sender@example.com"
	SMTPToken = ""
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.NoError(t, err)

	select {
	case command := <-server.authCommands:
		t.Fatalf("unexpected SMTP auth command: %s", command)
	default:
	}

	select {
	case message := <-server.messages:
		require.Contains(t, message, "<p>123456</p>")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP DATA")
	}
}

func TestSendEmailSkipsAuthWhenCredentialsAreIncomplete(t *testing.T) {
	server := newFakeSMTPServerWithSTARTTLSAdvertisement(t, false)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = false
	SMTPInsecureSkipVerify = false
	SMTPForceAuthLogin = false
	SMTPAccount = "sender@example.com"
	SMTPFrom = "sender@example.com"
	SMTPToken = ""
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.NoError(t, err)

	select {
	case command := <-server.authCommands:
		t.Fatalf("unexpected SMTP auth command: %s", command)
	default:
	}

	select {
	case message := <-server.messages:
		require.Contains(t, message, "<p>123456</p>")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP DATA")
	}
}

func TestSendEmailUsesNTLMWhenServerOnlySupportsNTLM(t *testing.T) {
	server := newFakeSMTPServer(t)
	server.authMechanisms = []string{"NTLM"}
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = true
	SMTPInsecureSkipVerify = true
	SMTPForceAuthLogin = false
	SMTPAccount = "no-reply"
	SMTPFrom = "no-reply@example.com"
	SMTPToken = "secret"
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.NoError(t, err)

	select {
	case command := <-server.authCommands:
		require.True(t, strings.HasPrefix(command, "AUTH NTLM "), "unexpected auth command: %s", command)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP AUTH")
	}
}

func TestSendEmailUsesNTLMForMicrosoftAccountWhenServerOnlySupportsNTLM(t *testing.T) {
	server := newFakeSMTPServer(t)
	server.authMechanisms = []string{"NTLM"}
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = true
	SMTPInsecureSkipVerify = true
	SMTPForceAuthLogin = false
	SMTPAccount = "no-reply@contoso.onmicrosoft.com"
	SMTPFrom = "no-reply@contoso.onmicrosoft.com"
	SMTPToken = "secret"
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.NoError(t, err)

	select {
	case command := <-server.authCommands:
		require.True(t, strings.HasPrefix(command, "AUTH NTLM "), "unexpected auth command: %s", command)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP AUTH")
	}
}

func TestSendEmailExplicitStartTLSRejectsUntrustedCertificateByDefault(t *testing.T) {
	server := newFakeSMTPServer(t)
	defer server.close()
	withSMTPSettings(t)

	SMTPServer = server.host
	SMTPPort = server.port
	SMTPSSLEnabled = false
	SMTPStartTLSEnabled = true
	SMTPInsecureSkipVerify = false
	SMTPForceAuthLogin = false
	SMTPAccount = "sender@example.com"
	SMTPFrom = "sender@example.com"
	SMTPToken = "secret"
	SystemName = "New API"

	err := SendEmail("Verification", "receiver@example.com", "<p>123456</p>")
	require.Error(t, err)
	require.Contains(t, fmt.Sprint(err), "certificate")
}

func TestEmailDeliveryConfiguredSMTP(t *testing.T) {
	server := newFakeSMTPServer(t)
	config := EmailDeliveryConfig{Revision: 1, Options: EmailDeliveryOptions{
		AuthMode: EmailAuthLegacy,
		Legacy:   LegacyEmailSettings{Host: server.host, Port: server.port, From: "sender@example.com", StartTLS: true, InsecureSkipVerify: true},
		Microsoft: MicrosoftEmailSettings{
			TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222",
			Mailbox: "graph-sender@example.com", ClientSecret: "application-secret",
		},
	}}
	report, err := DeliverConfiguredEmail(context.Background(), config, "Test", "receiver@example.com", "<p>content</p>")
	require.NoError(t, err)
	assert.Equal(t, "EMAIL_ACCEPTED", report.Code)
	assert.Equal(t, "skipped", report.Stages[1].Status)
	assert.Equal(t, "passed", report.Stages[4].Status)
	assert.Contains(t, <-server.messages, "<p>content</p>")
	// Once a protected configuration exists, production does not fall back to SMTP globals.
	previous := DefaultEmailDelivery.state.Load()
	t.Cleanup(func() { DefaultEmailDelivery.state.Store(previous) })
	PublishEmailDelivery(config, ErrEmailDeliveryUnavailable)
	assert.ErrorIs(t, SendEmail("Test", "receiver@example.com", "secret"), ErrEmailDeliveryUnavailable)
}

type emailRoundTripFunc func(*http.Request) (*http.Response, error)

func (f emailRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMicrosoftGraphEmailDelivery(t *testing.T) {
	previousTransport := http.DefaultTransport
	previousToken, previousRevision, previousFingerprint := microsoftTokens.token, microsoftTokens.revision, microsoftTokens.fingerprint
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
		microsoftTokens.token, microsoftTokens.revision, microsoftTokens.fingerprint = previousToken, previousRevision, previousFingerprint
	})
	config := EmailDeliveryConfig{Revision: previousRevision, Options: EmailDeliveryOptions{
		AuthMode: EmailAuthMicrosoft,
		Legacy:   LegacyEmailSettings{Host: "smtp.example.com", Port: 587, From: "smtp-sender@example.com"},
		Microsoft: MicrosoftEmailSettings{
			TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222",
			Mailbox: "sender@example.com", ClientSecret: "application-secret",
		},
	}}
	for _, tc := range []struct {
		name   string
		status []int
		code   string
		stage  string
		reason string
	}{
		{name: "accepted", status: []int{202}, code: "EMAIL_ACCEPTED", stage: "passed"},
		{name: "refresh rejected token", status: []int{401, 202}, code: "EMAIL_ACCEPTED", stage: "passed"},
		{name: "access denied", status: []int{403}, code: "EMAIL_DELIVERY_FAILED", stage: "failed", reason: "GRAPH_ACCESS_DENIED"},
		{name: "redirect blocked", status: []int{307}, code: "EMAIL_DELIVERY_FAILED", stage: "failed"},
		{name: "connection lost", status: []int{0}, code: "EMAIL_DELIVERY_UNKNOWN", stage: "unknown"},
		{name: "server error", status: []int{503}, code: "EMAIL_DELIVERY_UNKNOWN", stage: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config.Revision++
			tokenServer := &emailTokenRoundTripper{expires: 3600}
			calls := 0
			http.DefaultTransport = emailRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				defer request.Body.Close()
				if request.URL.Host == "login.microsoftonline.com" {
					return tokenServer.RoundTrip(request)
				}
				require.Less(t, calls, len(tc.status), "unexpected email retry")
				assert.Equal(t, "https://graph.microsoft.com/v1.0/users/sender@example.com/sendMail", request.URL.String())
				assert.Equal(t, http.MethodPost, request.Method)
				assert.Equal(t, "Bearer test-access-token", request.Header.Get("Authorization"))
				assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
				var payload struct {
					Message struct {
						Subject      string
						Body         struct{ ContentType, Content string }
						ToRecipients []struct {
							EmailAddress struct{ Address string }
						}
					}
					SaveToSentItems *bool
				}
				require.NoError(t, DecodeJson(request.Body, &payload))
				assert.Equal(t, "测试邮件", payload.Message.Subject)
				assert.Equal(t, "HTML", payload.Message.Body.ContentType)
				assert.Contains(t, payload.Message.Body.Content, "<p>content</p>")
				require.Len(t, payload.Message.ToRecipients, 2)
				assert.Equal(t, "first@example.com", payload.Message.ToRecipients[0].EmailAddress.Address)
				assert.Equal(t, "second@example.com", payload.Message.ToRecipients[1].EmailAddress.Address)
				require.NotNil(t, payload.SaveToSentItems)
				assert.False(t, *payload.SaveToSentItems)

				status := tc.status[calls]
				calls++
				if status == 0 {
					return nil, errors.New("connection lost with test-access-token")
				}
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Location": []string{"https://attacker.example/redirect"}},
					Body:       io.NopCloser(strings.NewReader("provider body with test-access-token")),
					Request:    request,
				}, nil
			})
			report, err := DeliverConfiguredEmail(context.Background(), config, "测试邮件", "first@example.com;second@example.com", "<p>content</p>")
			if tc.code == "EMAIL_ACCEPTED" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "test-access-token")
			}
			assert.Equal(t, tc.code, report.Code)
			assert.Equal(t, tc.reason, report.Reason)
			assert.Equal(t, []EmailDeliveryStage{{Name: "config", Status: "passed"}, {Name: "token", Status: "passed"}, {Name: "graph_submit", Status: tc.stage}}, report.Stages)
			assert.Equal(t, len(tc.status), calls)
			assert.EqualValues(t, len(tc.status), tokenServer.calls.Load())
		})
	}
}

type emailTokenRoundTripper struct {
	calls    atomic.Int32
	rejected bool
	expires  int
}

func (rt *emailTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	if err := request.ParseForm(); err != nil {
		return nil, err
	}
	if request.URL.Host != "login.microsoftonline.com" || request.Form.Get("scope") != "https://graph.microsoft.com/.default" || request.Form.Get("grant_type") != "client_credentials" || request.Form.Get("client_secret") != "application-secret" {
		return nil, errors.New("unexpected token request")
	}
	status, body := 200, fmt.Sprintf(`{"access_token":"test-access-token","token_type":"Bearer","expires_in":%d}`, rt.expires)
	if rt.rejected {
		status = 400
		body = `{"error":"invalid_client","error_description":"application-secret test-access-token"}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestMicrosoftOAuthTokenLifecycleAndPrivacy(t *testing.T) {
	previousTransport := http.DefaultTransport
	previousTokens := microsoftTokens.token
	previousRevision := microsoftTokens.revision
	previousFingerprint := microsoftTokens.fingerprint
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
		microsoftTokens.token = previousTokens
		microsoftTokens.revision = previousRevision
		microsoftTokens.fingerprint = previousFingerprint
	})
	rt := &emailTokenRoundTripper{expires: 3600}
	http.DefaultTransport = rt
	config := EmailDeliveryConfig{Revision: 1, Options: EmailDeliveryOptions{AuthMode: EmailAuthMicrosoft, Microsoft: MicrosoftEmailSettings{TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222", Mailbox: "sender@example.com", ClientSecret: "application-secret"}}}
	token, err := microsoftEmailToken(context.Background(), config, true)
	require.NoError(t, err)
	assert.Equal(t, "test-access-token", token)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := microsoftEmailToken(context.Background(), config, false); results <- err })
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), rt.calls.Load())
	config.Revision++
	_, err = microsoftEmailToken(context.Background(), config, false)
	require.NoError(t, err)
	assert.Equal(t, int32(2), rt.calls.Load())
	// A short-lived token is refreshed without sleeps or timing assertions.
	rt.expires = 1
	_, err = microsoftEmailToken(context.Background(), config, true)
	require.NoError(t, err)
	before := rt.calls.Load()
	_, err = microsoftEmailToken(context.Background(), config, false)
	require.NoError(t, err)
	assert.Greater(t, rt.calls.Load(), before)
	rt.expires = 0
	_, err = microsoftEmailToken(context.Background(), config, true)
	require.Error(t, err)
	rt.rejected = true
	_, err = microsoftEmailToken(context.Background(), config, true)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "application-secret")
	assert.NotContains(t, err.Error(), "test-access-token")
	report, err := DeliverConfiguredEmail(context.Background(), config, "Test", "receiver@example.com", "body")
	require.Error(t, err)
	assert.Equal(t, "failed", report.Stages[1].Status)
	assert.Equal(t, "skipped", report.Stages[2].Status)
	assert.NotContains(t, err.Error(), "application-secret")
}

func TestEmailDeliveryRejectsUnsafeOrIncompleteRequests(t *testing.T) {
	config := EmailDeliveryConfig{Options: EmailDeliveryOptions{AuthMode: EmailAuthLegacy, Legacy: LegacyEmailSettings{Host: "smtp.example.com", Port: 587, From: "sender@example.com"}}}
	for _, receiver := range []string{"display <receiver@example.com>", "receiver@example.com\r\nBcc: bad@example.com", "a@example.com,b@example.com"} {
		report, err := DeliverConfiguredEmail(context.Background(), config, "Test", receiver, "body")
		require.Error(t, err)
		assert.Equal(t, "failed", report.Stages[0].Status)
	}
	intent := EmailDeliveryTestIntent{}
	for _, raw := range []string{`{"expected_revision":1,"recipient":"a@example.com","extra":true}`, `{"expected_revision":1,"recipient":null}`, `null`, `{"recipient":"a@example.com"}`} {
		assert.Error(t, DecodeEmailDeliveryJSON([]byte(raw), &intent))
	}
}

func TestEmailDeliverySMTPSubmissionFailureSemantics(t *testing.T) {
	for _, scenario := range []struct{ name, reply, status, code string }{
		{"explicit rejection", "550 message rejected", "failed", "EMAIL_DELIVERY_FAILED"},
		{"connection lost after DATA", "", "unknown", "EMAIL_DELIVERY_UNKNOWN"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
				_ = writeSMTPLine(rw, "220 smtp fixture")
				for {
					line, err := rw.ReadString('\n')
					if err != nil {
						return
					}
					command := strings.TrimSpace(line)
					switch {
					case strings.HasPrefix(command, "EHLO"):
						_ = writeSMTPLine(rw, "250 smtp fixture")
					case strings.HasPrefix(command, "MAIL"), strings.HasPrefix(command, "RCPT"):
						_ = writeSMTPLine(rw, "250 OK")
					case command == "DATA":
						_ = writeSMTPLine(rw, "354 send data")
						for {
							line, err = rw.ReadString('\n')
							if err != nil {
								return
							}
							if strings.TrimSpace(line) == "." {
								break
							}
						}
						if scenario.reply != "" {
							_ = writeSMTPLine(rw, scenario.reply)
						}
						return
					default:
						return
					}
				}
			}()
			host, portText, err := net.SplitHostPort(listener.Addr().String())
			require.NoError(t, err)
			port, err := strconv.Atoi(portText)
			require.NoError(t, err)
			config := EmailDeliveryConfig{Options: EmailDeliveryOptions{AuthMode: EmailAuthLegacy, Legacy: LegacyEmailSettings{Host: host, Port: port, From: "sender@example.com"}}}
			report, err := DeliverConfiguredEmail(context.Background(), config, "Test", "receiver@example.com", "body")
			require.Error(t, err)
			assert.Equal(t, scenario.status, report.Stages[4].Status)
			assert.Equal(t, scenario.code, report.Code)
			<-done
		})
	}
}

func TestEmailDeliveryPreservesMicrosoftLegacyNTLM(t *testing.T) {
	server := newFakeSMTPServer(t)
	server.authMechanisms = []string{"NTLM"}
	config := EmailDeliveryConfig{Options: EmailDeliveryOptions{AuthMode: EmailAuthLegacy, Legacy: LegacyEmailSettings{Host: server.host, Port: server.port, Account: "sender@contoso.onmicrosoft.com", Password: "fixture-secret", From: "sender@example.com", StartTLS: true, InsecureSkipVerify: true}}}
	report, err := DeliverConfiguredEmail(context.Background(), config, "Test", "receiver@example.com", "body")
	require.NoError(t, err)
	assert.Equal(t, "EMAIL_ACCEPTED", report.Code)
	assert.True(t, strings.HasPrefix(<-server.authCommands, "AUTH NTLM "))
}

func TestMicrosoftOAuthCancelledDeliveryDoesNotRequestToken(t *testing.T) {
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	transport := &emailTokenRoundTripper{expires: 3600, rejected: true}
	http.DefaultTransport = transport
	config := EmailDeliveryConfig{Revision: 10001, Options: EmailDeliveryOptions{AuthMode: EmailAuthMicrosoft, Microsoft: MicrosoftEmailSettings{TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222", Mailbox: "sender@example.com", ClientSecret: "application-secret"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := DeliverConfiguredEmail(ctx, config, "Test", "receiver@example.com", "body")
	require.Error(t, err)
	assert.Zero(t, transport.calls.Load(), "cancelled delivery must not start a token request")
}

// The context signals when a caller reaches its cancellable wait, so concurrent
// refresh tests coordinate with channels rather than sleeps or timing comparisons.
type emailTokenWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *emailTokenWaitingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

type emailTokenBlockedTransport struct {
	entered chan *http.Request
	release chan struct{}
	exited  chan struct{}
	reply   emailTokenRoundTripper
}

func (transport *emailTokenBlockedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer close(transport.exited)
	transport.entered <- request
	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case <-transport.release:
		return transport.reply.RoundTrip(request)
	}
}
func withEmailTokenTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previousTransport := http.DefaultTransport
	microsoftTokens.Lock()
	previousToken, previousFingerprint, previousRevision := microsoftTokens.token, microsoftTokens.fingerprint, microsoftTokens.revision
	microsoftTokens.token = nil
	microsoftTokens.fingerprint = [32]byte{}
	microsoftTokens.revision = 0
	microsoftTokens.Unlock()
	http.DefaultTransport = transport
	t.Cleanup(func() {
		microsoftTokens.Lock()
		pending := microsoftTokens.refresh
		if pending != nil {
			pending.cancel()
		}
		microsoftTokens.Unlock()
		if pending != nil {
			<-pending.done
		}
		microsoftTokens.Lock()
		microsoftTokens.token, microsoftTokens.fingerprint, microsoftTokens.revision = previousToken, previousFingerprint, previousRevision
		microsoftTokens.Unlock()
		http.DefaultTransport = previousTransport
	})
}
func TestMicrosoftOAuthSharedRefreshCancellationAndFailure(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint("rejected=", rejected), func(t *testing.T) {
			transport := &emailTokenBlockedTransport{entered: make(chan *http.Request, 1), release: make(chan struct{}), exited: make(chan struct{}), reply: emailTokenRoundTripper{expires: 3600, rejected: rejected}}
			withEmailTokenTransport(t, transport)
			config := EmailDeliveryConfig{Revision: 1, Options: EmailDeliveryOptions{AuthMode: EmailAuthMicrosoft, Microsoft: MicrosoftEmailSettings{TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222", Mailbox: "sender@example.com", ClientSecret: "application-secret"}}}
			firstContext, cancel := context.WithCancel(t.Context())
			defer cancel()
			firstResult := make(chan error, 1)
			go func() { _, err := microsoftEmailToken(firstContext, config, false); firstResult <- err }()
			<-transport.entered
			secondContext := &emailTokenWaitingContext{Context: t.Context(), waiting: make(chan struct{})}
			secondResult := make(chan error, 1)
			go func() { _, err := microsoftEmailToken(secondContext, config, false); secondResult <- err }()
			<-secondContext.waiting
			cancel()
			require.ErrorIs(t, <-firstResult, context.Canceled)
			close(transport.release)
			err := <-secondResult
			if rejected {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "application-secret")
			} else {
				require.NoError(t, err)
			}
			assert.EqualValues(t, 1, transport.reply.calls.Load(), "active waiters share both success and failure without serial HTTP retries")
		})
	}
}
func TestMicrosoftOAuthLastWaiterCancelsHTTPRequest(t *testing.T) {
	transport := &emailTokenBlockedTransport{entered: make(chan *http.Request, 1), release: make(chan struct{}), exited: make(chan struct{})}
	withEmailTokenTransport(t, transport)
	config := EmailDeliveryConfig{Revision: 1, Options: EmailDeliveryOptions{AuthMode: EmailAuthMicrosoft, Microsoft: MicrosoftEmailSettings{TenantID: "11111111-1111-1111-1111-111111111111", ClientID: "22222222-2222-2222-2222-222222222222", Mailbox: "sender@example.com", ClientSecret: "application-secret"}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := microsoftEmailToken(ctx, config, false); result <- err }()
	request := <-transport.entered
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	<-transport.exited
	assert.ErrorIs(t, request.Context().Err(), context.Canceled)
}

func TestNotificationEmailTemplatesPreserveSecurityContentAndBlockActiveMarkup(t *testing.T) {
	for kind := range emailTemplateKinds {
		t.Run(string(kind), func(t *testing.T) {
			body, err := RenderEmailTemplate(kind, "<b>Account & status</b>", `<p>Verification code: <strong>482731</strong></p><a href="https://gateway.example/account-activation#token=synthetic-token&amp;purpose=security_password_reset">Continue</a><script>attack()</script><img src="https://tracking.invalid/pixel"><a href="javascript:attack()">unsafe</a><p onmouseover="attack()">safe</p>`)
			require.NoError(t, err)
			assert.Contains(t, body, `data-email-template="`+string(kind)+`"`)
			assert.Contains(t, body, "&lt;b&gt;Account &amp; status&lt;/b&gt;")
			assert.Contains(t, body, "<strong>482731</strong>")
			assert.Contains(t, body, `https://gateway.example/account-activation#token=synthetic-token&amp;purpose=security_password_reset`)
			assert.NotContains(t, body, "attack()")
			assert.NotContains(t, body, "tracking.invalid")
			assert.NotContains(t, body, "onmouseover")
			assert.Contains(t, body, "new-api · QuantumNous")
			assert.Equal(t, 1, strings.Count(body, "<!doctype html>"))
		})
	}
	_, err := RenderEmailTemplate(EmailKind("unknown"), "title", "body")
	require.Error(t, err)
	_, err = RenderEmailTemplate(EmailNotice, "title\r\nBcc: attacker@example.com", "body")
	require.Error(t, err)
}
func TestNotificationEmailTemplateReachesSMTPThroughBothSendAPIs(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint("configured=", configured), func(t *testing.T) {
			server := newFakeSMTPServer(t)
			defer server.close()
			old := DefaultEmailDelivery
			oldHost, oldPort, oldFrom, oldAccount, oldToken := SMTPServer, SMTPPort, SMTPFrom, SMTPAccount, SMTPToken
			oldSSL, oldTLS, oldSkip := SMTPSSLEnabled, SMTPStartTLSEnabled, SMTPInsecureSkipVerify
			t.Cleanup(func() {
				DefaultEmailDelivery = old
				SMTPServer, SMTPPort, SMTPFrom, SMTPAccount, SMTPToken = oldHost, oldPort, oldFrom, oldAccount, oldToken
				SMTPSSLEnabled, SMTPStartTLSEnabled, SMTPInsecureSkipVerify = oldSSL, oldTLS, oldSkip
			})
			DefaultEmailDelivery = &EmailDeliveryRuntime{}
			SMTPServer, SMTPPort, SMTPFrom, SMTPAccount, SMTPToken = server.host, server.port, "sender@example.com", "sender@example.com", "test-only"
			SMTPSSLEnabled, SMTPStartTLSEnabled, SMTPInsecureSkipVerify = false, true, true
			if configured {
				PublishEmailDelivery(EmailDeliveryConfig{Options: EmailDeliveryOptions{AuthMode: EmailAuthLegacy, Legacy: LegacyEmailSettings{Host: server.host, Port: server.port, From: SMTPFrom, Account: SMTPAccount, Password: SMTPToken, StartTLS: true, InsecureSkipVerify: true}}}, nil)
			}
			require.NoError(t, SendNotificationEmail(EmailSecurityChange, "Security changed", "receiver@example.com", "<p>Security settings changed</p>"))
			select {
			case message := <-server.messages:
				assert.Contains(t, message, `data-email-template="security-change"`)
				assert.Equal(t, 1, strings.Count(message, "<!doctype html>"))
				assert.Contains(t, message, "Security settings changed")
			case <-time.After(3 * time.Second):
				t.Fatal("SMTP did not receive the templated message")
			}
		})
	}
}
