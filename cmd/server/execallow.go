package main

import "regexp"

const (
	sqArg   = `(?:[^']|'\\'')*`
	propVal = `[^'\n]*`
	ruleTok = `[A-Za-z0-9_]+`
)

// execAllowPatterns mirrors AllowPatterns in pulp-cell/execallow/execallow.go.
// MUST stay byte-for-byte identical — the cell build's unit tests are the
// regression pin for both copies.
var execAllowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^rcon '` + sqArg + `'( 2>/dev/null)?(; rcon '` + sqArg + `'( 2>/dev/null)?)*$`),
	regexp.MustCompile(`^rcon( '` + sqArg + `')+( 2>/dev/null)?$`),
	regexp.MustCompile(`^(echo "RULE:` + ruleTok + `:\$\(rcon 'gamerule ` + ruleTok + `' 2>/dev/null\)"\n?)+$`),
	regexp.MustCompile(`^\(test -f /data/[A-Za-z0-9._-]+ && cat /data/[A-Za-z0-9._-]+\) \|\| echo '\[\]'$`),
	regexp.MustCompile(`^cat /data/[A-Za-z0-9._-]+ 2>/dev/null \|\| true$`),
	regexp.MustCompile(`^cat /data/server\.properties 2>/dev/null \|\| echo ''$`),
	regexp.MustCompile(`^ls /data/world/datapacks/ 2>/dev/null \|\| echo ''$`),
	regexp.MustCompile(`^printf '%s' '` + sqArg + `' > /data/[A-Za-z0-9._-]+$`),
	regexp.MustCompile(`^echo '[A-Za-z0-9+/=]*' \| base64 -d > /data/[A-Za-z0-9._-]+$`),
	regexp.MustCompile(`^sed -i 's/\^[A-Za-z0-9_-]+=\.\*/[A-Za-z0-9_:.-]*=` + propVal + `/' /data/server\.properties$`),
	regexp.MustCompile(`^test -f /data/world/[A-Za-z0-9._/-]+$`),
	regexp.MustCompile(`^gzip -t /data/world/[A-Za-z0-9._/-]+$`),
	regexp.MustCompile(`^touch /data/[A-Za-z0-9._-]+$`),
	regexp.MustCompile(`^rm -rf (/data/world/[A-Za-z0-9._/-]+)( /data/world/[A-Za-z0-9._/-]+)*$`),
	regexp.MustCompile(`^mkdir -p /data/world && cd /data/world && wget -qO /tmp/backup\.zip '` + sqArg + `' && unzip -qo /tmp/backup\.zip && rm /tmp/backup\.zip$`),
	regexp.MustCompile(`^mkdir -p /data/world/datapacks && wget -qO '/data/world/datapacks/[A-Za-z0-9._-]+' '` + sqArg + `'$`),
	regexp.MustCompile(`^rm -f '/data/world/datapacks/[A-Za-z0-9._-]+'$`),
}

// execCommandAllowed is the authorization gate for the native exec handler.
// Mirrors execallow.Allowed — keep behaviour identical.
func execCommandAllowed(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	if cmd[0] == "mcrcon" || cmd[0] == "rcon" {
		return true
	}
	if cmd[0] != "sh" || len(cmd) != 3 || cmd[1] != "-c" {
		return false
	}
	arg := cmd[2]
	for _, pat := range execAllowPatterns {
		if pat.MatchString(arg) {
			return true
		}
	}
	return false
}
