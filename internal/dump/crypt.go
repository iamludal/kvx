package dump

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
)

const ageMagic = "age-encryption.org/"

// IsEncrypted tells whether b is an age file, binary or ASCII-armored.
func IsEncrypted(b []byte) bool {
	t := bytes.TrimLeft(b, " \t\r\n")
	return bytes.HasPrefix(t, []byte(ageMagic)) || bytes.HasPrefix(t, []byte(armor.Header))
}

// ParseRecipients reads age recipients. Each value is an age public key
// (age1..., age1pq1...), an SSH public key (ssh-ed25519 ..., ssh-rsa ...),
// or the path of a recipients file holding such lines, like `age -R`.
func ParseRecipients(values []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !looksLikeRecipient(v) {
			lines, err := readRecipientsFile(v)
			if err != nil {
				return nil, err
			}
			rs, err := ParseRecipients(lines)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", v, err)
			}
			out = append(out, rs...)
			continue
		}
		r, err := parseRecipient(v)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errors.New("no age recipient given")
	}
	return out, nil
}

func looksLikeRecipient(v string) bool {
	return strings.HasPrefix(v, "age1") || strings.HasPrefix(v, "ssh-")
}

func parseRecipient(v string) (age.Recipient, error) {
	if strings.HasPrefix(v, "ssh-") {
		r, err := agessh.ParseRecipient(v)
		if err != nil {
			return nil, fmt.Errorf("invalid SSH recipient: %w", err)
		}
		return r, nil
	}
	rs, err := age.ParseRecipients(strings.NewReader(v))
	if err != nil {
		// Never echo the value: it could be a secret key pasted by mistake.
		return nil, fmt.Errorf("invalid age recipient %.12s...: %w", v, err)
	}
	return rs[0], nil
}

func readRecipientsFile(name string) ([]string, error) {
	f, err := os.Open(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%q is neither an age/SSH recipient nor a recipients file", name)
		}
		return nil, err
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			if !looksLikeRecipient(l) {
				return nil, fmt.Errorf("%s: unknown recipient type (a recipients file holds public keys)", name)
			}
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s: no recipient found", name)
	}
	return lines, nil
}

// Encrypt wraps w so that everything written is encrypted for recipients,
// ASCII-armored if asked. Close the result to flush it; it does not close w.
func Encrypt(w io.Writer, recipients []age.Recipient, armored bool) (io.WriteCloser, error) {
	var aw io.WriteCloser
	dst := w
	if armored {
		aw = armor.NewWriter(w)
		dst = aw
	}
	ew, err := age.Encrypt(dst, recipients...)
	if err != nil {
		return nil, err
	}
	return closers{ew, aw}, nil
}

type closers struct {
	io.WriteCloser
	armor io.WriteCloser
}

func (c closers) Close() error {
	if err := c.WriteCloser.Close(); err != nil {
		return err
	}
	if c.armor != nil {
		return c.armor.Close()
	}
	return nil
}

// Decrypt decrypts an age file (binary or armored) with the identities in
// identityFile: age identities (AGE-SECRET-KEY-...) or an unencrypted SSH
// private key.
func Decrypt(b []byte, identityFile string) ([]byte, error) {
	ids, err := loadIdentities(identityFile)
	if err != nil {
		return nil, err
	}
	var src io.Reader = bytes.NewReader(bytes.TrimLeft(b, " \t\r\n"))
	if bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte(armor.Header)) {
		src = armor.NewReader(src)
	}
	r, err := age.Decrypt(src, ids...)
	if err != nil {
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) {
			return nil, fmt.Errorf("decrypting: %s does not hold a key this file was encrypted for", identityFile)
		}
		return nil, fmt.Errorf("decrypting: %w", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}
	return out, nil
}

func loadIdentities(name string) ([]age.Identity, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("reading age identity: %w", err)
	}
	if bytes.Contains(b, []byte("PRIVATE KEY-----")) {
		id, err := agessh.ParseIdentity(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w (passphrase-protected SSH keys are not supported, decrypt with `age -d` and pipe)", name, err)
		}
		return []age.Identity{id}, nil
	}
	ids, err := age.ParseIdentities(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return ids, nil
}
