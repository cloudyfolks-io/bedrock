package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	LevelReadOnly  = "readonly"
	LevelReadWrite = "readwrite"
	LevelAll       = "all"
)

var reservedDatabases = map[string]bool{"postgres": true, "template0": true, "template1": true, engine.DefaultDatabase: true}

type Grant struct {
	Database string
	Level    string
}

func ReservedRole(name string) error { return reserved(name) }

func ReservedDatabase(name string) bool { return reservedDatabases[name] }

func groupName(database, level string) (string, error) {
	suffix, ok := levelSuffix[level]
	if !ok {
		return "", fmt.Errorf("unknown grant level %q", level)
	}
	return database + "_" + suffix, nil
}

func CreateDatabaseScript(name string, timeout time.Duration) (string, error) {
	quoted, err := ident(name)
	if err != nil {
		return "", err
	}
	groups, err := ensureGroups(name, timeout)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(preamble(timeout))
	fmt.Fprintf(&b, "CREATE DATABASE %s OWNER \"%s\" TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C.UTF-8' LC_CTYPE 'C.UTF-8';\n", quoted, engine.AdminUser)
	fmt.Fprintf(&b, "REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM PUBLIC;\n", quoted)
	fmt.Fprintf(&b, "GRANT CONNECT ON DATABASE %s TO \"%s\";\n", quoted, engine.AdminUser)
	b.WriteString(groups)
	return b.String(), nil
}

func DeleteDatabaseScript(name string, timeout time.Duration) (string, error) {
	quoted, err := ident(name)
	if err != nil {
		return "", err
	}
	groups, err := quoteAll(allGroups(name))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(preamble(timeout))
	fmt.Fprintf(&b, "DROP DATABASE IF EXISTS %s WITH (FORCE);\n", quoted)
	fmt.Fprintf(&b, "DROP ROLE IF EXISTS %s;\n", groups)
	return b.String(), nil
}

func grantStatements(user string, grants []Grant, timeout time.Duration) (string, error) {
	quotedUser, err := ident(user)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, grant := range grants {
		quotedDatabase, err := ident(grant.Database)
		if err != nil {
			return "", err
		}
		group, err := groupName(grant.Database, grant.Level)
		if err != nil {
			return "", err
		}
		quotedGroup, err := ident(group)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "GRANT %s TO %s;\n", quotedGroup, quotedUser)
		if grant.Level != LevelReadOnly {
			b.WriteString(connect(quotedDatabase, timeout))
			b.WriteString(defaultPrivileges(user, grant.Database))
		}
	}
	return b.String(), nil
}

func CreateUserScript(user, verifier string, grants []Grant, timeout time.Duration) (string, error) {
	if err := reserved(user); err != nil {
		return "", err
	}
	quotedUser, err := ident(user)
	if err != nil {
		return "", err
	}
	value, err := verifierLiteral(verifier)
	if err != nil {
		return "", err
	}
	body, err := grantStatements(user, grants, timeout)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%sCREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %s;\n%s", preamble(timeout), quotedUser, value, body), nil
}

func SetGrantsScript(user string, allDatabases []string, grants []Grant, timeout time.Duration) (string, error) {
	if err := reserved(user); err != nil {
		return "", err
	}
	quotedUser, err := ident(user)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(preamble(timeout))
	for _, database := range allDatabases {
		list, err := quoteAll(allGroups(database))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "REVOKE %s FROM %s;\n", list, quotedUser)
	}
	body, err := grantStatements(user, grants, timeout)
	if err != nil {
		return "", err
	}
	b.WriteString(body)
	return b.String(), nil
}

func ResetPasswordScript(user, verifier string, timeout time.Duration) (string, error) {
	if err := reserved(user); err != nil {
		return "", err
	}
	return resetPasswordScript(user, verifier, timeout)
}

func DeleteUserScript(user string, databases []string, timeout time.Duration) (string, error) {
	if err := reserved(user); err != nil {
		return "", err
	}
	quotedUser, err := ident(user)
	if err != nil {
		return "", err
	}
	lit, _ := literal(user)
	var b strings.Builder
	b.WriteString(preamble(timeout))
	fmt.Fprintf(&b, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = %s;\n", lit)
	seen := map[string]bool{}
	for _, database := range append(append([]string{}, databases...), "postgres") {
		if seen[database] {
			continue
		}
		seen[database] = true
		quotedDatabase, err := ident(database)
		if err != nil {
			return "", err
		}
		b.WriteString(connect(quotedDatabase, timeout))
		fmt.Fprintf(&b, "REASSIGN OWNED BY %s TO \"%s\";\nDROP OWNED BY %s;\n", quotedUser, engine.AdminUser, quotedUser)
	}
	b.WriteString(connect(`"postgres"`, timeout))
	fmt.Fprintf(&b, "DROP ROLE %s;\n", quotedUser)
	return b.String(), nil
}

func ListDatabasesScript(timeout time.Duration) string {
	return preamble(timeout) + "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY 1;\n"
}

func ParseDatabases(output string) []string {
	var databases []string
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); identPattern.MatchString(line) {
			databases = append(databases, line)
		}
	}
	return databases
}
