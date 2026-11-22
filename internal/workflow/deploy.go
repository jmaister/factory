package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"factory/internal/config"
	"factory/internal/runner"
	"factory/internal/tickets"
)

const buildTimeout = 30 * time.Minute

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// shell ejecuta un comando de shell en dir y devuelve su salida combinada.
func shell(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tail devuelve las últimas n líneas de un texto.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// deployCommit despliega un commit en una ranura libre (o la de la misma tarea):
// para la versión anterior, prepara el código, compila y arranca la aplicación con su gateway.
// Todas las ranuras comparten las bases de datos y data/ del repo.
func deployCommit(cfg *config.Config, t tickets.Tracker, task tickets.Task, sha, origin string, logf func(string, ...any)) {
	ref := refOf(task)
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	fail := func(msg string, err error) {
		text := fmt.Sprintf("Despliegue local no completado (%s, commit %s): %s", origin, short, msg)
		if err != nil {
			text += "\n\n" + err.Error()
		}
		_ = t.AddComment(task.ID, text)
		logf("%s: despliegue local: %s", ref, msg)
	}

	records, err := loadSlots(cfg)
	if err != nil {
		fail("no pude leer las ranuras", err)
		return
	}
	i := pickSlot(cfg, records, ref)
	gw, app := slotPorts(cfg, i)
	url := slotURL(cfg, i)
	dir := slotDir(cfg, i)

	setPhase(fmt.Sprintf("desplegando %s en la ranura %d (%s)", ref, i, origin))
	logf("%s: despliegue en la ranura %d iniciado (%s, commit %s, puertos %d/%d)", ref, i, origin, short, gw, app)
	defer setPhase("")

	stopSlot(cfg, i)
	if err := runner.Fetch(cfg.Repo.Path); err != nil {
		logf("%s: fetch: %v", ref, err)
	}
	if err := runner.DetachedWorktree(cfg.Repo.Path, dir, sha, cfg.LocalLinks); err != nil {
		fail("no pude preparar la carpeta de la ranura", err)
		return
	}
	if err := linkDBs(cfg, i); err != nil {
		fail("no pude enlazar los datos de la ranura", err)
		return
	}

	if cfg.Build != "" {
		if strings.Contains(origin, "master") {
			setPhase(fmt.Sprintf("build de master tras el merge de %s en la ranura %d", ref, i))
		} else {
			setPhase(fmt.Sprintf("build de %s en la ranura %d", ref, i))
		}
		logf("%s: build en curso: %s", ref, cfg.Build)
		ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
		out, err := shell(ctx, dir, cfg.Build)
		cancel()
		if err != nil {
			logf("%s: build fallido", ref)
			fail(fmt.Sprintf("el build falló (%s):\n%s", cfg.Build, tail(out, 30)), err)
			return
		}
		logf("%s: build OK", ref)
	}

	setPhase(fmt.Sprintf("arrancando %s en la ranura %d", ref, i))
	if err := startSlot(cfg, i); err != nil {
		fail("no pude arrancar la aplicación", err)
		return
	}
	records[i] = slotRecord{Ref: ref, SHA: sha, At: time.Now()}
	if err := saveSlots(cfg, records); err != nil {
		logf("%s: no pude guardar la ranura: %v", ref, err)
	}
	_ = t.AddComment(task.ID, fmt.Sprintf("Desplegado en local (%s, commit %s) en la ranura %d: %s", origin, short, i, url))
	logf("%s: desplegado en la ranura %d: %s", ref, i, url)
}

// LocalDeploy despliega la rama de la tarea tras el commit del agente.
func LocalDeploy(cfg *config.Config, t tickets.Tracker, task tickets.Task, sha string, logf func(string, ...any)) {
	deployCommit(cfg, t, task, sha, "rama task/"+strings.ToLower(refOf(task)), logf)
}

// DeployAfterMerge despliega master tras el merge del PR (commit de merge).
func DeployAfterMerge(cfg *config.Config, t tickets.Tracker, task tickets.Task, sha string, logf func(string, ...any)) {
	if sha == "" {
		logf("%s: despliegue tras merge: no hay commit de merge", refOf(task))
		return
	}
	deployCommit(cfg, t, task, sha, "master tras merge", logf)
}
