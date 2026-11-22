package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"factory/internal/ci"
	"factory/internal/config"
	"factory/internal/repos"
	"factory/internal/runner"
	"factory/internal/sessions"
	"factory/internal/tickets"
)

// StartOptions controlan el bucle de `factory start`.
type StartOptions struct {
	Interval time.Duration   // espera entre comprobaciones del tablero
	Once     bool            // procesar una tarea y salir
	Wake     <-chan struct{} // avisos de Vikunja (webhook) que despiertan el bucle antes de tiempo
	Log      func(format string, args ...any)
}

// Start procesa las tareas de TODO (y las de REWORK) una a una, y sincroniza REVISIÓN.
// Sin Once, sigue vigilando el tablero.
func Start(ctx context.Context, cfg *config.Config, opts StartOptions) error {
	t, err := trackerFor(cfg)
	if err != nil {
		return err
	}
	host, err := hostFor(cfg)
	if err != nil {
		return err
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return err
	}
	logf := opts.Log
	if logf == nil {
		logf = func(format string, args ...any) { fmt.Fprintf(os.Stdout, format+"\n", args...) }
	}

	for {
		if err := SyncReview(cfg, t, host, pid, logf); err != nil {
			logf("sincronizar REVISIÓN: %v", err)
		}
		SyncDone(cfg, t, pid, logf)
		SyncApproved(cfg, t, host, pid, logf)
		task, state, ok, err := nextWork(t, cfg, pid)
		if err != nil {
			return err
		}
		if ok {
			if err := processTask(ctx, cfg, t, host, pid, task, state, logf); err != nil {
				logf("error en %s: %v", refOf(task), err)
				if opts.Once {
					return nil
				}
				// Tras un error, la tarea vuelve a su columna: esperamos antes de reintentar para no entrar en bucle.
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(opts.Interval):
				case <-opts.Wake:
				}
				continue
			}
			if opts.Once {
				return nil
			}
			continue
		}
		if opts.Once {
			logf("no hay tareas en %s ni en %s", config.States["todo"], config.States["rework"])
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.Interval):
		case <-opts.Wake:
		}
	}
}

// nextWork devuelve la tarea a procesar: primero la más antigua de TODO; si no hay, la de REWORK.
// state es "todo" o "rework".
func nextWork(t tickets.Tracker, cfg *config.Config, pid int64) (tickets.Task, string, bool, error) {
	board, err := t.Board(pid)
	if err != nil {
		return tickets.Task{}, "", false, err
	}
	for _, state := range []string{"todo", "rework"} {
		col, err := columnFor(board, config.States[state])
		if err != nil {
			return tickets.Task{}, "", false, err
		}
		if len(col.Tasks) == 0 {
			continue
		}
		tasks := append([]tickets.Task(nil), col.Tasks...)
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
		return tasks[0], state, true, nil
	}
	return tickets.Task{}, "", false, nil
}

var sessionPattern = regexp.MustCompile(`Sesión de Claude: ([0-9a-f-]{36})`)

// sessionFromComments devuelve el ID de la última sesión de Claude registrada en los comentarios de la tarea.
func sessionFromComments(cs []tickets.Comment) (string, bool) {
	for i := len(cs) - 1; i >= 0; i-- {
		if m := sessionPattern.FindStringSubmatch(tickets.PlainComment(cs[i].Comment)); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// feedbackSince devuelve los comentarios escritos después del último "PR: <url>"
// que no son mensajes de la factory: es lo que hay que corregir.
func feedbackSince(cs []tickets.Comment) string {
	start := 0
	for i, c := range cs {
		if text := tickets.PlainComment(c.Comment); strings.HasPrefix(text, prMarker) || strings.HasPrefix(text, legacyMarker) {
			start = i + 1
		}
	}
	var parts []string
	for _, c := range cs[start:] {
		text := tickets.PlainComment(c.Comment)
		if strings.HasPrefix(text, "La factory") || strings.HasPrefix(text, "Sesión de Claude") {
			continue
		}
		parts = append(parts, "- "+text)
	}
	return strings.Join(parts, "\n")
}

// processTask ejecuta una tarea de TODO (nueva) o de REWORK (retoma su sesión).
// Termina en REVISIÓN (CI en verde), en REWORK (CI en rojo) o en DRAFT/TODO si no hay resultado.
func processTask(ctx context.Context, cfg *config.Config, t tickets.Tracker, host repos.Host, pid int64, task tickets.Task, state string, logf func(string, ...any)) error {
	ref := refOf(task)
	var dir string
	base := cfg.Repo.Branch
	branch := "task/" + strings.ToLower(ref)
	rework := state == "rework"
	logf("%s: empiezo (%s, desde %s)", ref, task.Title, config.States[state])
	// Lo primero: que el tablero refleje enseguida que la tarea está en marcha.
	if err := moveTo(t, cfg, pid, task.ID, "in_progress"); err != nil {
		return fmt.Errorf("pasar a EN CURSO: %w", err)
	}

	// Sesión: la guardamos en un comentario de la tarea. En REWORK se retoma la última; si no hay, se crea una.
	cs, err := t.Comments(task.ID)
	if err != nil {
		return fmt.Errorf("%s: leer comentarios: %w", ref, err)
	}
	sessionID, saved := sessionFromComments(cs)
	resume := rework && saved
	if !resume {
		id, err := sessions.NewUUID()
		if err != nil {
			return err
		}
		sessionID = id
		_ = t.AddComment(task.ID, fmt.Sprintf("Sesión de Claude: %s\nPara retomarla a mano: claude --resume %s", id, id))
	}

	// Cada tarea trabaja en su propio worktree: la carpeta principal del repo no se toca,
	// así que los cambios sin commitear que tengas allí no bloquean la factory.
	wt := filepath.Join(cfg.StateDir(), "worktrees", strings.ToLower(ref))
	if err := runner.Fetch(cfg.Repo.Path); err != nil {
		return fail(t, cfg, pid, task, "todo", "no pude actualizar el repo desde origin", err)
	}
	startPoint := "origin/" + base
	if rework {
		startPoint = "origin/" + branch
	}
	// Si la sesión de Claude sigue viva en un REWORK, reutilizamos su worktree (es su carpeta de trabajo).
	alive := runner.SessionAlive(ref)
	reuse := rework && alive
	if !reuse {
		if err := runner.WorktreeAdd(cfg.Repo.Path, wt, branch, startPoint); err != nil {
			return fail(t, cfg, pid, task, "todo", "no se pudo crear el worktree de la tarea", err)
		}
	}
	// El worktree se borra al terminar, salvo que la sesión de Claude siga abierta (la necesita).
	defer func() {
		if runner.SessionAlive(ref) {
			return
		}
		if err := runner.WorktreeRemove(cfg.Repo.Path, wt); err != nil {
			logf("%s: no pude quitar el worktree %s: %v", ref, wt, err)
		}
	}()
	dir = wt
	startSHA, err := runner.Head(dir)
	if err != nil {
		return fail(t, cfg, pid, task, "todo", "no pude leer el commit de partida", err)
	}

	session := runner.Session{
		Dir:         dir,
		BaseBranch:  base,
		Branch:      branch,
		Name:        "factory " + ref,
		Model:       cfg.Agent.Model,
		Identifier:  ref,
		Title:       task.Title,
		Description: task.Description,
		SessionID:   sessionID,
		Resume:      resume,
	}
	if rework && alive {
		// La sesión sigue abierta: el feedback se escribe directamente en ella.
		cs, err := t.Comments(task.ID)
		if err != nil {
			return fail(t, cfg, pid, task, "review", "no se pudieron leer los comentarios", err)
		}
		if err := runner.Inject(ref, "Hay cambios pedidos en la revisión de "+ref+". "+feedbackSince(cs)+" Aplícalos en esta rama, ejecuta los tests y responde con un resumen. No hagas commit ni push."); err != nil {
			return fail(t, cfg, pid, task, "review", "no pude enviar los cambios a la sesión de Claude", err)
		}
	} else {
		if rework {
			cs, err := t.Comments(task.ID)
			if err != nil {
				return fail(t, cfg, pid, task, "review", "no se pudieron leer los comentarios", err)
			}
			session.Prompt = fmt.Sprintf("Revisión de la tarea %s: hay cambios pedidos.\n\n%s\n\nAplícalos en esta rama, ejecuta los tests y responde con un resumen.\nNo hagas commit ni push: lo hará la factory cuando termines.", ref, feedbackSince(cs))
		}
		if err := runner.LaunchAgent(session, cfg.StateDir()); err != nil {
			return fail(t, cfg, pid, task, failState(rework), "no se pudo lanzar Claude", err)
		}
	}

	// Claude trabaja por turnos: cada vez que deja de procesar (hook Stop) miramos si hay cambios.
	// Si no los hay (p. ej. te hace preguntas), seguimos esperando. Cuando los hay, publicamos
	// y la sesión queda abierta para seguir la conversación.
	for {
		setPhase(fmt.Sprintf("esperando a Claude en %s (sesión %s)", ref, session.Name))
		reason := runner.WaitTurn(ref, cfg.StateDir(), ctx.Done())
		if reason == "cancel" {
			return ctx.Err()
		}
		changed, err := runner.Changed(dir, startSHA)
		if err != nil {
			return fail(t, cfg, pid, task, failState(rework), "no se pudo leer el estado del repo", err)
		}
		if changed {
			break
		}
		if reason == "gone" {
			return fail(t, cfg, pid, task, failState(rework), "la sesión de Claude terminó sin cambios en el repo", nil)
		}
		logf("%s: Claude ha respondido sin cambios todavía; sigo esperando", ref)
	}

	msg := fmt.Sprintf("%s: %s", ref, task.Title)
	if rework {
		msg = fmt.Sprintf("%s: cambios de revisión", ref)
	}
	setPhase(fmt.Sprintf("haciendo push de %s", ref))
	if err := runner.CommitAndPush(dir, branch, msg); err != nil {
		return fail(t, cfg, pid, task, failState(rework), "no se pudo hacer commit o push", err)
	}

	if cfg.LocalDeploy {
		setPhase(fmt.Sprintf("build de la rama de %s (despliegue local)", ref))
		if sha, err := runner.Head(dir); err == nil {
			LocalDeploy(cfg, t, task, sha, logf)
		} else {
			logf("%s: despliegue local: no pude leer el commit: %v", ref, err)
		}
	}

	owner, repoName, err := host.ParseRepo(cfg.Repo.URL)
	if err != nil {
		return fail(t, cfg, pid, task, failState(rework), "URL de repo no válida", err)
	}
	var prURL string
	if rework {
		// El PR ya existe y se actualiza con el push: reutilizamos el enlace publicado la primera vez.
		cs, err := t.Comments(task.ID)
		if err != nil {
			return fail(t, cfg, pid, task, "review", "no pude leer el enlace del PR", err)
		}
		link, _, found := lastPRLink(cs)
		if !found {
			return fail(t, cfg, pid, task, "review", "no encuentro el PR de esta tarea", nil)
		}
		prURL = link
	} else {
		body := fmt.Sprintf("Tarea %s: %s\n\n%s\n\nTarea de Vikunja: %s\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)",
			ref, task.Title, runner.PlainText(task.Description), t.TaskURL(task.ID))
		existing, found, err := host.FindPR(owner, repoName, branch, base)
		if err != nil {
			return fail(t, cfg, pid, task, "draft", "no pude buscar el PR de la rama", err)
		}
		if found {
			prURL = existing
		} else {
			prURL, err = host.CreatePR(owner, repoName, branch, base, fmt.Sprintf("%s: %s", ref, task.Title), body)
			if err != nil {
				return fail(t, cfg, pid, task, "draft", "no se pudo abrir el PR", err)
			}
		}
	}

	// Publicamos el PR como punto de corte para las revisiones y esperamos al CI antes de pedir revisión.
	_ = t.AddComment(task.ID, prMarker+prURL)
	setPhase(fmt.Sprintf("esperando CI del PR de %s", ref))
	ci, err := awaitCI(cfg, host, prURL, logf)
	if err != nil {
		logf("%s: no pude consultar el CI: %v", ref, err)
	}
	if ci.State == "failure" {
		cs, _ := t.Comments(task.ID)
		feedback := ciMarker + " en " + ref + ":\n- " + strings.Join(ci.Failures, "\n- ")
		if err := sendToRework(cfg, t, pid, task, cs, feedback); err != nil {
			return fmt.Errorf("pasar a REWORK por CI: %w", err)
		}
		logf("%s: CI falló → REWORK", ref)
		return nil
	}
	if err := moveTo(t, cfg, pid, task.ID, "review"); err != nil {
		return fmt.Errorf("pasar a REVISIÓN: %w", err)
	}
	note := "CI en verde."
	switch ci.State {
	case "":
		note = "No pude consultar el CI; revisa su estado en GitHub."
	case "none":
		note = "No detecté CI en el commit; revisa con cuidado."
	case "pending":
		note = "El CI sigue en curso tras esperar; revisa su estado en GitHub."
	}
	_ = t.AddComment(task.ID, "Lista para revisar (PR: "+prURL+"). "+note)
	logf("%s: lista para revisar %s (%s)", ref, prURL, ci.State)
	return nil
}

// awaitCI espera el resultado del CI del PR. Si hay ci-server (Forgejo), espera a sus runs para el commit
// de la cabeza del PR; si no aparecen en 10 minutos, o no hay ci-server, usa el estado de checks de GitHub.
func awaitCI(cfg *config.Config, host repos.Host, prURL string, logf func(string, ...any)) (repos.CIResult, error) {
	owner, name, number, err := parsePRURL(prURL)
	if err != nil {
		return repos.CIResult{}, err
	}
	var fj *ci.Forgejo
	if cfg.CI.Type == "forgejo" {
		fj = ci.NewForgejo(cfg.CI.URL, cfg.CI.Token, cfg.CI.Repo)
	}
	start := time.Now()
	for {
		info, err := host.PRInfo(owner, name, number)
		if err != nil {
			return repos.CIResult{}, err
		}
		if fj != nil {
			sum, err := fj.RunsFor(info.HeadSHA)
			switch {
			case err != nil:
				logf("PR #%d: no pude leer Forgejo Actions: %v", number, err)
			case sum.State() == "success" || sum.State() == "failure":
				res := repos.CIResult{State: sum.State()}
				for _, r := range sum.Runs {
					if r.Status == "failure" || r.Status == "cancelled" || r.Status == "error" {
						res.Failures = append(res.Failures, fmt.Sprintf("%s run #%d: %s", r.Workflow, r.Index, r.URL))
					}
				}
				return res, nil
			case sum.State() == "running":
				logf("PR #%d: build de Forgejo en curso, espero…", number)
			default:
				if time.Since(start) > 10*time.Minute {
					logf("PR #%d: Forgejo no ha arrancado ningún run; uso el estado de GitHub", number)
					fj = nil
					continue
				}
				logf("PR #%d: esperando a que Forgejo reciba el commit…", number)
			}
			time.Sleep(30 * time.Second)
			continue
		}
		sum, err := host.PRCheck(owner, name, number)
		if err != nil {
			return repos.CIResult{}, err
		}
		switch sum.MergeState {
		case "clean":
			return repos.CIResult{State: "success"}, nil
		}
		if time.Since(start) > 30*time.Minute {
			return repos.CIResult{State: "pending"}, nil
		}
		logf("PR #%d: checks %s, espero…", number, sum.MergeState)
		time.Sleep(30 * time.Second)
	}
}

// failState: si falla una tarea de REWORK vuelve a REVISIÓN (el PR sigue existiendo); si es nueva, a DRAFT.
func failState(rework bool) string {
	if rework {
		return "review"
	}
	return "draft"
}

// fail deja un comentario con el motivo y mueve la tarea al estado indicado (draft, todo, review).
func fail(t tickets.Tracker, cfg *config.Config, pid int64, task tickets.Task, state, reason string, cause error) error {
	msg := "La factory no pudo completar la tarea: " + reason
	if cause != nil {
		msg += "\n\n" + cause.Error()
	}
	_ = t.AddComment(task.ID, msg)
	_ = moveTo(t, cfg, pid, task.ID, state)
	if cause != nil {
		return fmt.Errorf("%s: %s: %w", refOf(task), reason, cause)
	}
	return fmt.Errorf("%s: %s", refOf(task), reason)
}
