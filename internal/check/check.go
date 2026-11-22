// Package check comprueba que la configuración del proyecto es accesible antes de lanzar agentes.
package check

import (
	"fmt"
	"sort"
	"strings"

	"factory/internal/config"
	"factory/internal/repos"
	"factory/internal/tickets"
)

// NewTracker crea el cliente de tickets según el tipo configurado.
func NewTracker(c config.Tickets) (tickets.Tracker, error) {
	switch c.Type {
	case "vikunja":
		return tickets.NewVikunja(c.URL, c.Token), nil
	default:
		return nil, fmt.Errorf("tipo de tickets no soportado: %s", c.Type)
	}
}

// NewRepoHost crea el cliente del servidor de repos según el tipo configurado.
func NewRepoHost(c config.Repo) (repos.Host, error) {
	switch c.Type {
	case "github":
		return repos.NewGitHub(c.Token), nil
	default:
		return nil, fmt.Errorf("tipo de repo no soportado: %s", c.Type)
	}
}

// Result es el resultado de la comprobación.
type Result struct {
	OK    bool
	Lines []string
}

// Run comprueba tickets (proyecto, tablero con todas las columnas) y repo.
func Run(cfg *config.Config) Result {
	r := Result{OK: true}

	t, err := NewTracker(cfg.Tickets)
	if err != nil {
		return Result{Lines: []string{"tickets ERROR: " + err.Error()}}
	}
	if err := t.Ping(); err != nil {
		r.OK = false
		r.Lines = append(r.Lines, "tickets ERROR: "+err.Error())
	} else if pid, err := t.FindProject(cfg.Tickets.Project); err != nil {
		r.OK = false
		r.Lines = append(r.Lines, "tickets ERROR: "+err.Error())
	} else if buckets, err := t.ListBuckets(pid); err != nil {
		r.OK = false
		r.Lines = append(r.Lines, "tickets ERROR: "+err.Error())
	} else {
		titles := map[string]bool{}
		for _, b := range buckets {
			titles[b.Title] = true
		}
		var missing []string
		for _, title := range config.States {
			if !titles[title] {
				missing = append(missing, title)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			r.OK = false
			r.Lines = append(r.Lines, fmt.Sprintf("tickets ERROR: faltan columnas [%s]", strings.Join(missing, ", ")))
		} else {
			r.Lines = append(r.Lines, fmt.Sprintf("tickets OK: proyecto '%s' (id %d), %d columnas", cfg.Tickets.Project, pid, len(config.States)))
		}
	}

	h, err := NewRepoHost(cfg.Repo)
	if err != nil {
		r.OK = false
		r.Lines = append(r.Lines, "repo ERROR: "+err.Error())
		return r
	}
	owner, name, err := h.ParseRepo(cfg.Repo.URL)
	if err != nil {
		r.OK = false
		r.Lines = append(r.Lines, "repo ERROR: "+err.Error())
		return r
	}
	info, err := h.GetRepo(owner, name)
	if err != nil {
		r.OK = false
		r.Lines = append(r.Lines, "repo ERROR: "+err.Error())
		return r
	}
	r.Lines = append(r.Lines, fmt.Sprintf("repo OK: %s (rama por defecto: %s)", info.FullName, info.DefaultBranch))
	return r
}
