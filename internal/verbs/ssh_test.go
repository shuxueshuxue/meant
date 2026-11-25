package verbs

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitSSH(t *testing.T) {
	cases := []struct {
		line    string
		ok      bool
		dest    string
		command []string
	}{
		{"ssh gpu ls -la", true, "gpu", []string{"ls", "-la"}},
		{"ssh -F cfg -o BatchMode=yes -p 2202 root@gpu uname", true, "root@gpu", []string{"uname"}},
		{"ssh -tt -i key host cmd", true, "host", []string{"cmd"}},
		{"ssh -p22 host cmd", true, "host", []string{"cmd"}},
		{"ssh -oBatchMode=yes host cmd arg", true, "host", []string{"cmd", "arg"}},
		{"ssh -J jump host cmd", true, "host", []string{"cmd"}},
		// Anything that is not "run this command there" is left alone.
		{"ssh host", false, "", nil},
		{"ssh -N -L 8080:localhost:80 host", false, "", nil},
		{"ssh -fN host", false, "", nil},
		{"ssh -W host:22 jump", false, "", nil},
		{"ssh -O exit host", false, "", nil},
		{"ssh -V", false, "", nil},
		{"ssh -s host sftp", false, "", nil},
		{"ssh -Z host cmd", false, "", nil}, // an option nobody has heard of
	}
	for _, c := range cases {
		_, dest, command, ok := splitSSH(strings.Fields(c.line))
		if ok != c.ok || (ok && (dest != c.dest || !reflect.DeepEqual(command, c.command))) {
			t.Errorf("%q: got ok=%v dest=%q command=%q", c.line, ok, dest, command)
		}
	}
}

// The original command must come out of the wrapper byte for byte: it is
// quoted once and never interpreted on the way.
func TestRelayCommandCarriesTheOriginalVerbatim(t *testing.T) {
	original := `X="a  b"; echo "$X" 'it'\''s' $(date) ` + "`id`" + ` \ && rm -rf "$HOME"/*`
	wrapped := relayCommand(Remote{Host: "h", Path: "/opt/meant"}, "sess", []string{"7f3a"}, original)
	want := "'" + strings.ReplaceAll(original, "'", `'\''`) + "'"
	if !strings.HasSuffix(wrapped, " "+want) {
		t.Errorf("original not carried verbatim:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "-g '\\''7f3a'\\''") {
		t.Errorf("acks given on this side must travel with the command:\n%s", wrapped)
	}
}
