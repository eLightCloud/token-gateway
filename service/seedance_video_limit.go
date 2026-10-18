package service

import "io"

// An unknown Content-Length must fail the upload stream before completion,
// rather than successfully committing a truncated or oversized object.
type seedanceVideoLimitReader struct {
	source    io.Reader
	remaining int64
}

func (r *seedanceVideoLimitReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.source.Read(probe[:])
		if n > 0 {
			return 0, seedanceProcessingFailure("video_size_limit_exceeded", nil)
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.source.Read(buffer)
	r.remaining -= int64(n)
	return n, err
}
