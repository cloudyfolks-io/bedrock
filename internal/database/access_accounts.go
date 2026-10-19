package database

import (
	"context"
	"fmt"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/credential"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

const valkeyDatabase = engine.DefaultDatabase

func userSpecOf(user v1alpha1.DatabaseUser, password string) engine.UserSpec {
	grants := make([]engine.Grant, len(user.Spec.Grants))
	for i, grant := range user.Spec.Grants {
		grants[i] = engine.Grant{Database: grant.Database, Level: grant.Level}
	}
	return engine.UserSpec{Name: UsernameOf(user), Password: password, Grants: grants, KeyPrefix: user.Spec.KeyPrefix}
}

func accountHash(user v1alpha1.DatabaseUser) string {
	hash := GrantsHash(UsernameOf(user), user.Spec.Grants)
	if user.Spec.KeyPrefix == "" {
		return hash
	}
	return GrantsHash(hash+"|"+user.Spec.KeyPrefix, nil)
}

func userProblem(kind string, user v1alpha1.DatabaseUser) string {
	name := UsernameOf(user)
	var err error
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		err = mysql.CheckUser(name)
	case v1alpha1.DatabaseEngineValkey:
		err = valkey.CheckUser(name)
	default:
		err = postgres.ReservedRole(name)
	}
	if err != nil {
		return err.Error()
	}
	if kind != v1alpha1.DatabaseEngineValkey {
		if user.Spec.KeyPrefix != "" {
			return "keyPrefix applies to Valkey users only"
		}
		return ""
	}
	if len(user.Spec.Grants) != 1 || user.Spec.Grants[0].Database != valkeyDatabase {
		return "a Valkey user takes exactly one grant on the database " + valkeyDatabase
	}
	return ""
}

func schemaProblem(kind, name string) string {
	var err error
	switch kind {
	case v1alpha1.DatabaseEngineMySQL:
		err = mysql.CheckSchema(name)
	default:
		if postgres.ReservedDatabase(name) {
			err = fmt.Errorf("the database name %s is reserved", name)
		}
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func createAccount(ctx context.Context, access Engine, spec engine.UserSpec) error {
	switch access.Kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.CreateUser(ctx, access.Run, spec)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.CreateUser(ctx, access.Run, spec)
	}
	verifier, err := credential.ScramSHA256(spec.Password)
	if err != nil {
		return err
	}
	script, err := postgres.CreateUserScript(spec.Name, verifier, postgresGrants(spec.Grants), access.Timeout)
	if err != nil {
		return err
	}
	_, err = access.Run(ctx, script)
	return err
}

func resetAccountPassword(ctx context.Context, access Engine, name, password string) error {
	switch access.Kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.ResetPassword(ctx, access.Run, name, password)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.ResetPassword(ctx, access.Run, name, password)
	}
	verifier, err := credential.ScramSHA256(password)
	if err != nil {
		return err
	}
	script, err := postgres.ResetPasswordScript(name, verifier, access.Timeout)
	if err != nil {
		return err
	}
	_, err = access.Run(ctx, script)
	return err
}

func setAccountGrants(ctx context.Context, access Engine, spec engine.UserSpec) error {
	switch access.Kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.SetGrants(ctx, access.Run, spec.Name, spec.Grants)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.SetGrants(ctx, access.Run, spec.Name, spec.Grants, spec.KeyPrefix)
	}
	databases, err := listDatabases(ctx, access)
	if err != nil {
		return err
	}
	script, err := postgres.SetGrantsScript(spec.Name, databases, postgresGrants(spec.Grants), access.Timeout)
	if err != nil {
		return err
	}
	_, err = access.Run(ctx, script)
	return err
}

func dropAccount(ctx context.Context, access Engine, name string) error {
	switch access.Kind {
	case v1alpha1.DatabaseEngineMySQL:
		return mysql.DeleteUser(ctx, access.Run, name)
	case v1alpha1.DatabaseEngineValkey:
		return valkey.DeleteUser(ctx, access.Run, name)
	}
	databases, err := listDatabases(ctx, access)
	if err != nil {
		return err
	}
	script, err := postgres.DeleteUserScript(name, databases, access.Timeout)
	if err != nil {
		return err
	}
	_, err = access.Run(ctx, script)
	return err
}

func createDatabase(ctx context.Context, access Engine, name string) error {
	if access.Kind == v1alpha1.DatabaseEngineMySQL {
		return mysql.CreateDatabase(ctx, access.Run, name)
	}
	script, err := postgres.CreateDatabaseScript(name, access.Timeout)
	if err != nil {
		return err
	}
	_, err = access.Run(ctx, script)
	return err
}

func dropDatabase(ctx context.Context, access Engine, name string) error {
	if access.Kind == v1alpha1.DatabaseEngineMySQL {
		return mysql.DeleteDatabase(ctx, access.Run, name)
	}
	script, err := postgres.DeleteDatabaseScript(name, access.Timeout)
	if err != nil {
		return err
	}
	_, err = access.Run(ctx, script)
	return err
}

func postgresGrants(grants []engine.Grant) []postgres.Grant {
	out := make([]postgres.Grant, len(grants))
	for i, grant := range grants {
		out[i] = postgres.Grant{Database: grant.Database, Level: grant.Level}
	}
	return out
}
