package common

// SecurityLinkEmailCopy contains the localized framing for an account security link.
type SecurityLinkEmailCopy struct {
	Language, Label, Preview, Heading, Warning, Footer, PoweredBy string
}

func SendSecurityLinkEmail(kind EmailKind, subject, receiver, content string, copy SecurityLinkEmailCopy) error {
	rendered, err := renderEmailTemplate(kind, subject, content, &copy)
	if err != nil {
		return err
	}
	return sendEmail(subject, receiver, rendered)
}
