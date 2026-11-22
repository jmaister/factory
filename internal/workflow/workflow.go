// Package workflow implementa los comandos de usuario y el bucle de la factory.
// El estado de una tarea es la columna del tablero en la que está.
package workflow

import (
	"fmt"
	"strconv"
	"strings"

	"factory/internal/check"
	"factory/internal/config"
	"factory/internal/repos"
	"factory/internal/tickets"
)

func trackerFor(cfg *config.Config) (tickets.Tracker, error) {
	return check.NewTracker(cfg.Tickets)
}

func hostFor(cfg *config.Config) (repos.Host, error) {
	return check.NewRepoHost(cfg.Repo)
}

// projectID devuelve el id del proyecto de Vikunja configurado.
func projectID(cfg *config.Config, t tickets.Tracker) (int64, error) {
	return t.FindProject(cfg.Tickets.Project)
}

// columnFor devuelve la columna del tablero con ese título.
func columnFor(board tickets.Board, title string) (tickets.Column, error) {
	for _, c := range board.Columns {
		if c.Title == title {
			return c, nil
		}
	}
	return tickets.Column{}, fmt.Errorf("el tablero no tiene la columna '%s'", title)
}

// moveTo mueve la tarea a la columna del estado lógico indicado.
func moveTo(t tickets.Tracker, cfg *config.Config, pid int64, taskID int64, state string) error {
	board, err := t.Board(pid)
	if err != nil {
		return err
	}
	col, err := columnFor(board, config.States[state])
	if err != nil {
		return err
	}
	return t.MoveTask(pid, board, col.ID, taskID)
}

func refOf(t tickets.Task) string {
	if t.Identifier != "" {
		return t.Identifier
	}
	return fmt.Sprintf("#%d", t.ID)
}

// New crea una tarea y la deja en DRAFT.
func New(cfg *config.Config, title string) (tickets.Task, string, error) {
	t, err := trackerFor(cfg)
	if err != nil {
		return tickets.Task{}, "", err
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return tickets.Task{}, "", err
	}
	task, err := t.CreateTask(pid, title)
	if err != nil {
		return tickets.Task{}, "", fmt.Errorf("crear tarea: %w", err)
	}
	url := t.TaskURL(task.ID)
	if err := moveTo(t, cfg, pid, task.ID, "draft"); err != nil {
		return task, url, fmt.Errorf("tarea #%d creada, pero no se pudo mover a DRAFT: %w", task.ID, err)
	}
	return task, url, nil
}

// findTask busca la tarea del proyecto por identificador (ACA-6, sin distinguir mayúsculas) o por id numérico.
func findTask(t tickets.Tracker, pid int64, ref string) (tickets.Task, bool, error) {
	if n, err := strconv.ParseInt(ref, 10, 64); err == nil {
		task, err := t.GetTask(n)
		if err != nil || task.ProjectID != pid {
			return tickets.Task{}, false, nil
		}
		return task, true, nil
	}
	tasks, err := t.ListTasks(pid)
	if err != nil {
		return tickets.Task{}, false, err
	}
	for _, task := range tasks {
		if strings.EqualFold(task.Identifier, ref) {
			return task, true, nil
		}
	}
	return tickets.Task{}, false, nil
}

// columnOf devuelve el título de la columna en la que está la tarea.
func columnOf(board tickets.Board, taskID int64) string {
	for _, c := range board.Columns {
		for _, tk := range c.Tasks {
			if tk.ID == taskID {
				return c.Title
			}
		}
	}
	return ""
}

// Ready pasa una tarea de DRAFT a TODO. ref es el identificador (ACA-6) o el id numérico.
func Ready(cfg *config.Config, ref string) (tickets.Task, string, error) {
	t, err := trackerFor(cfg)
	if err != nil {
		return tickets.Task{}, "", err
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return tickets.Task{}, "", err
	}
	task, found, err := findTask(t, pid, ref)
	if err != nil {
		return tickets.Task{}, "", err
	}
	if !found {
		return tickets.Task{}, "", fmt.Errorf("no encuentro la tarea '%s' en el proyecto", ref)
	}
	url := t.TaskURL(task.ID)
	board, err := t.Board(pid)
	if err != nil {
		return task, url, err
	}
	switch current := columnOf(board, task.ID); current {
	case config.States["todo"]:
		return task, url, nil
	case config.States["draft"]:
		return task, url, moveTo(t, cfg, pid, task.ID, "todo")
	default:
		return task, url, fmt.Errorf("la tarea %s está en '%s'; solo se pueden pasar tareas de DRAFT a TODO", ref, current)
	}
}

// List devuelve las columnas del tablero con sus tareas, en el orden del tablero.
func List(cfg *config.Config) ([]tickets.Column, error) {
	t, err := trackerFor(cfg)
	if err != nil {
		return nil, err
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return nil, err
	}
	board, err := t.Board(pid)
	if err != nil {
		return nil, err
	}
	return board.Columns, nil
}

// InstallWebhook crea en el proyecto un webhook hacia targetURL, si no existe ya.
func InstallWebhook(cfg *config.Config, targetURL string) (tickets.Webhook, bool, error) {
	t, err := trackerFor(cfg)
	if err != nil {
		return tickets.Webhook{}, false, err
	}
	v, ok := t.(*tickets.Vikunja)
	if !ok {
		return tickets.Webhook{}, false, fmt.Errorf("los webhooks solo están implementados para Vikunja")
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return tickets.Webhook{}, false, err
	}
	existing, err := v.Webhooks(pid)
	if err != nil {
		return tickets.Webhook{}, false, err
	}
	for _, h := range existing {
		if h.TargetURL == targetURL {
			return h, false, nil
		}
	}
	h, err := v.CreateWebhook(pid, targetURL, WebhookEvents)
	return h, err == nil, err
}

// WebhookEvents son los eventos de Vikunja que avisan a la factory.
var WebhookEvents = []string{"task.created", "task.updated", "task.comment.created"}
