package workflow

import (
	"fmt"
	"strings"

	"factory/internal/config"
	"factory/internal/runner"
	"factory/internal/sessions"
	"factory/internal/tickets"
)

// elaboratedMarker empieza el comentario que indica que la tarea ya tiene su elaboración.
const elaboratedMarker = "Elaborada con Claude"

// hasElaborated indica si la tarea ya fue elaborada: al pasar a TODO se retoma su sesión para implementarla.
func hasElaborated(cs []tickets.Comment) bool {
	for _, c := range cs {
		if strings.HasPrefix(tickets.PlainComment(c.Comment), elaboratedMarker) {
			return true
		}
	}
	return false
}

// Elaborate amplía una tarea de DRAFT: Claude la analiza contra el repo (solo lectura) y el resultado
// se añade a la descripción con un separador. La sesión queda guardada para implementarla después.
func Elaborate(cfg *config.Config, ref string, logf func(string, ...any)) error {
	t, err := trackerFor(cfg)
	if err != nil {
		return err
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return err
	}
	task, found, err := findTask(t, pid, ref)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no encuentro la tarea %s", ref)
	}
	board, err := t.Board(pid)
	if err != nil {
		return err
	}
	if cur := columnOf(board, task.ID); cur != config.States["draft"] {
		return fmt.Errorf("la tarea %s está en '%s'; solo se puede elaborar desde DRAFT", ref, cur)
	}
	cs, err := t.Comments(task.ID)
	if err != nil {
		return err
	}
	if hasElaborated(cs) {
		return fmt.Errorf("la tarea %s ya está elaborada", ref)
	}

	id, err := sessions.NewUUID()
	if err != nil {
		return err
	}
	_ = t.AddComment(task.ID, fmt.Sprintf("Sesión de Claude: %s\nPara retomarla a mano: claude --resume %s", id, id))

	setPhase(fmt.Sprintf("elaborando %s con Claude (solo lectura del repo)", ref))
	defer setPhase("")
	logf("%s: elaboración iniciada (sesión %s)", ref, id)
	out, err := runner.Elaborate(cfg.Repo.Path, id, cfg.Agent.Model, runner.ElaborationPrompt(ref, task.Title, task.Description))
	if err != nil {
		_ = t.AddComment(task.ID, "No pude elaborar la tarea: "+err.Error())
		return err
	}

	desc := task.Description + "\n<hr>\n<h2>Elaboración de Claude</h2>\n" + out
	if err := t.SetDescription(task.ID, desc); err != nil {
		return fmt.Errorf("guardar la elaboración: %w", err)
	}
	_ = t.AddComment(task.ID, fmt.Sprintf("%s (sesión %s). Al pasar a TODO la factory retoma esa sesión y la implementa.", elaboratedMarker, id))
	logf("%s: elaborada", ref)
	return nil
}
