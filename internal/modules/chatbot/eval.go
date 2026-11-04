package chatbot

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// The eval (K6): cases run through the same Parse and card as a real message, against the real
// model, printing what was sent, what came back and whether the checks hold. Run only on the
// owner's order (it spends API money): `./server chatbot-try`.

//go:embed evalcases.txt
var evalCasesFile string

type EvalCase struct {
	ID     int
	Group  string
	Text   string
	Checks string
}

// EvalCases reads the embedded file, keeping the groups / ids asked for (none = all).
func EvalCases(groups []string, ids []int) []EvalCase {
	var out []EvalCase
	group, id := "", 0
	for _, line := range strings.Split(strings.ReplaceAll(evalCasesFile, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			group = strings.Trim(line, "[]")
			continue
		}
		id++
		text, checks := line, "" // the last "|" splits: a message may hold "|" itself
		if i := strings.LastIndex(line, "|"); i >= 0 {
			text, checks = line[:i], line[i+1:]
		}
		c := EvalCase{ID: id, Group: group, Text: strings.ReplaceAll(strings.TrimSpace(text), `\n`, "\n"),
			Checks: strings.TrimSpace(checks)}
		if (len(groups) == 0 || containsFold(groups, group)) && (len(ids) == 0 || containsInt(ids, id)) {
			out = append(out, c)
		}
	}
	return out
}

// RunEval runs the cases and prints each one, then a summary. It returns how many failed a check.
func RunEval(ctx context.Context, c llm.Client, lists Lists, lang string, today time.Time, cases []EvalCase,
	timeout time.Duration, w io.Writer) int {
	failed, totalMS := 0, int64(0)
	var usage llm.Usage
	for _, ec := range cases {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		out, err := Parse(cctx, c, today, lists, ec.Text, nil)
		ms := time.Since(start).Milliseconds()
		cancel()
		totalMS += ms
		usage.In += out.Usage.In
		usage.Out += out.Usage.Out
		usage.CacheRead += out.Usage.CacheRead
		usage.CacheWrite += out.Usage.CacheWrite

		fmt.Fprintf(w, "\n#%d [%s] %s\n", ec.ID, ec.Group, strings.ReplaceAll(ec.Text, "\n", " / "))
		fmt.Fprintf(w, "  received: %s\n", oneLine(out.Raw))
		var problems []string
		switch {
		case err != nil:
			fmt.Fprintf(w, "  error:    %s %v\n", llm.ErrorCode(err), err)
			problems = []string{"call failed"}
		default:
			if len(out.Items) > 0 {
				card := cardText(Card{Items: out.Items, Lang: lang}, lists, today)
				fmt.Fprintf(w, "  card:     %s\n", strings.ReplaceAll(card, "\n", "\n            "))
			} else {
				fmt.Fprintf(w, "  answer:   hint=%s fail=%s\n", out.Hint, out.Fail)
			}
			problems = checkCase(ec.Checks, out, lists, today)
		}
		mark := "✓"
		if len(problems) > 0 {
			mark, failed = "✗ "+strings.Join(problems, "; "), failed+1
		} else if ec.Checks == "" {
			mark = "· (no checks, look at it)"
		}
		fmt.Fprintf(w, "  check:    %s\n  %d ms | in %d · out %d · cache read %d · write %d\n", mark, ms,
			out.Usage.In, out.Usage.Out, out.Usage.CacheRead, out.Usage.CacheWrite)
	}
	fmt.Fprintf(w, "\n%d cases | %d failed | %d ms total | in %d · out %d · cache read %d · write %d\n",
		len(cases), failed, totalMS, usage.In, usage.Out, usage.CacheRead, usage.CacheWrite)
	return failed
}

// checkCase returns what does not hold ("" checks = nothing to check).
func checkCase(checks string, out Outcome, lists Lists, today time.Time) []string {
	var bad []string
	for _, ch := range strings.Split(checks, ";") {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		idx := 0
		if n, rest, ok := strings.Cut(ch, ":"); ok {
			if i, err := strconv.Atoi(n); err == nil {
				idx, ch = i-1, rest
			}
		}
		negate := strings.Contains(ch, "!=")
		key, want, hasEq := strings.Cut(strings.Replace(ch, "!=", "=", 1), "=")
		if !hasEq { // "expense 50"
			typ, amt, _ := strings.Cut(ch, " ")
			if idx >= len(out.Items) {
				bad = append(bad, fmt.Sprintf("item %d missing", idx+1))
				continue
			}
			it := out.Items[idx]
			a, _ := strconv.ParseFloat(strings.TrimSpace(amt), 64)
			if it.Type != typ || it.Amount != a {
				bad = append(bad, fmt.Sprintf("%s: got %s %s", ch, it.Type, money(it.Amount)))
			}
			continue
		}
		var got string
		switch key {
		case "items":
			got = strconv.Itoa(len(out.Items))
		case "hint":
			got = out.Hint
			if len(out.Items) > 0 || out.Fail != "" {
				got = "(none)"
			}
		case "fail":
			got = out.Fail
		default:
			if idx >= len(out.Items) {
				bad = append(bad, fmt.Sprintf("item %d missing", idx+1))
				continue
			}
			it := out.Items[idx]
			switch key {
			case "wallet":
				got = nameOrDash(walletName(textsEN, lists, it.AccountID), it.AccountID == nil)
			case "to":
				got = nameOrDash(walletName(textsEN, lists, it.TransferToID), it.TransferToID == nil)
			case "desc":
				got = "-"
				if it.Description != nil {
					got = *it.Description
				}
			case "category":
				got = "-"
				for _, c := range lists.Categories {
					if it.CategoryID != nil && c.ID == *it.CategoryID {
						got = c.Name
					}
				}
			case "date":
				got = it.Date
				switch want {
				case "today":
					want = today.Format(time.DateOnly)
				case "yesterday":
					want = today.AddDate(0, 0, -1).Format(time.DateOnly)
				}
			default:
				bad = append(bad, "unknown check "+key)
				continue
			}
		}
		if strings.EqualFold(got, strings.TrimSpace(want)) == negate {
			bad = append(bad, fmt.Sprintf("%s: got %s", ch, got))
		}
	}
	return bad
}

func nameOrDash(name string, none bool) string {
	if none {
		return "-"
	}
	return name
}

func oneLine(s string) string {
	if s == "" {
		return "(nothing)"
	}
	return strings.Join(strings.Fields(s), " ")
}

func containsFold(xs []string, x string) bool {
	for _, v := range xs {
		if strings.EqualFold(strings.TrimSpace(v), x) {
			return true
		}
	}
	return false
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
