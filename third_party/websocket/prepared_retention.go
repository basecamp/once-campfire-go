//go:build !js

package websocket

import "unsafe"

// RetainedSize resolves compression before cache admission so its backing
// capacity cannot grow after the cache charges this immutable message.
func (p *PreparedMessage) RetainedSize() int {
	p.deflate()
	return int(unsafe.Sizeof(*p)) + cap(p.data) + cap(p.compressed)
}
