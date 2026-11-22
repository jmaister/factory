package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// signalFile es el fichero que escribe el hook Stop de Claude cada vez que deja de procesar.
func signalFile(stateDir, identifier string) string {
	return filepath.Join(stateDir, "signals", SessionName(identifier)+".stop")
}

// LaunchAgent arranca Claude Code en una sesión de screen en segundo plano, con un hook Stop que avisa
// a la factory cada vez que Claude deja de procesar. No espera: la sesión queda viva (Remote Control).
func LaunchAgent(s Session, stateDir string) error {
	prompt := s.Prompt
	if prompt == "" {
		prompt = Prompt(s.Identifier, s.Title, s.Description)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "signals"), 0o755); err != nil {
		return err
	}
	base := filepath.Join(stateDir, SessionName(s.Identifier))
	promptFile, scriptFile, settingsFile := base+".prompt", base+".sh", base+".settings.json"
	sig := signalFile(stateDir, s.Identifier)
	_ = os.Remove(sig)

	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return err
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "date +%s > " + shellQuote(sig)},
				}},
			},
		},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := os.WriteFile(settingsFile, raw, 0o600); err != nil {
		return err
	}
	resume := "--session-id " + s.SessionID
	if s.Resume {
		resume = "--resume " + s.SessionID
	}
	script := fmt.Sprintf("#!/usr/bin/env bash\ncd %s || exit 1\nexec claude \"$(cat %s)\" --model %s --name %s --remote-control %s --settings %s %s\n",
		shellQuote(s.Dir), shellQuote(promptFile), shellQuote(s.Model), shellQuote(s.Name), shellQuote(s.Name), shellQuote(settingsFile), resume)
	if err := os.WriteFile(scriptFile, []byte(script), 0o700); err != nil {
		return err
	}
	session := SessionName(s.Identifier)
	if out, err := exec.Command("screen", "-dmS", session, "bash", scriptFile).CombinedOutput(); err != nil {
		return fmt.Errorf("no pude lanzar la sesión de Claude: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WaitTurn espera a que Claude deje de procesar (señal del hook Stop) o a que la sesión termine.
// Devuelve "stop", "gone" o "cancel".
func WaitTurn(identifier, stateDir string, cancel <-chan struct{}) string {
	sig := signalFile(stateDir, identifier)
	session := SessionName(identifier)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return "cancel"
		case <-ticker.C:
			if _, err := os.Stat(sig); err == nil {
				_ = os.Remove(sig)
				return "stop"
			}
			if !screenRunning(session) {
				return "gone"
			}
		}
	}
}

// SessionAlive indica si la sesión de Claude de la tarea sigue abierta.
func SessionAlive(identifier string) bool {
	return screenRunning(SessionName(identifier))
}

// Inject escribe un mensaje en la sesión viva de Claude y pulsa Enter.
func Inject(identifier, text string) error {
	text = strings.Join(strings.Fields(text), " ")
	session := SessionName(identifier)
	if out, err := exec.Command("screen", "-S", session, "-X", "stuff", text).CombinedOutput(); err != nil {
		return fmt.Errorf("no pude escribir en la sesión: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// El Enter va aparte: si llega pegado al texto, Claude lo toma como salto de línea y no envía.
	time.Sleep(time.Second)
	if out, err := exec.Command("screen", "-S", session, "-X", "stuff", "\r").CombinedOutput(); err != nil {
		return fmt.Errorf("no pude enviar el mensaje a la sesión: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// EndSession cierra la sesión de screen de la tarea (el proceso de Claude termina; su conversación queda en disco).
func EndSession(identifier string) error {
	out, err := exec.Command("screen", "-S", SessionName(identifier), "-X", "quit").CombinedOutput()
	if err != nil {
		return fmt.Errorf("no pude cerrar la sesión: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
