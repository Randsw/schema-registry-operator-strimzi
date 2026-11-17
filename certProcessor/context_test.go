package certprocessor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/randsw/schema-registry-operator-strimzi/internal/testutil"
)

// TestCancelledContextAbortsPureGoPaths pins B5 on the pure-Go certificate
// building paths: a context that is already cancelled must abort the call
// with an error wrapping context.Canceled before any keystore output is
// produced. The ensureContextAlive checks replaced the bounded keytool and
// openssl child processes of the previous pipeline, so caller cancellation is
// still honoured end to end without any subprocess.
func TestCancelledContextAbortsPureGoPaths(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}
	uc, err := testutil.GenerateTestUserCert(ca, "test1234")
	if err != nil {
		t.Fatalf("failed to generate user cert: %v", err)
	}
	keystore, _, err := cp.GenerateTLSforHTTP(context.Background(), ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
	if err != nil {
		t.Fatalf("failed to generate TLS keystore: %v", err)
	}

	tests := map[string]struct {
		call func(ctx context.Context) ([]byte, error)
	}{
		"CreateTruststore": {
			call: func(ctx context.Context) ([]byte, error) {
				out, _, err := cp.CreateTruststore(ctx, ca.CACertPEM, "test1234")
				return out, err
			},
		},
		"CreateKeystore from PEM components": {
			call: func(ctx context.Context) ([]byte, error) {
				out, _, err := cp.CreateKeystore(ctx, ca.CACertPEM, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
				return out, err
			},
		},
		"CreateKeystore from user.p12": {
			call: func(ctx context.Context) ([]byte, error) {
				out, _, err := cp.CreateKeystore(ctx, "", "", "", string(uc.PKCS12Data), "test1234")
				return out, err
			},
		},
		"GenerateTLSforHTTP": {
			call: func(ctx context.Context) ([]byte, error) {
				out, _, err := cp.GenerateTLSforHTTP(ctx, ca.CACertPEM, ca.CAKeyPEM, "test1234", "confluent-schema-registry", "kafka")
				return out, err
			},
		},
		"ConvertPEMToJKS": {
			call: func(ctx context.Context) ([]byte, error) {
				out, _, err := cp.ConvertPEMToJKS(ctx, uc.UserCertPEM, uc.UserKeyPEM, "", "test1234")
				return out, err
			},
		},
		"KeystoreCertExpiry": {
			call: func(ctx context.Context) ([]byte, error) {
				_, err := cp.KeystoreCertExpiry(ctx, keystore, "test1234")
				return nil, err
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			out, err := tt.call(ctx)
			if err == nil {
				t.Fatal("expected an error for a cancelled context")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("expected the error to wrap context.Canceled, got %v", err)
			}
			if out != nil {
				t.Error("expected no keystore output for a cancelled context")
			}
		})
	}
}

// TestExpiredContextAbortsPureGoPaths pins the deadline half of B5: a context
// whose deadline has already passed aborts the call with an error wrapping
// context.DeadlineExceeded, the same way the bounded child processes of the
// previous pipeline used to be killed at their deadline.
func TestExpiredContextAbortsPureGoPaths(t *testing.T) {
	cp := NewCertProcessor(logr.Logger{})

	ca, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if _, _, err := cp.CreateTruststore(ctx, ca.CACertPEM, "test1234"); err == nil {
		t.Fatal("expected an error for an expired context")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected the error to wrap context.DeadlineExceeded, got %v", err)
	}

	if _, err := cp.KeystoreCertExpiry(ctx, []byte("irrelevant"), "test1234"); err == nil {
		t.Fatal("expected an error for an expired context")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected the error to wrap context.DeadlineExceeded, got %v", err)
	}
}
