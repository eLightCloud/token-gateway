package common

import (
	"errors"
	"net/smtp"
	"strings"
)

type microsoftEmailAuth struct{ mailbox, token string }

func (a *microsoftEmailAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil || !server.TLS || !strings.EqualFold(server.Name, "smtp.office365.com") || !smtpServerSupportsAuth(server, "XOAUTH2") {
		return "", nil, errors.New("Microsoft SMTP requires verified TLS and XOAUTH2 support")
	}
	return "XOAUTH2", []byte("user=" + a.mailbox + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}
func (a *microsoftEmailAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("Microsoft SMTP authentication was rejected")
	}
	return nil, nil
}
