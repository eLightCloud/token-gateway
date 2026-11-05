package common

import (
	"errors"
	"net/smtp"
	"strings"

	ntlmssp "github.com/Azure/go-ntlmssp"
)

type smtpAutoAuth struct {
	username    string
	password    string
	mech        string
	configured  bool
	server      string
	forceLogin  bool
	preferLogin bool
}

func AutoSMTPAuth(username, password string) smtp.Auth {
	return &smtpAutoAuth{username: username, password: password}
}

func (a *smtpAutoAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	host := SMTPServer
	useLoginAuth := SMTPForceAuthLogin
	preferLogin := shouldUseSMTPLoginAuth()
	if a.configured {
		host = a.server
		useLoginAuth = a.forceLogin
		preferLogin = a.preferLogin
	}
	if !useLoginAuth && preferLogin {
		useLoginAuth = !(server != nil && len(server.Auth) == 1 && smtpServerSupportsAuth(server, "NTLM"))
	}
	if useLoginAuth {
		a.mech = "LOGIN"
		return "LOGIN", []byte{}, nil
	}

	switch {
	case smtpServerSupportsAuth(server, "PLAIN"):
		a.mech = "PLAIN"
		return smtp.PlainAuth("", a.username, a.password, host).Start(server)
	case smtpServerSupportsAuth(server, "LOGIN"):
		a.mech = "LOGIN"
		return "LOGIN", []byte{}, nil
	case smtpServerSupportsAuth(server, "NTLM"):
		a.mech = "NTLM"
		negotiateMessage, err := ntlmssp.NewNegotiateMessage("", "")
		if err != nil {
			return "", nil, err
		}
		return "NTLM", negotiateMessage, nil
	default:
		a.mech = "PLAIN"
		return smtp.PlainAuth("", a.username, a.password, host).Start(server)
	}
}

func (a *smtpAutoAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}

	switch a.mech {
	case "LOGIN":
		switch string(fromServer) {
		case "Username:":
			return []byte(a.username), nil
		case "Password:":
			return []byte(a.password), nil
		default:
			return nil, errors.New("unknown SMTP AUTH LOGIN challenge")
		}
	case "NTLM":
		return ntlmssp.NewAuthenticateMessage(fromServer, a.username, a.password, nil)
	default:
		return nil, errors.New("unexpected SMTP auth challenge")
	}
}

func smtpServerSupportsAuth(server *smtp.ServerInfo, mechanism string) bool {
	if server == nil {
		return false
	}
	for _, auth := range server.Auth {
		if strings.EqualFold(auth, mechanism) {
			return true
		}
	}
	return false
}
