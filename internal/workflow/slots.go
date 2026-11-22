package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"factory/internal/config"
)

// slotRecord dice qué tarea y commit tiene desplegado una ranura.
type slotRecord struct {
	Ref string    `json:"ref"`
	SHA string    `json:"sha"`
	At  time.Time `json:"at"`
}

// SlotInfo es una ranura desplegada, para mostrarla en el TUI.
type SlotInfo struct {
	Index int
	URL   string
}

// dbFiles son las bases de datos que cada ranura tiene como copia propia.
var dbFiles = []string{"academia.db", "academia.db-wal", "academia.db-shm", "taronja-gateway.db", "taronja-gateway.db-wal", "taronja-gateway.db-shm"}

func slotsPath(cfg *config.Config) string { return filepath.Join(cfg.StateDir(), "slots.json") }

func loadSlots(cfg *config.Config) (map[int]slotRecord, error) {
	out := map[int]slotRecord{}
	raw, err := os.ReadFile(slotsPath(cfg))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var asStr map[string]slotRecord
	if err := json.Unmarshal(raw, &asStr); err != nil {
		return nil, fmt.Errorf("slots.json corrupto: %w", err)
	}
	for k, v := range asStr {
		var i int
		if _, err := fmt.Sscanf(k, "%d", &i); err == nil {
			out[i] = v
		}
	}
	return out, nil
}

func saveSlots(cfg *config.Config, m map[int]slotRecord) error {
	asStr := map[string]slotRecord{}
	for i, v := range m {
		asStr[fmt.Sprint(i)] = v
	}
	raw, err := json.MarshalIndent(asStr, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StateDir(), 0o755); err != nil {
		return err
	}
	tmp := slotsPath(cfg) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, slotsPath(cfg))
}

// slotPorts devuelve los puertos del gateway y de la aplicación de la ranura i.
func slotPorts(cfg *config.Config, i int) (gateway, app int) {
	gateway = cfg.Slots.BasePort + 10*i
	return gateway, gateway + 1
}

func slotURL(cfg *config.Config, i int) string {
	gw, _ := slotPorts(cfg, i)
	return fmt.Sprintf("%s:%d", strings.TrimRight(cfg.Slots.PublicHost, "/"), gw)
}

func slotDir(cfg *config.Config, i int) string {
	return filepath.Join(cfg.StateDir(), "slots", fmt.Sprint(i))
}
func slotDataDir(cfg *config.Config, i int) string {
	return filepath.Join(cfg.StateDir(), "slots", fmt.Sprint(i)+"-data")
}
func slotPidFile(cfg *config.Config, i int) string {
	return filepath.Join(cfg.StateDir(), "slots", fmt.Sprint(i)+".pids")
}
func slotSession(cfg *config.Config, i int) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(cfg.Tickets.Project), "-"), "-") + "-slot" + fmt.Sprint(i)
}

// DeployedSlots devuelve, por identificador de tarea, la ranura donde está desplegada.
func DeployedSlots(cfg *config.Config) map[string]SlotInfo {
	out := map[string]SlotInfo{}
	m, err := loadSlots(cfg)
	if err != nil {
		return out
	}
	for i, rec := range m {
		out[rec.Ref] = SlotInfo{Index: i, URL: slotURL(cfg, i)}
	}
	return out
}

// pickSlot elige ranura: la que ya tiene la tarea, una libre, o la más antigua.
func pickSlot(cfg *config.Config, m map[int]slotRecord, ref string) int {
	for i, rec := range m {
		if rec.Ref == ref {
			return i
		}
	}
	// La primera ranura libre (sin tarea desplegada). Si no hay ninguna, se sustituye la más antigua.
	for i := 0; i < cfg.Slots.Count; i++ {
		if _, ok := m[i]; !ok {
			return i
		}
	}
	oldest, first := 0, true
	for i, rec := range m {
		if first || rec.At.Before(m[oldest].At) {
			oldest, first = i, false
		}
	}
	return oldest
}

// stopSlot para la aplicación de la ranura: mata sus procesos y cierra su sesión de screen.
func stopSlot(cfg *config.Config, i int) {
	if raw, err := os.ReadFile(slotPidFile(cfg, i)); err == nil {
		for _, f := range strings.Fields(string(raw)) {
			var pid int
			if _, err := fmt.Sscanf(f, "%d", &pid); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Kill()
				}
			}
		}
		_ = os.Remove(slotPidFile(cfg, i))
	}
	_ = exec.Command("screen", "-S", slotSession(cfg, i), "-X", "quit").Run()
}

// linkDBs enlaza en la carpeta de código las bases de datos del repo: todas las ranuras comparten los mismos datos.
func linkDBs(cfg *config.Config, i int) error {
	dir := slotDir(cfg, i)
	for _, f := range dbFiles {
		dst := filepath.Join(dir, f)
		_ = os.Remove(dst)
		if err := os.Symlink(filepath.Join(cfg.Repo.Path, f), dst); err != nil {
			return err
		}
	}
	return nil
}

// startSlot arranca la aplicación y el gateway de la ranura en una sesión de screen.
// Los procesos se apuntan en un fichero para poder pararlos después.
func startSlot(cfg *config.Config, i int) error {
	dir := slotDir(cfg, i)
	gw, app := slotPorts(cfg, i)
	logPath := filepath.Join(cfg.StateDir(), "slots", fmt.Sprint(i)+".log")
	script := fmt.Sprintf(`#!/usr/bin/env bash
cd %s || exit 1
export TG_SERVER_HOST=0.0.0.0 TG_SERVER_PORT=%d TG_APP_PORT=%d PUBLIC_URL=%s:%d
( exec %s run --port %d ) &
echo $! > %s
( exec %s ) &
echo $! >> %s
wait
`, shellQuote(dir), gw, app, strings.TrimRight(cfg.Slots.PublicHost, "/"), gw,
		cfg.AppBinary, app, shellQuote(slotPidFile(cfg, i)),
		cfg.GatewayCommand, shellQuote(slotPidFile(cfg, i)))
	scriptPath := filepath.Join(cfg.StateDir(), "slots", fmt.Sprint(i)+".sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		return err
	}
	out, err := exec.Command("screen", "-L", "-Logfile", logPath, "-dmS", slotSession(cfg, i), "bash", scriptPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("no pude arrancar la ranura: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
