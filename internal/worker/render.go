package worker

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/base64"
)

//go:embed onstart.sh.tmpl
var onstartScript string

const maxOnstartLen = 4048

// Render returns the onstart script, or a gzip+base64 wrapper when the
// plain script would exceed the provider's onstart size limit.
func Render() string {
	if len(onstartScript) < maxOnstartLen {
		return onstartScript
	}
	return wrap(onstartScript)
}

// wrap gzips s and emits a shell one-liner that reproduces it on the host.
func wrap(s string) string {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	b64 := base64.StdEncoding.EncodeToString(gz.Bytes())
	return "echo " + b64 + " | base64 -d | gunzip | bash\n"
}
