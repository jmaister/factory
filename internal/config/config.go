// Package config carga factory.yaml, la configuración de UN proyecto. Los secretos van en factory.env
// (junto al YAML) y se referencian como ${VAR}. Los estados del tablero son fijos y viven aquí.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// States son las columnas del tablero de Vikunja que usa la factory (clave lógica → título).
var States = map[string]string{
	"draft":       "📝 DRAFT",
	"todo":        "📥 TODO",
	"in_progress": "🤖 EN CURSO",
	"review":      "👀 REVISIÓN",
	"rework":      "🔁 REWORK",
	"approved":    "👍 APROBADO",
	"done":        "✅ HECHO",
}

// EnvFileName es el fichero de secretos que se busca junto a factory.yaml.
const EnvFileName = "factory.env"

type Tickets struct {
	Type    string `yaml:"type"`
	URL     string `yaml:"url"`
	Token   string `yaml:"token"`
	Project string `yaml:"project"`
}

type Repo struct {
	Type   string `yaml:"type"`
	URL    string `yaml:"url"`
	Path   string `yaml:"path"`
	Branch string `yaml:"branch"`
	Token  string `yaml:"token"`
}

type Agent struct {
	Model     string `yaml:"model"`
	MaxRework int    `yaml:"max_rework"`
}

// Slots configura las ranuras de despliegue local.
// La ranura i usa el gateway en base_port + 10·i y la aplicación en ese puerto + 1.
type Slots struct {
	Count      int    `yaml:"count"`
	BasePort   int    `yaml:"base_port"`
	PublicHost string `yaml:"public_host"` // p. ej. http://192.168.1.35
}

type Webhook struct {
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"`
}

// CIServer es el servidor de CI donde se ejecutan los workflows (p. ej. Forgejo Actions).
type CIServer struct {
	Type  string `yaml:"type"`
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
	Repo  string `yaml:"repo"` // owner/nombre en el servidor de CI
}

// Service es un proceso que arranca en cada ranura de despliegue local.
// Puerto: el de la ranura (base_port + 10·i) más `port`. En `command` y `env` se pueden usar:
// {port} (puerto de este servicio), {<nombre>_port} (puerto de otro servicio), {slot} (número de ranura)
// y {public_url} (URL pública de la ranura).
type Service struct {
	Name    string            `yaml:"name"`
	Port    int               `yaml:"port"`
	Public  bool              `yaml:"public"` // la URL que se muestra al usuario sale de este servicio
	Command string            `yaml:"command"`
	Env     map[string]string `yaml:"env"`
}

// Config es la configuración de un proyecto.
type Config struct {
	Dir     string  `yaml:"-"` // carpeta de factory.yaml (donde están factory.env y el estado)
	Tickets Tickets `yaml:"tickets"`
	Repo    Repo    `yaml:"repo"`
	Build   string  `yaml:"build"`
	// LocalDeploy: al terminar el agente, compila y arranca en local la rama de la tarea (build + run).
	LocalDeploy bool `yaml:"local_deploy"`
	// LocalLinks: ficheros o carpetas (ignorados por git) que se enlazan desde el repo para el despliegue local.
	LocalLinks []string `yaml:"local_links"`
	// Slots: ranuras de despliegue local (cada una con su copia de datos y sus puertos).
	Slots Slots `yaml:"slots"`
	// Services: procesos que arrancan en cada ranura (p. ej. la app y un proxy). Ver Service.
	Services []Service `yaml:"services"`
	Agent    Agent     `yaml:"agent"`
	Webhook  Webhook   `yaml:"webhook"`
	CI       CIServer  `yaml:"ci-server"`
	Timezone string    `yaml:"timezone"` // zona horaria para mostrar y registrar horas (p. ej. Europe/Madrid)
}

// DefaultModel y DefaultMaxRework se usan si la configuración no los indica.
const (
	DefaultModel      = "claude-haiku-5-5"
	DefaultMaxRework  = 3
	DefaultListenAddr = ":8787"
)

var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadEnvFile carga KEY=VALUE en el entorno (sin sobrescribir lo ya definido).
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(strings.TrimSpace(key)); !exists {
			os.Setenv(strings.TrimSpace(key), strings.TrimSpace(value))
		}
	}
	return sc.Err()
}

func expand(value string) (string, error) {
	var missing []string
	out := varPattern.ReplaceAllStringFunc(value, func(m string) string {
		name := varPattern.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("variables no definidas en %s: %s", EnvFileName, strings.Join(missing, ", "))
	}
	return out, nil
}

// Load lee factory.yaml, carga factory.env de su misma carpeta y valida la configuración.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err := LoadEnvFile(filepath.Join(dir, EnvFileName)); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("YAML inválido: %w", err)
	}
	cfg.Dir = dir
	if cfg.Timezone != "" {
		loc, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("timezone '%s' no válida: %w", cfg.Timezone, err)
		}
		time.Local = loc
	}

	for _, f := range []*string{&cfg.Tickets.URL, &cfg.Tickets.Token, &cfg.Tickets.Project, &cfg.Repo.URL, &cfg.Repo.Token, &cfg.Webhook.PublicURL, &cfg.CI.URL, &cfg.CI.Token} {
		if *f, err = expand(*f); err != nil {
			return nil, err
		}
	}

	if cfg.Tickets.Type == "" || cfg.Tickets.URL == "" || cfg.Tickets.Token == "" || cfg.Tickets.Project == "" {
		return nil, fmt.Errorf("tickets: faltan type, url, token o project")
	}
	if cfg.Repo.Type == "" || cfg.Repo.URL == "" || cfg.Repo.Token == "" {
		return nil, fmt.Errorf("repo: faltan type, url o token")
	}
	if cfg.CI.Type != "" && (cfg.CI.URL == "" || cfg.CI.Token == "" || cfg.CI.Repo == "") {
		return nil, fmt.Errorf("ci-server: faltan url, token o repo")
	}
	if cfg.Repo.Path == "" {
		cfg.Repo.Path = "."
	}
	if !filepath.IsAbs(cfg.Repo.Path) {
		cfg.Repo.Path = filepath.Join(dir, cfg.Repo.Path)
	}
	if st, err := os.Stat(cfg.Repo.Path); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("repo.path '%s' no es una carpeta", cfg.Repo.Path)
	}
	if cfg.Slots.Count == 0 {
		cfg.Slots.Count = 5
	}
	if cfg.Slots.BasePort == 0 {
		cfg.Slots.BasePort = 8010
	}
	if cfg.Slots.PublicHost == "" {
		cfg.Slots.PublicHost = "http://localhost"
	}
	if cfg.LocalDeploy && len(cfg.Services) == 0 {
		return nil, fmt.Errorf("local_deploy está activo pero no hay services: define al menos uno")
	}
	for _, svc := range cfg.Services {
		if svc.Name == "" || svc.Command == "" {
			return nil, fmt.Errorf("cada service necesita name y command")
		}
	}
	if cfg.Repo.Branch == "" {
		cfg.Repo.Branch = "main"
	}
	if cfg.Agent.Model == "" {
		cfg.Agent.Model = DefaultModel
	}
	if cfg.Agent.MaxRework == 0 {
		cfg.Agent.MaxRework = DefaultMaxRework
	}
	if cfg.Webhook.Listen == "" {
		cfg.Webhook.Listen = DefaultListenAddr
	}
	return &cfg, nil
}

// Token devuelve el token del webhook (FACTORY_WEBHOOK_TOKEN de factory.env).
func (c *Config) Token() string { return os.Getenv("FACTORY_WEBHOOK_TOKEN") }

// EnvPath es la ruta de factory.env de este proyecto.
func (c *Config) EnvPath() string { return filepath.Join(c.Dir, EnvFileName) }

// StateDir es donde la factory guarda su estado (sesiones, logs). Va fuera del repo para no ensuciarlo.
func (c *Config) StateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".local", "state", "factory", strings.ToLower(c.Tickets.Project))
}

// TestAccount es una cuenta de prueba de la aplicación desplegada en local.
type TestAccount struct {
	Role     string
	User     string
	Password string
}

// TestAccounts lee las cuentas de prueba de factory.env: TEST_<ROL>_USER y TEST_<ROL>_PASSWORD.
func (c *Config) TestAccounts() []TestAccount {
	var out []TestAccount
	for _, role := range []string{"ADMIN", "USER"} {
		u, p := os.Getenv("TEST_"+role+"_USER"), os.Getenv("TEST_"+role+"_PASSWORD")
		if u != "" {
			out = append(out, TestAccount{Role: strings.ToLower(role), User: u, Password: p})
		}
	}
	return out
}
