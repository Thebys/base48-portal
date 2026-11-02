package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// A query whose context ends while it runs must not keep its read lock.
// modernc.org/sqlite v1.40.0 dropped the rows of such a query without closing
// them, so the read transaction stayed open. In rollback-journal mode no other
// connection could then commit until the process restarted: on 2026-11-02 one
// reload of /admin/finance locked production for seven hours.
func TestCancelledQueryReleasesLock(t *testing.T) {
	path := "file:" + t.TempDir() + "/lock.db"

	reads, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer reads.Close()

	// The writer stands for the other process, cron.
	writes, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(200)")
	if err != nil {
		t.Fatal(err)
	}
	defer writes.Close()
	writes.SetMaxOpenConns(1)

	if _, err := writes.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writes.Exec(`WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM s WHERE n < 2000)
		INSERT INTO t SELECT n FROM s`); err != nil {
		t.Fatal(err)
	}

	// Where the cancel lands is a race, so try it at many points.
	for i := range 2000 {
		ctx, cancel := context.WithCancel(context.Background())
		delay := time.Duration(i%200) * time.Microsecond
		go func() {
			time.Sleep(delay)
			cancel()
		}()
		if rows, err := reads.QueryContext(ctx, `SELECT n FROM t ORDER BY n`); err == nil {
			for rows.Next() {
			}
			rows.Close()
		}
		cancel()

		if _, err := writes.Exec(`UPDATE t SET n = n WHERE n = 1`); err != nil {
			t.Fatalf("a write fails after %d cancelled queries: %v", i+1, err)
		}
	}
}
