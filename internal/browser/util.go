package browser

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// reqCounter is a monotonic counter used as the second component of request
// IDs, ensuring uniqueness even if requests are generated in the same
// millisecond.
var reqCounter uint64

// newRequestID returns a short, unique ID for correlating a Go-side
// dispatch with its JS callback. Format: <8 hex chars>-<counter>.
func newRequestID() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	n := atomic.AddUint64(&reqCounter, 1)
	return fmt.Sprintf("%x-%d", buf, n)
}

// parseDataURL extracts the base64-encoded payload from a "data:..." URL.
func parseDataURL(s string) ([]byte, error) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return nil, errors.New("not a data URL")
	}
	rest := s[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return nil, errors.New("malformed data URL: missing comma")
	}
	// rest[:comma] looks like "image/png;base64"
	header := rest[:comma]
	payload := rest[comma+1:]
	if !strings.Contains(header, "base64") {
		// Some payloads (e.g. text/plain) are not base64-encoded.
		return []byte(payload), nil
	}
	return base64.StdEncoding.DecodeString(payload)
}
