// Package tickets define la interfaz de sistemas de tickets y su implementación para Vikunja.
//
// El estado de una tarea es la columna del tablero kanban en la que está.
package tickets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Bucket es una columna del tablero (sin tareas).
type Bucket struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// Task es una tarea de Vikunja (solo los campos que usa la factory).
type Task struct {
	ID          int64  `json:"id"`
	Identifier  string `json:"identifier"` // p. ej. ACA-6: prefijo del proyecto + número dentro del proyecto
	Title       string `json:"title"`
	Description string `json:"description"` // HTML
	ProjectID   int64  `json:"project_id"`
	Done        bool   `json:"done"`
}

// Comment es un comentario de una tarea.
type Comment struct {
	ID      int64  `json:"id"`
	Comment string `json:"comment"`
	Created string `json:"created"`
	Author  struct {
		Username string `json:"username"`
	} `json:"author"`
}

// Column es una columna del tablero con sus tareas.
type Column struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Tasks []Task `json:"tasks"`
}

// Board es el tablero kanban de un proyecto.
type Board struct {
	ViewID  int64
	Columns []Column
}

// Tracker es lo que la factory necesita de cualquier gestor de tickets.
type Tracker interface {
	Ping() error
	FindProject(title string) (int64, error)
	ListBuckets(projectID int64) ([]Bucket, error)
	Board(projectID int64) (Board, error)
	CreateTask(projectID int64, title string) (Task, error)
	GetTask(id int64) (Task, error)
	// ListTasks devuelve las tareas del proyecto (con su identificador).
	ListTasks(projectID int64) ([]Task, error)
	// AddComment añade un comentario a la tarea.
	AddComment(taskID int64, text string) error
	// Comments devuelve los comentarios de la tarea, en orden cronológico.
	Comments(taskID int64) ([]Comment, error)
	// MoveTask mueve la tarea a la columna indicada del tablero.
	MoveTask(projectID int64, board Board, columnID, taskID int64) error
	// SetTitle cambia el título de una tarea existente, dejando el resto de campos intactos.
	SetTitle(taskID int64, title string) error
	// TaskURL devuelve la URL web de una tarea.
	TaskURL(id int64) string
}

// Vikunja implementa Tracker contra la API v1 de Vikunja.
type Vikunja struct {
	web    string // URL web de la instancia
	base   string // web + /api/v1
	token  string
	client *http.Client
}

func NewVikunja(url, token string) *Vikunja {
	web := strings.TrimRight(url, "/")
	return &Vikunja{
		web:    web,
		base:   web + "/api/v1",
		token:  token,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (v *Vikunja) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, v.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+v.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (v *Vikunja) Ping() error {
	var projects []map[string]any
	return v.do(http.MethodGet, "/projects", nil, &projects)
}

func (v *Vikunja) FindProject(title string) (int64, error) {
	var projects []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	if err := v.do(http.MethodGet, "/projects", nil, &projects); err != nil {
		return 0, err
	}
	var ids []int64
	for _, p := range projects {
		if p.Title == title {
			ids = append(ids, p.ID)
		}
	}
	switch len(ids) {
	case 0:
		return 0, fmt.Errorf("no existe ningún proyecto de Vikunja llamado '%s'", title)
	case 1:
		return ids[0], nil
	default:
		return 0, fmt.Errorf("hay %d proyectos llamados '%s'; renómbralos", len(ids), title)
	}
}

// kanbanViewID devuelve el id de la vista kanban del proyecto.
func (v *Vikunja) kanbanViewID(projectID int64) (int64, error) {
	var views []struct {
		ID       int64  `json:"id"`
		ViewKind string `json:"view_kind"`
	}
	if err := v.do(http.MethodGet, fmt.Sprintf("/projects/%d/views", projectID), nil, &views); err != nil {
		return 0, err
	}
	for _, view := range views {
		if view.ViewKind == "kanban" {
			return view.ID, nil
		}
	}
	return 0, fmt.Errorf("el proyecto %d no tiene vista kanban", projectID)
}

func (v *Vikunja) ListBuckets(projectID int64) ([]Bucket, error) {
	viewID, err := v.kanbanViewID(projectID)
	if err != nil {
		return nil, err
	}
	var buckets []Bucket
	err = v.do(http.MethodGet, fmt.Sprintf("/projects/%d/views/%d/buckets", projectID, viewID), nil, &buckets)
	return buckets, err
}

// Board devuelve las columnas del tablero con las tareas de cada una.
func (v *Vikunja) Board(projectID int64) (Board, error) {
	viewID, err := v.kanbanViewID(projectID)
	if err != nil {
		return Board{}, err
	}
	var cols []Column
	if err := v.do(http.MethodGet, fmt.Sprintf("/projects/%d/views/%d/tasks", projectID, viewID), nil, &cols); err != nil {
		return Board{}, err
	}
	return Board{ViewID: viewID, Columns: cols}, nil
}

func (v *Vikunja) CreateTask(projectID int64, title string) (Task, error) {
	var t Task
	err := v.do(http.MethodPut, fmt.Sprintf("/projects/%d/tasks", projectID), map[string]any{"title": title}, &t)
	return t, err
}

func (v *Vikunja) GetTask(id int64) (Task, error) {
	var t Task
	err := v.do(http.MethodGet, fmt.Sprintf("/tasks/%d", id), nil, &t)
	return t, err
}

func (v *Vikunja) ListTasks(projectID int64) ([]Task, error) {
	var tasks []Task
	err := v.do(http.MethodGet, fmt.Sprintf("/projects/%d/tasks?per_page=500", projectID), nil, &tasks)
	return tasks, err
}

func (v *Vikunja) Comments(taskID int64) ([]Comment, error) {
	var cs []Comment
	err := v.do(http.MethodGet, fmt.Sprintf("/tasks/%d/comments", taskID), nil, &cs)
	return cs, err
}

func (v *Vikunja) AddComment(taskID int64, text string) error {
	return v.do(http.MethodPut, fmt.Sprintf("/tasks/%d/comments", taskID), map[string]any{"comment": HTMLComment(text)}, nil)
}

var (
	urlPattern = regexp.MustCompile(`https?://[^\s<]+`)
	tagPattern = regexp.MustCompile(`<[^>]+>`)
)

// HTMLComment convierte texto plano en HTML para Vikunja: escapa, respeta saltos de línea
// y convierte las URL en enlaces clicables.
func HTMLComment(text string) string {
	escaped := html.EscapeString(text)
	escaped = urlPattern.ReplaceAllStringFunc(escaped, func(u string) string {
		return fmt.Sprintf(`<a href="%s" target="_blank" rel="noopener noreferrer">%s</a>`, u, u)
	})
	return "<p>" + strings.ReplaceAll(escaped, "\n", "<br>") + "</p>"
}

// PlainComment devuelve el texto de un comentario de Vikunja sin etiquetas HTML.
func PlainComment(raw string) string {
	text := tagPattern.ReplaceAllString(strings.ReplaceAll(raw, "<br>", "\n"), "")
	return strings.TrimSpace(html.UnescapeString(text))
}

func (v *Vikunja) MoveTask(projectID int64, board Board, columnID, taskID int64) error {
	return v.do(http.MethodPost,
		fmt.Sprintf("/projects/%d/views/%d/buckets/%d/tasks", projectID, board.ViewID, columnID),
		map[string]any{"task_id": taskID}, nil)
}

func (v *Vikunja) SetTitle(taskID int64, title string) error {
	// Vikunja reemplaza la tarea entera al actualizarla: leemos el objeto completo y solo cambiamos el título.
	var raw map[string]any
	if err := v.do(http.MethodGet, fmt.Sprintf("/tasks/%d", taskID), nil, &raw); err != nil {
		return err
	}
	raw["title"] = title
	return v.do(http.MethodPost, fmt.Sprintf("/tasks/%d", taskID), raw, nil)
}

func (v *Vikunja) TaskURL(id int64) string {
	return fmt.Sprintf("%s/tasks/%d", v.web, id)
}

// Webhook es un webhook de un proyecto de Vikunja.
type Webhook struct {
	ID        int64    `json:"id"`
	TargetURL string   `json:"target_url"`
	Events    []string `json:"events"`
}

// Webhooks lista los webhooks del proyecto.
func (v *Vikunja) Webhooks(projectID int64) ([]Webhook, error) {
	var hs []Webhook
	err := v.do(http.MethodGet, fmt.Sprintf("/projects/%d/webhooks", projectID), nil, &hs)
	return hs, err
}

// CreateWebhook crea un webhook en el proyecto para los eventos indicados.
func (v *Vikunja) CreateWebhook(projectID int64, targetURL string, events []string) (Webhook, error) {
	var h Webhook
	err := v.do(http.MethodPut, fmt.Sprintf("/projects/%d/webhooks", projectID),
		map[string]any{"target_url": targetURL, "events": events}, &h)
	return h, err
}
