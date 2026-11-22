package notify

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Change describes a rebuilt rule set whose content changed.
type Change struct {
	Name          string
	Files         []string // published file names, e.g. trigger.mrs
	Trigger       string
	Added         int
	Removed       int
	Total         int
	SampleAdded   []string
	SampleRemoved []string
	Warning       string
}

const sampleLines = 10

// ChangeMessage renders a change notice as Telegram HTML.
func ChangeMessage(c Change) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>: %s, теперь %s %s\n", esc(c.Name), delta(c.Added, c.Removed), num(c.Total), plural(c.Total, "правило", "правила", "правил"))
	if len(c.Files) > 0 {
		b.WriteString("Файл ")
		for i, f := range c.Files {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("<code>" + esc(f) + "</code>")
		}
		b.WriteByte('\n')
	}
	switch c.Trigger {
	case "schedule":
		b.WriteString("Обновление по расписанию\n")
	case "manual":
		b.WriteString("Обновлено вручную из панели\n")
	}
	list(&b, "Новые", c.SampleAdded, c.Added)
	list(&b, "Убраны", c.SampleRemoved, c.Removed)
	if c.Warning != "" {
		b.WriteString("\nПредупреждение: " + esc(c.Warning) + "\n")
	}
	return clip(b.String())
}

// TestMessage confirms the bot can reach an admin.
func TestMessage(panel string) string {
	return "<b>MRSmith</b> на связи. Сюда будут приходить новые и убранные правила, когда наборы обновятся из источников." +
		"\n\nПроверка отправлена из панели " + esc(panel) + "."
}

func list(b *strings.Builder, title string, sample []string, total int) {
	if total == 0 {
		return
	}
	b.WriteString("\n" + title + ":\n")
	shown := 0
	for _, s := range sample {
		if shown == sampleLines {
			break
		}
		if utf8.RuneCountInString(s) > 120 {
			s = string([]rune(s)[:119]) + "…"
		}
		b.WriteString("<code>" + esc(s) + "</code>\n")
		shown++
	}
	if rest := total - shown; rest > 0 {
		fmt.Fprintf(b, "и ещё %s\n", num(rest))
	}
}

func delta(added, removed int) string {
	switch {
	case added > 0 && removed > 0:
		return "+" + num(added) + " и −" + num(removed)
	case removed > 0:
		return "−" + num(removed)
	default:
		return "+" + num(added)
	}
}

// clip keeps a message under Telegram's limit without cutting a tag.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxText {
		return s
	}
	lines := strings.Split(s, "\n")
	var b strings.Builder
	for _, l := range lines {
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(l)+2 > maxText-20 {
			b.WriteString("…\n")
			break
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

func esc(s string) string { return html.EscapeString(s) }

// num groups thousands with a narrow no-break space: 61 674.
func num(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	r := strings.Join(out, " ")
	if neg {
		r = "-" + r
	}
	return r
}

func plural(n int, one, few, many string) string {
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}
