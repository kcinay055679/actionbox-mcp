package trust

import (
	"errors"
	"strings"
	"testing"
)

const (
	pin     = "D2C741CE32706A61019CA0A4F55BEB55DE3B6B7C"
	foreign = "9801A69633B1B1FCDF1E4C7BAD72C905223AB10F"
	subkey  = "2C8585844EB185CDF38410289AFC0264C11EDF21"
)

func validSig(signing, primary string) string {
	return "[GNUPG:] VALIDSIG " + signing + " 2026-10-28 1793193897 0 4 0 22 8 00 " + primary
}

func status(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

var goodStatus = status(
	"[GNUPG:] NEWSIG",
	"[GNUPG:] KEY_CONSIDERED "+pin+" 0",
	"[GNUPG:] SIG_ID ILkt7PH63q02Elvn2GoO17FQn8o 2026-10-28 1793193897",
	"[GNUPG:] GOODSIG F55BEB55DE3B6B7C Owner <owner@test>",
	validSig(pin, pin),
)

func TestEvaluateStatusAccepts(t *testing.T) {
	cases := map[string][]byte{
		"primary key": goodStatus,
		"subkey": status(
			"[GNUPG:] NEWSIG",
			"[GNUPG:] GOODSIG 9AFC0264C11EDF21 Sub <sub@test>",
			validSig(subkey, pin),
		),
		"lowercase pin": goodStatus,
		"stray stdout":  append([]byte("gpgv: noise\n"), goodStatus...),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			p := pin
			if name == "lowercase pin" {
				p = strings.ToLower(pin)
			}
			if err := evaluateStatus(out, 0, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEvaluateStatusRejects(t *testing.T) {
	cases := []struct {
		name   string
		out    []byte
		exit   int
		reason string
	}{
		{"exit code", goodStatus, 1, ReasonSignatureStatus},
		{"empty", nil, 0, ReasonSignatureStatus},
		{"foreign primary", status("[GNUPG:] NEWSIG", "[GNUPG:] GOODSIG AD72C905223AB10F F <f@test>", validSig(foreign, foreign)), 0, ReasonForeignKey},
		{"foreign subkey of other primary", status("[GNUPG:] NEWSIG", "[GNUPG:] GOODSIG 9AFC0264C11EDF21 S <s@test>", validSig(pin, foreign)), 0, ReasonForeignKey},
		{"key not in keyring", status(
			"[GNUPG:] NEWSIG",
			"[GNUPG:] ERRSIG AD72C905223AB10F 22 8 00 1793193897 9 "+foreign,
			"[GNUPG:] NO_PUBKEY AD72C905223AB10F",
		), 2, ReasonForeignKey},
		{"expired key", status(
			"[GNUPG:] NEWSIG",
			"[GNUPG:] KEYEXPIRED 1735776000",
			"[GNUPG:] EXPKEYSIG F55BEB55DE3B6B7C Owner <owner@test>",
			validSig(pin, pin),
		), 0, ReasonSignatureStatus},
		{"revoked key", status(
			"[GNUPG:] NEWSIG",
			"[GNUPG:] REVKEYSIG F55BEB55DE3B6B7C Owner <owner@test>",
			validSig(pin, pin),
		), 0, ReasonSignatureStatus},
		{"expired sig", append(goodStatus, status("[GNUPG:] EXPSIG F55BEB55DE3B6B7C Owner <owner@test>")...), 0, ReasonSignatureStatus},
		{"keyrevoked", append(goodStatus, status("[GNUPG:] KEYREVOKED")...), 0, ReasonSignatureStatus},
		{"bad sig", status("[GNUPG:] NEWSIG", "[GNUPG:] BADSIG F55BEB55DE3B6B7C Owner <owner@test>"), 1, ReasonSignatureStatus},
		{"no newsig", status("[GNUPG:] GOODSIG F55BEB55DE3B6B7C Owner <owner@test>", validSig(pin, pin)), 0, ReasonSignatureStatus},
		{"no goodsig", status("[GNUPG:] NEWSIG", validSig(pin, pin)), 0, ReasonSignatureStatus},
		{"no validsig", status("[GNUPG:] NEWSIG", "[GNUPG:] GOODSIG F55BEB55DE3B6B7C Owner <owner@test>"), 0, ReasonSignatureStatus},
		{"two good sigs", append(goodStatus, goodStatus...), 0, ReasonSignatureStatus},
		{"owner and foreign sig", append(goodStatus, status(
			"[GNUPG:] NEWSIG",
			"[GNUPG:] ERRSIG AD72C905223AB10F 22 8 00 1793193897 9 "+foreign,
			"[GNUPG:] NO_PUBKEY AD72C905223AB10F",
		)...), 2, ReasonForeignKey},
		{"short validsig", status("[GNUPG:] NEWSIG", "[GNUPG:] GOODSIG F55BEB55DE3B6B7C O <o@test>", "[GNUPG:] VALIDSIG "+pin), 0, ReasonSignatureStatus},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertReason(t, evaluateStatus(c.out, c.exit, pin), c.reason)
		})
	}
}

func assertReason(t *testing.T, err error, reason string) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("want failure %q, got %v", reason, err)
	}
	if f.Reason != reason {
		t.Fatalf("reason %q (%s), want %q", f.Reason, f.Detail, reason)
	}
}
