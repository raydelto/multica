package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// codexLunaReserveRateLimits is an account/rateLimits/read result recorded
// from codex-cli 0.159.0 (supportsLunaReserve: true) on a ChatGPT Plus account
// whose 5-hour allowance was used up. Account and credit IDs are replaced.
const codexLunaReserveRateLimits = `{"ordinaryUsageAllowed":false,` +
	`"rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1790688358},"secondary":{"usedPercent":26,"windowDurationMins":10080,"resetsAt":1791224584},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"individualLimit":null,"spendControlReached":false,"planType":"plus","rateLimitReachedType":"rate_limit_reached"},` +
	`"rateLimitsByLimitId":{` +
	`"codex":{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1790688358},"secondary":{"usedPercent":26,"windowDurationMins":10080,"resetsAt":1791224584},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"individualLimit":null,"spendControlReached":false,"planType":"plus","rateLimitReachedType":"rate_limit_reached"},` +
	`"base_model_inference":{"limitId":"base_model_inference","limitName":"gpt-reserve","normalModelSlug":"gpt-5.6-luna","primary":{"usedPercent":3,"windowDurationMins":10080,"resetsAt":1791283445},"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":"plus","rateLimitReachedType":null}},` +
	`"rateLimitResetCredits":{"availableCount":1,"credits":[{"id":"RateLimitResetCredit_test","resetType":"codexRateLimits","status":"available","grantedAt":1790102365,"expiresAt":1792694365,"title":"Full reset (Weekly + 5 hr)","description":"test"}]},` +
	`"accountId":"00000000-0000-0000-0000-000000000000",` +
	`"rateLimitUpsell":{"banner_type":"luna_reserve","title":"You’re now using Luna, a faster model for simpler tasks.","description":"Use your reset to continue using the most advanced models, or wait for usage to reset after {time}.","ctas":[{"action":"open_pricing_dialog","label":"Upgrade"},{"action":"reset_usage","label":"Reset usage"}],"presentation":"dismissible","reset_at":1790688358,"referral":null,"request_url":null}}`

const codexUsageLimitMessage = "You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at 10:25 AM."

// mutateCodexRateLimits applies edit to a decoded copy of the recorded
// payload and re-encodes it.
func mutateCodexRateLimits(t *testing.T, edit func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(codexLunaReserveRateLimits), &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func reserveBucket(m map[string]any) map[string]any {
	return m["rateLimitsByLimitId"].(map[string]any)["base_model_inference"].(map[string]any)
}

func TestCodexLunaReserveAvailable(t *testing.T) {
	cases := []struct {
		name  string
		model string
		edit  func(m map[string]any)
		want  bool
	}{
		{name: "recorded payload offers reserve", model: "gpt-5.6-luna", want: true},
		{name: "empty model with no blocked slug", model: "", want: true},
		{name: "no upsell banner", model: "gpt-5.6-luna", edit: func(m map[string]any) { m["rateLimitUpsell"] = nil }},
		{name: "other banner type", model: "gpt-5.6-luna", edit: func(m map[string]any) {
			m["rateLimitUpsell"].(map[string]any)["banner_type"] = "usage_limit"
		}},
		{name: "ordinary usage allowed", model: "gpt-5.6-luna", edit: func(m map[string]any) { m["ordinaryUsageAllowed"] = true }},
		{name: "ordinary usage unknown", model: "gpt-5.6-luna", edit: func(m map[string]any) { delete(m, "ordinaryUsageAllowed") }},
		{name: "reserve window used up", model: "gpt-5.6-luna", edit: func(m map[string]any) {
			reserveBucket(m)["primary"].(map[string]any)["usedPercent"] = 100
		}},
		{name: "reserve reports limit reached", model: "gpt-5.6-luna", edit: func(m map[string]any) {
			reserveBucket(m)["rateLimitReachedType"] = "rate_limit_reached"
		}},
		{name: "banner blocks a different model", model: "gpt-5.6-luna", edit: func(m map[string]any) {
			m["rateLimitUpsell"].(map[string]any)["blocked_model_slug"] = "gpt-5.5"
		}},
		{name: "banner blocks this model", model: "gpt-5.6-luna", want: true, edit: func(m map[string]any) {
			m["rateLimitUpsell"].(map[string]any)["blocked_model_slug"] = "gpt-5.6-luna"
		}},
		{name: "already on reserve", model: "gpt-reserve"},
		{name: "already on reserve, other casing", model: " GPT-Reserve "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := codexLunaReserveRateLimits
			if tc.edit != nil {
				raw = mutateCodexRateLimits(t, tc.edit)
			}
			got, reason := codexLunaReserveAvailable(json.RawMessage(raw), tc.model)
			if got != tc.want {
				t.Fatalf("codexLunaReserveAvailable = %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
	if ok, _ := codexLunaReserveAvailable(json.RawMessage(`not json`), "gpt-5.6-luna"); ok {
		t.Fatal("unreadable payload must not offer reserve")
	}
}

func TestCodexErrorInfoCode(t *testing.T) {
	if got := codexErrorInfoCode(map[string]any{"codexErrorInfo": "usageLimitExceeded"}); got != "usageLimitExceeded" {
		t.Fatalf("string variant = %q", got)
	}
	if got := codexErrorInfoCode(map[string]any{"codexErrorInfo": map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 502}}}); got != "httpConnectionFailed" {
		t.Fatalf("object variant = %q", got)
	}
	if got := codexErrorInfoCode(map[string]any{"codexErrorInfo": nil}); got != "" {
		t.Fatalf("null variant = %q", got)
	}
}

func TestCodexLunaReserveTurnInput(t *testing.T) {
	original := []map[string]any{{"type": "text", "text": "do the task"}}
	if got := codexLunaReserveTurnInput(original, false); len(got) != 1 || got[0]["text"] != "do the task" {
		t.Fatalf("turn without tool activity must replay the input, got %v", got)
	}
	got := codexLunaReserveTurnInput(original, true)
	if len(got) != 1 || got[0]["text"] != codexLunaReserveContinuePrompt {
		t.Fatalf("turn with tool activity must ask to continue, got %v", got)
	}
}

// writeFakeCodexLunaReserveServer writes a stateful fake app-server. Each
// turn/start consumes the next word of outcomes: "quota" fails the turn with
// usageLimitExceeded, "toolquota" runs a command first and then fails the same
// way, "ok" completes it with a final answer. Every request is
// appended to the returned request log.
func writeFakeCodexLunaReserveServer(t *testing.T, rateLimits, outcomes string) (string, string) {
	t.Helper()
	reqLog := filepath.Join(t.TempDir(), "requests.jsonl")
	quotaErr := `{"message":"` + codexUsageLimitMessage + `","codexErrorInfo":"usageLimitExceeded","additionalDetails":null}`
	body := `turns=0
while IFS= read -r line; do
  printf '%s\n' "$line" >> '` + reqLog + `'
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"jsonrpc":"2.0","id":'"$id"',"result":{}}' ;;
    *'"method":"thread/start"'*)
      echo '{"jsonrpc":"2.0","id":'"$id"',"result":{"thread":{"id":"thr","model":"gpt-5.6-luna"},"model":"gpt-5.6-luna"}}' ;;
    *'"method":"account/rateLimits/read"'*)
      echo '{"jsonrpc":"2.0","id":'"$id"',"result":` + rateLimits + `}' ;;
    *'"method":"turn/start"'*)
      turns=$((turns+1))
      outcome=$(echo '` + outcomes + `' | cut -d' ' -f"$turns")
      echo '{"jsonrpc":"2.0","id":'"$id"',"result":{"turn":{"id":"turn-'"$turns"'","status":"inProgress"}}}'
      echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr","turn":{"id":"turn-'"$turns"'","status":"inProgress"}}}'
      if [ "$outcome" = "ok" ]; then
        echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr","turnId":"turn-'"$turns"'","item":{"type":"agentMessage","id":"msg-'"$turns"'","text":"OK","phase":"final_answer"}}}'
        echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr","turn":{"id":"turn-'"$turns"'","status":"completed","error":null}}}'
      else
        if [ "$outcome" = "toolquota" ]; then
          echo '{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"thr","turnId":"turn-'"$turns"'","item":{"type":"commandExecution","id":"cmd-1","command":"ls"}}}'
          echo '{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"thr","turnId":"turn-'"$turns"'","item":{"type":"commandExecution","id":"cmd-1","command":"ls","aggregatedOutput":""}}}'
        fi
        echo '{"jsonrpc":"2.0","method":"error","params":{"threadId":"thr","turnId":"turn-'"$turns"'","willRetry":false,"error":` + quotaErr + `}}'
        echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr","turn":{"id":"turn-'"$turns"'","status":"failed","error":` + quotaErr + `}}}'
      fi ;;
    *)
      if [ -n "$id" ]; then echo '{"jsonrpc":"2.0","id":'"$id"',"result":{}}'; fi ;;
  esac
done
`
	return writeFakeCodexAppServer(t, body), reqLog
}

type codexLoggedRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func readCodexRequestLog(t *testing.T, path string) []codexLoggedRequest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reqs []codexLoggedRequest
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r codexLoggedRequest
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("parse request %q: %v", line, err)
		}
		reqs = append(reqs, r)
	}
	return reqs
}

func codexRequestsByMethod(reqs []codexLoggedRequest, method string) []codexLoggedRequest {
	var out []codexLoggedRequest
	for _, r := range reqs {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// assertCodexLunaReserveSafety checks the invariants every scenario shares:
// only the expected read-only/turn methods are called (never a reset, credit
// consumption, pricing or purchase flow), the thread is created on the
// configured model, and the reserve model appears nowhere except as the
// turn/start model of the single reserve retry.
func assertCodexLunaReserveSafety(t *testing.T, reqs []codexLoggedRequest, wantReserveTurn bool) {
	t.Helper()
	allowed := map[string]bool{
		"initialize":              true,
		"initialized":             true,
		"thread/start":            true,
		"turn/start":              true,
		"account/rateLimits/read": true,
	}
	for _, r := range reqs {
		if !allowed[r.Method] {
			t.Fatalf("unexpected app-server method %q (params %v)", r.Method, r.Params)
		}
		for _, forbidden := range []string{"reset", "credit", "consume", "pricing", "purchase", "upgrade"} {
			if strings.Contains(strings.ToLower(r.Method), forbidden) {
				t.Fatalf("method %q must never be called", r.Method)
			}
		}
	}
	starts := codexRequestsByMethod(reqs, "thread/start")
	if len(starts) != 1 || starts[0].Params["model"] != "gpt-5.6-luna" {
		t.Fatalf("thread/start must use the configured model, got %v", starts)
	}
	reserveTurns := 0
	for i, r := range reqs {
		encoded, _ := json.Marshal(r.Params)
		mentionsReserve := strings.Contains(strings.ToLower(string(encoded)), codexLunaReserveModel)
		if r.Method == "account/rateLimits/read" {
			continue
		}
		if !mentionsReserve {
			continue
		}
		if r.Method != "turn/start" || r.Params["model"] != codexLunaReserveModel {
			t.Fatalf("request %d (%s) carries the reserve model outside a reserve turn: %s", i, r.Method, encoded)
		}
		reserveTurns++
	}
	if want := map[bool]int{true: 1, false: 0}[wantReserveTurn]; reserveTurns != want {
		t.Fatalf("reserve turns = %d, want %d", reserveTurns, want)
	}
	turns := codexRequestsByMethod(reqs, "turn/start")
	if _, ok := turns[0].Params["model"]; ok {
		t.Fatalf("the first turn must keep the thread's configured model, got %v", turns[0].Params)
	}
}

func TestCodexExecuteContinuesWithLunaReserve(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath, reqLog := writeFakeCodexLunaReserveServer(t, codexLunaReserveRateLimits, "quota ok")
	result, messages := executeFakeCodexCollectingMessages(t, fakePath, ExecOptions{Model: "gpt-5.6-luna", Timeout: 5 * time.Second}, 10*time.Second)
	if result.Status != "completed" || result.Output != "OK" {
		t.Fatalf("expected the reserve turn to complete, got status=%q output=%q error=%q", result.Status, result.Output, result.Error)
	}
	reqs := readCodexRequestLog(t, reqLog)
	assertCodexLunaReserveSafety(t, reqs, true)
	turns := codexRequestsByMethod(reqs, "turn/start")
	if len(turns) != 2 {
		t.Fatalf("turn/start calls = %d, want 2", len(turns))
	}
	if first, retry := turns[0].Params["input"], turns[1].Params["input"]; !jsonEqual(t, first, retry) {
		t.Fatalf("a turn that failed before any tool ran must be replayed as-is: %v vs %v", first, retry)
	}
	reads := codexRequestsByMethod(reqs, "account/rateLimits/read")
	if len(reads) != 1 || reads[0].Params["supportsLunaReserve"] != true {
		t.Fatalf("expected one reserve-aware rate limits read, got %v", reads)
	}
	var sawNotice bool
	for _, m := range messages {
		if m.Type == MessageLog && strings.Contains(m.Content, "Luna Reserve") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Fatalf("expected a transcript notice about Luna Reserve, got %v", messages)
	}
}

func TestCodexExecuteUsageLimitWithoutReserveIsQuotaFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	noReserve := mutateCodexRateLimits(t, func(m map[string]any) { m["rateLimitUpsell"] = nil })
	fakePath, reqLog := writeFakeCodexLunaReserveServer(t, noReserve, "quota ok")
	result := executeFakeCodex(t, fakePath, ExecOptions{Model: "gpt-5.6-luna", Timeout: 5 * time.Second})
	assertCodexQuotaFailure(t, result)
	reqs := readCodexRequestLog(t, reqLog)
	assertCodexLunaReserveSafety(t, reqs, false)
	if n := len(codexRequestsByMethod(reqs, "turn/start")); n != 1 {
		t.Fatalf("turn/start calls = %d, want 1", n)
	}
}

func TestCodexExecuteLunaReserveUsedUpIsQuotaFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	usedUp := mutateCodexRateLimits(t, func(m map[string]any) {
		reserveBucket(m)["primary"].(map[string]any)["usedPercent"] = 100
	})
	fakePath, reqLog := writeFakeCodexLunaReserveServer(t, usedUp, "quota ok")
	result := executeFakeCodex(t, fakePath, ExecOptions{Model: "gpt-5.6-luna", Timeout: 5 * time.Second})
	assertCodexQuotaFailure(t, result)
	reqs := readCodexRequestLog(t, reqLog)
	assertCodexLunaReserveSafety(t, reqs, false)
	if n := len(codexRequestsByMethod(reqs, "turn/start")); n != 1 {
		t.Fatalf("turn/start calls = %d, want 1", n)
	}
}

func TestCodexExecuteLunaReserveTurnHittingLimitIsQuotaFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	// The read still offers the reserve, but the reserve turn itself is
	// refused: the adapter must stop after one retry.
	fakePath, reqLog := writeFakeCodexLunaReserveServer(t, codexLunaReserveRateLimits, "quota quota ok")
	result := executeFakeCodex(t, fakePath, ExecOptions{Model: "gpt-5.6-luna", Timeout: 5 * time.Second})
	assertCodexQuotaFailure(t, result)
	reqs := readCodexRequestLog(t, reqLog)
	assertCodexLunaReserveSafety(t, reqs, true)
	if n := len(codexRequestsByMethod(reqs, "turn/start")); n != 2 {
		t.Fatalf("turn/start calls = %d, want 2", n)
	}
	if n := len(codexRequestsByMethod(reqs, "account/rateLimits/read")); n != 1 {
		t.Fatalf("rate limits reads = %d, want 1", n)
	}
}

func TestCodexExecuteConfiguredReserveModelIsNotRetried(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	for _, model := range []string{"gpt-reserve", "GPT-Reserve", " Gpt-RESERVE "} {
		t.Run(model, func(t *testing.T) {
			fakePath, reqLog := writeFakeCodexLunaReserveServer(t, codexLunaReserveRateLimits, "quota ok")
			result := executeFakeCodex(t, fakePath, ExecOptions{Model: model, Timeout: 5 * time.Second})
			assertCodexQuotaFailure(t, result)
			reqs := readCodexRequestLog(t, reqLog)
			if starts := codexRequestsByMethod(reqs, "thread/start"); len(starts) != 1 || starts[0].Params["model"] != model {
				t.Fatalf("thread/start must carry the configured model %q unchanged, got %v", model, starts)
			}
			turns := codexRequestsByMethod(reqs, "turn/start")
			if len(turns) != 1 {
				t.Fatalf("turn/start calls = %d, want 1", len(turns))
			}
			if got, ok := turns[0].Params["model"]; ok {
				t.Fatalf("turn/start must not override the configured model, got %v", got)
			}
			if n := len(codexRequestsByMethod(reqs, "account/rateLimits/read")); n != 0 {
				t.Fatalf("rate limits reads = %d, want 0", n)
			}
		})
	}
}

func TestCodexExecuteLunaReserveContinuesAfterToolActivity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath, reqLog := writeFakeCodexLunaReserveServer(t, codexLunaReserveRateLimits, "toolquota ok")
	result := executeFakeCodex(t, fakePath, ExecOptions{Model: "gpt-5.6-luna", Timeout: 5 * time.Second})
	if result.Status != "completed" || result.Output != "OK" {
		t.Fatalf("expected the reserve turn to complete, got status=%q output=%q error=%q", result.Status, result.Output, result.Error)
	}
	reqs := readCodexRequestLog(t, reqLog)
	assertCodexLunaReserveSafety(t, reqs, true)
	turns := codexRequestsByMethod(reqs, "turn/start")
	if len(turns) != 2 {
		t.Fatalf("turn/start calls = %d, want 2", len(turns))
	}
	want := []any{map[string]any{"type": "text", "text": codexLunaReserveContinuePrompt}}
	if !jsonEqual(t, turns[1].Params["input"], want) {
		t.Fatalf("a turn that already ran tools must continue, not replay: %v", turns[1].Params["input"])
	}
}

func assertCodexQuotaFailure(t *testing.T, result Result) {
	t.Helper()
	if result.Status != "failed" {
		t.Fatalf("expected failed, got status=%q output=%q error=%q", result.Status, result.Output, result.Error)
	}
	if got := taskfailure.Classify(result.Error); got != taskfailure.ReasonAgentProviderQuotaLimit {
		t.Fatalf("error %q classified as %q, want %q", result.Error, got, taskfailure.ReasonAgentProviderQuotaLimit)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ea, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	eb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ea) == string(eb)
}
