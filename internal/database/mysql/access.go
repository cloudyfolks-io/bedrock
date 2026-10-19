package mysql

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	MaxKill           = 200
	execTimeoutMillis = 30000

	adminPrivileges = "CREATE USER, RELOAD, SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES, TRIGGER, EVENT, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, EXECUTE, LOCK TABLES, CREATE TEMPORARY TABLES"

	systemSchemaRevoke = "SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER, INDEX, REFERENCES, TRIGGER, EVENT, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, EXECUTE, LOCK TABLES, CREATE TEMPORARY TABLES"
)

var (
	identRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	hashRe  = regexp.MustCompile(`^0x[0-9a-f]{20,400}$`)
	errnoRe = regexp.MustCompile(`ERROR (\d{4}) \([0-9A-Z]{5}\)`)

	InternalUsers = []string{"operator", "xtrabackup", "monitor", "clustercheck", "proxyadmin", "replication", "orchestrator", "heartbeat"}

	reservedAccounts = map[string]bool{
		"root": true, engine.AdminUser: true, "mysql.sys": true, "mysql.session": true, "mysql.infoschema": true,
		"operator": true, "xtrabackup": true, "monitor": true, "clustercheck": true,
		"proxyadmin": true, "replication": true, "orchestrator": true, "heartbeat": true,
	}

	systemSchemas = map[string]bool{"mysql": true, "sys": true, "information_schema": true, "performance_schema": true}

	levelPrivileges = map[string]string{
		"readonly":  "SELECT, SHOW VIEW, EXECUTE",
		"readwrite": "SELECT, INSERT, UPDATE, DELETE, SHOW VIEW, EXECUTE, LOCK TABLES, CREATE TEMPORARY TABLES",
		"all":       "ALL PRIVILEGES",
	}
)

func Superuser(rootPassword string) (map[string][]byte, error) {
	data := map[string][]byte{"root": []byte(rootPassword)}
	for _, user := range InternalUsers {
		password, err := engine.RandomPassword()
		if err != nil {
			return nil, err
		}
		data[user] = []byte(password)
	}
	return data, nil
}

func userAllowed(name string) error {
	if !identRe.MatchString(name) {
		return fmt.Errorf("identifier %q is not allowed", name)
	}
	if reservedAccounts[name] {
		return fmt.Errorf("%q is a reserved account name", name)
	}
	return nil
}

func databaseAllowed(name string) error {
	if !identRe.MatchString(name) {
		return fmt.Errorf("identifier %q is not allowed", name)
	}
	if systemSchemas[name] {
		return fmt.Errorf("%q is a system schema", name)
	}
	return nil
}

func queryPreamble() string {
	return fmt.Sprintf("SET SESSION max_execution_time = %d;\n", execTimeoutMillis)
}

func quotedDatabase(name string) (string, error) {
	if err := databaseAllowed(name); err != nil {
		return "", err
	}
	return "`" + name + "`", nil
}

func quotedAccount(name string) (string, error) {
	if err := userAllowed(name); err != nil {
		return "", err
	}
	return "'" + name + "'@'%'", nil
}

func hashLiteral(hash string) (string, error) {
	if !hashRe.MatchString(hash) {
		return "", fmt.Errorf("password hash has an unexpected shape")
	}
	return hash, nil
}

func BootstrapScript(adminHash string) (string, error) {
	hash, err := hashLiteral(adminHash)
	if err != nil {
		return "", err
	}
	var script strings.Builder
	script.WriteString("DROP USER IF EXISTS 'root'@'%';\n")
	script.WriteString("GRANT SYSTEM_USER ON *.* TO 'root'@'localhost';\n")
	fmt.Fprintf(&script, "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED WITH caching_sha2_password AS %s;\n", engine.AdminUser, hash)
	fmt.Fprintf(&script, "ALTER USER '%s'@'%%' IDENTIFIED WITH caching_sha2_password AS %s;\n", engine.AdminUser, hash)
	fmt.Fprintf(&script, "GRANT %s ON *.* TO '%s'@'%%' WITH GRANT OPTION;\n", adminPrivileges, engine.AdminUser)
	fmt.Fprintf(&script, "REVOKE %s ON mysql.* FROM '%s'@'%%';\n", systemSchemaRevoke, engine.AdminUser)
	fmt.Fprintf(&script, "CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n", engine.DefaultDatabase)
	return script.String(), nil
}

func grantStatements(user string, grants []engine.Grant) (string, error) {
	account, err := quotedAccount(user)
	if err != nil {
		return "", err
	}
	var script strings.Builder
	for _, grant := range grants {
		database, err := quotedDatabase(grant.Database)
		if err != nil {
			return "", err
		}
		privileges, ok := levelPrivileges[grant.Level]
		if !ok {
			return "", fmt.Errorf("unknown grant level %q", grant.Level)
		}
		fmt.Fprintf(&script, "GRANT %s ON %s.* TO %s;\n", privileges, database, account)
	}
	return script.String(), nil
}

func CreateUserScript(user, hash string, grants []engine.Grant) (string, error) {
	account, err := quotedAccount(user)
	if err != nil {
		return "", err
	}
	literal, err := hashLiteral(hash)
	if err != nil {
		return "", err
	}
	statements, err := grantStatements(user, grants)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CREATE USER %s IDENTIFIED WITH caching_sha2_password AS %s;\n%s", account, literal, statements), nil
}

func AdminPasswordScript(hash string) (string, error) {
	literal, err := hashLiteral(hash)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED WITH caching_sha2_password AS %s;\n", engine.AdminUser, literal), nil
}

func SetGrantsScript(user string, grants []engine.Grant) (string, error) {
	account, err := quotedAccount(user)
	if err != nil {
		return "", err
	}
	statements, err := grantStatements(user, grants)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("REVOKE ALL PRIVILEGES, GRANT OPTION FROM %s;\n%s", account, statements), nil
}

func ResetPasswordScript(user, hash string) (string, error) {
	account, err := quotedAccount(user)
	if err != nil {
		return "", err
	}
	literal, err := hashLiteral(hash)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER USER %s IDENTIFIED WITH caching_sha2_password AS %s;\n", account, literal), nil
}

func DropUserScript(user string) (string, error) {
	account, err := quotedAccount(user)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("DROP USER %s;\n", account), nil
}

func SessionIDsQuery(user string) (string, error) {
	if !identRe.MatchString(user) {
		return "", fmt.Errorf("identifier %q is not allowed", user)
	}
	return queryPreamble() + fmt.Sprintf("SELECT id FROM performance_schema.processlist WHERE user = '%s';\n", user), nil
}

func KillScript(ids []int) (string, error) {
	if len(ids) > MaxKill {
		return "", fmt.Errorf("at most %d sessions can be killed at once", MaxKill)
	}
	var script strings.Builder
	for _, id := range ids {
		if id <= 0 {
			return "", fmt.Errorf("session id %d is not valid", id)
		}
		fmt.Fprintf(&script, "KILL CONNECTION %d;\n", id)
	}
	return script.String(), nil
}

func CreateDatabaseScript(name string) (string, error) {
	database, err := quotedDatabase(name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CREATE DATABASE %s CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n", database), nil
}

func GranteesQuery(database string) (string, error) {
	if err := databaseAllowed(database); err != nil {
		return "", err
	}
	return queryPreamble() + fmt.Sprintf("SELECT DISTINCT user FROM mysql.db WHERE db = '%s' AND host = '%%';\n", database), nil
}

func DeleteDatabaseScript(database string, grantees []string) (string, error) {
	quoted, err := quotedDatabase(database)
	if err != nil {
		return "", err
	}
	var script strings.Builder
	for _, grantee := range grantees {
		account, err := quotedAccount(grantee)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&script, "REVOKE ALL PRIVILEGES ON %s.* FROM %s;\n", quoted, account)
	}
	fmt.Fprintf(&script, "DROP DATABASE %s;\n", quoted)
	return script.String(), nil
}

var ListUsersQuery = queryPreamble() + `SELECT u.user, coalesce(d.db, ''), coalesce(d.Create_priv, ''), coalesce(d.Insert_priv, '')
FROM mysql.user u LEFT JOIN mysql.db d ON d.user = u.user AND d.host = u.host
WHERE u.host = '%' AND u.user NOT IN ('dbadmin', 'operator', 'xtrabackup', 'monitor', 'replication', 'orchestrator', 'clustercheck', 'proxyadmin', 'heartbeat', 'mysql.sys', 'mysql.session', 'mysql.infoschema')
ORDER BY u.user, d.db;
`

var ListDatabasesQuery = queryPreamble() + "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('mysql', 'sys', 'information_schema', 'performance_schema') ORDER BY 1;\n"

type UserRow struct {
	Name  string
	DB    string
	Level string
}

func grantLevel(createPriv, insertPriv string) string {
	switch {
	case createPriv == "Y":
		return "all"
	case insertPriv == "Y":
		return "readwrite"
	default:
		return "readonly"
	}
}

func ParseUsers(out string) []UserRow {
	var rows []UserRow
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 4 || userAllowed(fields[0]) != nil {
			continue
		}
		row := UserRow{Name: fields[0]}
		if fields[1] != "" {
			row.DB = strings.ReplaceAll(fields[1], `\_`, "_")
			if databaseAllowed(row.DB) != nil {
				continue
			}
			row.Level = grantLevel(fields[2], fields[3])
		}
		rows = append(rows, row)
	}
	return rows
}

func Classify(stderr string) error {
	if match := errnoRe.FindStringSubmatch(stderr); match != nil {
		return classifyErrno(match[1], stderr)
	}
	switch {
	case strings.Contains(stderr, "Operation CREATE USER failed"), strings.Contains(stderr, "database exists"):
		return engine.ErrExists
	case strings.Contains(stderr, "doesn't exist"), strings.Contains(stderr, "Unknown database"):
		return engine.ErrNotFound
	}
	return nil
}

func classifyErrno(errno, stderr string) error {
	switch errno {
	case "1396":
		if strings.Contains(stderr, "Operation CREATE USER failed") {
			return engine.ErrExists
		}
		return engine.ErrNotFound
	case "1007":
		return engine.ErrExists
	case "1008", "1049", "1507", "1133":
		return engine.ErrNotFound
	case "1044", "1045", "1064":
		return engine.ErrUnavailable
	}
	return nil
}

func run(ctx context.Context, runner engine.Runner, script string, err error) error {
	if err != nil {
		return err
	}
	_, err = runner.Run(ctx, script)
	return err
}

func Bootstrap(ctx context.Context, runner engine.Runner, adminPassword string) error {
	hash, err := CachingSHA2HexLiteral(adminPassword)
	if err != nil {
		return err
	}
	script, err := BootstrapScript(hash)
	return run(ctx, runner, script, err)
}

func ResetAdmin(ctx context.Context, runner engine.Runner, password string) error {
	hash, err := CachingSHA2HexLiteral(password)
	if err != nil {
		return err
	}
	script, err := AdminPasswordScript(hash)
	return run(ctx, runner, script, err)
}

func CreateUser(ctx context.Context, runner engine.Runner, user engine.UserSpec) error {
	hash, err := CachingSHA2HexLiteral(user.Password)
	if err != nil {
		return err
	}
	script, err := CreateUserScript(user.Name, hash, user.Grants)
	return run(ctx, runner, script, err)
}

func ResetPassword(ctx context.Context, runner engine.Runner, name, password string) error {
	hash, err := CachingSHA2HexLiteral(password)
	if err != nil {
		return err
	}
	script, err := ResetPasswordScript(name, hash)
	if err := run(ctx, runner, script, err); err != nil {
		return err
	}
	killSessions(ctx, runner, name)
	return nil
}

func DeleteUser(ctx context.Context, runner engine.Runner, name string) error {
	script, err := DropUserScript(name)
	if err := run(ctx, runner, script, err); err != nil {
		return err
	}
	killSessions(ctx, runner, name)
	return nil
}

func SetGrants(ctx context.Context, runner engine.Runner, name string, grants []engine.Grant) error {
	script, err := SetGrantsScript(name, grants)
	return run(ctx, runner, script, err)
}

func killSessions(ctx context.Context, runner engine.Runner, user string) {
	query, err := SessionIDsQuery(user)
	if err != nil {
		return
	}
	out, err := runner.Run(ctx, query)
	if err != nil {
		return
	}
	for index, line := range engine.Lines(out) {
		if index >= MaxKill {
			return
		}
		if id, err := strconv.Atoi(line); err == nil {
			if kill, err := KillScript([]int{id}); err == nil {
				_, _ = runner.Run(ctx, kill)
			}
		}
	}
}

func ListUsers(ctx context.Context, runner engine.Runner) ([]engine.UserInfo, error) {
	out, err := runner.Run(ctx, ListUsersQuery)
	if err != nil {
		return nil, err
	}
	position := map[string]int{}
	users := []engine.UserInfo{}
	for _, row := range ParseUsers(out) {
		index, seen := position[row.Name]
		if !seen {
			index = len(users)
			position[row.Name] = index
			users = append(users, engine.UserInfo{Name: row.Name, Grants: []engine.Grant{}})
		}
		if row.DB != "" {
			users[index].Grants = append(users[index].Grants, engine.Grant{Database: row.DB, Level: row.Level})
		}
	}
	return users, nil
}

func CreateDatabase(ctx context.Context, runner engine.Runner, name string) error {
	script, err := CreateDatabaseScript(name)
	return run(ctx, runner, script, err)
}

func DeleteDatabase(ctx context.Context, runner engine.Runner, name string) error {
	query, err := GranteesQuery(name)
	if err != nil {
		return err
	}
	out, err := runner.Run(ctx, query)
	if err != nil {
		return err
	}
	script, err := DeleteDatabaseScript(name, identifiers(out))
	return run(ctx, runner, script, err)
}

func ListDatabases(ctx context.Context, runner engine.Runner) ([]string, error) {
	out, err := runner.Run(ctx, ListDatabasesQuery)
	if err != nil {
		return nil, err
	}
	return identifiers(out), nil
}

func identifiers(out string) []string {
	var found []string
	for _, line := range engine.Lines(out) {
		if identRe.MatchString(line) {
			found = append(found, line)
		}
	}
	return found
}
