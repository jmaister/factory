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

// SyncReview mira las tareas de REVISIÓN: si el PR está mergeado pasan a APROBADO, se crea el tag
// y se hace el deploy; si hay comentarios de code review nuevos, pasan a REWORK.
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
			setPhase(fmt.Sprintf("merge del PR #%d detectado: desplegando master", number))
			if err := moveTo(t, cfg, pid, task.ID, "approved"); err != nil {
				logf("%s: no pude pasar a APROBADO: %v", ref, err)
				continue
			}
			_ = t.AddComment(task.ID, fmt.Sprintf("PR #%d mergeado. Tarea aprobada.", number))
			logf("%s: PR #%d mergeado → APROBADO", ref, number)
			DeployAfterMerge(cfg, t, task, info.MergeSHA, logf)
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

// SyncApproved hace el merge de los PR de las tareas en APROBADO que todavía están abiertos.
// Después sigue el mismo camino que un merge detectado: tag y despliegue.
func SyncApproved(cfg *config.Config, t tickets.Tracker, host repos.Host, pid int64, logf func(string, ...any)) {
	board, err := t.Board(pid)
	if err != nil {
		return
	}
	col, err := columnFor(board, config.States["approved"])
	if err != nil {
		return
	}
	for _, task := range col.Tasks {
		ref := refOf(task)
		cs, err := t.Comments(task.ID)
		if err != nil {
			continue
		}
		link, _, ok := lastPRLink(cs)
		if !ok {
			continue
		}
		owner, name, number, err := parsePRURL(link)
		if err != nil {
			continue
		}
		info, err := host.PRInfo(owner, name, number)
		if err == nil && info.Merged {
			// Mergeado a mano: si falta el tag o el despliegue de master, los hacemos ahora.
			ensureMergeDeploy(cfg, t, task, info.MergeSHA, cs, logf)
			maybeFinishApproved(cfg, t, pid, task, info.MergeSHA, cs, logf)
			continue
		}
		if err != nil || info.PRState != "open" {
			continue
		}
		setPhase(fmt.Sprintf("haciendo merge del PR #%d en GitHub (%s)", number, ref))
		if err := host.MergePR(owner, name, number); err != nil {
			_ = t.AddComment(task.ID, "No pude hacer el merge del PR: "+err.Error()+"\nHazlo a mano en GitHub o revisa los checks requeridos.")
			logf("%s: %v", ref, err)
			continue
		}
		_ = t.AddComment(task.ID, fmt.Sprintf("PR #%d mergeado por la factory (tarea aprobada).", number))
		logf("%s: PR #%d mergeado → tag y despliegue", ref, number)
		setPhase(fmt.Sprintf("merge hecho: desplegando master (%s)", ref))
		if merged, err := host.PRInfo(owner, name, number); err == nil {
			DeployAfterMerge(cfg, t, task, merged.MergeSHA, logf)
		}
	}
}

// maybeFinishApproved pasa a HECHO una tarea aprobada cuando el merge a master está en verde en Forgejo
// y su despliegue en local de master se completó.
func maybeFinishApproved(cfg *config.Config, t tickets.Tracker, pid int64, task tickets.Task, mergeSHA string, cs []tickets.Comment, logf func(string, ...any)) {
	if cfg.CI.Type != "forgejo" || mergeSHA == "" {
		return
	}
	sum, err := ci.NewForgejo(cfg.CI.URL, cfg.CI.Token, cfg.CI.Repo).RunsFor(mergeSHA)
	if err != nil || sum.State() != "success" {
		return
	}
	if err := moveTo(t, cfg, pid, task.ID, "done"); err != nil {
		logf("%s: no pude pasar a HECHO: %v", refOf(task), err)
		return
	}
	_ = t.AddComment(task.ID, "Merge a master en verde en Forgejo: tarea pasada a HECHO automáticamente.")
	logf("%s: merge a master OK → HECHO", refOf(task))
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
		if strings.Contains(tickets.PlainComment(c.Comment), "Desplegado en local (master tras merge, commit "+short) {
			return
		}
	}
	DeployAfterMerge(cfg, t, task, mergeSHA, logf)
}
