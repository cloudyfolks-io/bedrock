package postgres

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/database/credential"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

var (
	identPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	scramPattern = regexp.MustCompile(`^SCRAM-SHA-256\$[0-9]{1,6}:[A-Za-z0-9+/=]+\$[A-Za-z0-9+/=]+:[A-Za-z0-9+/=]+$`)
	levelSuffix  = map[string]string{"readonly": "ro", "readwrite": "rw", "all": "all"}
)

var reservedRoles = map[string]bool{"postgres": true, "dbadmin": true, "streaming_replica": true, "cnpg_pooler_pgbouncer": true}

func ValidIdentifier(name string) bool { return identPattern.MatchString(name) }

func reserved(name string) error {
	for _, suffix := range levelSuffix {
		if strings.HasSuffix(name, "_"+suffix) {
			return fmt.Errorf("%q ends with a group role suffix", name)
		}
	}
	if reservedRoles[name] || strings.HasPrefix(name, "pg_") {
		return fmt.Errorf("%q is a reserved role name", name)
	}
	return nil
}

func preamble(timeout time.Duration) string {
	ms := timeout.Milliseconds()
	return fmt.Sprintf("SET statement_timeout = '%d';\nSET idle_in_transaction_session_timeout = '%d';\n", ms, ms)
}

func connect(quotedDatabase string, timeout time.Duration) string {
	return "\\connect " + quotedDatabase + "\n" + preamble(timeout)
}

func ident(name string) (string, error) {
	if !identPattern.MatchString(name) {
		return "", fmt.Errorf("identifier %q is not allowed", name)
	}
	return `"` + name + `"`, nil
}

func literal(name string) (string, error) {
	if !identPattern.MatchString(name) {
		return "", fmt.Errorf("identifier %q is not allowed", name)
	}
	return "'" + name + "'", nil
}

func verifierLiteral(verifier string) (string, error) {
	if !scramPattern.MatchString(verifier) {
		return "", fmt.Errorf("password verifier has an unexpected shape")
	}
	return "'" + verifier + "'", nil
}

func allGroups(database string) []string {
	return []string{database + "_ro", database + "_rw", database + "_all"}
}

func quoteAll(names []string) (string, error) {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		q, err := ident(name)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, ", "), nil
}

func BootstrapScript(timeout time.Duration) (string, error) {
	groups, err := ensureGroups(engine.DefaultDatabase, timeout)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(preamble(timeout))
	fmt.Fprintf(&b, "ALTER ROLE \"%s\" WITH NOSUPERUSER CREATEDB CREATEROLE;\n", engine.AdminUser)
	fmt.Fprintf(&b, "ALTER ROLE \"%s\" SET createrole_self_grant = 'inherit,set';\n", engine.AdminUser)
	fmt.Fprintf(&b, "REVOKE CONNECT, TEMPORARY ON DATABASE \"%s\" FROM PUBLIC;\n", engine.DefaultDatabase)
	fmt.Fprintf(&b, "GRANT CONNECT ON DATABASE \"%s\" TO \"%s\";\n", engine.DefaultDatabase, engine.AdminUser)
	b.WriteString(groups)
	return b.String(), nil
}

func ensureGroups(database string, timeout time.Duration) (string, error) {
	quotedDatabase, err := ident(database)
	if err != nil {
		return "", err
	}
	ro, rw, all := database+"_ro", database+"_rw", database+"_all"
	var b strings.Builder
	b.WriteString(connect(quotedDatabase, timeout))
	for _, group := range []string{ro, rw, all} {
		lit, err := literal(group)
		if err != nil {
			return "", err
		}
		quoted, _ := ident(group)
		fmt.Fprintf(&b, "DO $do$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s AND rolcanlogin) THEN RAISE EXCEPTION 'a login role uses a group role name'; END IF; IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %s) THEN CREATE ROLE %s NOLOGIN; END IF; END $do$;\n", lit, lit, quoted)
	}
	groupList, err := quoteAll([]string{ro, rw, all})
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "GRANT CONNECT ON DATABASE %s TO %s;\n", quotedDatabase, groupList)
	fmt.Fprintf(&b, "GRANT USAGE ON SCHEMA public TO %s;\n", groupList)
	fmt.Fprintf(&b, "GRANT CREATE ON SCHEMA public TO \"%s\";\n", all)
	fmt.Fprintf(&b, "GRANT SELECT ON ALL TABLES IN SCHEMA public TO \"%s\", \"%s\", \"%s\";\n", ro, rw, all)
	fmt.Fprintf(&b, "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO \"%s\", \"%s\", \"%s\";\n", ro, rw, all)
	fmt.Fprintf(&b, "GRANT INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO \"%s\", \"%s\";\n", rw, all)
	fmt.Fprintf(&b, "GRANT UPDATE ON ALL SEQUENCES IN SCHEMA public TO \"%s\", \"%s\";\n", rw, all)
	fmt.Fprintf(&b, "GRANT ALL ON ALL TABLES IN SCHEMA public TO \"%s\";\n", all)
	fmt.Fprintf(&b, "GRANT TEMPORARY ON DATABASE %s TO \"%s\";\n", quotedDatabase, all)
	b.WriteString(defaultPrivileges(engine.AdminUser, database))
	return b.String(), nil
}

func defaultPrivileges(owner, database string) string {
	var b strings.Builder
	ro, rw, all := database+"_ro", database+"_rw", database+"_all"
	fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT SELECT ON TABLES TO \"%s\", \"%s\", \"%s\";\n", owner, ro, rw, all)
	fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO \"%s\", \"%s\", \"%s\";\n", owner, ro, rw, all)
	fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT INSERT, UPDATE, DELETE ON TABLES TO \"%s\", \"%s\";\n", owner, rw, all)
	fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT UPDATE ON SEQUENCES TO \"%s\", \"%s\";\n", owner, rw, all)
	fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT ALL ON TABLES TO \"%s\";\n", owner, all)
	if owner != engine.AdminUser {
		fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT ALL ON TABLES TO \"%s\";\n", owner, engine.AdminUser)
		fmt.Fprintf(&b, "ALTER DEFAULT PRIVILEGES FOR ROLE \"%s\" IN SCHEMA public GRANT ALL ON SEQUENCES TO \"%s\";\n", owner, engine.AdminUser)
	}
	return b.String()
}

func resetPasswordScript(user, verifier string, timeout time.Duration) (string, error) {
	quoted, err := ident(user)
	if err != nil {
		return "", err
	}
	lit, _ := literal(user)
	value, err := verifierLiteral(verifier)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%sALTER ROLE %s PASSWORD %s;\nSELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = %s;\n", preamble(timeout), quoted, value, lit), nil
}

func ResetAdminScript(password string, timeout time.Duration) (string, error) {
	verifier, err := credential.ScramSHA256(password)
	if err != nil {
		return "", err
	}
	return resetPasswordScript(engine.AdminUser, verifier, timeout)
}
