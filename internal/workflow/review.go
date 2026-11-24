package workflow

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"factory/internal/ci"
	"factory/internal/config"
	"factory/internal/repos"
	"factory/internal/runner"
	"factory/internal/tickets"
)

// Marcadores de los comentarios que la factory deja en la tarea.
// "PR: <url>" se publica cada vez que se sube la rama: es el punto de corte para las revisiones.
const (
	prMarker     = "PR: "
	legacyMarker = "Lista para revisar: "
	reworkMarker = "Cambios pedidos"
	ciMarker     = "CI falló"
)

// lastPRLink devuelve el último enlace de PR publicado en la tarea y el momento en que se publicó.
func lastPRLink(cs []tickets.Comment) (string, time.Time, bool) {
	for i := len(cs) - 1; i >= 0; i-- {
		text := tickets.PlainComment(cs[i].Comment)
		for _, m := range []string{prMarker, legacyMarker} {
			if strings.HasPrefix(text, m) {
				at, _ := parseTime(cs[i].Created)
				return strings.TrimSpace(strings.TrimPrefix(text, m)), at, true
			}
		}
	}
	return "", time.Time{}, false
}

// reworkRounds cuenta cuántas veces se ha mandado la tarea a REWORK.
func reworkRounds(cs []tickets.Comment) int {
	n := 0
	for _, c := range cs {
		text := tickets.PlainComment(c.Comment)
		if strings.HasPrefix(text, reworkMarker) || strings.HasPrefix(text, ciMarker) {
			n++
		}
	}
	return n
}

// sendToRework deja el feedback en la tarea y la pasa a REWORK; si se agotaron las rondas, la pasa a DRAFT.
func sendToRework(cfg *config.Config, t tickets.Tracker, pid int64, task tickets.Task, cs []tickets.Comment, feedback string) error {
	if reworkRounds(cs) >= cfg.Agent.MaxRework {
		_ = t.AddComment(task.ID, fmt.Sprintf("Se alcanzó el máximo de %d rondas de REWORK automáticas. Necesito tu decisión: revisa la tarea y muévela de nuevo a TODO o déjala en DRAFT.", cfg.Agent.MaxRework))
		return moveTo(t, cfg, pid, task.ID, "draft")
	}
	if err := t.AddComment(task.ID, feedback); err != nil {
		return err
	}
	return moveTo(t, cfg, pid, task.ID, "rework")
}

// parsePRURL extrae owner, repo y número de una URL de PR de GitHub.
func parsePRURL(raw string) (owner, name string, number int, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("no es una URL de PR: %s", raw)
	}
	number, err = strconv.Atoi(parts[3])
	return parts[0], parts[1], number, err
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05Z07:00", s)
}

// nextPatchTag devuelve el siguiente tag vX.Y.Z (incrementa el parche del último).
func nextPatchTag(latest string) (string, error) {
	if latest == "" {
		return "v0.0.1", nil
	}
	var a, b, c int
	if _, err := fmt.Sscanf(latest, "v%d.%d.%d", &a, &b, &c); err != nil {
		return "", fmt.Errorf("tag no válido: %s", latest)
	}
	return fmt.Sprintf("v%d.%d.%d", a, b, c+1), nil
}

// SyncReview mira las tareas de REVISIÓN: si el PR está mergeado despliega master y pasa la tarea a HECHO
// cuando el CI del merge está en verde; si hay comentarios de code review nuevos, pasan a REWORK.
func SyncReview(cfg *config.Config, t tickets.Tracker, host repos.Host, pid int64, logf func(string, ...any)) error {
	board, err := t.Board(pid)
	if err != nil {
		return err
	}
	col, err := columnFor(board, config.States["review"])
	if err != nil {
		return err
	}
	for _, task := range col.Tasks {
		ref := refOf(task)
		cs, err := t.Comments(task.ID)
		if err != nil {
			logf("%s: no pude leer comentarios: %v", ref, err)
			continue
		}
		link, since, ok := lastPRLink(cs)
		if !ok {
			continue
		}
		owner, name, number, err := parsePRURL(link)
		if err != nil {
			logf("%s: %v", ref, err)
			continue
		}

		info, err := host.PRInfo(owner, name, number)
		if err != nil {
			logf("%s: no pude consultar el PR #%d: %v", ref, number, err)
			continue
		}
		if info.Merged {
			// Merge hecho (por ti en GitHub): desplegamos master una vez y la tarea pasa a HECHO
			// cuando el CI del merge esté en verde.
			if !hasCommentPrefix(cs, fmt.Sprintf("PR #%d mergeado", number)) {
				_ = t.AddComment(task.ID, fmt.Sprintf("PR #%d mergeado.", number))
				logf("%s: PR #%d mergeado", ref, number)
			}
			setPhase(fmt.Sprintf("merge del PR #%d: desplegando master (%s)", number, ref))
			ensureMergeDeploy(cfg, t, task, info.MergeSHA, cs, logf)
			maybeFinishMerged(cfg, t, pid, task, info.MergeSHA, cs, logf)
			continue
		}

		items, err := host.ReviewsSince(owner, name, number, since)
		if err != nil {
			logf("%s: no pude leer las revisiones del PR #%d: %v", ref, number, err)
			continue
		}
		if len(items) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString(reworkMarker + " en code review:\n")
		for _, it := range items {
			if it.Path != "" {
				fmt.Fprintf(&b, "- [%s] %s (%s)\n", it.Author, it.Body, it.Path)
			} else {
				fmt.Fprintf(&b, "- [%s] %s\n", it.Author, it.Body)
			}
		}
		if err := sendToRework(cfg, t, pid, task, cs, strings.TrimSpace(b.String())); err != nil {
			logf("%s: no pude pasar a REWORK: %v", ref, err)
			continue
		}
		logf("%s: %d comentarios de code review → REWORK", ref, len(items))
	}
	return nil
}

// createDeployTag crea el siguiente tag de parche sobre el commit de merge y lo deja en la tarea.
func createDeployTag(t tickets.Tracker, host repos.Host, task tickets.Task, owner, name, sha string, logf func(string, ...any)) {
	ref := refOf(task)
	latest, err := host.LatestSemverTag(owner, name)
	if err != nil {
		_ = t.AddComment(task.ID, "No pude leer los tags del repo para crear el tag de deploy: "+err.Error())
		logf("%s: tags: %v", ref, err)
		return
	}
	tag, err := nextPatchTag(latest)
	if err != nil {
		_ = t.AddComment(task.ID, err.Error())
		return
	}
	if err := host.CreateTag(owner, name, tag, sha); err != nil {
		_ = t.AddComment(task.ID, "No pude crear el tag "+tag+": "+err.Error())
		logf("%s: tag %s: %v", ref, tag, err)
		return
	}
	_ = t.AddComment(task.ID, fmt.Sprintf("Tag %s creado sobre el merge.", tag))
	logf("%s: tag %s creado", ref, tag)
}

// SyncDone cierra la sesión de Claude y el worktree de las tareas que ya están en HECHO.
// La conversación queda guardada: se puede retomar con `claude --resume <id>` (ID en el comentario de sesión).
func SyncDone(cfg *config.Config, t tickets.Tracker, pid int64, logf func(string, ...any)) {
	board, err := t.Board(pid)
	if err != nil {
		return
	}
	col, err := columnFor(board, config.States["done"])
	if err != nil {
		return
	}
	// Las ranuras de test de las tareas en HECHO se liberan: se paran sus aplicaciones y quedan libres.
	if records, err := loadSlots(cfg); err == nil {
		changed := false
		for _, task := range col.Tasks {
			for i, rec := range records {
				if rec.Ref == refOf(task) {
					stopSlot(cfg, i)
					delete(records, i)
					changed = true
					logf("%s: tarea en HECHO: ranura %d liberada", refOf(task), i)
				}
			}
		}
		if changed {
			_ = saveSlots(cfg, records)
		}
	}
	for _, task := range col.Tasks {
		ref := refOf(task)
		if !runner.SessionAlive(ref) {
			continue
		}
		if err := runner.EndSession(ref); err != nil {
			logf("%s: %v", ref, err)
			continue
		}
		wt := filepath.Join(cfg.StateDir(), "worktrees", strings.ToLower(ref))
		_ = runner.WorktreeRemove(cfg.Repo.Path, wt)
		logf("%s: tarea en HECHO: sesión de Claude cerrada y worktree borrado", ref)
	}
}

// maybeFinishMerged pasa a HECHO una tarea con el PR mergeado cuando el CI del merge a master está en verde.
// Sin ci-server configurado, basta con que el despliegue de master esté hecho.
func maybeFinishMerged(cfg *config.Config, t tickets.Tracker, pid int64, task tickets.Task, mergeSHA string, cs []tickets.Comment, logf func(string, ...any)) {
	if mergeSHA == "" {
		return
	}
	if cfg.CI.Type != "forgejo" {
		short := mergeSHA
		if len(short) > 7 {
			short = short[:7]
		}
		if !hasCommentPrefix(cs, "Desplegado en local (master tras merge, commit "+short) {
			return
		}
	} else {
		sum, err := ci.NewForgejo(cfg.CI.URL, cfg.CI.Token, cfg.CI.Repo).RunsFor(mergeSHA)
		if err != nil || sum.State() != "success" {
			return
		}
	}
	if err := moveTo(t, cfg, pid, task.ID, "done"); err != nil {
		logf("%s: no pude pasar a HECHO: %v", refOf(task), err)
		return
	}
	_ = t.AddComment(task.ID, "Merge a master terminado y desplegado: tarea pasada a HECHO automáticamente.")
	logf("%s: merge a master OK → HECHO", refOf(task))
}

// hasCommentPrefix indica si algún comentario de la tarea empieza por ese texto.
func hasCommentPrefix(cs []tickets.Comment, prefix string) bool {
	for _, c := range cs {
		if strings.HasPrefix(tickets.PlainComment(c.Comment), prefix) {
			return true
		}
	}
	return false
}

// ensureMergeDeploy despliega master si la tarea aún no lo tiene (merge hecho fuera de la factory).
func ensureMergeDeploy(cfg *config.Config, t tickets.Tracker, task tickets.Task, mergeSHA string, cs []tickets.Comment, logf func(string, ...any)) {
	if mergeSHA == "" {
		return
	}
	short := mergeSHA
	if len(short) > 7 {
		short = short[:7]
	}
	for _, c := range cs {
		text := tickets.PlainComment(c.Comment)
		if strings.Contains(text, "Desplegado en local (master tras merge, commit "+short) {
			return
		}
		if strings.HasPrefix(text, "Despliegue local no completado (master tras merge") {
			return // ya falló: no se reintenta en cada vuelta
		}
	}
	DeployAfterMerge(cfg, t, task, mergeSHA, logf)
}

// mergeFailedMarker empieza el comentario que deja la factory cuando un merge falla.
const mergeFailedMarker = "No pude hacer el merge del PR"
