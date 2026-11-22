// Package tui muestra el tablero de la factory: un panel con la lista de tareas (agrupadas por columna)
// y un panel con el detalle de la tarea seleccionada, su progreso y lo que puedes hacer a continuación.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"factory/internal/config"
	"factory/internal/runner"
	"factory/internal/webhook"
	"factory/internal/workflow"
)

const (
	refreshEvery = 15 * time.Second
	maxLogLines  = 500
	spinEvery    = 120 * time.Millisecond
)

var spinFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

type spinTickMsg time.Time

func spinTick() tea.Cmd {
	return tea.Tick(spinEvery, func(t time.Time) tea.Msg { return spinTickMsg(t) })
}

// Orden de las columnas en el tablero (y de las secciones de la lista).
var columnOrder = []string{"draft", "todo", "in_progress", "review", "rework", "approved", "done"}

// Etapas del pipeline para la barra de progreso (rework vuelve a la etapa del agente).
var stages = []string{"Borrador", "Cola", "Agente", "Revisión", "Aprobado", "Hecho"}

var (
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	labelStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	sectionStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	selectedStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("63"))
	errStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	spinnerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")) // cian brillante
	footerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("236"))
	footerKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4"))
	focusColor     = lipgloss.Color("39")
	blurColor      = lipgloss.Color("240")
)

type snapshotMsg struct {
	views    []workflow.TaskView
	err      error
	at       time.Time
	deployed string
}

// PhaseMsg indica qué está haciendo la factory ahora ("" = inactiva).
type PhaseMsg string

// DeployDoneMsg indica que terminó un despliegue manual.
type DeployDoneMsg struct {
	Ref string
	Err error
}

type tickMsg time.Time

// LogMsg es una línea de log del bucle.
type LogMsg string

// WebhookMsg es un aviso recibido de Vikunja.
type WebhookMsg struct {
	Event string
	At    time.Time
}

// WebhookInfoMsg actualiza el estado del receptor de webhooks.
type WebhookInfoMsg string

// LoopDoneMsg indica que el bucle ha terminado.
type LoopDoneMsg struct{ Err error }

// Model es el estado del TUI.
type Model struct {
	cfg          *config.Config
	views        []workflow.TaskView
	cursor       int // índice en views
	detailScroll int
	focusDetails bool
	err          error
	at           time.Time
	width        int
	height       int
	loading      bool

	phase    string    // qué hace la factory ahora
	phaseAt  time.Time // desde cuándo está en esa fase
	spin     int       // fotograma del spinner
	deployed string    // tarea desplegada en local
	busy     bool      // hay un despliegue manual en curso
	accounts []config.TestAccount
	deployFn func(ref string) tea.Cmd
	busyRef  string // tarea del despliegue manual en curso

	showLog        bool     // vista de log (tecla l)
	showComments   bool     // vista de comentarios de la tarea (tecla c)
	showPalette    bool     // vista de prueba de colores (tecla t)
	showHelp       bool     // vista de ayuda (tecla h)
	commentsScroll int      // primera línea visible de la vista de comentarios
	logLines       []string // últimas líneas de actividad
	logScroll      int      // líneas subidas desde el final

	webhookInfo string    // estado del receptor (escuchando, sin token…)
	lastEvent   string    // último aviso recibido
	lastEventAt time.Time // cuándo llegó
	eventCount  int       // avisos recibidos desde que arrancó
	lastLog     string    // última línea de actividad del bucle
	loopDone    bool
	loopErr     error
}

// New crea el TUI para la configuración dada.
func New(cfg *config.Config) Model {
	return Model{cfg: cfg, loading: true}
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		views, err := workflow.Snapshot(m.cfg)
		return snapshotMsg{views: views, err: err, at: time.Now()}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init arranca la primera lectura del tablero y el refresco periódico.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), tick(), spinTick())
}

// Update procesa teclas, lecturas y el refresco periódico.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case snapshotMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.views = orderedViews(msg.views)
			m.at = msg.at

			if m.cursor >= len(m.views) {
				m.cursor = max(0, len(m.views)-1)
			}
		}
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	case LogMsg:
		m.lastLog = string(msg)
		m.logLines = append(m.logLines, string(msg))
		if len(m.logLines) > maxLogLines {
			m.logLines = m.logLines[len(m.logLines)-maxLogLines:]
		}
	case PhaseMsg:
		if string(msg) != m.phase {
			m.phaseAt = time.Now()
		}
		m.phase = string(msg)
	case spinTickMsg:
		m.spin = (m.spin + 1) % len(spinFrames)
		return m, spinTick()
	case DeployDoneMsg:
		m.busy = false
		if msg.Err != nil {
			m.lastLog = "despliegue de " + msg.Ref + " fallido: " + msg.Err.Error()
		}
	case WebhookInfoMsg:
		m.webhookInfo = string(msg)
	case WebhookMsg:
		m.lastEvent, m.lastEventAt = msg.Event, msg.At
		m.eventCount++
	case LoopDoneMsg:
		m.loopDone, m.loopErr = true, msg.Err
		m.lastLog = "bucle detenido"
		if msg.Err != nil {
			m.lastLog = "bucle detenido: " + msg.Err.Error()
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.showLog = false
			m.showComments = false
			m.showPalette = false
			m.showHelp = false
		case "h":
			m.showHelp = !m.showHelp
		case "t":
			m.showPalette = !m.showPalette
		case "c":
			m.showComments = !m.showComments
			m.showLog = false
			m.commentsScroll = 0
		case "l":
			m.showLog = !m.showLog
			m.logScroll = 0
		case "tab":
			m.focusDetails = !m.focusDetails
		case "up", "k":
			if m.showComments {
				if m.commentsScroll > m.maxCommentsScroll() {
					m.commentsScroll = m.maxCommentsScroll()
				}
				if m.commentsScroll > 0 {
					m.commentsScroll--
				}
				break
			}
			if m.showLog {
				if m.logScroll < len(m.logLines)-1 {
					m.logScroll++
				}
			} else if m.focusDetails {
				if m.detailScroll > 0 {
					m.detailScroll--
				}
			} else if m.cursor > 0 {
				m.cursor--
				m.detailScroll = 0
			}
		case "down", "j":
			if m.showComments {
				if m.commentsScroll < m.maxCommentsScroll() {
					m.commentsScroll++
				}
				break
			}
			if m.showLog {
				if m.logScroll > 0 {
					m.logScroll--
				}
			} else if m.focusDetails {
				m.detailScroll++
			} else if m.cursor < len(m.views)-1 {
				m.cursor++
				m.detailScroll = 0
			}
		case "d":
			if m.busy || len(m.views) == 0 || m.deployFn == nil {
				break
			}
			ref := refOf(m.views[m.cursor])
			m.busy = true
			m.busyRef = ref
			return m, m.deployFn(ref)
		case "r":
			m.loading = true
			return m, m.fetch()
		}
	}
	return m, nil
}

// orderedViews agrupa las tareas por columna, en el orden del tablero.
func orderedViews(views []workflow.TaskView) []workflow.TaskView {
	var out []workflow.TaskView
	for _, st := range columnOrder {
		for _, v := range views {
			if v.State == st {
				out = append(out, v)
			}
		}
	}
	for _, v := range views { // columnas desconocidas, al final
		if !contains(columnOrder, v.State) {
			out = append(out, v)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// View dibuja cabecera, panel de tareas y panel de detalle.
func (m Model) View() string {
	if m.width < 20 || m.height < 12 {
		return "Ventana demasiado pequeña"
	}
	header := headerStyle.Render(fmt.Sprintf("Factory · %s", m.cfg.Tickets.Project)) + "  " + dimStyle.Render(m.statusLine())
	footer := lipgloss.NewStyle().MaxWidth(m.width).Render(footerText(m.showLog, m.showComments, m.showPalette, m.showHelp))
	body := m.height - 4 // cabecera, dos líneas de estado y footer

	var content string
	if m.showHelp {
		content = m.panel("Ayuda", renderHelp(), m.width, body, true)
	} else if m.showPalette {
		content = m.panel("Paleta de colores", renderPalette(), m.width, body, true)
	} else if m.showComments {
		content = m.panel("Contenido", m.renderComments(body-2, m.width-2), m.width, body, true)
	} else if m.showLog {
		content = m.panel("Log", m.renderLog(body-2, m.width-2), m.width, body, true)
	} else {
		listH := max(8, body/2)
		detailH := body - listH
		list := m.panel("Tareas", m.renderList(listH-2, m.width-2), m.width, listH, !m.focusDetails)
		details := m.panel("Detalle", m.renderDetails(detailH-2, m.width-2), m.width, detailH, m.focusDetails)
		content = list + "\n" + details
	}
	status := m.statusBar()
	return strings.Join([]string{header, status, content, footer}, "\n")
}

// hint es un atajo: la tecla en blanco brillante y la acción en gris.
type hint struct{ key, action string }

// footerText lista los atajos disponibles en la vista actual, con la tecla resaltada.
func footerText(showLog, showComments, showPalette, showHelp bool) string {
	var hints []hint
	switch {
	case showHelp:
		hints = []hint{{"h/esc", "volver"}, {"q", "salir"}}
	case showPalette:
		hints = []hint{{"t/esc", "volver"}, {"q", "salir"}}
	case showComments:
		hints = []hint{{"↑/↓", "desplazar"}, {"c/esc", "volver"}, {"q", "salir"}}
	case showLog:
		hints = []hint{{"↑/↓", "desplazar"}, {"l/esc", "volver"}, {"q", "salir"}}
	default:
		hints = []hint{{"Tab", "foco"}, {"↑/↓", "mover"}, {"d", "desplegar en local"}, {"c", "contenido"}, {"r", "refrescar"}, {"l", "log"}, {"t", "colores"}, {"h", "ayuda"}, {"q", "salir"}}
	}
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, footerKeyStyle.Render(" "+h.key+" ")+footerStyle.Render(" "+h.action+" "))
	}
	return strings.Join(parts, footerStyle.Render(" "))
}

// renderLog muestra las últimas líneas del bucle; ↑/↓ suben por el histórico.
func (m Model) renderLog(rows, width int) string {
	if len(m.logLines) == 0 {
		return dimStyle.Render(" (sin actividad todavía)")
	}
	end := len(m.logLines) - m.logScroll
	if end < 1 {
		end = 1
	}
	start := max(0, end-rows)
	var out []string
	for _, l := range m.logLines[start:end] {
		out = append(out, truncate(l, width))
	}
	return strings.Join(out, "\n")
}

// panel dibuja un recuadro con título; el color del borde indica si tiene el foco.
// Cada línea se mide con lipgloss.Width (que entiende los colores), así que los bordes siempre encajan.
func (m Model) panel(title, content string, width, height int, focused bool) string {
	color := blurColor
	if focused {
		color = focusColor
	}
	bc := lipgloss.NewStyle().Foreground(color)
	inner := width - 2
	titleStr := lipgloss.NewStyle().Foreground(color).Bold(focused).Render(" " + title + " ")
	fill := max(0, width-4-lipgloss.Width(titleStr))
	top := bc.Render("╭─") + titleStr + bc.Render(strings.Repeat("─", fill)+"─╮")

	lines := strings.Split(content, "\n")
	var out []string
	out = append(out, top)
	for i := 0; i < height-2; i++ {
		l := ""
		if i < len(lines) {
			l = lipgloss.NewStyle().MaxWidth(inner).Render(lines[i])
		}
		out = append(out, bc.Render("│")+padRight(l, inner)+bc.Render("│"))
	}
	out = append(out, bc.Render("╰"+strings.Repeat("─", max(0, width-2))+"╯"))
	return strings.Join(out, "\n")
}

func (m Model) statusLine() string {
	switch {
	case m.loading:
		return "leyendo el tablero…"
	case m.err != nil:
		return "error: " + m.err.Error()
	}
	counts := map[string]int{}
	for _, v := range m.views {
		counts[v.State]++
	}
	var parts []string
	for _, st := range columnOrder {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", config.States[st], counts[st]))
		}
	}
	return strings.Join(parts, " · ") + fmt.Sprintf("   [actualizado %s]", m.at.Format("15:04:05"))
}

// renderList muestra las tareas agrupadas por columna, con el cursor visible dentro del panel.
func (m Model) renderList(rows, width int) string {
	// Una sección por columna del tablero, aunque esté vacía; las columnas desconocidas, al final.
	order := append([]string{}, columnOrder...)
	for _, v := range m.views {
		if !contains(order, v.State) {
			order = append(order, v.State)
		}
	}

	var lines []string
	cursorLine := 0
	for _, st := range order {
		var group []int
		for i, v := range m.views {
			if v.State == st {
				group = append(group, i)
			}
		}
		title := config.States[st]
		if title == "" && len(group) > 0 {
			title = m.views[group[0]].Column
		}
		if title == "" {
			title = st
		}
		lines = append(lines, sectionStyle.Render(fmt.Sprintf("── %s (%d) ", title, len(group))))
		if len(group) == 0 {
			lines = append(lines, dimStyle.Render("   (vacía)"))
		}
		for _, i := range group {
			v := m.views[i]
			mark := "  "
			if m.isActive(v) {
				if i == m.cursor {
					// En la fila seleccionada el glifo va sin color: un reset interno cortaría el fondo de la selección.
					mark = spinFrames[m.spin] + " "
				} else {
					mark = spinnerStyle.Render(spinFrames[m.spin]) + " "
				}
			} else if v.SlotURL != "" {
				mark = "● "
			}
			tag := "    "
			if v.SlotIdx >= 0 {
				tag = fmt.Sprintf("R%d  ", v.SlotIdx)
			}
			row := truncate(fmt.Sprintf("%s%-10s %s%s", mark, refOf(v), tag, v.Task.Title), width)
			if i == m.cursor {
				cursorLine = len(lines)
				lines = append(lines, selectedStyle.Render(padRight(row, width)))
			} else {
				lines = append(lines, row)
			}
		}
	}

	start := 0
	if cursorLine >= rows {
		start = cursorLine - rows + 1
	}
	var out []string
	for i := start; i < len(lines) && len(out) < rows; i++ {
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// renderDetails muestra la tarea seleccionada, su barra de progreso y los pasos siguientes.
func (m Model) renderDetails(rows, width int) string {
	if len(m.views) == 0 {
		return ""
	}
	v := m.views[m.cursor]
	headline, steps := workflow.Guide(v)
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", labelStyle.Render(refOf(v)), v.Task.Title)
	fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Estado:"), v.Column)
	fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Progreso:"), progressBar(v.State, width-12))
	fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Vikunja:"), link(v.URL))
	if v.PRURL != "" {
		fmt.Fprintf(&b, "%s %s %s\n", labelStyle.Render("PR:"), link(v.PRURL), prStateLabel(v.PRState))
	}
	if v.PRURL == "" || v.PRState == "merged" || v.PRState == "closed" {
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("CI:"), dimStyle.Render("N/A"))
	} else {
		fmt.Fprintf(&b, "%s %s %s\n", labelStyle.Render("CI:"), ciSummary(v), dimStyle.Render("(GitHub)"))
	}
	if v.PRState == "merged" {
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Merge a master:"), forgejoSummary(v))
	} else if v.PRURL != "" && v.PRState == "open" {
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Forgejo:"), forgejoSummary(v))
	}
	fmt.Fprintf(&b, "%s %s  (screen: %s)\n", labelStyle.Render("Sesión:"), v.Session, v.Screen)
	wt := v.Worktree
	if wt == "" {
		wt = "(no existe: se crea al empezar la tarea y se borra al llegar a HECHO)"
	}
	fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Worktree de la tarea:"), wt)
	if v.SlotURL != "" {
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Probar en local:"), link(v.SlotURL))
		fmt.Fprintf(&b, "%s %d\n", labelStyle.Render("Ranura de test:"), v.SlotIdx)
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Carpeta de test:"), v.SlotDir)
		for _, a := range m.accounts {
			fmt.Fprintf(&b, "  %s: %s / %s\n", a.Role, a.User, a.Password)
		}
	}
	fmt.Fprintf(&b, "%s %d\n", labelStyle.Render("Comentarios:"), v.Comments)
	if v.Last != "" {
		fmt.Fprintf(&b, "%s %s\n", labelStyle.Render("Último:"), truncate(strings.ReplaceAll(v.Last, "\n", " "), width-10))
	}
	b.WriteString("\n" + sectionStyle.Render(headline) + "\n")
	for i, s := range steps {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
	}
	lines := strings.Split(b.String(), "\n")
	start := min(m.detailScroll, max(0, len(lines)-rows))
	end := min(len(lines), start+rows)
	return strings.Join(lines[start:end], "\n")
}

// progressBar dibuja el avance de la tarea por el pipeline: segmentos llenos hasta su etapa.
func progressBar(state string, width int) string {
	stage := map[string]int{"draft": 0, "todo": 1, "in_progress": 2, "review": 3, "rework": 2, "approved": 4, "done": 5}[state]
	n := len(stages)
	barW := max(n, width-14) // dejamos sitio para la etiqueta " 4/6 Revisión"
	seg := max(1, barW/n)
	var b strings.Builder
	for i := 0; i < n; i++ {
		color := lipgloss.Color("240")
		if i <= stage {
			color = lipgloss.Color("42")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", seg)))
	}
	label := fmt.Sprintf(" %d/%d %s", stage+1, n, stages[stage])
	if state == "rework" {
		label = " REWORK"
	}
	return b.String() + dimStyle.Render(label)
}

func refOf(v workflow.TaskView) string {
	if v.Task.Identifier != "" {
		return v.Task.Identifier
	}
	return fmt.Sprintf("#%d", v.Task.ID)
}

func truncate(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// link envuelve un texto en un hipervínculo OSC 8: en terminales compatibles se puede pulsar.
func link(url string) string {
	if url == "" {
		return ""
	}
	return "\x1b]8;;" + url + "\x1b\\" + url + "\x1b]8;;\x1b\\"
}

func padRight(s string, n int) string {
	if l := lipgloss.Width(s); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}

// statusBar es la línea de estado: webhook (último aviso y contador) y actividad del bucle.
func (m Model) statusBar() string {
	wh := "webhook: " + m.webhookInfo
	if m.eventCount > 0 {
		wh = fmt.Sprintf("webhook: %s · último %s hace %s · %d avisos", m.lastEvent, m.lastEventAt.Format("15:04:05"), ago(m.lastEventAt), m.eventCount)
	}
	bucle := "bucle: " + m.lastLog
	if m.lastLog == "" {
		bucle = "bucle: esperando"
	}
	if m.loopDone {
		bucle = "bucle: detenido"
	}
	fase := "fase: inactiva"
	switch {
	case m.phase != "":
		fase = fmt.Sprintf("%s fase: %s (%s)", spinFrames[m.spin], m.phase, roundedSince(m.phaseAt))
	case m.busy:
		fase = fmt.Sprintf("%s fase: despliegue manual en curso", spinFrames[m.spin])
	}
	desplegada := "local: ninguna"
	if parts := m.slotSummary(); parts != "" {
		desplegada = "local: " + parts
	}
	line1 := dimStyle.Render(fase + "  │  " + desplegada)
	line2 := dimStyle.Render(wh + "  │  " + bucle)
	// MaxWidth recorta respetando las secuencias de color, así que la línea nunca se sale de la pantalla.
	clip := lipgloss.NewStyle().MaxWidth(m.width)
	return clip.Render(line1) + "\n" + clip.Render(line2)
}

// isActive indica si la tarea está siendo procesada o desplegada ahora (muestra el spinner).
func (m Model) isActive(v workflow.TaskView) bool {
	if v.State == "done" {
		return false
	}
	ref := refOf(v)
	if m.busy && ref == m.busyRef {
		return true
	}
	return m.phase != "" && mentions(m.phase, ref)
}

// roundedSince devuelve el tiempo transcurrido desde t, redondeado al segundo.
func roundedSince(t time.Time) string {
	return time.Since(t).Round(time.Second).String()
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return d.Round(time.Minute).String()
}

// RunOptions configura `factory start`: TUI, bucle y receptor de webhooks en el mismo proceso.
type RunOptions struct {
	Interval time.Duration
	Listen   string // dirección del webhook; vacía para desactivarlo
	Token    string // token del webhook
}

// Run abre el TUI y ejecuta el bucle de la factory y el receptor de webhooks. Al cerrar el TUI se paran.
func Run(cfg *config.Config, opts RunOptions) error {
	model := New(cfg)
	model.accounts = cfg.TestAccounts()
	model.webhookInfo = "desactivado"
	if opts.Listen != "" {
		model.webhookInfo = "sin token (ejecuta factory webhook install)"
		if opts.Token != "" {
			model.webhookInfo = "escuchando en " + opts.Listen
		}
	}
	var p *tea.Program
	logf := fileLogger(cfg, func(line string) { p.Send(LogMsg(line)) })
	model.deployFn = func(ref string) tea.Cmd {
		return func() tea.Msg {
			err := workflow.DeployTask(cfg, ref, logf)
			return DeployDoneMsg{Ref: ref, Err: err}
		}
	}
	p = tea.NewProgram(model, tea.WithAltScreen())
	workflow.SetPhase(func(s string) { p.Send(PhaseMsg(s)) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wake := make(chan struct{}, 1)

	if opts.Listen != "" && opts.Token != "" {
		go func() {
			if err := webhook.Serve(opts.Listen, opts.Token, wake, func(ev string) {
				p.Send(WebhookMsg{Event: ev, At: time.Now()})
			}, logf); err != nil {
				p.Send(WebhookInfoMsg("error: " + err.Error()))
			}
		}()
	}
	go func() {
		err := workflow.Start(ctx, cfg, workflow.StartOptions{Interval: opts.Interval, Wake: wake, Log: logf})
		p.Send(LoopDoneMsg{Err: err})
	}()

	_, err := p.Run()
	cancel()
	return err
}

// fileLogger escribe cada línea con la hora en factory.log (en el estado de la factory) y la pasa a emit.
func fileLogger(cfg *config.Config, emit func(string)) func(string, ...any) {
	path := filepath.Join(cfg.StateDir(), "factory.log")
	return func(format string, args ...any) {
		line := fmt.Sprintf("%s %s", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
		_ = os.MkdirAll(cfg.StateDir(), 0o755)
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, line)
			f.Close()
		}
		if emit != nil {
			emit(line)
		}
	}
}

// ciSummary describe el estado de los checks del PR, según GitHub.
func ciSummary(v workflow.TaskView) string {
	if v.CIErr != "" {
		return errStyle.Render("no disponible: " + truncate(v.CIErr, 60))
	}
	if v.CI == nil {
		return dimStyle.Render("sin datos")
	}
	ok := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	var s string
	switch v.CI.MergeState {
	case "clean":
		s = ok.Render("✅ OK")
	case "unstable":
		s = spinnerStyle.Render("⏳ checks en curso o con fallos")
	case "blocked":
		s = errStyle.Render("⛔ bloqueado (checks requeridos o revisión pendiente)")
	case "behind":
		s = errStyle.Render("⚠ desactualizado respecto a master")
	case "dirty":
		s = errStyle.Render("✖ conflictos")
	case "draft":
		s = dimStyle.Render("borrador")
	default:
		s = spinnerStyle.Render("⏳ calculando")
	}
	if v.CI.ActionsRuns > 0 {
		s += dimStyle.Render(fmt.Sprintf("  (Actions: %d runs)", v.CI.ActionsRuns))
	}
	return s
}

// prStateLabel muestra el estado del PR junto a su enlace.
func prStateLabel(state string) string {
	switch state {
	case "merged":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true).Render("[MERGED]")
	case "closed":
		return errStyle.Render("[CERRADO]")
	case "open":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("[ABIERTO]")
	}
	return ""
}

// mentions indica si el texto menciona la referencia como palabra completa (ACA-1 no está en ACA-11).
func mentions(text, ref string) bool {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(ref) + `($|[^0-9])`)
	return re.MatchString(text)
}

// forgejoSummary describe los runs de Forgejo Actions del último commit del PR.
func forgejoSummary(v workflow.TaskView) string {
	if v.ForgejoErr != "" {
		return errStyle.Render("no disponible: "+truncate(v.ForgejoErr, 60)) + dimStyle.Render(" (Forgejo Actions)")
	}
	if v.Forgejo == nil {
		return dimStyle.Render("sin datos (Forgejo Actions)")
	}
	sum := v.Forgejo
	src := dimStyle.Render("(Forgejo Actions)")
	if len(sum.Runs) == 0 {
		return spinnerStyle.Render("⏳ esperando el run de Forgejo para este commit (puede tardar unos minutos)") + " " + src
	}
	last := sum.Runs[0]
	var s string
	switch sum.State() {
	case "running":
		s = spinnerStyle.Render(fmt.Sprintf("⏳ build en curso: run #%d (%s)", last.Index, last.Workflow))
	case "failure":
		s = errStyle.Render(fmt.Sprintf("❌ ERROR: run #%d (%s)", last.Index, last.Workflow))
	default:
		s = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(fmt.Sprintf("✅ OK: run #%d (%s)", last.Index, last.Workflow))
	}
	return s + " " + link(last.URL) + " " + src
}

// commentLines construye las líneas de la vista de comentarios: el más nuevo primero, con el texto ajustado al ancho.
func (m Model) commentLines(width int) []string {
	if len(m.views) == 0 {
		return nil
	}
	v := m.views[m.cursor]
	var lines []string

	// 1) Contenido de la tarea (descripción).
	desc := runner.PlainText(v.Task.Description)
	if desc == "" {
		lines = append(lines, dimStyle.Render("  (sin descripción)"))
	} else {
		for _, l := range wrap(desc, width-2) {
			lines = append(lines, "  "+l)
		}
	}

	// 2) Separador y comentarios, del más nuevo al más viejo.
	lines = append(lines, "", sectionStyle.Render(fmt.Sprintf("──── Comentarios (%d) ────", len(v.AllComments))))
	if len(v.AllComments) == 0 {
		lines = append(lines, dimStyle.Render("Sin comentarios."))
		return lines
	}
	for i := len(v.AllComments) - 1; i >= 0; i-- {
		c := v.AllComments[i]
		lines = append(lines, labelStyle.Render(c.At.In(time.Local).Format("2006-01-02 15:04"))+"  "+dimStyle.Render(c.Author))
		for _, l := range wrap(c.Text, width-2) {
			lines = append(lines, "  "+l)
		}
		lines = append(lines, "")
	}
	return lines
}

// renderComments muestra el contenido de la tarea con scroll y una barra a la derecha que indica la posición.
func (m Model) renderComments(rows, width int) string {
	lines := m.commentLines(width - 2) // dos columnas reservadas: texto y barra
	total := len(lines)
	start := min(m.commentsScroll, max(0, total-rows))
	end := min(total, start+rows)
	// Tamaño y posición del bloque de la barra (proporcionales a lo visible frente al total).
	thumb := rows
	top := 0
	if total > rows {
		thumb = max(1, rows*rows/total)
		top = start * (rows - thumb) / (total - rows)
	}
	var out []string
	for i := 0; i < rows; i++ {
		l := ""
		if start+i < end {
			l = lines[start+i]
		}
		bar := " "
		if total > rows {
			if i >= top && i < top+thumb {
				bar = lipgloss.NewStyle().Foreground(focusColor).Render("█")
			} else {
				bar = dimStyle.Render("░")
			}
		}
		out = append(out, padRight(lipgloss.NewStyle().MaxWidth(width-2).Render(l), width-1)+bar)
	}
	return strings.Join(out, "\n")
}

// wrap parte un texto en líneas de como mucho n caracteres, respetando saltos de línea.
func wrap(text string, n int) []string {
	if n < 10 {
		n = 10
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		r := []rune(para)
		if len(r) == 0 {
			out = append(out, "")
			continue
		}
		for len(r) > n {
			cut := n
			for i := n; i > n/2; i-- {
				if r[i] == ' ' {
					cut = i
					break
				}
			}
			out = append(out, string(r[:cut]))
			r = []rune(strings.TrimLeft(string(r[cut:]), " "))
		}
		out = append(out, string(r))
	}
	return out
}

// renderPalette muestra las 256 combinaciones de la paleta básica (fondo × texto) y los estilos del footer.
func renderPalette() string {
	var b strings.Builder
	b.WriteString(dimStyle.Render("fila = fondo, columna = texto. Cada celda muestra el código del texto.") + "\n")
	b.WriteString("     ")
	for fg := 0; fg < 16; fg++ {
		b.WriteString(fmt.Sprintf("%3d ", fg))
	}
	b.WriteString("\n")
	for bg := 0; bg < 16; bg++ {
		b.WriteString(fmt.Sprintf("%3d  ", bg))
		for fg := 0; fg < 16; fg++ {
			cell := lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprint(fg))).Background(lipgloss.Color(fmt.Sprint(bg))).Render(fmt.Sprintf("%3d", fg))
			b.WriteString(cell + " ")
		}
		b.WriteString("\n")
	}
	b.WriteString("\nEstilos actuales del footer:\n")
	b.WriteString("  tecla: " + footerKeyStyle.Render(" c ") + "   acción: " + footerStyle.Render(" comentarios ") + "\n")
	return b.String()
}

// slotSummary lista las tareas desplegadas en ranuras, con su número de ranura.
func (m Model) slotSummary() string {
	var parts []string
	for _, v := range m.views {
		if v.SlotURL != "" {
			parts = append(parts, fmt.Sprintf("%s (ranura %d)", refOf(v), v.SlotIdx))
		}
	}
	return strings.Join(parts, " · ")
}

// maxCommentsScroll es el scroll máximo de la vista de contenido: cuando la última línea llega al final de la ventana.
func (m Model) maxCommentsScroll() int {
	rows := m.height - 6 // cabecera, dos líneas de estado, footer y bordes del panel
	return max(0, len(m.commentLines(m.width-4))-rows)
}

// renderHelp resume el flujo: cada paso con 🤖 (automático) o ✋ (manual).
func renderHelp() string {
	const (
		bot  = "🤖"
		hand = "✋"
	)
	steps := []struct{ from, to, who, text string }{
		{"📝 DRAFT", "📥 TODO", hand, "tú escribes la tarea y la pasas a TODO"},
		{"📥 TODO", "🤖 EN CURSO", bot, "la factory coge la más antigua"},
		{"🤖 EN CURSO", "👀 REVISIÓN", bot, "Claude programa, hace PR, build local y espera al CI"},
		{"", "", hand, "mientras, puedes hablar con Claude desde Remote Control (factory ACA-n)"},
		{"👀 REVISIÓN", "🔁 REWORK", bot, "CI rojo o comentarios de code review en GitHub"},
		{"👀 REVISIÓN", "🔁 REWORK", hand, "escribes en Vikunja y mueves la tarea"},
		{"🔁 REWORK", "🤖 EN CURSO", bot, "retoma la misma sesión con el feedback"},
		{"👀 REVISIÓN", "👍 APROBADO", hand, "haces merge en GitHub (o mueves con el PR abierto)"},
		{"👍 APROBADO", "✅ HECHO", bot, "tag y build/run en una ranura de test"},
		{"👍 APROBADO", "✅ HECHO", hand, "pasas la tarea a HECHO cuando esté bien"},
		{"✅ HECHO", "", bot, "libera la ranura y cierra la sesión de Claude"},
	}
	var b strings.Builder
	prev := ""
	for _, s := range steps {
		if prev != "" && s.who != prev {
			b.WriteString("\n")
		}
		prev = s.who
		flow := ""
		if s.from != "" {
			flow = stripState(s.from) + " → " + stripState(s.to)
		}
		fmt.Fprintf(&b, "%s %-28s %s\n", s.who, flow, s.text)
	}
	b.WriteString("\n" + dimStyle.Render("d desplegar · c contenido · l log · t colores"))
	return b.String()
}

// stripState quita el icono de la columna del nombre del estado (solo se usa en la ayuda).
func stripState(s string) string {
	f := strings.Fields(s)
	if len(f) < 2 {
		return strings.Join(f, " ")
	}
	return strings.Join(f[1:], " ")
}
