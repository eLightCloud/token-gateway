package service

import (
	"errors"
	"io"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

var ErrSeedanceSourceIncompatible = errors.New("source video does not meet enhancement input requirements")

// Validate actual MP4 codec headers, rather than trusting the requested 720p
// label. This reads metadata only; video/audio samples are copied unchanged.
func validateSeedanceSourceMedia(reader io.ReadSeeker) error {
	// Bound container metadata before the MP4 parser reads ftyp automatically.
	for count := 0; ; count++ {
		if count > 10000 {
			return ErrSeedanceSourceIncompatible
		}
		info, err := mp4.ReadBoxInfo(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrSeedanceSourceIncompatible
		}
		if (info.Type == mp4.BoxTypeFtyp() && info.Size > 64<<10) || (info.Type == mp4.BoxTypeMoov() && info.Size > 32<<20) {
			return ErrSeedanceSourceIncompatible
		}
		if _, err = info.SeekToEnd(reader); err != nil {
			return ErrSeedanceSourceIncompatible
		}
	}
	foundVideo := false
	visited := 0
	_, err := mp4.ReadBoxStructure(reader, func(handle *mp4.ReadHandle) (any, error) {
		visited++
		if visited > 10000 || len(handle.Path) > 10 {
			return nil, ErrSeedanceSourceIncompatible
		}
		kind := handle.BoxInfo.Type.String()
		switch kind {
		case "moov", "trak", "mdia", "minf", "stbl", "stsd", "avc1", "avc3", "hvc1", "hev1":
			_, err := handle.Expand()
			return nil, err
		case "avcC", "hvcC", "colr":
			if handle.BoxInfo.Size > 1<<20 {
				return nil, ErrSeedanceSourceIncompatible
			}
			payload, _, err := handle.ReadPayload()
			if err != nil {
				return nil, ErrSeedanceSourceIncompatible
			}
			switch box := payload.(type) {
			case *mp4.AVCDecoderConfiguration:
				for _, entry := range box.SequenceParameterSets {
					var sps h264.SPS
					if sps.Unmarshal(entry.NALUnit) != nil {
						return nil, ErrSeedanceSourceIncompatible
					}
					transfer := uint8(2)
					if sps.VUI != nil && sps.VUI.ColourDescriptionPresentFlag {
						transfer = sps.VUI.TransferCharacteristics
					}
					if err := validateSeedanceSourceFormat(sps.Width(), sps.Height(), transfer, sps.BitDepthLumaMinus8); err != nil {
						return nil, err
					}
					foundVideo = true
				}
			case *mp4.HvcC:
				for _, array := range box.NaluArrays {
					if array.NaluType != 33 {
						continue
					}
					for _, entry := range array.Nalus {
						var sps h265.SPS
						if sps.Unmarshal(entry.NALUnit) != nil {
							return nil, ErrSeedanceSourceIncompatible
						}
						transfer := uint8(2)
						if sps.VUI != nil && sps.VUI.ColourDescriptionPresentFlag {
							transfer = sps.VUI.TransferCharacteristics
						}
						if err := validateSeedanceSourceFormat(sps.Width(), sps.Height(), transfer, uint32(box.BitDepthLumaMinus8)); err != nil {
							return nil, err
						}
						foundVideo = true
					}
				}
			case *mp4.Colr:
				if box.TransferCharacteristics == 16 || box.TransferCharacteristics == 18 {
					return nil, ErrSeedanceSourceIncompatible
				}
			}
		}
		return nil, nil
	})
	if err != nil || !foundVideo {
		return ErrSeedanceSourceIncompatible
	}
	return nil
}

func validateSeedanceSourceFormat(width, height int, transfer uint8, bitDepthMinus8 uint32) error {
	if min(width, height) < 360 || min(width, height) > 1080 || max(width, height) > 1920 {
		return ErrSeedanceSourceIncompatible
	}
	// PQ and HLG are HDR. Unspecified high-bit-depth video cannot be established
	// as SDR; ordinary unspecified 8-bit AVC/HEVC follows the codec default.
	if transfer == 16 || transfer == 18 || (bitDepthMinus8 > 0 && (transfer == 0 || transfer == 2)) {
		return ErrSeedanceSourceIncompatible
	}
	return nil
}
