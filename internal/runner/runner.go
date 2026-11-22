// Package runner trabaja sobre la carpeta local del proyecto: crea la rama de la tarea,
// lanza una sesión interactiva de Claude Code (con Remote Control) y, al terminar, hace commit y push.
package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Session describe una sesión de agente para una tarea.
type Session struct {
	Dir         string // carpeta local del repo
	BaseBranch  string // rama de partida (p. ej. master)
	Branch      string // rama de la tarea (p. ej. task/aca-9)
	Name        string // nombre visible en Remote Control (p. ej. "factory ACA-9")
	Model       string // modelo de Claude Code (p. ej. claude-haiku-5-5)
	Identifier  string
	Title       string
	Description string
	SessionID   string // ID de sesión de Claude Code (--session-id al crear, --resume al retomar)
	Resume      bool   // true: retoma SessionID en lugar de crear una sesión nueva
	Prompt      string // mensaje inicial; si está vacío se usa Prompt(...)
}

var tagPattern = regexp.MustCompile(`<[^>]+>`)

// PlainText convierte la descripción HTML de Vikunja en texto simple.
func PlainText(html string) string {
	text := tagPattern.ReplaceAllString(html, "\n")
	text = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"").Replace(text)
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// Prompt construye el mensaje inicial. Empieza con "grill me" para que el skill de mattpocock
// haga preguntas antes de implementar.
func Prompt(identifier, title, description string) string {
	return fmt.Sprintf(`grill me

Tarea %s: %s

Descripción:
%s

Cuando el plan esté claro y lo hayas aprobado, impleméntalo:
- Analiza el código existente antes de cambiar nada.
- Añade o actualiza tests si el proyecto los tiene, y ejecútalos.
- No hagas commit ni push: lo hará la factory cuando yo salga de la sesión.`, identifier, title, PlainText(description))
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// CurrentBranch devuelve la rama actual de la carpeta.
func CurrentBranch(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}

// Prepare exige un árbol limpio y deja la rama de la tarea lista.
// Si la rama ya existe (REWORK), hace checkout de ella; si no, la crea desde la rama base.
func Prepare(dir, baseBranch, branch string, existing bool) error {
	status, err := git(dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("la carpeta %s tiene cambios sin commitear (incluidos ficheros sin seguimiento):\n%s\nlimpia la carpeta antes de empezar", dir, strings.TrimSpace(status))
	}
	if existing {
		_, err := git(dir, "checkout", branch)
		return err
	}
	if _, err := git(dir, "checkout", baseBranch); err != nil {
		return err
	}
	// Partimos de la última versión de la base para no sacar PRs desactualizados.
	if _, err := git(dir, "pull", "--ff-only", "origin", baseBranch); err != nil {
		return fmt.Errorf("actualizar %s desde origin: %w", baseBranch, err)
	}
	if _, err := git(dir, "checkout", "-b", branch); err != nil {
		return err
	}
	return nil
}

// SessionName es el nombre de la sesión de screen que aloja al agente de una tarea.
func SessionName(identifier string) string {
	return "factory-" + strings.ToLower(identifier)
}

// screenRunning indica si existe la sesión de screen con ese nombre.
func screenRunning(name string) bool {
	out, err := exec.Command("screen", "-ls").CombinedOutput()
	if err != nil && len(out) == 0 {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasSuffix(fields[0], "."+name) {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Head devuelve el commit actual de la carpeta.
func Head(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Changed indica si hay cambios desde el commit de partida: ficheros sin commitear o commits nuevos.
// El agente a veces hace commit por su cuenta, así que no basta con mirar git status.
func Changed(dir, startSHA string) (bool, error) {
	status, err := git(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) != "" {
		return true, nil
	}
	head, err := Head(dir)
	if err != nil {
		return false, err
	}
	return head != startSHA, nil
}

// CommitAndPush hace commit de todos los cambios y sube la rama al remoto origin.
func CommitAndPush(dir, branch, message string) error {
	if _, err := git(dir, "add", "-A"); err != nil {
		return err
	}
	status, err := git(dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" { // si el agente ya hizo commit, solo falta subir la rama
		if _, err := git(dir, "-c", "user.name=factory-bot", "-c", "user.email=factory-bot@localhost", "commit", "-m", message); err != nil {
			return err
		}
	}
	_, err = git(dir, "push", "-u", "origin", branch)
	return err
}

// Restore vuelve a la rama indicada (la que tenía el repo antes de la tarea).
func Restore(dir, branch string) error {
	_, err := git(dir, "checkout", branch)
	return err
}

// Git ejecuta un comando git en dir y devuelve su salida (para otros paquetes).
func Git(dir string, args ...string) (string, error) {
	return git(dir, args...)
}

// Fetch actualiza las referencias remotas (no toca el árbol de trabajo).
func Fetch(repoDir string) error {
	_, err := git(repoDir, "fetch", "--prune", "origin")
	return err
}

// WorktreeAdd crea (o recrea) un worktree en wtDir con la rama `branch` apuntando a startPoint.
// Así cada tarea trabaja en su propia copia y la carpeta principal del repo no se toca.
func WorktreeAdd(repoDir, wtDir, branch, startPoint string) error {
	_ = WorktreeRemove(repoDir, wtDir)
	if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
		return err
	}
	_, err := git(repoDir, "worktree", "add", "-B", branch, wtDir, startPoint)
	return err
}

// WorktreeRemove quita el worktree de wtDir (si existe).
func WorktreeRemove(repoDir, wtDir string) error {
	_, _ = git(repoDir, "worktree", "remove", "--force", wtDir)
	_ = os.RemoveAll(wtDir)
	_, _ = git(repoDir, "worktree", "prune")
	return nil
}

// DetachedWorktree deja una carpeta con el commit `sha` (en modo detached) para desplegar.
// Enlaza desde el repo los ficheros de `links` que existan (p. ej. .env, node_modules) y que git ignora.
func DetachedWorktree(repoDir, dir, sha string, links []string) error {
	_ = WorktreeRemove(repoDir, dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if _, err := git(repoDir, "worktree", "add", "--detach", dir, sha); err != nil {
		return err
	}
	for _, l := range links {
		src := filepath.Join(repoDir, l)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(dir, l)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		_ = os.RemoveAll(dst)
		if err := os.Symlink(src, dst); err != nil {
			return err
		}
	}
	return nil
}
