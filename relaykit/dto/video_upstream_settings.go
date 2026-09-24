package dto

import (
	"fmt"
	"strings"
)

// Southbound video task protocol and vendor-extension contracts. Values are
// stored in the channel setting JSON and interpreted only by the bound task
// plugin; the host validates syntax and combination at save time.
const (
	VideoUpstreamProtocolArk         = "ark"
	VideoUpstreamProtocolOpenAIVideo = "openai_video"
	VideoUpstreamProfileStandard     = "standard"
	VideoUpstreamProfileCodeYY       = "seedance_codeyy"
	VideoUpstreamProfileZapgogo      = "seedance_zapgogo"
)

// ValidateVideoUpstream validates the southbound video protocol settings.
// Empty values keep the defaults (ark / standard). A profile is only
// meaningful on openai_video channels: ark rejects extension values so a
// misconfigured channel fails at save time instead of at first submission.
func (s *ChannelSettings) ValidateVideoUpstream() error {
	if s == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(s.VideoUpstreamProtocol)) {
	case "", VideoUpstreamProtocolArk, VideoUpstreamProtocolOpenAIVideo:
	default:
		return fmt.Errorf("invalid video_upstream_protocol: %s", s.VideoUpstreamProtocol)
	}
	switch strings.ToLower(strings.TrimSpace(s.VideoUpstreamProfile)) {
	case "", VideoUpstreamProfileStandard, VideoUpstreamProfileCodeYY, VideoUpstreamProfileZapgogo:
	default:
		return fmt.Errorf("invalid video_upstream_profile: %s", s.VideoUpstreamProfile)
	}
	protocol := strings.ToLower(strings.TrimSpace(s.VideoUpstreamProtocol))
	profile := strings.ToLower(strings.TrimSpace(s.VideoUpstreamProfile))
	if profile != "" && profile != VideoUpstreamProfileStandard && protocol != VideoUpstreamProtocolOpenAIVideo {
		return fmt.Errorf("video_upstream_profile %s requires video_upstream_protocol %s", profile, VideoUpstreamProtocolOpenAIVideo)
	}
	return nil
}

// ResolvedVideoUpstream returns the protocol and profile with defaults
// applied to legacy empty values. Saving is gated by ValidateVideoUpstream;
// unrecognized legacy values retain the native Ark default.
func (s ChannelSettings) ResolvedVideoUpstream() (protocol, profile string) {
	protocol = strings.ToLower(strings.TrimSpace(s.VideoUpstreamProtocol))
	profile = strings.ToLower(strings.TrimSpace(s.VideoUpstreamProfile))
	if protocol != VideoUpstreamProtocolOpenAIVideo {
		return VideoUpstreamProtocolArk, VideoUpstreamProfileStandard
	}
	if profile != VideoUpstreamProfileCodeYY && profile != VideoUpstreamProfileZapgogo {
		profile = VideoUpstreamProfileStandard
	}
	return protocol, profile
}
