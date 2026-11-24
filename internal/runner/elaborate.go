package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ElaborationPrompt pide a Claude que convierta una tarea vaga en una propuesta con opciones y criterios.
func ElaborationPrompt(identifier, title, description string) string {
	return fmt.Sprintf(`Eres el analista de la tarea %s. Su descripción es vaga: elabórala con lo que hay en el repositorio (solo puedes leer, no modificar nada).

Título: %s

Descripción actual:
%s

Responde SOLO con HTML válido (sin bloques de código), con estas secciones en este orden:
<h2>Objetivo</h2>, <h2>Alcance</h2>, <h2>Opciones</h2> (cada opción con sus pros y contras en una lista), <h2>Recomendación</h2>, <h2>Preguntas abiertas</h2> (cada una con la opción por defecto que tomarías), <h2>Criterios de aceptación</h2>.
Usa <p>, <ul>, <li> y <strong>. Sé concreto: cita ficheros y funciones que existan.`, identifier, title, PlainText(description))
}

// Elaborate ejecuta Claude en modo no interactivo sobre el repo (solo lectura) y devuelve el HTML de la elaboración.
// La sesión queda guardada con el ID dado, para retomarla luego con --resume.
func Elaborate(repoDir, sessionID, model, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
		"--session-id", sessionID,
		"--model", model,
		"--allowedTools", "Read,Grep,Glob")
	cmd.Dir = repoDir
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude falló: %w: %s", err, strings.TrimSpace(errOut.String()))
	}
	text := strings.TrimSpace(out.String())
	text = strings.TrimPrefix(text, "```html")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text), nil
}
