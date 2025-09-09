// Code generate by anchor. DO NOT EDIT.
package model

import (
	"context"
	"fmt"
	root "myexampleapp"
	"net/url"
	"time"

	"myexampleapp/pkg/config"
	"myexampleapp/pkg/zgen/querier"

	anchor_app "github.com/cloudcarver/anchor/pkg/app"
	"github.com/cloudcarver/anchor/pkg/logger"
	anchor_utils "github.com/cloudcarver/anchor/pkg/utils"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pkg/errors"
)

var log = logger.NewLogAgent("model")

var (
	ErrAlreadyInTransaction = errors.New("already in transaction")
)

type ModelInterface interface {
	querier.Querier
	RunTransaction(ctx context.Context, f func(model ModelInterface) error) error
	RunTransactionWithTx(ctx context.Context, f func(tx pgx.Tx, model ModelInterface) error) error
	InTransaction() bool
	SpawnWithTx(tx pgx.Tx) ModelInterface
	Close()
}

type Model struct {
	querier.Querier
	beginTx       func(ctx context.Context) (pgx.Tx, error)
	p             *pgxpool.Pool
	inTransaction bool
}

func (m *Model) InTransaction() bool {
	return m.inTransaction
}

func (m *Model) SpawnWithTx(tx pgx.Tx) ModelInterface {
	return &Model{
		Querier: querier.New(tx),
		beginTx: func(ctx context.Context) (pgx.Tx, error) {
			return nil, ErrAlreadyInTransaction
		},
		inTransaction: true,
	}
}

func (m *Model) Close() {
	if m.p != nil {
		m.p.Close()
	}
}

func (m *Model) RunTransactionWithTx(ctx context.Context, f func(tx pgx.Tx, model ModelInterface) error) error {
	tx, err := m.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	txm := m.SpawnWithTx(tx)

	if err := f(tx, txm); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (m *Model) RunTransaction(ctx context.Context, f func(model ModelInterface) error) error {
	return m.RunTransactionWithTx(ctx, func(_ pgx.Tx, model ModelInterface) error {
		return f(model)
	})
}
func NewModel(cfg *config.Config, meta anchor_app.PluginMeta) (ModelInterface, error) {
	anchorCfg := cfg.Anchor

	var dsn string
	if anchorCfg.Pg.DSN != nil {
		dsn = *anchorCfg.Pg.DSN
	} else {
		url := &url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(anchorCfg.Pg.User, anchorCfg.Pg.Password),
			Host:     fmt.Sprintf("%s:%d", anchorCfg.Pg.Host, anchorCfg.Pg.Port),
			Path:     anchorCfg.Pg.Db,
			RawQuery: "sslmode=" + anchor_utils.IfElse(anchorCfg.Pg.SSLMode == "", "require", anchorCfg.Pg.SSLMode),
		}
		dsn = url.String()
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse pgxpool config: %s", anchor_utils.ReplaceSensitiveStringBySha256(dsn, anchorCfg.Pg.Password))
	}
	config.MaxConns = 30
	config.MinConns = 5

	var (
		retryLimit = 10
		retry      = 0
	)

	var p *pgxpool.Pool

	for {
		err := func() error {
			ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
			defer cancel()

			pool, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				log.Warnf("failed to init pgxpool: %s", err.Error())
				return errors.Wrapf(err, "failed to init pgxpool: %s", dsn)
			}

			p = pool

			if err := pool.Ping(ctx); err != nil {
				log.Warnf("failed to ping database: %s", err.Error())
				pool.Close()
				return errors.Wrap(err, "failed to ping db")
			}
			return nil
		}()
		if err == nil {
			break
		}
		if retry >= retryLimit {
			return nil, err
		}
		retry++
		time.Sleep(3 * time.Second)
	}

	d, err := iofs.New(root.Migrations, "sql/migrations")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create migration source driver")
	}

	dsnURL, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse dsn: %s", anchor_utils.ReplaceSensitiveStringBySha256(dsn, anchorCfg.Pg.Password))
	}
	dsnURL.Scheme = "pgx5"
	dsnQuery := dsnURL.Query()
	dsnQuery.Add("x-migrations-table", fmt.Sprintf("%s_migrations", meta.Namespace))
	dsnURL.RawQuery = dsnQuery.Encode()

	m, err := migrate.NewWithSourceInstance("iofs", d, dsnURL.String())
	if err != nil {
		return nil, errors.Wrap(err, "failed to init migrate")
	}
	if err := m.Up(); err != nil {
		if !errors.Is(err, migrate.ErrNoChange) {
			return nil, errors.Wrap(err, "failed to migrate up")
		}
	}

	return &Model{Querier: querier.New(p), beginTx: p.Begin, p: p}, nil
}
