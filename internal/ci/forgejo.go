// Package ci lee los resultados de los workflows (Forgejo Actions) de un commit.
package ci

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Run es una ejecución de un workflow.
type Run struct {
	Index    int64  // número del run en el repo (el que aparece en la URL)
	Workflow string // p. ej. ci.yml
	Status   string // success, failure, running, waiting…
	SHA      string
	URL      string
}

// Summary es el estado agregado de los runs de un commit.
type Summary struct {
	Runs    []Run
	Running int
	OK      int
	Failed  int
}

// State: none (sin runs todavía), running, success o failure.
func (s Summary) State() string {
	switch {
	case len(s.Runs) == 0:
		return "none"
	case s.Failed > 0:
		return "failure"
	case s.Running > 0:
		return "running"
	default:
		return "success"
	}
}

// Forgejo consulta la API de Forgejo Actions de un repo.
type Forgejo struct {
	base   string
	token  string
	repo   string
	client *http.Client
}

// NewForgejo crea el cliente. repo es "owner/nombre".
func NewForgejo(url, token, repo string) *Forgejo {
	return &Forgejo{
		base:   strings.TrimRight(url, "/") + "/api/v1",
		token:  token,
		repo:   repo,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// RunsFor devuelve los runs de un commit concreto.
func (f *Forgejo) RunsFor(sha string) (Summary, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/repos/%s/actions/runs?limit=50", f.base, f.repo), nil)
	if err != nil {
		return Summary{}, err
	}
	req.Header.Set("Authorization", "token "+f.token)
	resp, err := f.client.Do(req)
	if err != nil {
		return Summary{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Summary{}, fmt.Errorf("Forgejo: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Runs []struct {
			Index     int64  `json:"index_in_repo"`
			Workflow  string `json:"workflow_id"`
			Status    string `json:"status"`
			CommitSHA string `json:"commit_sha"`
			HTMLURL   string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return Summary{}, err
	}
	var sum Summary
	for _, r := range data.Runs {
		if r.CommitSHA != sha {
			continue
		}
		run := Run{Index: r.Index, Workflow: r.Workflow, Status: r.Status, SHA: r.CommitSHA, URL: r.HTMLURL}
		sum.Runs = append(sum.Runs, run)
		switch r.Status {
		case "success":
			sum.OK++
		case "failure", "cancelled", "error":
			sum.Failed++
		default:
			sum.Running++
		}
	}
	return sum, nil
}
