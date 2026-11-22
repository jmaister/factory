// Package sessions genera los identificadores de sesión de Claude Code (UUID v4) para --session-id.
// Las sesiones se registran como comentarios de la tarea en Vikunja, no en un fichero local.
package sessions

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewUUID genera un UUID v4, que es lo que acepta --session-id.
func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}
