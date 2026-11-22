package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"factory/internal/ci"
	"factory/internal/config"
	"factory/internal/repos"
	"factory/internal/runner"
	"factory/internal/tickets"
)

// TaskView es una tarea con lo que el TUI necesita mostrar.
type TaskView struct {
	Task        tickets.Task
	State       string              // clave lógica (draft, todo, in_progress, review, rework, approved, done)
	Column      string              // título de la columna
	PRURL       string              // último PR publicado (si hay)
	Session     string              // nombre de la sesión de Claude (Remote Control)
	Screen      string              // nombre de la sesión de screen
	Last        string              // último comentario (texto plano)
	Comments    int                 // número de comentarios
	URL         string              // URL de la tarea en Vikunja
	PRState     string              // open, merged o closed ("" si no hay PR o no se pudo leer)
	CI          *repos.CheckSummary // estado de los checks del PR (nil si no hay PR abierto o no se pudo leer)
	CIErr       string              // motivo si no se pudo leer el CI
	Forgejo     *ci.Summary         // runs de Forgejo Actions del último commit del PR (nil si no hay ci-server o PR abierto)
	ForgejoErr  string              // motivo si no se pudieron leer los runs de Forgejo
	AllComments []CommentView       // todos los comentarios, en orden cronológico
	SlotURL     string              // URL de la ranura donde está desplegada ("" si no lo está)
	SlotIdx     int                 // índice de la ranura (-1 si no está desplegada)
	SlotDir     string              // carpeta de la ranura de test ("" si no está desplegada)
	Worktree    string              // worktree de la tarea (agente), si existe
}

// stateOf devuelve la clave lógica de un título de columna.
func stateOf(column string) string {
	for k, v := range config.States {
		if v == column {
			return k
		}
	}
	return ""
}

// Snapshot devuelve todas las tareas del tablero, en el orden de las columnas.
func Snapshot(cfg *config.Config) ([]TaskView, error) {
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
	var views []TaskView
	for _, col := range board.Columns {
		for _, task := range col.Tasks {
			v := TaskView{
				Task:    task,
				State:   stateOf(col.Title),
				Column:  col.Title,
				Session: fmt.Sprintf("factory %s", refOf(task)),
				Screen:  runner.SessionName(refOf(task)),
				URL:     t.TaskURL(task.ID),
			}
			if cs, err := t.Comments(task.ID); err == nil {
				v.Comments = len(cs)
				if len(cs) > 0 {
					v.Last = tickets.PlainComment(cs[len(cs)-1].Comment)
				}
				for _, c := range cs {
					at, _ := parseTime(c.Created)
					v.AllComments = append(v.AllComments, CommentView{At: at, Author: c.Author.Username, Text: tickets.PlainComment(c.Comment)})
				}
				if link, _, ok := lastPRLink(cs); ok {
					v.PRURL = link
					var sha string
					v.PRState, sha, v.CI, v.CIErr = prStatus(cfg, link)
					if (v.PRState == "open" || v.PRState == "merged") && cfg.CI.Type == "forgejo" && sha != "" {
						sum, err := ci.NewForgejo(cfg.CI.URL, cfg.CI.Token, cfg.CI.Repo).RunsFor(sha)
						if err != nil {
							v.ForgejoErr = err.Error()
						} else {
							v.Forgejo = &sum
						}
					}
				}
			}
			views = append(views, v)
		}
	}
	slots := DeployedSlots(cfg)
	for i := range views {
		wt := filepath.Join(cfg.StateDir(), "worktrees", strings.ToLower(refOf(views[i].Task)))
		if _, err := os.Stat(wt); err == nil {
			views[i].Worktree = wt
		}
	}
	for i := range views {
		views[i].SlotIdx = -1
		if sl, ok := slots[refOf(views[i].Task)]; ok {
			views[i].SlotURL, views[i].SlotIdx = sl.URL, sl.Index
			views[i].SlotDir = slotDir(cfg, sl.Index)
		}
	}
	return views, nil
}

// Guide explica el estado de la tarea y qué puede hacer el usuario a continuación.
func Guide(v TaskView) (headline string, steps []string) {
	ref := refOf(v.Task)
	switch v.State {
	case "draft":
		return "Especificación en escritura. La factory no la toca.",
			[]string{
				"Escribe título y descripción en Vikunja.",
				"Cuando esté lista, muévela a 📥 TODO (o ejecuta: factory ready " + ref + ").",
			}
	case "todo":
		return "En cola. La factory la tomará en su próximo ciclo (menos de 1 minuto).",
			[]string{
				"No hace falta nada.",
				"Si quieres cambiar la especificación, hazlo ya: la sesión aún no ha empezado.",
			}
	case "in_progress":
		return "El agente está trabajando en una sesión de Claude (en segundo plano).",
			[]string{
				"Para hablar con el agente, abre Claude (app o web) y busca la sesión «" + v.Session + "» (Remote Control).",
				"Cuando el agente termine, la factory hace commit, push y abre el PR solo.",
			}
	case "review":
		if h, steps, ok := ciGuide(v); ok {
			return h, steps
		}
		pr := "(no encuentro el enlace al PR)"
		if v.PRURL != "" {
			pr = v.PRURL
		}
		return "PR abierto y CI revisado. Esperando tu decisión.",
			[]string{
				"Revisa el PR: " + pr,
				"Para pedir cambios: deja comentarios de code review en GitHub, o escribe en Vikunja y mueve la tarea a 🔁 REWORK.",
				"Para aprobar: haz merge del PR en GitHub. La factory pasa la tarea a 👍 APROBADO, crea el tag y ejecuta build y run.",
			}
	case "rework":
		return "Cambios pedidos. La factory retomará la sesión en su próximo ciclo.",
			[]string{
				"No hace falta nada.",
				"Si quieres seguir la conversación, usa la sesión «" + v.Session + "» (Remote Control).",
			}
	case "approved":
		return "Merge hecho. La factory ha creado el tag y ejecutado build y run.",
			[]string{
				"Revisa los comentarios de la tarea: tag, build y run.",
				"Cuando todo esté bien, muévela a ✅ HECHO.",
			}
	case "done":
		return "Tarea terminada.", []string{"No hace falta nada."}
	default:
		return "Columna desconocida: " + v.Column, []string{"Revisa la configuración del tablero."}
	}
}

// Explain es el texto plano de Guide, para mostrarlo fuera del TUI.
func Explain(v TaskView) string {
	h, steps := Guide(v)
	return h + "\n- " + strings.Join(steps, "\n- ")
}

// prStatus lee el estado del PR (abierto, mergeado o cerrado), el commit de su cabeza y, si está
// abierto, los checks según GitHub.
func prStatus(cfg *config.Config, prURL string) (string, string, *repos.CheckSummary, string) {
	owner, name, number, err := parsePRURL(prURL)
	if err != nil {
		return "", "", nil, err.Error()
	}
	host, err := hostFor(cfg)
	if err != nil {
		return "", "", nil, err.Error()
	}
	info, err := host.PRInfo(owner, name, number)
	if err != nil {
		return "", "", nil, err.Error()
	}
	switch {
	case info.Merged:
		return "merged", info.MergeSHA, nil, ""
	case info.PRState != "open":
		return "closed", info.HeadSHA, nil, ""
	}
	sum, err := host.PRCheck(owner, name, number)
	if err != nil {
		return "open", info.HeadSHA, nil, err.Error()
	}
	return "open", info.HeadSHA, &sum, ""
}

// ciGuide dice qué hacer según el build de Forgejo del PR (esperar, arreglar o revisar).
func ciGuide(v TaskView) (string, []string, bool) {
	if v.Forgejo == nil || len(v.Forgejo.Runs) == 0 {
		return "", nil, false
	}
	sum := v.Forgejo
	last := sum.Runs[0]
	switch sum.State() {
	case "running":
		return fmt.Sprintf("Esperando el build de Forgejo (run #%d, %s). Se actualizará solo.", last.Index, last.Workflow),
			[]string{"Seguimiento del build: " + last.URL, "No hace falta nada de momento."}, true
	case "failure":
		return fmt.Sprintf("El build de Forgejo ha fallado (run #%d, %s).", last.Index, last.Workflow),
			[]string{
				"Mira el log del build: " + last.URL,
				"Para que la factory lo arregle: escribe en Vikunja qué hay que corregir y mueve la tarea a 🔁 REWORK.",
				"O arréglalo tú en la rama y haz push. La factory lo verá cuando la muevas a 🔁 REWORK.",
			}, true
	default:
		return fmt.Sprintf("Build de Forgejo correcto (run #%d, %s). PR listo para revisar.", last.Index, last.Workflow),
			[]string{
				"Revisa el PR y, si está bien, haz merge en GitHub: la factory pasará la tarea a 👍 APROBADO y desplegará en local.",
				"Para pedir cambios: deja comentarios de code review en GitHub, o escribe en Vikunja y mueve la tarea a 🔁 REWORK.",
			}, true
	}
}

// CommentView es un comentario de la tarea listo para mostrar.
type CommentView struct {
	At     time.Time
	Author string
	Text   string
}
