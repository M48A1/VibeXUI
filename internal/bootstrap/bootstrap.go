package bootstrap

import _ "embed"

//go:embed agent-install.sh
var Script []byte
