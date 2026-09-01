package database

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Connect establishes a database connection using the provided DSN.
//
// Driver is chosen by DSN shape:
//   - "sqlite://path" or "sqlite:path"  → SQLite (file or :memory:)
//   - anything else                     → Postgres
//
// Examples:
//
//	DATABASE_URL=sqlite:./bench.db
//	DATABASE_URL=sqlite::memory:
//	DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable
func Connect(dsn string) (*gorm.DB, error) {
	logLevel := logger.Warn
	if os.Getenv("DB_LOG_LEVEL") == "info" {
		logLevel = logger.Info
	} else if os.Getenv("DB_LOG_LEVEL") == "silent" {
		logLevel = logger.Silent
	}
	// Two GORM knobs that get recommended a lot. Both are off by default here,
	// and both defaults were measured rather than assumed — k6, 50 concurrent
	// writers, 4 CPUs, three 20-second runs each, median req/s on inserts:
	//
	//	off / off        740     what ships
	//	PrepareStmt      738     no difference
	//	+ skip the tx  1,294     +75%
	//
	// PrepareStmt caches a prepared statement per connection so a query is
	// planned once instead of per request. On this workload it measured as
	// nothing — the cache is mutex-guarded and under concurrency the contention
	// cancels out the saved planning. It also breaks against a connection pooler
	// in transaction mode (pgbouncer, RDS Proxy), because server-side prepared
	// statements do not survive a pooler that hands each transaction a different
	// backend. No measured gain, real downsides, so: opt in with
	// DB_PREPARED_STATEMENTS=true if your own numbers disagree.
	gormCfg := &gorm.Config{
		Logger:      logger.Default.LogMode(logLevel),
		PrepareStmt: os.Getenv("DB_PREPARED_STATEMENTS") == "true",
	}

	// Skipping the default transaction is the one that actually pays — GORM
	// wraps every Create, Update and Delete in an implicit transaction, so a
	// single-row insert costs BEGIN + INSERT + COMMIT where one round trip would
	// do. Turning it off was worth 75% here.
	//
	// It is still off by default, and that is a correctness decision rather than
	// a cautious one. The resource generator emits models with relations, and
	// saving an invoice with its line items is several INSERTs that GORM's
	// implicit transaction is currently what makes atomic — the generated
	// handler does not open its own. Without it, a failure halfway through
	// leaves an invoice holding some of its lines, with nothing logged and
	// nobody the wiser until the totals stop adding up.
	//
	// So: if your resources are flat, DB_SKIP_DEFAULT_TRANSACTION=true is close
	// to free throughput. If you generate anything with line items, leave it
	// alone until the generated handlers wrap their own writes.
	if os.Getenv("DB_SKIP_DEFAULT_TRANSACTION") == "true" {
		gormCfg.SkipDefaultTransaction = true
	}

	var (
		db  *gorm.DB
		err error
	)

	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		db, err = gorm.Open(sqlite.Open(strings.TrimPrefix(dsn, "sqlite://")), gormCfg)
	case strings.HasPrefix(dsn, "sqlite:"):
		db, err = gorm.Open(sqlite.Open(strings.TrimPrefix(dsn, "sqlite:")), gormCfg)
	case strings.HasPrefix(dsn, "mysql://"), strings.HasPrefix(dsn, "mysql:"):
		// go-sql-driver wants "user:pass@tcp(host:port)/db", not a URL, so the
		// scheme is stripped rather than parsed. parseTime is not optional:
		// without it DATETIME columns arrive as []byte and every time.Time
		// field on every model fails to scan.
		my := strings.TrimPrefix(strings.TrimPrefix(dsn, "mysql://"), "mysql:")
		if !strings.Contains(my, "parseTime=") {
			sep := "?"
			if strings.Contains(my, "?") {
				sep = "&"
			}
			my += sep + "parseTime=true&loc=UTC"
		}
		db, err = gorm.Open(mysql.Open(my), gormCfg)
	default:
		db, err = gorm.Open(postgres.New(postgres.Config{
			DSN:                  dsn,
			PreferSimpleProtocol: true,
		}), gormCfg)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// Connection pool settings. SQLite ignores most of these — single-writer
	// semantics mean MaxOpenConns above 1 only helps concurrent reads, and
	// SQLite serialises writes internally. Postgres uses every knob.
	//
	// Idle defaults to Open, and that default matters more than it looks. When
	// idle is lower, a request past the idle limit returns its connection to a
	// full pool, so the connection is CLOSED — and the next request opens a new
	// one, which makes Postgres fork a backend process. Under concurrency that
	// is a connection storm, and it surfaces as database CPU rather than as
	// anything you would think to look for in the application.
	//
	// Measured with k6 at 50 VUs, 4 CPUs per container, single-row reads:
	// idle=10 gave ~810 req/s with Postgres pinned near 840% while the API used
	// 196%; idle=100 gave ~2,720 req/s with both around 300%. Same binary, same
	// query.
	//
	// Both are tunable because the right answer depends on the workload. If
	// your queries are heavy enough to saturate the database — an unindexed
	// COUNT over a large table on every request, say — a smaller pool acts as
	// admission control and can measure faster, because queueing in the app is
	// cheaper than thrashing in Postgres. Start here, then measure.
	maxOpen := getEnvInt("DB_MAX_OPEN_CONNS", 100)
	if maxOpen < 1 {
		maxOpen = 1
	}
	maxIdle := getEnvInt("DB_MAX_IDLE_CONNS", maxOpen)
	if maxIdle < 1 || maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	log.Println("Database connected successfully")
	return db, nil
}

// getEnvInt reads a whole-number env var. A malformed value falls back rather
// than failing the boot — a typo in DB_MAX_OPEN_CONNS should not stop the app
// from starting.
func getEnvInt(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
		log.Printf("warning: %s=%q is not a number, using %d", key, raw, fallback)
	}
	return fallback
}
