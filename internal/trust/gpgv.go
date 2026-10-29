package trust

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const statusPrefix = "[GNUPG:] "

// step 4; gpgv instead of git: git would run the repo's gpg.*.program
func verifySignature(ctx context.Context, keyring, pin string, sig, payload []byte) error {
	out, exitCode, err := runGpgv(ctx, keyring, sig, payload)
	if err != nil {
		return err
	}
	return evaluateStatus(out, exitCode, pin)
}

func runGpgv(ctx context.Context, keyring string, sig, payload []byte) (status []byte, exitCode int, err error) {
	gpgv, err := exec.LookPath("gpgv")
	if err != nil {
		return nil, 0, fmt.Errorf("trust: %w", err)
	}
	// relative keyring name would be looked up in the gpg homedir
	keyring, err = filepath.Abs(keyring)
	if err != nil {
		return nil, 0, err
	}
	dir, err := os.MkdirTemp("", "actionbox-gpgv-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)
	sigFile, payloadFile := filepath.Join(dir, "sig"), filepath.Join(dir, "payload")
	if err := os.WriteFile(sigFile, sig, 0o600); err != nil {
		return nil, 0, err
	}
	if err := os.WriteFile(payloadFile, payload, 0o600); err != nil {
		return nil, 0, err
	}

	cmd := exec.CommandContext(ctx, gpgv, "--homedir", dir, "--keyring", keyring, "--status-fd", "1", sigFile, payloadFile)
	cmd.Env = []string{"LC_ALL=C"}
	status, err = cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return status, exitErr.ExitCode(), nil
	}
	return status, 0, err
}

func evaluateStatus(out []byte, exitCode int, pin string) error {
	counts := map[string]int{}
	var primaries []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line, ok := strings.CutPrefix(scanner.Text(), statusPrefix)
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		counts[fields[0]]++
		if fields[0] == "VALIDSIG" && len(fields) >= 11 {
			primaries = append(primaries, fields[10])
		}
	}

	// NO_PUBKEY: signer not in pinned keyring, gpgv gives no VALIDSIG then
	if counts["NO_PUBKEY"] > 0 {
		return fail(ReasonForeignKey, "signing key not in keyring")
	}
	for _, primary := range primaries {
		if !strings.EqualFold(primary, pin) {
			return fail(ReasonForeignKey, "signed by %s", primary)
		}
	}

	if exitCode != 0 {
		return fail(ReasonSignatureStatus, "gpgv exit code %d", exitCode)
	}
	for _, keyword := range []string{"NEWSIG", "GOODSIG", "VALIDSIG"} {
		if counts[keyword] != 1 {
			return fail(ReasonSignatureStatus, "%d %s", counts[keyword], keyword)
		}
	}
	if len(primaries) != 1 {
		return fail(ReasonSignatureStatus, "malformed VALIDSIG")
	}
	// expired/revoked keys still exit 0 with VALIDSIG
	for _, keyword := range []string{"BADSIG", "ERRSIG", "EXPSIG", "EXPKEYSIG", "REVKEYSIG", "KEYEXPIRED", "KEYREVOKED", "NO_PUBKEY"} {
		if counts[keyword] > 0 {
			return fail(ReasonSignatureStatus, "%s", keyword)
		}
	}
	return nil
}
