package valkey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	ListUsersCommand = "ACL LIST\n"
	maxPrefixLength  = 64
)

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z0-9:_.\-]{0,64}$`)
	hexRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)

	levelRules = map[string]string{
		"readonly":  "-@all +@read +@connection +exists +type +ttl +pttl +scan -@dangerous -@admin",
		"readwrite": "-@all +@read +@write +@keyspace +@connection -@dangerous -@admin",
		"all":       "+@all -@dangerous -@admin",
	}

	reservedUsers = map[string]bool{"default": true, engine.AdminUser: true, PlatformUser: true, ExporterUser: true}

	replyErrorPrefixes = []string{"ERR", "NOPERM", "WRONGPASS", "NOAUTH", "(error)"}
)

func Superuser(platformPassword string) (map[string][]byte, error) {
	exporter, err := engine.RandomPassword()
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"password": []byte(platformPassword), "exporter-password": []byte(exporter)}, nil
}

func PasswordHash(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func reserved(name string) error {
	if reservedUsers[name] || strings.HasPrefix(name, "_") {
		return fmt.Errorf("%q is a reserved account name", name)
	}
	return nil
}

func CheckUser(name string) error { return checkUser(name) }

func checkUser(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("user name %q is not allowed", name)
	}
	return reserved(name)
}

func checkHash(hash string) error {
	if !hexRe.MatchString(hash) {
		return fmt.Errorf("password hash has an unexpected shape")
	}
	return nil
}

func scopeRules(prefix string) (string, error) {
	if len(prefix) > maxPrefixLength || !prefixRe.MatchString(prefix) {
		return "", fmt.Errorf("key prefix is not allowed")
	}
	if prefix == "" {
		return "resetkeys ~* resetchannels &*", nil
	}
	return fmt.Sprintf("resetkeys ~%s* resetchannels &%s*", prefix, prefix), nil
}

func levelRulesFor(grants []engine.Grant) (string, error) {
	if len(grants) != 1 || grants[0].Database != engine.DefaultDatabase {
		return "", fmt.Errorf("a valkey user takes exactly one grant on %q", engine.DefaultDatabase)
	}
	rules, ok := levelRules[grants[0].Level]
	if !ok {
		return "", fmt.Errorf("unknown grant level %q", grants[0].Level)
	}
	return rules, nil
}

func CreateUserScript(user, hash string, grants []engine.Grant, prefix string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	if err := checkHash(hash); err != nil {
		return "", err
	}
	rules, err := levelRulesFor(grants)
	if err != nil {
		return "", err
	}
	scope, err := scopeRules(prefix)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ACL SETUSER %s reset on #%s %s %s\nACL SAVE\n", user, hash, scope, rules), nil
}

func SetGrantsScript(user string, grants []engine.Grant, prefix string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	rules, err := levelRulesFor(grants)
	if err != nil {
		return "", err
	}
	scope, err := scopeRules(prefix)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ACL SETUSER %s %s %s\nCLIENT KILL USER %s\nACL SAVE\n", user, scope, rules, user), nil
}

func ResetPasswordScript(user, hash string) (string, error) {
	if user != engine.AdminUser {
		if err := checkUser(user); err != nil {
			return "", err
		}
	}
	if err := checkHash(hash); err != nil {
		return "", err
	}
	return fmt.Sprintf("ACL SETUSER %s resetpass #%s\nCLIENT KILL USER %s\nACL SAVE\n", user, hash, user), nil
}

func DeleteUserScript(user string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	return fmt.Sprintf("ACL DELUSER %s\nACL SAVE\n", user), nil
}

func GetUserScript(user string) (string, error) {
	if err := checkUser(user); err != nil {
		return "", err
	}
	return "ACL GETUSER " + user + "\n", nil
}

type UserRow struct {
	Name   string
	Level  string
	Prefix string
}

func levelOf(tokens []string) (string, string) {
	level, prefix := "readonly", ""
	for _, token := range tokens {
		switch {
		case strings.HasPrefix(token, "~"):
			prefix = strings.TrimSuffix(strings.TrimPrefix(token, "~"), "*")
		case token == "+@all":
			level = "all"
		case token == "+@write" && level != "all":
			level = "readwrite"
		}
	}
	if !prefixRe.MatchString(prefix) {
		prefix = ""
	}
	return level, prefix
}

func ParseACLList(out string) []UserRow {
	var rows []UserRow
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 || fields[0] != "user" || !nameRe.MatchString(fields[1]) || reserved(fields[1]) != nil {
			continue
		}
		level, prefix := levelOf(fields[2:])
		rows = append(rows, UserRow{Name: fields[1], Level: level, Prefix: prefix})
	}
	return rows
}

func Classify(stderr string) error {
	text := strings.TrimSpace(stderr)
	switch {
	case hasAuthError(text):
		return engine.ErrUnavailable
	case strings.Contains(text, "already exists"):
		return engine.ErrExists
	case strings.Contains(text, "unknown user"), strings.Contains(text, "No such user"), strings.Contains(text, "doesn't exist"):
		return engine.ErrNotFound
	case strings.HasPrefix(text, "ERR"):
		return engine.ErrUnavailable
	}
	return nil
}

func hasAuthError(text string) bool {
	for _, word := range []string{"NOAUTH", "WRONGPASS", "NOPERM"} {
		if strings.HasPrefix(text, word) || strings.Contains(text, word+" ") {
			return true
		}
	}
	return false
}

func CheckReplies(stdout string) error {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range replyErrorPrefixes {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			if err := Classify(strings.TrimSpace(strings.TrimPrefix(line, "(error)"))); err != nil {
				return err
			}
			return fmt.Errorf("valkey replied: %s", line)
		}
	}
	return nil
}

func execute(ctx context.Context, runner engine.Runner, script string) (string, error) {
	out, err := runner.Run(ctx, script)
	if err != nil {
		return out, err
	}
	return out, CheckReplies(out)
}

func ResetAdmin(ctx context.Context, runner engine.Runner, password string) error {
	script, err := ResetPasswordScript(engine.AdminUser, PasswordHash(password))
	if err != nil {
		return err
	}
	_, err = execute(ctx, runner, script)
	return err
}

func CreateUser(ctx context.Context, runner engine.Runner, user engine.UserSpec) error {
	script, err := CreateUserScript(user.Name, PasswordHash(user.Password), user.Grants, user.KeyPrefix)
	if err != nil {
		return err
	}
	lookup, err := GetUserScript(user.Name)
	if err != nil {
		return err
	}
	existing, err := execute(ctx, runner, lookup)
	if err != nil {
		return err
	}
	if found := strings.TrimSpace(existing); found != "" && found != "(nil)" {
		return engine.ErrExists
	}
	_, err = execute(ctx, runner, script)
	return err
}

func DeleteUser(ctx context.Context, runner engine.Runner, name string) error {
	script, err := DeleteUserScript(name)
	if err != nil {
		return err
	}
	_, err = execute(ctx, runner, script)
	return err
}

func ResetPassword(ctx context.Context, runner engine.Runner, name, password string) error {
	if err := checkUser(name); err != nil {
		return err
	}
	script, err := ResetPasswordScript(name, PasswordHash(password))
	if err != nil {
		return err
	}
	_, err = execute(ctx, runner, script)
	return err
}

func SetGrants(ctx context.Context, runner engine.Runner, name string, grants []engine.Grant, prefix string) error {
	script, err := SetGrantsScript(name, grants, prefix)
	if err != nil {
		return err
	}
	_, err = execute(ctx, runner, script)
	return err
}

func ListUsers(ctx context.Context, runner engine.Runner) ([]engine.UserInfo, error) {
	out, err := execute(ctx, runner, ListUsersCommand)
	if err != nil {
		return nil, err
	}
	users := []engine.UserInfo{}
	for _, row := range ParseACLList(out) {
		users = append(users, engine.UserInfo{
			Name: row.Name, KeyPrefix: row.Prefix,
			Grants: []engine.Grant{{Database: engine.DefaultDatabase, Level: row.Level}},
		})
	}
	return users, nil
}
