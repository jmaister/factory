// Comando factory: software factory basada en Vikunja (tickets) y GitHub (código).
// La configuración es de un solo proyecto: factory.yaml (y factory.env con los secretos).
//
// Uso:
//
//	factory check   [-c factory.yaml]
//	factory new     [-c factory.yaml] <título>
//	factory list    [-c factory.yaml]
//	factory ready   [-c factory.yaml] <ACA-n | id>
//	factory start   [-c factory.yaml] [--once] [--interval 1m] [--listen :8787]   (abre el TUI)
//	factory deploy  [-c factory.yaml] <ACA-n>
//	factory webhook install [-c factory.yaml] [--url http://<host>:8787]
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"factory/internal/check"
	"factory/internal/config"
	"factory/internal/tickets"
	"factory/internal/tui"
	"factory/internal/workflow"
)

const usage = `uso:
  factory check   [-c factory.yaml]
  factory new     [-c factory.yaml] <título>
  factory list    [-c factory.yaml]
  factory ready   [-c factory.yaml] <ACA-n | id>
  factory start   [-c factory.yaml] [--once] [--interval 1m] [--listen :8787]   (abre el TUI)
  factory deploy  [-c factory.yaml] <ACA-n>
  factory webhook install [-c factory.yaml] [--url http://<host>:8787]`

// version se fija al compilar: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" || cmd == "--version" {
		fmt.Println("factory", version)
		return 0
	}
	if cmd == "webhook" {
		return cmdWebhook(rest)
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	cfgPath := fs.String("c", "factory.yaml", "ruta a factory.yaml")
	once := fs.Bool("once", false, "start: procesa una tarea y sale")
	interval := fs.Duration("interval", time.Minute, "start: espera entre comprobaciones del tablero")
	listen := fs.String("listen", "", "start: dirección del webhook (por defecto, la de factory.yaml)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error de configuración: %v\n", err)
		return 2
	}

	switch cmd {
	case "check":
		return cmdCheck(cfg)
	case "new":
		title := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if title == "" {
			fmt.Fprintln(os.Stderr, "falta el título: factory new <título>")
			return 2
		}
		return cmdNew(cfg, title)
	case "list":
		return cmdList(cfg)
	case "ready":
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "uso: factory ready <ACA-n | id>")
			return 2
		}
		return cmdReady(cfg, fs.Arg(0))
	case "deploy":
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "uso: factory deploy <ACA-n>")
			return 2
		}
		return cmdDeploy(cfg, fs.Arg(0))
	case "start":
		addr := *listen
		if addr == "" {
			addr = cfg.Webhook.Listen
		}
		if *once {
			return cmdStartOnce(cfg, addr)
		}
		return cmdStart(cfg, *interval, addr)
	default:
		fmt.Fprintf(os.Stderr, "comando desconocido: %s\n%s\n", cmd, usage)
		return 2
	}
}

func cmdCheck(cfg *config.Config) int {
	r := check.Run(cfg)
	for _, line := range r.Lines {
		fmt.Printf("    %s\n", line)
	}
	if !r.OK {
		return 1
	}
	return 0
}

func cmdNew(cfg *config.Config, title string) int {
	task, url, err := workflow.New(cfg, title)
	if task.ID != 0 {
		fmt.Printf("%s %s\n", taskRef(task), task.Title)
		fmt.Printf("estado: %s\n", config.States["draft"])
		fmt.Printf("url: %s\n", url)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func cmdList(cfg *config.Config) int {
	cols, err := workflow.List(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, c := range cols {
		fmt.Printf("%s (%d)\n", c.Title, len(c.Tasks))
		for _, t := range c.Tasks {
			fmt.Printf("    %s %s\n", taskRef(t), t.Title)
		}
	}
	return 0
}

func cmdReady(cfg *config.Config, ref string) int {
	task, url, err := workflow.Ready(cfg, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s %s\n", taskRef(task), task.Title)
	fmt.Printf("estado: %s\n", config.States["todo"])
	fmt.Printf("url: %s\n", url)
	return 0
}

// cmdStart abre el TUI: el bucle de la factory y el receptor de webhooks corren dentro del mismo proceso.
// cmdDeploy despliega en local la rama de una tarea (lo mismo que la tecla d del TUI).
func cmdDeploy(cfg *config.Config, ref string) int {
	logf := func(format string, args ...any) {
		fmt.Printf("%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
	if err := workflow.DeployTask(cfg, ref, logf); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func cmdStart(cfg *config.Config, interval time.Duration, listen string) int {
	opts := tui.RunOptions{Interval: interval, Listen: listen, Token: cfg.Token()}
	if err := tui.Run(cfg, opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// cmdStartOnce procesa una tarea (o comprueba REVISIÓN) y sale, sin TUI.
func cmdStartOnce(cfg *config.Config, listen string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stdout, format+"\n", args...) }
	wake := make(chan struct{}, 1)
	opts := workflow.StartOptions{Once: true, Wake: wake, Log: logf}
	if err := workflow.Start(ctx, cfg, opts); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// cmdWebhook: `factory webhook install [--url ...]` crea el webhook de Vikunja hacia la factory.
func cmdWebhook(args []string) int {
	if len(args) == 0 || args[0] != "install" {
		fmt.Fprintln(os.Stderr, "uso: factory webhook install [-c factory.yaml] [--url http://<host>:8787]")
		return 2
	}
	fs := flag.NewFlagSet("webhook install", flag.ContinueOnError)
	cfgPath := fs.String("c", "factory.yaml", "ruta a factory.yaml")
	base := fs.String("url", "", "URL pública de la factory (por defecto, webhook.public_url)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error de configuración: %v\n", err)
		return 2
	}
	publicURL := *base
	if publicURL == "" {
		publicURL = cfg.Webhook.PublicURL
	}
	if publicURL == "" {
		fmt.Fprintln(os.Stderr, "falta --url (o webhook.public_url en factory.yaml)")
		return 2
	}
	token, err := ensureWebhookToken(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	target := strings.TrimRight(publicURL, "/") + "/webhook?token=" + token
	h, created, err := workflow.InstallWebhook(cfg, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if created {
		fmt.Printf("webhook creado (id %d) hacia %s/webhook\n", h.ID, strings.TrimRight(publicURL, "/"))
	} else {
		fmt.Printf("el webhook ya existía (id %d)\n", h.ID)
	}
	return 0
}

// ensureWebhookToken devuelve el token del webhook; si no existe, lo genera y lo añade a factory.env (600).
func ensureWebhookToken(cfg *config.Config) (string, error) {
	if tok := cfg.Token(); tok != "" {
		return tok, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	f, err := os.OpenFile(cfg.EnvPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "FACTORY_WEBHOOK_TOKEN=%s\n", tok); err != nil {
		return "", err
	}
	os.Setenv("FACTORY_WEBHOOK_TOKEN", tok)
	return tok, nil
}

// taskRef devuelve el identificador de la tarea (ACA-6) o, si no tiene, su id global.
func taskRef(t tickets.Task) string {
	if t.Identifier != "" {
		return t.Identifier
	}
	return fmt.Sprintf("#%d", t.ID)
}

// newLogger escribe cada línea en la terminal y en factory.log (en el estado de la factory),
// para poder seguirla con `tail -f` aunque la terminal esté ocupada por una sesión de Claude.
func newLogger(cfg *config.Config) func(string, ...any) {
	path := filepath.Join(cfg.StateDir(), "factory.log")
	return func(format string, args ...any) {
		line := fmt.Sprintf("%s %s", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
		fmt.Fprintln(os.Stdout, line)
		_ = os.MkdirAll(cfg.StateDir(), 0o755)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, line)
			f.Close()
		}
	}
}
