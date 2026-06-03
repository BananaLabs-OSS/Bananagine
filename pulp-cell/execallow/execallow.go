// Package execallow is the pure-regex authorization kernel for Bananagine's
// in-container exec handler, lifted verbatim out of the pulp-cell
// POST /orchestration/servers/:id/exec handler so it can be unit-tested.
//
// The parent `package main` only builds for GOOS=wasip1 GOARCH=wasm because
// the Pulp/Fiber capabilities it imports use //go:wasmimport (no native
// function bodies). This subpackage imports ONLY regexp, so it is
// host-buildable and `go test`-able natively — the same split the sibling
// `resources` package uses.
//
// SECURITY: this gate is the RCE+self-DoS fix (audit Bananagine CRITICAL,
// commits e1d7c79/d2d7362/36d637a/5e9b4ba). If main.go's allowlist and this
// copy ever diverge, the test table here is the canonical regression pin —
// keep them byte-for-byte identical.
//
// The previous gate matched only a PREFIX of the `sh -c` string, so any string
// that began with an allowed prefix (e.g. `cat /data/x; <arbitrary>`) reached
// the container shell and ran arbitrary commands — in-container RCE. The fix is
// to match the ENTIRE command string against anchored (`^…$`) patterns that pin
// each legitimate operation's command skeleton.
//
// IMPORTANT — patterns are reconciled against the REAL Evolution caller
// (Evolution/pulp-cell: whitelist.go, sync.go, router.go, transitions.go,
// admin.go), NOT an imagined mcrcon shape. The deployed caller uses:
//   - an in-container `rcon` wrapper (argv `["rcon", <cmd>]`, and `sh -c`
//     batches like `rcon 'gamerule x true'; rcon 'save-all flush'`, plus
//     `$(rcon 'gamerule x')` gamerule reads) — NOT `mcrcon -H…`;
//   - `; `-joined batches (not `&& sleep N &&`);
//   - sed substitutions on /data/server.properties with BARE (not quoted)
//     property values that may legitimately contain `/` (e.g. MOTD "Best 24/7").
//
// Security boundary — what pins the STRUCTURE vs. what carries USER VALUES:
//   - The legit structure may use shell metacharacters (`;`, `&&`, `$()`,
//     `|`) because they are part of the FIXED, anchored skeleton, not the
//     caller's free input.
//   - Every user-influenced value (motd, seed, world name, gamerule value,
//     ops/ban JSON, URLs, datapack filenames) is confined to a span that a
//     malicious value cannot break out of.
package execallow

import "regexp"

const (
	// sqArg matches a POSIX single-quoted argument body: any run of
	// non-quote bytes, with embedded quotes written as the `'\''` splice.
	// Used for the regions where the caller injects escaped free-form values.
	sqArg = `(?:[^']|'\\'')*`
	// propVal is a BARE server.properties value as emitted by the caller's
	// sed substitution. It sits inside `sed -i 's/^KEY=.*/KEY=<propVal>/' …`,
	// i.e. wrapped in single quotes by the FIXED skeleton. The only byte that
	// could terminate that wrapper is a single quote, so propVal excludes `'`
	// (and a literal newline, which an anchored match must not span). `/` IS
	// admitted — it is harmless to the gate and is required for slash-bearing
	// MOTDs/URLs; sed-level `/` correctness is the caller's concern.
	propVal = `[^'\n]*`
	// ruleTok pins the user-influenced rule name in a `gamerule <rule>` read.
	ruleTok = `[A-Za-z0-9_]+`
)

// AllowPatterns is the exhaustive set of legitimate `sh -c` exec command
// shapes, each anchored end-to-end. Notably the sed pattern permits ONLY the
// `s/^KEY=.*/KEY=VALUE/` substitution against /data/server.properties and
// forbids GNU sed's `e`/`w`/`r` commands.
//
// MUST stay byte-for-byte identical to execAllowPatterns in ../main.go.
var AllowPatterns = []*regexp.Regexp{
	// RCON write batch (gamerules + console): one or more `rcon '<cmd>'`
	// invocations `; `-joined, each with an optional `2>/dev/null`.
	regexp.MustCompile(`^rcon '` + sqArg + `'( 2>/dev/null)?(; rcon '` + sqArg + `'( 2>/dev/null)?)*$`),
	// RCON status poll with multiple quoted args: `rcon 'tps' 'list' 2>/dev/null`.
	regexp.MustCompile(`^rcon( '` + sqArg + `')+( 2>/dev/null)?$`),
	// gamerule read loop: echo "RULE:<rule>:$(rcon 'gamerule <rule>' 2>/dev/null)" lines.
	regexp.MustCompile(`^(echo "RULE:` + ruleTok + `:\$\(rcon 'gamerule ` + ruleTok + `' 2>/dev/null\)"\n?)+$`),
	// Read JSON-or-empty: (test -f /data/F && cat /data/F) || echo '[]'.
	regexp.MustCompile(`^\(test -f /data/[A-Za-z0-9._-]+ && cat /data/[A-Za-z0-9._-]+\) \|\| echo '\[\]'$`),
	// Read a /data file, empty on absence: cat /data/F 2>/dev/null || true.
	regexp.MustCompile(`^cat /data/[A-Za-z0-9._-]+ 2>/dev/null \|\| true$`),
	// Read server.properties: cat /data/server.properties 2>/dev/null || echo ''.
	regexp.MustCompile(`^cat /data/server\.properties 2>/dev/null \|\| echo ''$`),
	// List datapacks: ls /data/world/datapacks/ 2>/dev/null || echo ''.
	regexp.MustCompile(`^ls /data/world/datapacks/ 2>/dev/null \|\| echo ''$`),
	// Write a /data file from caller content: printf '%s' '<escaped>' > /data/F.
	regexp.MustCompile(`^printf '%s' '` + sqArg + `' > /data/[A-Za-z0-9._-]+$`),
	// Write a /data file from base64: echo '<b64>' | base64 -d > /data/F.
	regexp.MustCompile(`^echo '[A-Za-z0-9+/=]*' \| base64 -d > /data/[A-Za-z0-9._-]+$`),
	// server.properties edit: sed substitution only, fixed file, no e/w/r.
	regexp.MustCompile(`^sed -i 's/\^[A-Za-z0-9_-]+=\.\*/[A-Za-z0-9_:.-]*=` + propVal + `/' /data/server\.properties$`),
	// World presence/integrity checks.
	regexp.MustCompile(`^test -f /data/world/[A-Za-z0-9._/-]+$`),
	regexp.MustCompile(`^gzip -t /data/world/[A-Za-z0-9._/-]+$`),
	// Touch a sentinel file under /data.
	regexp.MustCompile(`^touch /data/[A-Za-z0-9._-]+$`),
	// World wipe: rm -rf of an explicit list of /data/world/ paths only.
	regexp.MustCompile(`^rm -rf (/data/world/[A-Za-z0-9._/-]+)( /data/world/[A-Za-z0-9._/-]+)*$`),
	// World restore: mkdir && cd && wget url && unzip && rm.
	regexp.MustCompile(`^mkdir -p /data/world && cd /data/world && wget -qO /tmp/backup\.zip '` + sqArg + `' && unzip -qo /tmp/backup\.zip && rm /tmp/backup\.zip$`),
	// Datapack install / remove.
	regexp.MustCompile(`^mkdir -p /data/world/datapacks && wget -qO '/data/world/datapacks/[A-Za-z0-9._-]+' '` + sqArg + `'$`),
	regexp.MustCompile(`^rm -f '/data/world/datapacks/[A-Za-z0-9._-]+'$`),
}

// Allowed is the authorization gate for the in-container exec handler.
// `mcrcon` and the `rcon` wrapper in argv form are inert (docker.Exec passes
// argv straight to the host with no shell, so every element is a literal). For
// `sh -c <str>`, the whole string must match one anchored allow pattern;
// partial/prefix matches do not pass.
//
// MUST stay behaviour-identical to execCommandAllowed in ../main.go.
func Allowed(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	if cmd[0] == "mcrcon" || cmd[0] == "rcon" {
		// Direct argv form — no host shell, so every element is an inert literal.
		return true
	}
	if cmd[0] != "sh" || len(cmd) != 3 || cmd[1] != "-c" {
		return false
	}
	arg := cmd[2]
	for _, pat := range AllowPatterns {
		if pat.MatchString(arg) {
			return true
		}
	}
	return false
}
