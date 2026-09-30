package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"myexampleapp/pkg/config"
	"myexampleapp/pkg/zgen/schemas/counter"
	"myexampleapp/pkg/zgen/taskgen"

	"github.com/cloudcarver/anclax/core"
	anclaxconfig "github.com/cloudcarver/anclax/pkg/config"
	taskcore "github.com/cloudcarver/anclax/pkg/taskcore/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/mock/gomock"
)

func TestNewModelDoesNotDiscloseDSNCredentials(t *testing.T) {
	dsn := "postgres://audit-user:database-secret-canary@%zz/database"
	_, err := NewModel(&config.Config{Anclax: anclaxconfig.Config{Pg: anclaxconfig.Pg{DSN: &dsn}}}, nil)
	if err == nil {
		t.Fatal("expected invalid database configuration error")
	}
	if strings.Contains(err.Error(), "database-secret-canary") || strings.Contains(err.Error(), dsn) {
		t.Fatalf("error disclosed database credentials: %v", err)
	}
}

func TestTransactionSharesCoreTxWithQueriesAndTaskRunner(t *testing.T) {
	ctrl := gomock.NewController(t)
	tx := core.NewMockTx(ctrl)
	store := taskcore.NewMockTaskStoreInterface(ctrl)
	runner := taskgen.NewTaskRunner(store)
	m := &Model{beginTx: func(context.Context) (core.Tx, error) { return tx, nil }}
	ctx := context.Background()

	gomock.InOrder(
		tx.EXPECT().Exec(ctx, gomock.Any(), int32(7)).Return(pgconn.NewCommandTag("UPDATE 1"), nil),
		store.EXPECT().PushTaskWithTx(ctx, tx, gomock.Any()).Return(int32(42), nil),
		tx.EXPECT().Commit(ctx).Return(nil),
		tx.EXPECT().Rollback(gomock.Any()).Return(pgx.ErrTxClosed),
	)

	err := m.RunTransactionWithTx(ctx, func(gotTx core.Tx, txm ModelInterface) error {
		if gotTx != tx || !txm.InTransaction() || m.InTransaction() {
			t.Fatal("transaction must be shared while keeping the parent model outside it")
		}
		if err := txm.RunTransaction(ctx, func(ModelInterface) error {
			t.Fatal("nested transactions must not run")
			return nil
		}); !errors.Is(err, ErrAlreadyInTransaction) {
			t.Fatalf("nested transaction error = %v", err)
		}
		if err := txm.IncrementCounter(ctx, 7); err != nil {
			return err
		}
		_, err := runner.RunIncrementCounterWithTx(ctx, gotTx, &counter.IncrementCounterParams{Amount: 7})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpawnWithCoreTx(t *testing.T) {
	// Failure hooks supply core.Tx, which need not implement the larger pgx.Tx.
	tx := core.NewMockTx(gomock.NewController(t))
	ctx := context.Background()
	tx.EXPECT().Exec(ctx, gomock.Any(), int32(3)).Return(pgconn.NewCommandTag("UPDATE 1"), nil)
	if err := (&Model{}).SpawnWithTx(tx).IncrementCounter(ctx, 3); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionRollsBackAfterCancellation(t *testing.T) {
	tx := core.NewMockTx(gomock.NewController(t))
	m := &Model{beginTx: func(context.Context) (core.Tx, error) { return tx, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx.EXPECT().Rollback(gomock.Any()).DoAndReturn(func(rbCtx context.Context) error {
		if rbCtx.Err() != nil {
			t.Fatalf("rollback context is cancelled: %v", rbCtx.Err())
		}
		if _, ok := rbCtx.Deadline(); !ok {
			t.Fatal("rollback must have a timeout")
		}
		return nil
	})
	err := m.RunTransaction(ctx, func(ModelInterface) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("transaction error = %v", err)
	}
}

func TestTransactionPreservesErrors(t *testing.T) {
	for _, stage := range []string{"begin", "callback", "commit"} {
		t.Run(stage, func(t *testing.T) {
			wantErr := errors.New(stage + " failed")
			tx := core.NewMockTx(gomock.NewController(t))
			m := &Model{beginTx: func(context.Context) (core.Tx, error) {
				if stage == "begin" {
					return nil, wantErr
				}
				return tx, nil
			}}
			if stage != "begin" {
				tx.EXPECT().Rollback(gomock.Any()).Return(nil)
			}
			if stage == "commit" {
				tx.EXPECT().Commit(gomock.Any()).Return(wantErr)
			}
			err := m.RunTransaction(context.Background(), func(ModelInterface) error {
				if stage == "begin" {
					t.Fatal("callback ran after begin failed")
				}
				if stage == "callback" {
					return wantErr
				}
				return nil
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("transaction error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestTransactionRollsBackOnPanic(t *testing.T) {
	tx := core.NewMockTx(gomock.NewController(t))
	m := &Model{beginTx: func(context.Context) (core.Tx, error) { return tx, nil }}
	tx.EXPECT().Rollback(gomock.Any()).Return(nil)
	defer func() {
		if got := recover(); got != "callback panic" {
			t.Fatalf("panic = %v", got)
		}
	}()
	_ = m.RunTransaction(context.Background(), func(ModelInterface) error { panic("callback panic") })
}
