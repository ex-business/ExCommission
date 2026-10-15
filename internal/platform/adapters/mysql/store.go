package mysql

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	foundation "github.com/ex-business/ExCommission/internal/platform/domain"
	"github.com/ex-business/ExCommission/internal/platform/ports"
	driver "github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	DB    *sql.DB
	Audit func(context.Context, ports.Executor, foundation.Scope, string, string, string) error
}

func Open(ctx context.Context, address, user, password, database string) (*Store, error) {
	cfg := driver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = address
	cfg.User = user
	cfg.Passwd = password
	cfg.DBName = database
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	cfg.Collation = "utf8mb4_0900_bin"
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, errors.New("database configuration invalid")
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("database unavailable")
	}
	return &Store{DB: db}, nil
}

// Migrate 通过同一连接的数据库命名锁串行执行，DDL可重入且逐条校验版本摘要。
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':migrate'),30)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("migration lock unavailable")
	}
	defer conn.ExecContext(context.Background(), "DO RELEASE_LOCK(CONCAT(DATABASE(), ':migrate'))")
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(128) PRIMARY KEY, checksum CHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL) ENGINE=InnoDB`); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, file := range files {
		body, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		checksum := digest(body)
		var old string
		err = conn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version=?", file.Name()).Scan(&old)
		if err == nil {
			if old != checksum {
				return fmt.Errorf("migration checksum conflict: %s", file.Name())
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, statement := range strings.Split(string(body), ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err = conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %s: %w", file.Name(), err)
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations VALUES (?,?,UTC_TIMESTAMP(6))", file.Name(), checksum); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Within(ctx context.Context, fn func(ports.Executor) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tracked := &trackedExecutor{Executor: tx}
	if err = fn(tracked); err != nil {
		return err
	}
	metadata := foundation.RequestFrom(ctx)
	if tracked.mutated && s.Audit != nil && !metadata.Command && metadata.Scope.Valid() && metadata.Subject != "" {
		action := metadata.Action
		if action == "" {
			action = "transaction"
		}
		if err = s.Audit(ctx, tx, metadata.Scope, action, "unit-of-work", "SUCCEEDED"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type trackedExecutor struct {
	ports.Executor
	mutated bool
}

func (t *trackedExecutor) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	r, e := t.Executor.ExecContext(ctx, q, args...)
	if e == nil {
		n, err := r.RowsAffected()
		if err == nil && n > 0 {
			t.mutated = true
		}
	}
	return r, e
}
