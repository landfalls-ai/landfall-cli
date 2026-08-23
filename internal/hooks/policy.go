// policy.go — the visible local allow-list that decides whether a command the
// engineer is about to run counts as "touching production", and — this is the
// part that matters — what may be said about it (#233, story #192). A Go port of
// `src/hooks/policy.mjs`.
//
// ── The property this file exists to guarantee ────────────────────────────
// Story #192's hard constraint is that a *classified intent* leaves the machine,
// never a raw command line. #231 enforces that at the server's edge with a
// `.strict()` schema whose `entityHints` cannot hold a space. This file enforces
// something stronger, one layer earlier:
//
//	NOTHING DERIVED FROM THE COMMAND LINE IS EVER PUT INTO THE PAYLOAD.
//
// A rule's Category and EntityHints are literals the engineer typed into the
// policy file. The command line is only ever an input to a boolean — does this
// rule match, yes or no. There is no extraction step, no tokenizer, no "pull the
// namespace out of the -n flag". That is what makes the acceptance criterion
// ("the engineer can read exactly what leaves the machine") literally true
// rather than approximately true: the set of things that can leave is the set of
// strings visible in that file, and it can be read in full with `landfall hooks
// policy`.
//
// IntentFor takes NO command-line parameter, and that is the structural half of
// the guarantee: a function that cannot see the command line cannot leak it. Do
// not add one.
//
// ── Matching is literal, never heuristic ──────────────────────────────────
// #192 says "explicit allow-list of prod-identifying patterns (not heuristics)"
// and #233 repeats it. So a rule matches on:
//
//	Command  the program name, compared exactly against the first token of the
//	         command line (basename, so `/usr/local/bin/kubectl` still matches
//	         `kubectl`)
//	AllOf    literal substrings that must ALL appear
//	NoneOf   literal substrings that must NONE appear
//
// No regular expressions — not as a style preference but because a regex in a
// user-editable file that runs on every Bash call is both an unreviewable
// heuristic and a denial-of-service surface against the engineer's own shell.
//
// A rule that fails validation is SKIPPED and reported, never silently repaired:
// a policy file the engineer believes says one thing while it does another is
// the failure mode this whole design is built to avoid.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/install"
)

// IntentCategories is the classification vocabulary, mirroring
// `INTENT_CATEGORIES` in the server's shared contracts package
// (`edge-intent.ts`). It is duplicated rather than shared because this repo is
// deliberately standalone (feature 050) and depends on nothing in the monorepo —
// but it is a CLOSED list at both ends, so a rule naming a category the server
// would reject is caught here, on the engineer's machine, instead of as a 400 in
// the middle of an incident.
//
// A function, not a package variable, so no importer can rewrite it.
func IntentCategories() []string {
	return []string{"kubernetes", "cloud", "database", "logs", "deployment", "network", "other"}
}

// EntityHintPattern is the same bound the server's `EntityHintSchema` applies:
// start alphanumeric, then identifier punctuation only — no whitespace, no shell
// metacharacter. Checked here so an unsendable hint is a policy-file error the
// engineer sees at rest, not a rejected request during an outage.
var EntityHintPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

// MaxEntityHints — the server caps the list at 10; a longer one is a dump, not a
// classification.
const MaxEntityHints = 10

// PolicyFileName is the allow-list's basename.
const PolicyFileName = "prod-policy.json"

// PolicyPath is where the policy file lives.
//
// Beside the cached session (credentials.json) under the XDG config home,
// because it is the same kind of thing: per-user local state for this CLI. It is
// NOT in the repo being worked on — a policy that travelled with a checkout
// would let a cloned repository decide what an engineer's machine reports.
func PolicyPath() string {
	return filepath.Join(install.XDGConfigHome(), "landfall", PolicyFileName)
}

// Rule is one validated allow-list entry. AllOf/NoneOf/EntityHints are never
// nil after validation — an absent list is an EMPTY list, so a caller never has
// to distinguish the two.
type Rule struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Command     string   `json:"command"`
	AllOf       []string `json:"allOf"`
	NoneOf      []string `json:"noneOf"`
	Category    string   `json:"category"`
	EntityHints []string `json:"entityHints"`
}

// Policy is the result of reading the policy file.
//
// Errors carries every rejected rule so the caller can print them; the surviving
// rules are still usable, because one bad rule should not disarm the others.
type Policy struct {
	Exists bool
	Rules  []Rule
	Errors []string
}

// StarterPolicy is the commented starter policy `landfall hooks policy --init`
// writes. Ordered fields, because this file is meant to be read by a human.
type StarterPolicyFile struct {
	Comment string       `json:"$comment"`
	Version int          `json:"version"`
	Rules   []PolicyRule `json:"rules"`
}

// PolicyRule is the on-disk shape of one rule. Separate from Rule because the
// file omits what it does not set, while a validated Rule never does.
type PolicyRule struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Command     string   `json:"command"`
	AllOf       []string `json:"allOf,omitempty"`
	NoneOf      []string `json:"noneOf,omitempty"`
	Category    string   `json:"category"`
	EntityHints []string `json:"entityHints,omitempty"`
}

// StarterPolicy is the documented starter file's content.
func StarterPolicy() StarterPolicyFile {
	return StarterPolicyFile{
		Comment: "Landfall prod-investigation allow-list. A command matches only if `command` " +
			"equals its program name and every string in `allOf` appears in it. ONLY the " +
			"`category` and `entityHints` written here are ever sent — never the command line. " +
			"Run `landfall hooks policy` to print exactly what each rule would send.",
		Version: 1,
		Rules: []PolicyRule{{
			ID:          "example-kubectl-prod-context",
			Description: "kubectl aimed at the production cluster",
			Command:     "kubectl",
			AllOf:       []string{"--context=prod"},
			Category:    "kubernetes",
			EntityHints: []string{"prod-cluster"},
		}},
	}
}

// StarterPolicyJSON is StarterPolicy rendered exactly as `--init` writes it:
// two-space indent, one trailing newline, HTML escaping OFF (the `$comment`
// carries backticks and an em dash, and a Go default encoder would mangle any
// `<`/`>`/`&` a future edit adds).
func StarterPolicyJSON() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// The value is a plain struct of strings; an encode failure is unreachable.
	_ = enc.Encode(StarterPolicy())
	return buf.Bytes()
}

// ValidateRule validates one raw rule. Returns the rule, or an error string —
// never a partially corrected rule.
func ValidateRule(raw map[string]any, index int) (*Rule, string) {
	where := ruleLabel(raw, index)
	if raw == nil {
		return nil, where + ": not an object"
	}
	id, ok := nonEmptyString(raw["id"])
	if !ok {
		return nil, where + `: missing "id"`
	}
	command, ok := nonEmptyString(raw["command"])
	if !ok {
		return nil, where + `: missing "command" (the program name to match)`
	}
	category, _ := raw["category"].(string)
	if !containsString(IntentCategories(), category) {
		return nil, where + `: "category" must be one of ` + strings.Join(IntentCategories(), ", ")
	}

	allOf, ok := literalList(raw["allOf"])
	if !ok {
		return nil, where + `: "allOf" must be a list of non-empty strings`
	}
	noneOf, ok := literalList(raw["noneOf"])
	if !ok {
		return nil, where + `: "noneOf" must be a list of non-empty strings`
	}

	var hints []any
	switch v := raw["entityHints"].(type) {
	case nil:
		hints = nil
	case []any:
		hints = v
	default:
		return nil, where + `: "entityHints" must be a list`
	}
	if len(hints) > MaxEntityHints {
		return nil, fmt.Sprintf("%s: at most %d entity hints (found %d)", where, MaxEntityHints, len(hints))
	}
	entityHints := make([]string, 0, len(hints))
	for _, h := range hints {
		s, isString := h.(string)
		if !isString || !EntityHintPattern.MatchString(s) {
			return nil, where + ": entity hint " + jsonLiteral(h) +
				" is not a bare identifier — no whitespace, no shell metacharacters"
		}
		entityHints = append(entityHints, s)
	}

	description, _ := raw["description"].(string)
	return &Rule{
		ID:          id,
		Description: description,
		Command:     command,
		AllOf:       allOf,
		NoneOf:      noneOf,
		Category:    category,
		EntityHints: entityHints,
	}, ""
}

// ruleLabel is “rule ${raw?.id ? `"${raw.id}"` : `#${index + 1}`}“ — the id
// when it is TRUTHY (so an empty-string id still falls back to the position),
// stringified the way a JS template would.
func ruleLabel(raw map[string]any, index int) string {
	if raw != nil {
		if s, ok := truthyString(raw["id"]); ok {
			return `rule "` + s + `"`
		}
	}
	return fmt.Sprintf("rule #%d", index+1)
}

// truthyString renders a JS-truthy value the way `${value}` would.
func truthyString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return "", false
		}
		return t, true
	case float64:
		if t == 0 {
			return "", false
		}
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", t), "0"), "."), true
	case bool:
		if !t {
			return "", false
		}
		return "true", true
	case nil:
		return "", false
	default:
		return fmt.Sprint(t), true
	}
}

func nonEmptyString(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

// literalList is `literals(value)`: undefined → [], a list of non-empty strings
// → itself, anything else → refused.
func literalList(v any) ([]string, bool) {
	if v == nil {
		return []string{}, true
	}
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, isString := item.(string)
		if !isString || s == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// jsonLiteral is `JSON.stringify(value)`, used to quote an offending hint back
// at the engineer exactly as they wrote it.
func jsonLiteral(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%q", fmt.Sprint(v))
	}
	return strings.TrimRight(buf.String(), "\n")
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// LoadPolicy reads and validates the policy file.
//
// A missing file is NOT an error: it is the shipped state, and it means this
// feature does nothing at all.
func LoadPolicy(filePath string) Policy {
	text, err := os.ReadFile(filePath) //nolint:gosec // the path is this CLI's own config location
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Policy{Exists: false, Rules: []Rule{}, Errors: []string{}}
		}
		return Policy{Exists: true, Rules: []Rule{}, Errors: []string{
			"cannot read " + filePath + ": " + err.Error(),
		}}
	}

	var parsed map[string]any
	if err := json.Unmarshal(text, &parsed); err != nil {
		return Policy{Exists: true, Rules: []Rule{}, Errors: []string{
			filePath + " is not valid JSON: " + err.Error(),
		}}
	}
	rawRules, ok := parsed["rules"].([]any)
	if !ok {
		return Policy{Exists: true, Rules: []Rule{}, Errors: []string{
			filePath + `: expected a "rules" array`,
		}}
	}

	rules := make([]Rule, 0, len(rawRules))
	errs := []string{}
	for i, raw := range rawRules {
		var obj map[string]any
		switch v := raw.(type) {
		case map[string]any:
			obj = v
		case []any:
			// `typeof [] === 'object'` in JS, so an array entry gets past the
			// object check and is refused on the next one instead — as an empty
			// object here, which reaches the same `missing "id"` error.
			obj = map[string]any{}
		default:
			// `!raw || typeof raw !== 'object'` — a non-object entry names itself
			// by position, since it has no id to name itself by.
			errs = append(errs, fmt.Sprintf("rule #%d: not an object", i+1))
			continue
		}
		rule, ruleErr := ValidateRule(obj, i)
		if ruleErr != "" {
			errs = append(errs, ruleErr)
			continue
		}
		rules = append(rules, *rule)
	}
	return Policy{Exists: true, Rules: rules, Errors: errs}
}

// envAssignment matches a leading `NAME=value` (unquoted value only).
var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=[^\s'"]*(\s|$)`)

// ProgramName is the program name a command line invokes: basename of the
// command word.
//
// Two shapes are handled beyond "first token", both because missing them would
// make a rule quietly not fire on a command the engineer plainly meant it to:
//
//	VAR=value kubectl …       leading environment assignments are part of the
//	                          same simple command in POSIX shell grammar, and
//	                          `KUBECONFIG=… kubectl` is how people actually reach
//	                          a second cluster
//	"/opt/my tools/kubectl"   a quoted command word, taken whole
//
// Deliberately NOT handled: wrappers (`sudo kubectl`, `env kubectl`, `xargs`),
// pipelines, and `;`-separated lists. Those invoke a different program, and a
// matcher that looked "through" them would be exactly the heuristic #192 rules
// out. An engineer who runs prod commands under `sudo` writes a `sudo` rule, and
// can see that they did.
func ProgramName(commandLine string) string {
	rest := strings.TrimSpace(commandLine)
	// Skip leading NAME=value assignments (unquoted values only — a quoted value
	// may contain spaces, and guessing where it ends is parsing).
	for envAssignment.MatchString(rest) {
		cut := strings.IndexFunc(rest, isSpace)
		if cut < 0 {
			rest = ""
			break
		}
		rest = strings.TrimLeft(rest[cut:], " \t\n\v\f\r")
	}
	if rest == "" {
		return ""
	}

	var word string
	if q := rest[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			word = rest[1 : 1+end]
		} else {
			word = rest[1:]
		}
	} else {
		word = strings.FieldsFunc(rest, isSpace)[0]
	}
	if word == "" {
		return ""
	}
	parts := strings.FieldsFunc(word, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		// `"/".split(/[/\\]/)` is ["", ""], whose `.pop()` is "".
		return ""
	}
	return parts[len(parts)-1]
}

func isSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// RuleMatches reports whether rule matches commandLine. Pure, literal,
// allocation-light.
func RuleMatches(rule Rule, commandLine string) bool {
	if ProgramName(commandLine) != rule.Command {
		return false
	}
	for _, needle := range rule.AllOf {
		if !strings.Contains(commandLine, needle) {
			return false
		}
	}
	for _, needle := range rule.NoneOf {
		if strings.Contains(commandLine, needle) {
			return false
		}
	}
	return true
}

// MatchCommand is the first matching rule, in file order, or nil. File order is
// the engineer's.
func MatchCommand(rules []Rule, commandLine string) *Rule {
	for i := range rules {
		if RuleMatches(rules[i], commandLine) {
			return &rules[i]
		}
	}
	return nil
}

// Intent is the EXACT request body a matched rule produces. Three keys, in this
// order, and nothing else — the server's schema is `.strict()`.
type Intent struct {
	Category    string   `json:"category"`
	EntityHints []string `json:"entityHints"`
	StartedAt   string   `json:"startedAt"`
}

// IntentFor builds the whole payload from the rule plus a timestamp.
//
// THERE IS DELIBERATELY NO commandLine PARAMETER: this function cannot see the
// command line, so it cannot leak it, and that is checked by a test rather than
// trusted. Adding one is the single change that would break the guarantee this
// package exists for.
func IntentFor(rule Rule, startedAt time.Time) Intent {
	hints := make([]string, len(rule.EntityHints))
	copy(hints, rule.EntityHints)
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return Intent{
		Category:    rule.Category,
		EntityHints: hints,
		// JavaScript's Date#toISOString: UTC, always three fractional-second
		// digits, a literal trailing Z.
		StartedAt: startedAt.UTC().Format("2006-01-02T15:04:05.000") + "Z",
	}
}
