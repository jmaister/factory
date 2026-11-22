// Package webhook recibe los avisos de Vikunja y despierta el bucle de la factory.
// Es solo una señal: el bucle sigue comprobando el tablero periódicamente por si se pierde algún aviso.
package webhook

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
)

// Serve escucha en addr y, por cada aviso válido, envía una señal a wake (sin bloquear).
// El token debe venir en la query (?token=...).
func Serve(addr, token string, wake chan<- struct{}, onEvent func(event string), logf func(string, ...any)) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if onEvent != nil {
			onEvent(eventName(body))
		}
		select {
		case wake <- struct{}{}:
		default: // ya hay una señal pendiente
		}
		w.WriteHeader(http.StatusOK)
	})
	logf("webhook escuchando en %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	return srv.ListenAndServe()
}

// eventName saca el nombre del evento del JSON de Vikunja (para el log).
func eventName(body []byte) string {
	var v struct {
		EventName string `json:"event_name"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.EventName == "" {
		return "(desconocido)"
	}
	return v.EventName
}
