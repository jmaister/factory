package workflow

import (
	"fmt"
	"strings"

	"factory/internal/config"
	"factory/internal/runner"
)

// phaseFn recibe lo que la factory está haciendo ahora (para la barra de estado del TUI).
var phaseFn = func(string) {}

// SetPhase registra el receptor de fases (lo usa el TUI).
func SetPhase(f func(string)) { phaseFn = f }

func setPhase(s string) { phaseFn(s) }

// DeployTask despliega en local la rama de una tarea (la que está en origin), a petición del usuario.
func DeployTask(cfg *config.Config, task string, logf func(string, ...any)) error {
	t, err := trackerFor(cfg)
	if err != nil {
		return err
	}
	pid, err := projectID(cfg, t)
	if err != nil {
		return err
	}
	found, ok, err := findTask(t, pid, task)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no encuentro la tarea %s", task)
	}
	_ = runner.Fetch(cfg.Repo.Path)

	// Si la tarea tiene PR, desplegamos la cabeza del PR: la rama puede haberse borrado al mergear.
	if cs, err := t.Comments(found.ID); err == nil {
		if link, _, ok := lastPRLink(cs); ok {
			owner, name, number, err := parsePRURL(link)
			if err == nil {
				host, err := hostFor(cfg)
				if err == nil {
					if info, err := host.PRInfo(owner, name, number); err == nil && info.HeadSHA != "" {
						if _, err := runner.Git(cfg.Repo.Path, "cat-file", "-e", info.HeadSHA); err != nil {
							_, _ = runner.Git(cfg.Repo.Path, "fetch", "origin", fmt.Sprintf("refs/pull/%d/head", number))
						}
						deployCommit(cfg, t, found, info.HeadSHA, fmt.Sprintf("PR #%d (manual)", number), logf)
						return nil
					}
				}
			}
		}
	}

	branch := "task/" + strings.ToLower(task)
	sha, err := runner.Git(cfg.Repo.Path, "rev-parse", "origin/"+branch)
	if err != nil {
		return fmt.Errorf("no encuentro la rama %s en origin ni un PR: %w", branch, err)
	}
	deployCommit(cfg, t, found, strings.TrimSpace(sha), "rama "+branch+" (manual)", logf)
	return nil
}
