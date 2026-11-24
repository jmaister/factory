package workflow

import (
	"fmt"

	"factory/internal/config"
	"factory/internal/tickets"
)

// YoloLabel es la etiqueta de Vikunja que marca una tarea que no necesita intervención humana.
const YoloLabel = "yolo"

// isYolo indica si la tarea lleva la etiqueta YOLO.
func isYolo(t tickets.Tracker, task tickets.Task) bool {
	labels, err := t.Labels(task.ID)
	if err != nil {
		return false
	}
	for _, l := range labels {
		if l == YoloLabel {
			return true
		}
	}
	return false
}

// MarkYolo pone la etiqueta YOLO a una tarea de DRAFT y la pasa a TODO.
func MarkYolo(cfg *config.Config, ref string) error {
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
		return fmt.Errorf("la tarea %s está en '%s'; YOLO solo se puede marcar desde DRAFT", ref, cur)
	}
	if err := t.AddLabel(task.ID, YoloLabel); err != nil {
		return fmt.Errorf("no pude añadir la etiqueta %s: %w", YoloLabel, err)
	}
	return moveTo(t, cfg, pid, task.ID, "todo")
}
