package certprocessor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// assertPasswordNotInArgs verifies that the password does not appear anywhere
// in the command's argument vector: it must only travel through the process
// environment, so it is never visible in /proc/<pid>/cmdline.
func assertPasswordNotInArgs(t *testing.T, cmd *exec.Cmd, password string) {
	t.Helper()
	for _, arg := range cmd.Args {
		if strings.Contains(arg, password) {
			t.Errorf("password leaked into command arguments: %q", arg)
		}
	}
}

// assertPasswordInEnv verifies that the password is present in the command's
// environment under passwordEnvVar.
func assertPasswordInEnv(t *testing.T, cmd *exec.Cmd, password string) {
	t.Helper()
	want := passwordEnvVar + "=" + password
	for _, entry := range cmd.Env {
		if entry == want {
			return
		}
	}
	t.Errorf("password not found in cmd.Env under %s; env = %v", passwordEnvVar, cmd.Env)
}

// TestCommandBuildersFeedPasswordViaEnv asserts, for every command-building
// path, that the password never appears in cmd.Args and is present in cmd.Env
// under passwordEnvVar. The end-to-end behavior of these env-fed forms is
// proven by the certProcessor tests that execute the real keytool/openssl
// tools.
func TestCommandBuildersFeedPasswordViaEnv(t *testing.T) {
	const pw = "s3cret-PW-123"

	tests := map[string]struct {
		build func() *exec.Cmd
	}{
		"keytoolImportCertCmd": {
			build: func() *exec.Cmd {
				return keytoolImportCertCmd(context.Background(), "/tmp/ks.jks", "CARoot", "/tmp/ca.crt", pw)
			},
		},
		"keytoolImportKeystoreCmd": {
			build: func() *exec.Cmd {
				return keytoolImportKeystoreCmd(context.Background(), "/tmp/dest.jks", "/tmp/src.p12", pw)
			},
		},
		"keytoolListCmd": {
			build: func() *exec.Cmd {
				return keytoolListCmd(context.Background(), "/tmp/ks.jks", pw)
			},
		},
		"opensslExportPKCS12Cmd with CA chain": {
			build: func() *exec.Cmd {
				return opensslExportPKCS12Cmd(context.Background(), "/tmp/tls.crt", "/tmp/tls.key", "/tmp/ca.crt", "alias", "/tmp/out.p12", pw)
			},
		},
		"opensslExportPKCS12Cmd without CA chain": {
			build: func() *exec.Cmd {
				return opensslExportPKCS12Cmd(context.Background(), "/tmp/tls.crt", "/tmp/tls.key", "", "alias", "/tmp/out.p12", pw)
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cmd := tt.build()
			assertPasswordNotInArgs(t, cmd, pw)
			assertPasswordInEnv(t, cmd, pw)
		})
	}
}

// TestCommandBuildersUseEnvFedFlagForms asserts the exact env-fed flag syntax
// used for each tool: keytool's ":env" modifier and openssl's "env:" pass
// phrase source.
func TestCommandBuildersUseEnvFedFlagForms(t *testing.T) {
	t.Run("keytool importcert uses -storepass:env", func(t *testing.T) {
		cmd := keytoolImportCertCmd(context.Background(), "/tmp/ks.jks", "CARoot", "/tmp/ca.crt", "pw")
		if !containsAdjacent(cmd.Args, "-storepass:env", passwordEnvVar) {
			t.Errorf("expected -storepass:env %s in args, got %v", passwordEnvVar, cmd.Args)
		}
	})

	t.Run("keytool importkeystore uses -srcstorepass:env and -deststorepass:env", func(t *testing.T) {
		cmd := keytoolImportKeystoreCmd(context.Background(), "/tmp/dest.jks", "/tmp/src.p12", "pw")
		if !containsAdjacent(cmd.Args, "-srcstorepass:env", passwordEnvVar) {
			t.Errorf("expected -srcstorepass:env %s in args, got %v", passwordEnvVar, cmd.Args)
		}
		if !containsAdjacent(cmd.Args, "-deststorepass:env", passwordEnvVar) {
			t.Errorf("expected -deststorepass:env %s in args, got %v", passwordEnvVar, cmd.Args)
		}
	})

	t.Run("keytool list uses -storepass:env", func(t *testing.T) {
		cmd := keytoolListCmd(context.Background(), "/tmp/ks.jks", "pw")
		if !containsAdjacent(cmd.Args, "-storepass:env", passwordEnvVar) {
			t.Errorf("expected -storepass:env %s in args, got %v", passwordEnvVar, cmd.Args)
		}
	})

	t.Run("openssl pkcs12 uses -passout env:", func(t *testing.T) {
		cmd := opensslExportPKCS12Cmd(context.Background(), "/tmp/tls.crt", "/tmp/tls.key", "", "alias", "/tmp/out.p12", "pw")
		if !containsAdjacent(cmd.Args, "-passout", "env:"+passwordEnvVar) {
			t.Errorf("expected -passout env:%s in args, got %v", passwordEnvVar, cmd.Args)
		}
	})
}

// containsAdjacent reports whether args contains first immediately followed
// by second.
func containsAdjacent(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

// TestWithPasswordEnvDoesNotTouchProcessEnv verifies that withPasswordEnv
// never modifies the operator process environment (no os.Setenv): the parent
// env must stay clean, and the variable must only appear in the child's env
// slice.
func TestWithPasswordEnvDoesNotTouchProcessEnv(t *testing.T) {
	before, ok := os.LookupEnv(passwordEnvVar)
	if ok {
		// Pre-existing value in the test process env; remember it so the
		// assertion below can distinguish "unchanged" from "removed".
		t.Logf("pre-existing %s in process env", passwordEnvVar)
	}

	cmd := withPasswordEnv(exec.Command("true"), "test-password")

	after, okAfter := os.LookupEnv(passwordEnvVar)
	if ok != okAfter || before != after {
		t.Errorf("operator process environment was modified: %s changed from %q (present=%v) to %q (present=%v)",
			passwordEnvVar, before, ok, after, okAfter)
	}

	assertPasswordInEnv(t, cmd, "test-password")
}

// TestWithPasswordEnvOverridesParentValue verifies that a pre-existing
// passwordEnvVar in the parent environment is dropped from the child env so
// the value set here always wins.
func TestWithPasswordEnvOverridesParentValue(t *testing.T) {
	t.Setenv(passwordEnvVar, "parent-value")

	cmd := withPasswordEnv(exec.Command("true"), "child-value")

	// Exactly one occurrence of the variable, holding the child value.
	count := 0
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, passwordEnvVar+"=") {
			count++
			if entry != passwordEnvVar+"=child-value" {
				t.Errorf("expected the child value to win, got %q", entry)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one %s entry in cmd.Env, got %d", passwordEnvVar, count)
	}
}

// TestWithPasswordEnvInheritsParentEnvironment verifies that the child env
// still contains the parent environment (os.Environ) besides the password.
func TestWithPasswordEnvInheritsParentEnvironment(t *testing.T) {
	t.Setenv("SSR_TEST_CANARY", "canary-value")

	cmd := withPasswordEnv(exec.Command("true"), "pw")

	found := false
	for _, entry := range cmd.Env {
		if entry == "SSR_TEST_CANARY=canary-value" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected the parent environment to be inherited by the child env")
	}
}
