package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/mudler/agent-controller/internal/server"
)

// tokenBytes is the random size of a token; base64url gives 43 characters.
const tokenBytes = 32

// runToken implements "token new --name N --scopes a,b".
func runToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "new" {
		fmt.Fprintln(stderr, "usage: agent-controller token new --name NAME --scopes SCOPE[,SCOPE]")
		return 2
	}
	out, err := newToken(args[1:], rand.Reader)
	if err != nil {
		fmt.Fprintf(stderr, "token: %v\n", err)
		return 2
	}
	fmt.Fprint(stdout, out)
	return 0
}

// newToken creates a random token and returns the text to print: the
// token and the tokens.json entry that holds only its hash.
func newToken(args []string, random io.Reader) (string, error) {
	fs := flag.NewFlagSet("token new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "token name; it becomes requestedBy and lastChangedBy")
	scopes := fs.String("scopes", "", "comma-separated scopes")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("parsing flags: %w", err)
	}
	if strings.TrimSpace(*name) == "" {
		return "", errors.New("--name is required")
	}
	list := []string{}
	for _, s := range strings.Split(*scopes, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !slices.Contains(server.AllScopes, server.Scope(s)) {
			return "", fmt.Errorf("unknown scope %q", s)
		}
		if !slices.Contains(list, s) {
			list = append(list, s)
		}
	}
	if len(list) == 0 {
		return "", errors.New("--scopes needs at least one scope")
	}

	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	entry, err := json.Marshal(map[string]server.TokenEntry{*name: {SHA256: server.HashToken(token), Scopes: list}})
	if err != nil {
		return "", fmt.Errorf("encoding entry: %w", err)
	}
	return fmt.Sprintf("token: %s\n%s: %s\n", token, server.TokensKey, entry), nil
}
