package certprocessor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// helperCommand returns an *exec.Cmd built with exec.CommandContext running
// this test binary in "-test.run=TestExecHelperProcess" re-exec mode, so the
// context/cancellation tests can simulate slow or noisy keytool/openssl child
// processes without relying on the real tools.
func helperCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmdArgs := append([]string{"-test.run=TestExecHelperProcess", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

// TestExecHelperProcess is not a real test: it is the child process re-entered
// by helperCommand. Everything after "--" is the simulated command line:
//   - "sleep <duration>" blocks in-process for the duration, simulating a
//     hung keytool; SIGKILL from context cancellation kills it directly.
//   - "stderr <message>" writes the message to stderr and exits 1, simulating
//     a failing tool whose diagnostics land on stderr (B17).
func TestExecHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "sleep":
		d, err := time.ParseDuration(args[1])
		if err != nil {
			d = 30 * time.Second
		}
		time.Sleep(d)
		os.Exit(0)
	case "stderr":
		_, _ = os.Stderr.WriteString(args[1])
		os.Exit(1)
	default:
		os.Exit(0)
	}
}

// TestCommandBuildersBindContext asserts that every command-building path uses
// exec.CommandContext: cancelling the builder context must make Start() fail
// with context.Canceled before any process runs, proving the command carries
// the supplied context (B5).
func TestCommandBuildersBindContext(t *testing.T) {
	tests := map[string]struct {
		build func(ctx context.Context) *exec.Cmd
	}{
		"keytoolImportCertCmd": {
			build: func(ctx context.Context) *exec.Cmd {
				return keytoolImportCertCmd(ctx, "/tmp/ks.jks", "CARoot", "/tmp/ca.crt", "pw")
			},
		},
		"keytoolImportKeystoreCmd": {
			build: func(ctx context.Context) *exec.Cmd {
				return keytoolImportKeystoreCmd(ctx, "/tmp/dest.jks", "/tmp/src.p12", "pw")
			},
		},
		"keytoolListCmd": {
			build: func(ctx context.Context) *exec.Cmd {
				return keytoolListCmd(ctx, "/tmp/ks.jks", "pw")
			},
		},
		"opensslExportPKCS12Cmd": {
			build: func(ctx context.Context) *exec.Cmd {
				return opensslExportPKCS12Cmd(ctx, "/tmp/tls.crt", "/tmp/tls.key", "", "alias", "/tmp/out.p12", "pw")
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			cmd := tt.build(ctx)
			err := cmd.Start()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected Start() to fail with context.Canceled (command bound to ctx), got %v", err)
			}
			if cmd.Process != nil {
				// Should not happen, but never leak a started process.
				_ = cmd.Process.Kill()
			}
		})
	}
}

// TestRunWithCancelAbortsChild proves the cancellation wiring end to end: a
// cancelled context aborts the child instead of letting a hung keytool block
// the caller forever (B5).
func TestRunWithCancelAbortsChild(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})

	t.Run("pre-cancelled context aborts immediately", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cmd := helperCommand(ctx, "sleep", "30s")
		start := time.Now()
		_, err := runCertCommand(ctx, cp, cmd)
		if err == nil {
			t.Fatal("expected an error when the context is already cancelled")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected the error to wrap context.Canceled, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("expected an immediate abort, took %v", elapsed)
		}
	})

	t.Run("mid-run cancellation kills a sleeping child", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cmd := helperCommand(ctx, "sleep", "30s")
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start the helper process: %v", err)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		// Wait on the already-started command: exec's context watcher kills
		// the child on cancellation and Wait returns the context error.
		err := cmd.Wait()
		if err == nil {
			t.Fatal("expected an error after cancelling during the run")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("expected the child to be killed within seconds of cancellation, took %v", elapsed)
		}
	})

	t.Run("CreateTruststore aborts on a cancelled context", func(t *testing.T) {
		// The exported API must honour the caller's cancellation: keytool is
		// started with the bounded (already cancelled) context and os/exec
		// refuses to run it, surfacing the cancellation as an error.
		ca, err := testutil.GenerateClusterCACert("CTX-ABORT")
		if err != nil {
			t.Fatalf("failed to generate CA: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = cp.CreateTruststore(ctx, ca.CACertPEM, "test1234")
		if err == nil {
			t.Fatal("expected an error for a cancelled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected the error to wrap context.Canceled, got %v", err)
		}
	})
}

// TestRunWithDeadlineKillsHungChild proves the defensive default timeout: a
// context without a caller deadline gets execCommandTimeout attached, and a
// hung child is killed when it elapses (B5). The timeout is shortened so the
// test finishes quickly.
func TestRunWithDeadlineKillsHungChild(t *testing.T) {
	original := execCommandTimeout
	execCommandTimeout = 400 * time.Millisecond
	defer func() { execCommandTimeout = original }()

	cp := NewCertProcessor(logr.Logger{})

	ctx, cancel := withExecDeadline(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("withExecDeadline must add a deadline to a context without one")
	}

	start := time.Now()
	_, err := runCertCommand(ctx, cp, helperCommand(ctx, "sleep", "30s"))
	if err == nil {
		t.Fatal("expected an error from the timed-out child")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected the error to wrap context.DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("expected the hung child to die near the deadline, took %v", elapsed)
	}
}

// TestWithExecDeadline asserts the deadline policy: a caller deadline passes
// through unchanged; otherwise the defensive default is applied.
func TestWithExecDeadline(t *testing.T) {
	original := execCommandTimeout
	execCommandTimeout = time.Hour
	defer func() { execCommandTimeout = original }()

	t.Run("caller deadline is preserved", func(t *testing.T) {
		parent, pcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pcancel()
		ctx, cancel := withExecDeadline(parent)
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected a deadline on the returned context")
		}
		if parentDL, _ := parent.Deadline(); !dl.Equal(parentDL) {
			t.Errorf("expected the caller deadline %v to be kept, got %v", parentDL, dl)
		}
	})

	t.Run("default applies without a caller deadline", func(t *testing.T) {
		ctx, cancel := withExecDeadline(context.Background())
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected the defensive default deadline")
		}
		if until := time.Until(dl); until <= 0 || until > execCommandTimeout {
			t.Errorf("expected the deadline within %v, got %v", execCommandTimeout, until)
		}
	})
}

// TestRunCertCommandSurfacesStderr proves B17: keytool/openssl write their
// diagnostics to stderr and cmd.Output() only captures stdout, so the error
// would otherwise be a bare "exit status 1". runCertCommand must include the
// captured stderr in the returned error (which is also what it logs).
func TestRunCertCommandSurfacesStderr(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})
	ctx := context.Background()

	cmd := helperCommand(ctx, "stderr", "keytool error: java.lang.Exception: problem in the certificate")
	_, err := runCertCommand(ctx, cp, cmd)
	if err == nil {
		t.Fatal("expected an error from a failing command")
	}
	if !strings.Contains(err.Error(), "keytool error: java.lang.Exception: problem in the certificate") {
		t.Errorf("expected the stderr diagnostics in the error, got: %v", err)
	}
}
