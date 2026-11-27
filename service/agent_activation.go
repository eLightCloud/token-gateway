package service

import (
	"errors"
	"net"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func SendAgentActivationResumeMail(address, lang string) {
	defer func() {
		if recover() != nil {
			common.SysError("agent activation resume mail worker failed")
		}
	}()
	email, err := ValidateAccountEmail(address)
	if err != nil {
		return
	}
	user, err := model.GetUniqueUserByEmail(email)
	if err != nil {
		if !errors.Is(err, model.ErrEmailNotFound) && !errors.Is(err, model.ErrEmailAmbiguous) {
			common.SysError("could not look up agent activation resume account")
		}
		return
	}
	token, err := model.NewAgentActivationResume(user.Id, email)
	if err != nil {
		if !errors.Is(err, model.ErrAuthFlowInvalid) && !errors.Is(err, model.ErrEmailBindingResendWait) {
			common.SysError("could not create agent activation resume flow")
		}
		return
	}
	if err := deliverResellerActivationLink(user.Id, model.SecurityAgentResumePurpose, email, token, "", "", lang); err != nil {
		common.SysError("could not send agent activation resume email")
	}
}

// AgentActivationOrigin returns the public origin for invitations and binding.
func AgentActivationOrigin() (string, error) {
	origin, err := common.NormalizeOrigin(system_setting.ServerAddress)
	if err != nil {
		return "", model.ErrAgentActivationTransport
	}
	address, err := url.Parse(origin)
	if err != nil {
		return "", model.ErrAgentActivationTransport
	}
	if address.Scheme == "https" || address.Hostname() == "localhost" || net.ParseIP(address.Hostname()).IsLoopback() {
		return origin, nil
	}
	return "", model.ErrAgentActivationTransport
}
